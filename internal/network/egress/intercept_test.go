package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	sbxdns "github.com/karthikponna/tryboxi/internal/network/dns"
	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/secrets"
)

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) add(e Event) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() }
func (r *recorder) has(typ, contains string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.Type == typ && strings.Contains(fmt.Sprintf("%+v", e), contains) {
			return true
		}
	}
	return false
}

// upstream records what the real server receives.
func upstream(t *testing.T) (*httptest.Server, chan *http.Request) {
	seen := make(chan *http.Request, 10)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r2 := r.Clone(context.Background())
		r2.Body = io.NopCloser(strings.NewReader(string(body)))
		seen <- r2
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func newProxy(t *testing.T, srv *httptest.Server, bs []Binding, rec *recorder) *Proxy {
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pol := policy.MustCompile(policy.Spec{Mode: policy.ModeAllowlist, Allow: []string{"registry.npmjs.org"}})
	roots := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	return &Proxy{
		Policy:   func() *policy.Policy { return pol },
		DNS:      &sbxdns.Handler{Policy: pol},
		Emit:     rec.add,
		Bindings: func() []Binding { return bs },
		CA:       ca,
		Resolve: func(context.Context, string) (netip.Addr, error) {
			return netip.MustParseAddr("93.184.216.34"), nil
		},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		},
		UpstreamRoots: roots,
		LogAllowed:    true,
	}
}

// guestRequest sends one HTTPS request "from the sandbox" through the proxy.
func guestRequest(t *testing.T, p *Proxy, host string, req *http.Request) (*http.Response, string) {
	t.Helper()
	guestSide, proxySide := net.Pipe()
	done := make(chan struct{})
	go func() {
		p.Handle(context.Background(), proxySide, netip.MustParseAddr("93.184.216.34"), 443)
		close(done)
	}()
	tc := tls.Client(guestSide, &tls.Config{ServerName: host, RootCAs: p.CA.Pool()})
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tc.Handshake(); err != nil {
		t.Fatalf("guest TLS handshake: %v", err)
	}
	req.Host = host
	req.Close = true
	if err := req.Write(tc); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	tc.Close()
	<-done
	return resp, string(b)
}

