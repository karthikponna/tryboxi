// Package egress decides and relays one outbound guest TCP connection:
// raw-IP rules dial directly; otherwise the TLS SNI / HTTP Host is peeked,
// checked against the policy, re-resolved on the host, and spliced. For
// hosts that a sandbox holds secrets for, TLS is terminated with the
// daemon's CA so placeholders can be swapped for real credentials.
package egress

import (
	"bytes"
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	sbxdns "github.com/karthikponna/tryboxi/internal/network/dns"
	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/network/sniff"
	"github.com/karthikponna/tryboxi/internal/secrets"
)

// Event is a policy decision or an audit record.
type Event struct {
	Type      string `json:"type"` // net.allowed | net.denied | net.ratelimited | secret.used | secret.blocked
	Host      string `json:"host,omitempty"`
	Dest      string `json:"dest,omitempty"`
	Reason    string `json:"reason,omitempty"`
	BytesUp   int64  `json:"bytes_up,omitempty"`
	BytesDown int64  `json:"bytes_down,omitempty"`
	Ms        int64  `json:"ms,omitempty"`
	Secret    string `json:"secret,omitempty"`
	Method    string `json:"method,omitempty"`
	Path      string `json:"path,omitempty"`
}

// Binding attaches a host-side secret to a sandbox under a placeholder.
type Binding struct {
	Placeholder string
	Secret      secrets.Secret
}

// Limits bound what one sandbox can do on the network.
type Limits struct {
	MaxConns       int     // concurrent outbound connections (0 = unlimited)
	ConnsPerMinute int     // new connections per minute (0 = unlimited)
	BytesPerSec    float64 // combined up+down bandwidth (0 = unlimited)
}

// Proxy relays a sandbox's outbound connections.
type Proxy struct {
	Policy     func() *policy.Policy
	DNS        *sbxdns.Handler
	Emit       func(Event)
	Bindings   func() []Binding
	CA         *CA
	LogAllowed bool
	Dialer     net.Dialer
	// Resolve, Dial, and UpstreamRoots are test hooks; defaults use
	// DNS.ResolveForDial, Dialer, and the system roots.
	Resolve       func(ctx context.Context, host string) (netip.Addr, error)
	Dial          func(ctx context.Context, network, addr string) (net.Conn, error)
	UpstreamRoots *x509.CertPool

	mu      sync.Mutex
	limits  Limits
	connLim *rate.Limiter
	bwLim   *rate.Limiter
	active  atomic.Int64
}

// SetLimits installs network limits.
func (p *Proxy) SetLimits(l Limits) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.limits = l
	p.connLim, p.bwLim = nil, nil
	if l.ConnsPerMinute > 0 {
		p.connLim = rate.NewLimiter(rate.Limit(float64(l.ConnsPerMinute)/60), max(1, l.ConnsPerMinute/6))
	}
	if l.BytesPerSec > 0 {
		p.bwLim = rate.NewLimiter(rate.Limit(l.BytesPerSec), max(64<<10, int(l.BytesPerSec/4)))
	}
}

func (p *Proxy) emit(e Event) {
	if p.Emit != nil {
		p.Emit(e)
	}
}

func (p *Proxy) bindings() []Binding {
	if p.Bindings == nil {
		return nil
	}
	return p.Bindings()
}

func (p *Proxy) resolve(ctx context.Context, host string) (netip.Addr, error) {
	if p.Resolve != nil {
		return p.Resolve(ctx, host)
	}
	return p.DNS.ResolveForDial(ctx, host)
}

func (p *Proxy) dial(ctx context.Context, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, "tcp", addr)
	}
	return p.Dialer.DialContext(ctx, "tcp", addr)
}

// admit applies the connection-count and rate limits.
func (p *Proxy) admit(dest string) bool {
	p.mu.Lock()
	lim, connLim := p.limits, p.connLim
	p.mu.Unlock()
	if lim.MaxConns > 0 && p.active.Load() >= int64(lim.MaxConns) {
		p.emit(Event{Type: "net.ratelimited", Dest: dest, Reason: "too many concurrent connections (limit " + strconv.Itoa(lim.MaxConns) + ")"})
		return false
	}
	if connLim != nil && !connLim.Allow() {
		p.emit(Event{Type: "net.ratelimited", Dest: dest, Reason: "too many new connections (limit " + strconv.Itoa(lim.ConnsPerMinute) + "/min)"})
		return false
	}
	return true
}

