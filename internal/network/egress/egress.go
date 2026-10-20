// Package egress decides and relays one outbound guest TCP connection:
// raw-IP rules dial directly; otherwise the TLS SNI / HTTP Host is peeked,
// checked against the policy, re-resolved on the host, and spliced.
package egress

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	sbxdns "github.com/karthikponna/tryboxi/internal/network/dns"
	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/network/sniff"
)

// Event is a policy decision.
type Event struct {
	Type, Host, Dest, Reason string
}

// Proxy relays guest connections.
type Proxy struct {
	Policy func() *policy.Policy
	DNS    *sbxdns.Handler
	Emit   func(Event)
	Dialer net.Dialer
}

func (p *Proxy) emit(e Event) {
	if p.Emit != nil {
		p.Emit(e)
	}
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
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if pol.AllowIP(dst, port) {
		up, err := p.Dialer.DialContext(dctx, "tcp", destStr)
		if err != nil {
			return
		}
		Splice(guest, up)
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
	if !pol.AllowHostPort(res.Host, port) {
		p.emit(Event{Type: "net.denied", Host: res.Host, Dest: destStr, Reason: "host not in allowlist"})
		return
	}
	ip, err := p.DNS.ResolveForDial(dctx, res.Host)
	if err != nil {
		p.emit(Event{Type: "net.denied", Host: res.Host, Dest: destStr, Reason: "resolve: " + err.Error()})
		return
	}
	up, err := p.Dialer.DialContext(dctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(int(port))))
	if err != nil {
		return
	}
	if _, err := up.Write(res.Buffered); err != nil {
		up.Close()
		return
	}
	Splice(guest, up)
}

type closeWriter interface{ CloseWrite() error }

// Splice copies both ways with half-close, then closes both.
func Splice(a, b net.Conn) {
	var wg sync.WaitGroup
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	wg.Add(2)
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}