func TestPlaceholderSwappedForAllowedHost(t *testing.T) {
	srv, seen := upstream(t)
	rec := &recorder{}
	bs := []Binding{{Placeholder: "sbxph_openai_abc", Secret: secrets.Secret{Name: "OPENAI_API_KEY", Kind: secrets.KindEnv, Value: "sk-real-secret-123", Hosts: []string{"example.com"}}}}
	p := newProxy(t, srv, bs, rec)
	req, _ := http.NewRequest("POST", "https://example.com/v1/chat", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer sbxph_openai_abc")
	resp, body := guestRequest(t, p, "example.com", req)
	if resp.StatusCode != 200 || body != "ok" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	got := <-seen
	if got.Header.Get("Authorization") != "Bearer sk-real-secret-123" {
		t.Fatalf("upstream got %q", got.Header.Get("Authorization"))
	}
	if !rec.has("secret.used", "OPENAI_API_KEY") || !rec.has("net.allowed", "example.com") {
		t.Fatalf("events %+v", rec.events)
	}
}

func TestPlaceholderToOtherHostBlocked(t *testing.T) {
	srv, seen := upstream(t)
	rec := &recorder{}
	bs := []Binding{
		{Placeholder: "sbxph_openai_abc", Secret: secrets.Secret{Name: "OPENAI_API_KEY", Kind: secrets.KindEnv, Value: "sk-real-secret-123", Hosts: []string{"api.openai.com"}}},
		{Placeholder: "sbxph_other_xyz", Secret: secrets.Secret{Name: "OTHER", Kind: secrets.KindEnv, Value: "other-secret-456", Hosts: []string{"example.com"}}},
	}
	p := newProxy(t, srv, bs, rec)
	req, _ := http.NewRequest("GET", "https://example.com/steal", nil)
	req.Header.Set("X-Exfil", "sbxph_openai_abc")
	resp, _ := guestRequest(t, p, "example.com", req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
	select {
	case r := <-seen:
		t.Fatalf("request reached upstream: %v", r.Header)
	default:
	}
	if !rec.has("secret.blocked", "OPENAI_API_KEY") {
		t.Fatalf("events %+v", rec.events)
	}
}

func TestGitProxy(t *testing.T) {
	srv, seen := upstream(t)
	rec := &recorder{}
	gitSecret := secrets.Secret{Name: "GITHUB", Kind: secrets.KindGit, Value: "ghp_realtoken", Hosts: []string{"example.com"},
		Repos: []string{"acme/app"}, PushBranches: []string{"sbx/*"}}
	p := newProxy(t, srv, []Binding{{Placeholder: "sbxph_github_q", Secret: gitSecret}}, rec)
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_realtoken"))

	// Fetch: token added.
	req, _ := http.NewRequest("GET", "https://example.com/acme/app.git/info/refs?service=git-upload-pack", nil)
	resp, _ := guestRequest(t, p, "example.com", req)
	if resp.StatusCode != 200 {
		t.Fatalf("fetch status %d", resp.StatusCode)
	}
	if got := (<-seen).Header.Get("Authorization"); got != wantAuth {
		t.Fatalf("fetch auth %q", got)
	}

	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	zero := strings.Repeat("0", 40)
	push := func(ref string) *http.Request {
		body := pkt(zero+" "+strings.Repeat("a", 40)+" "+ref+"\x00report-status") + "0000" + "PACKDATA"
		r, _ := http.NewRequest("POST", "https://example.com/acme/app.git/git-receive-pack", strings.NewReader(body))
		return r
	}
	// Push to an allowed branch: forwarded with the full body.
	resp, _ = guestRequest(t, p, "example.com", push("refs/heads/sbx/fix-1"))
	if resp.StatusCode != 200 {
		t.Fatalf("allowed push status %d", resp.StatusCode)
	}
	got := <-seen
	b, _ := io.ReadAll(got.Body)
	if got.Header.Get("Authorization") != wantAuth || !strings.HasSuffix(string(b), "0000PACKDATA") {
		t.Fatalf("push forwarded wrong: auth=%q body=%q", got.Header.Get("Authorization"), b)
	}
	// Push to main: rejected before reaching upstream.
	resp, body := guestRequest(t, p, "example.com", push("refs/heads/main"))
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "main") {
		t.Fatalf("main push: %d %q", resp.StatusCode, body)
	}
	// Other repo: no credentials for reads, pushes refused.
	req, _ = http.NewRequest("GET", "https://example.com/evil/repo.git/info/refs?service=git-upload-pack", nil)
	guestRequest(t, p, "example.com", req)
	if got := (<-seen).Header.Get("Authorization"); got != "" {
		t.Fatalf("token leaked to other repo: %q", got)
	}
	req, _ = http.NewRequest("GET", "https://example.com/evil/repo.git/info/refs?service=git-receive-pack", nil)
	if resp, _ := guestRequest(t, p, "example.com", req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("push discovery to other repo: %d", resp.StatusCode)
	}
}

type chunkReader struct {
	chunks []string
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	c.chunks = c.chunks[1:]
	return n, nil
}
func (c *chunkReader) Close() error { return nil }

func TestScrubReaderAcrossChunks(t *testing.T) {
	pairs := [][2][]byte{{[]byte("SECRETVALUE123"), []byte("sbxph_x")}}
	for split := 1; split < 14; split++ {
		body := "before SECRETVALUE123 middle SECRETVALUE123 end"
		i := strings.Index(body, "SECRETVALUE123") + split
		r := &scrubReader{r: &chunkReader{chunks: []string{body[:i], body[i:]}}, pairs: pairs}
		out, _ := io.ReadAll(r)
		if string(out) != "before sbxph_x middle sbxph_x end" {
			t.Fatalf("split %d: %q", split, out)
		}
	}
}

func TestEchoedSecretScrubbed(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"auth":%q}`, r.Header.Get("Authorization"))
	}))
	t.Cleanup(srv.Close)
	rec := &recorder{}
	bs := []Binding{{Placeholder: "sbxph_t_1", Secret: secrets.Secret{Name: "T", Kind: secrets.KindEnv, Value: "real-token-999", Hosts: []string{"example.com"}}}}
	p := newProxy(t, srv, bs, rec)
	req, _ := http.NewRequest("GET", "https://example.com/headers", nil)
	req.Header.Set("Authorization", "Bearer sbxph_t_1")
	_, body := guestRequest(t, p, "example.com", req)
	if strings.Contains(body, "real-token-999") || !strings.Contains(body, "Bearer sbxph_t_1") {
		t.Fatalf("echoed body: %s", body)
	}
}

func TestEchoUncompressedEvenIfClientAsksGzip(t *testing.T) {
	var gotAE string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAE = r.Header.Get("Accept-Encoding")
		fmt.Fprintf(w, `{"auth":%q}`, r.Header.Get("Authorization"))
	}))
	t.Cleanup(srv.Close)
	bs := []Binding{{Placeholder: "sbxph_t_1", Secret: secrets.Secret{Name: "T", Kind: secrets.KindEnv, Value: "real-token-999", Hosts: []string{"example.com"}}}}
	p := newProxy(t, srv, bs, &recorder{})
	req, _ := http.NewRequest("GET", "https://example.com/gzip", nil)
	req.Header.Set("Authorization", "Bearer sbxph_t_1")
	req.Header.Set("Accept-Encoding", "gzip")
	_, body := guestRequest(t, p, "example.com", req)
	if gotAE != "identity" || strings.Contains(body, "real-token-999") {
		t.Fatalf("accept-encoding=%q body=%s", gotAE, body)
	}
}

func TestCleartextPlaceholderBlocked(t *testing.T) {
	srv, _ := upstream(t)
	rec := &recorder{}
	bs := []Binding{{Placeholder: "sbxph_k_1", Secret: secrets.Secret{Name: "K", Kind: secrets.KindEnv, Value: "real-value-1", Hosts: []string{"example.com"}}}}
	p := newProxy(t, srv, bs, rec)
	guestSide, proxySide := net.Pipe()
	go func() {
		_, _ = guestSide.Write([]byte("GET /?k=sbxph_k_1 HTTP/1.1\r\nHost: registry.npmjs.org\r\n\r\n"))
	}()
	p.Handle(context.Background(), proxySide, netip.MustParseAddr("93.184.216.34"), 80)
	if !rec.has("secret.blocked", "cleartext") {
		t.Fatalf("events %+v", rec.events)
	}
}

func TestRateLimit(t *testing.T) {
	srv, _ := upstream(t)
	rec := &recorder{}
	p := newProxy(t, srv, nil, rec)
	p.SetLimits(Limits{ConnsPerMinute: 6})
	for i := 0; i < 3; i++ {
		guestSide, proxySide := net.Pipe()
		guestSide.Close()
		p.Handle(context.Background(), proxySide, netip.MustParseAddr("93.184.216.34"), 443)
	}
	if !rec.has("net.ratelimited", "new connections") {
		t.Fatalf("events %+v", rec.events)
	}
}
