package egress

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	sbxdns "github.com/karthikponna/tryboxi/internal/network/dns"
	"github.com/karthikponna/tryboxi/internal/network/policy"
)

type resolver map[string][]netip.Addr

func (r resolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ips, ok := r[host]; ok {
		return ips, nil
	}
	return nil, &net.DNSError{Err: "nx", Name: host, IsNotFound: true}
}

func TestDeniedHostIsClosed(t *testing.T) {
	pol := policy.MustCompile(policy.Spec{Mode: policy.ModeAllowlist, Allow: []string{"allowed.example"}})
	var events []Event
	p := &Proxy{Policy: func() *policy.Policy { return pol }, DNS: &sbxdns.Handler{Policy: pol, Resolver: resolver{}},
		Emit: func(e Event) { events = append(events, e) }}
	guest, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		p.Handle(context.Background(), guest, netip.MustParseAddr("93.184.216.34"), 80)
		close(done)
	}()
	_, _ = client.Write([]byte("GET / HTTP/1.1\r\nHost: denied.example\r\n\r\n"))
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadAll(client); err != nil && err != io.EOF {
		// pipe closed by proxy: expected
	}
	<-done
	if len(events) != 1 || events[0].Host != "denied.example" {
		t.Fatalf("events=%+v", events)
	}
}

func TestBlockedRangeNeverDialed(t *testing.T) {
	pol := policy.MustCompile(policy.Spec{Mode: policy.ModeFull})
	var events []Event
	p := &Proxy{Policy: func() *policy.Policy { return pol }, DNS: &sbxdns.Handler{Policy: pol, Resolver: resolver{}},
		Emit: func(e Event) { events = append(events, e) }}
	guest, _ := net.Pipe()
	p.Handle(context.Background(), guest, netip.MustParseAddr("169.254.169.254"), 80)
	if len(events) != 1 || events[0].Reason != "blocked address range" {
		t.Fatalf("events=%+v", events)
	}
}
