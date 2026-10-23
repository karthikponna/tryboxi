package egress

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/karthikponna/tryboxi/internal/network/sniff"
	"github.com/karthikponna/tryboxi/internal/secrets"
)

// prefixConn replays already-peeked bytes before reading from the conn.
type prefixConn struct {
	net.Conn
	r io.Reader
}

func (c *prefixConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// countConn counts bytes in each direction.
type countConn struct {
	net.Conn
	in, out atomic.Int64
}

func (c *countConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.in.Add(int64(n))
	return n, err
}

func (c *countConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.out.Add(int64(n))
	return n, err
}

// oneConnListener serves exactly one connection, then reports closed once
// that connection is done.
type oneConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

type trackedConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func (t *trackedConn) Close() error {
	t.once.Do(func() { close(t.done) })
	return t.Conn.Close()
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = &trackedConn{Conn: l.conn, done: l.done} })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// intercept terminates the guest's TLS with a CA-signed certificate for
// host, then forwards each HTTP request upstream over real TLS after
// swapping placeholders and applying git rules.
func (p *Proxy) intercept(ctx context.Context, guest net.Conn, res sniff.Result, port uint16, upAddr string) (int64, int64) {
	host := res.Host
	cert, err := p.CA.Leaf(host)
	if err != nil {
		p.emit(Event{Type: "net.denied", Host: host, Reason: "proxy certificate: " + err.Error()})
		return 0, 0
	}
	cc := &countConn{Conn: &prefixConn{Conn: guest, r: io.MultiReader(bytes.NewReader(res.Buffered), guest)}}
	tc := tls.Server(cc, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"http/1.1"},
		MinVersion:   tls.VersionTLS12,
	})
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = tc.HandshakeContext(hctx)
	cancel()
	if err != nil {
		p.emit(Event{Type: "net.denied", Host: host, Reason: "TLS handshake with sandbox failed (is the sandboxd CA trusted in the VM?): " + err.Error()})
		return cc.out.Load(), cc.in.Load()
	}
	hostport := host
	if port != 443 {
		hostport = net.JoinHostPort(host, strconv.Itoa(int(port)))
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return p.dial(ctx, upAddr)
		},
		TLSClientConfig:     &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: p.UpstreamRoots},
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
	}
	defer tr.CloseIdleConnections()
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "https"
			pr.Out.URL.Host = hostport
			pr.Out.Host = hostport
			// Uncompressed responses let scrubResponse catch echoed secrets.
			pr.Out.Header.Set("Accept-Encoding", "identity")
		},
		Transport:     tr,
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			scrubResponse(resp, p.bindings())
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "sandboxd proxy: upstream error: "+err.Error(), http.StatusBadGateway)
		},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		used, err := p.authorize(r, host)
		if err != nil {
			p.emit(Event{Type: "secret.blocked", Host: host, Method: r.Method, Path: r.URL.Path, Reason: err.Error()})
			http.Error(w, "sandboxd proxy: "+err.Error(), http.StatusForbidden)
			return
		}
		for _, name := range used {
			p.emit(Event{Type: "secret.used", Host: host, Secret: name, Method: r.Method, Path: r.URL.Path})
		}
		rp.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	_ = srv.Serve(&oneConnListener{conn: tc, done: make(chan struct{})})
	return cc.out.Load(), cc.in.Load()
}

// scrubResponse replaces real secret values echoed back by a server with
// their placeholders, so a reflecting endpoint can't leak them into the
// VM. Compressed bodies are not inspected.
func scrubResponse(resp *http.Response, bs []Binding) {
	var pairs [][2][]byte
	for _, b := range bs {
		pairs = append(pairs, [2][]byte{[]byte(b.Secret.Value), []byte(b.Placeholder)})
		if b.Secret.Kind == secrets.KindGit {
			basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + b.Secret.Value))
			pairs = append(pairs, [2][]byte{[]byte(basic), []byte(b.Placeholder)})
		}
	}
	if len(pairs) == 0 {
		return
	}
	for k, vals := range resp.Header {
		for i, v := range vals {
			for _, pr := range pairs {
				v = strings.ReplaceAll(v, string(pr[0]), string(pr[1]))
			}
			resp.Header[k][i] = v
		}
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return
	}
	resp.Body = &scrubReader{r: resp.Body, pairs: pairs}
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
}

// scrubReader streams a body while replacing secret values, holding back
// enough bytes to catch values split across reads.
type scrubReader struct {
	r     io.ReadCloser
	pairs [][2][]byte
	buf   []byte
	out   []byte
	eof   bool
}

func (s *scrubReader) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.eof {
			if len(s.buf) == 0 {
				return 0, io.EOF
			}
			s.out, s.buf = s.replace(s.buf), nil
			break
		}
		tmp := make([]byte, 32<<10)
		n, err := s.r.Read(tmp)
		s.buf = append(s.buf, tmp[:n]...)
		if err == io.EOF {
			s.eof = true
		} else if err != nil {
			return 0, err
		}
		keep := 0
		for _, pr := range s.pairs {
			keep = max(keep, len(pr[0])-1)
		}
		if !s.eof && len(s.buf) > keep {
			// Replace complete matches first; an incomplete match can only
			// start in the last keep bytes, so hold those back.
			done := s.replace(s.buf)
			s.out = done[:len(done)-keep]
			s.buf = append([]byte(nil), done[len(done)-keep:]...)
		}
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

func (s *scrubReader) replace(b []byte) []byte {
	for _, pr := range s.pairs {
		b = bytes.ReplaceAll(b, pr[0], pr[1])
	}
	return b
}

func (s *scrubReader) Close() error { return s.r.Close() }