// Handle owns guest (already accepted) whose original destination was
// dst:port, and returns when the connection is done.
func (p *Proxy) Handle(ctx context.Context, guest net.Conn, dst netip.Addr, port uint16) {
	defer guest.Close()
	pol := p.Policy()
	destStr := net.JoinHostPort(dst.String(), strconv.Itoa(int(port)))
	if pol.Mode == policy.ModeNone {
		return
	}
	if policy.Blocked(dst) {
		p.emit(Event{Type: "net.denied", Dest: destStr, Reason: "blocked address range"})
		return
	}
	if !p.admit(destStr) {
		return
	}
	p.active.Add(1)
	defer p.active.Add(-1)
	start := time.Now()
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if pol.AllowIP(dst, port) {
		up, err := p.dial(dctx, destStr)
		if err != nil {
			return
		}
		upB, downB := p.splice(guest, up)
		p.logAllowed("", destStr, upB, downB, start)
		return
	}
	if !pol.HostPort(port) {
		p.emit(Event{Type: "net.denied", Dest: destStr, Reason: "port not allowed for hostname rules"})
		return
	}
	res, _ := sniff.Peek(guest, 5*time.Second)
	if res.Host == "" {
		p.emit(Event{Type: "net.denied", Dest: destStr, Reason: "no TLS SNI or HTTP Host to match against the allowlist"})
		return
	}
	// Placeholders in cleartext HTTP are never forwarded: secrets are only
	// injected over TLS, and this catches exfiltration attempts.
	if res.Kind == sniff.KindHTTP {
		for _, b := range p.bindings() {
			if bytes.Contains(res.Buffered, []byte(b.Placeholder)) {
				p.emit(Event{Type: "secret.blocked", Host: res.Host, Dest: destStr, Secret: b.Secret.Name, Reason: "secret placeholder sent over cleartext HTTP"})
				return
			}
		}
	}
	if !pol.AllowHostPort(res.Host, port) && !p.secretHost(res.Host) {
		p.emit(Event{Type: "net.denied", Host: res.Host, Dest: destStr, Reason: "host not in allowlist"})
		return
	}
	ip, err := p.resolve(dctx, res.Host)
	if err != nil {
		p.emit(Event{Type: "net.denied", Host: res.Host, Dest: destStr, Reason: "resolve: " + err.Error()})
		return
	}
	upAddr := net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
	if res.Kind == sniff.KindTLS && p.CA != nil && p.secretHost(res.Host) {
		cancel()
		upB, downB := p.intercept(ctx, guest, res, port, upAddr)
		p.logAllowed(res.Host, destStr, upB, downB, start)
		return
	}
	up, err := p.dial(dctx, upAddr)
	if err != nil {
		return
	}
	if _, err := up.Write(res.Buffered); err != nil {
		up.Close()
		return
	}
	upB, downB := p.splice(guest, up)
	p.logAllowed(res.Host, destStr, upB+int64(len(res.Buffered)), downB, start)
}

// SecretHost reports whether any bound secret may be sent to host. Those
// hosts are reachable even when not on the allowlist: binding a secret to
// a host is an explicit decision by the user.
func (p *Proxy) SecretHost(host string) bool {
	return p.secretHost(host)
}

func (p *Proxy) secretHost(host string) bool {
	for _, b := range p.bindings() {
		if b.Secret.MatchHost(host) {
			return true
		}
	}
	return false
}

func (p *Proxy) logAllowed(host, dest string, up, down int64, start time.Time) {
	if !p.LogAllowed {
		return
	}
	p.emit(Event{Type: "net.allowed", Host: host, Dest: dest, BytesUp: up, BytesDown: down, Ms: time.Since(start).Milliseconds()})
}

type closeWriter interface{ CloseWrite() error }

// splice copies both ways (bandwidth-limited) with half-close; it returns
// bytes sent guest->upstream and upstream->guest.
func (p *Proxy) splice(guest, up net.Conn) (int64, int64) {
	p.mu.Lock()
	bw := p.bwLim
	p.mu.Unlock()
	var upN, downN int64
	var wg sync.WaitGroup
	cp := func(dst, src net.Conn, n *int64) {
		defer wg.Done()
		*n = copyLimited(dst, src, bw)
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	wg.Add(2)
	go cp(up, guest, &upN)
	go cp(guest, up, &downN)
	wg.Wait()
	guest.Close()
	up.Close()
	return upN, downN
}

func copyLimited(dst io.Writer, src io.Reader, lim *rate.Limiter) int64 {
	buf := make([]byte, 32<<10)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if lim != nil {
				_ = lim.WaitN(context.Background(), min(n, lim.Burst()))
			}
			w, werr := dst.Write(buf[:n])
			total += int64(w)
			if werr != nil {
				return total
			}
		}
		if err != nil {
			return total
		}
	}
}

// Splice copies both ways with half-close, then closes both (no limits).
func Splice(a, b net.Conn) {
	(&Proxy{}).splice(a, b)
}