// replacePlaceholders swaps allowed placeholders in s for real values and
// reports which secrets were used or which disallowed one was found.
func replacePlaceholders(s, host string, bs []Binding) (string, []string, error) {
	var used []string
	for _, b := range bs {
		if !strings.Contains(s, b.Placeholder) {
			continue
		}
		if b.Secret.Kind != secrets.KindEnv || !b.Secret.MatchHost(host) {
			return s, nil, fmt.Errorf("secret %s may not be sent to %s", b.Secret.Name, host)
		}
		s = strings.ReplaceAll(s, b.Placeholder, b.Secret.Value)
		used = append(used, b.Secret.Name)
	}
	return s, used, nil
}

// authorize rewrites credentials in r for host and enforces git rules.
func (p *Proxy) authorize(r *http.Request, host string) ([]string, error) {
	bs := p.bindings()
	var used []string
	add := func(names []string) {
		for _, n := range names {
			dup := false
			for _, u := range used {
				dup = dup || u == n
			}
			if !dup {
				used = append(used, n)
			}
		}
	}
	for k, vals := range r.Header {
		for i, v := range vals {
			nv, u, err := replacePlaceholders(v, host, bs)
			if err != nil {
				return nil, err
			}
			// Basic auth hides placeholders in base64.
			if raw, ok := strings.CutPrefix(nv, "Basic "); ok {
				if dec, err := base64.StdEncoding.DecodeString(raw); err == nil {
					d2, u2, err := replacePlaceholders(string(dec), host, bs)
					if err != nil {
						return nil, err
					}
					if len(u2) > 0 {
						nv = "Basic " + base64.StdEncoding.EncodeToString([]byte(d2))
						u = append(u, u2...)
					}
				}
			}
			r.Header[k][i] = nv
			add(u)
		}
	}
	if q := r.URL.RawQuery; q != "" {
		nq, u, err := replacePlaceholders(q, host, bs)
		if err != nil {
			return nil, err
		}
		r.URL.RawQuery = nq
		add(u)
	}
	for _, b := range bs {
		if b.Secret.Kind != secrets.KindGit || !b.Secret.MatchHost(host) {
			continue
		}
		repo, service, ok := gitRequest(r)
		if !ok {
			continue
		}
		if !b.Secret.MatchRepo(repo) {
			if service == "git-receive-pack" {
				return nil, fmt.Errorf("git push to %s is not allowed by secret %s (allowed repos: %s)", repo, b.Secret.Name, strings.Join(b.Secret.Repos, ", "))
			}
			continue // no credentials: public repos still work
		}
		if service == "git-receive-pack" {
			if len(b.Secret.PushBranches) == 0 {
				return nil, fmt.Errorf("git push is disabled for secret %s (read-only)", b.Secret.Name)
			}
			if r.Method == http.MethodPost {
				if err := checkPush(r, b.Secret); err != nil {
					return nil, err
				}
			}
		}
		r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+b.Secret.Value)))
		add([]string{b.Secret.Name})
		break
	}
	return used, nil
}

// gitRequest recognizes git smart-HTTP requests: /owner/repo(.git)/info/refs
// ?service=..., /owner/repo(.git)/git-upload-pack, .../git-receive-pack.
func gitRequest(r *http.Request) (repo, service string, ok bool) {
	pth := strings.Trim(r.URL.Path, "/")
	var rest string
	switch {
	case strings.HasSuffix(pth, "/info/refs"):
		rest = strings.TrimSuffix(pth, "/info/refs")
		service = r.URL.Query().Get("service")
	case strings.HasSuffix(pth, "/git-upload-pack"):
		rest, service = strings.TrimSuffix(pth, "/git-upload-pack"), "git-upload-pack"
	case strings.HasSuffix(pth, "/git-receive-pack"):
		rest, service = strings.TrimSuffix(pth, "/git-receive-pack"), "git-receive-pack"
	default:
		return "", "", false
	}
	if service != "git-upload-pack" && service != "git-receive-pack" {
		return "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(rest, ".git"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return strings.ToLower(parts[0] + "/" + parts[1]), service, true
}

var errBadPkt = errors.New("malformed git push request")

// checkPush reads the ref-update commands at the start of a receive-pack
// request and rejects updates to branches the secret doesn't allow. The
// body is restored for forwarding.
func checkPush(r *http.Request, s secrets.Secret) error {
	body := io.Reader(r.Body)
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return errBadPkt
		}
		body = zr
		r.Header.Del("Content-Encoding")
		r.ContentLength = -1
		r.Header.Del("Content-Length")
	}
	br := bufio.NewReaderSize(body, 64<<10)
	var head bytes.Buffer
	for {
		lenHex := make([]byte, 4)
		if _, err := io.ReadFull(br, lenHex); err != nil {
			return errBadPkt
		}
		head.Write(lenHex)
		n, err := strconv.ParseUint(string(lenHex), 16, 16)
		if err != nil {
			return errBadPkt
		}
		if n == 0 {
			break // flush-pkt ends the command list
		}
		if n < 4 {
			return errBadPkt
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(br, line); err != nil {
			return errBadPkt
		}
		head.Write(line)
		cmd := string(line)
		if i := strings.IndexByte(cmd, 0); i >= 0 {
			cmd = cmd[:i]
		}
		f := strings.Fields(cmd)
		if len(f) < 3 {
			return errBadPkt
		}
		ref := f[2]
		branch, ok := strings.CutPrefix(ref, "refs/heads/")
		if !ok {
			return fmt.Errorf("push to %s is not allowed (only branches)", ref)
		}
		if !s.MatchPushBranch(branch) {
			return fmt.Errorf("push to branch %q is not allowed by secret %s (allowed: %s)", branch, s.Name, strings.Join(s.PushBranches, ", "))
		}
	}
	orig := r.Body
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head.Bytes()), br), orig}
	return nil
}
