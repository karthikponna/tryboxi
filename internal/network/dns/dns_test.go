package dns

import (
	"context"
	"net"
	"net/netip"
	"testing"

	mdns "github.com/miekg/dns"

	"github.com/karthikponna/tryboxi/internal/network/policy"
)

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ips, ok := f[host]; ok {
		return ips, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func query(t *testing.T, h *Handler, name string) *mdns.Msg {
	t.Helper()
	m := new(mdns.Msg)
	m.SetQuestion(mdns.Fqdn(name), mdns.TypeA)
	b, _ := m.Pack()
	out, err := h.Answer(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	var r mdns.Msg
	if err := r.Unpack(out); err != nil {
		t.Fatal(err)
	}
	return &r
}

func TestAnswer(t *testing.T) {
	var denied []string
	h := &Handler{
		Policy: policy.MustCompile(policy.Spec{Mode: policy.ModeAllowlist, Allow: []string{"github.com", "rebind.example"}}),
		Resolver: fakeResolver{
			"github.com":     {netip.MustParseAddr("140.82.112.3")},
			"rebind.example": {netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("192.168.1.5")},
		},
		OnDeny: func(name, reason string) { denied = append(denied, name) },
	}
	r := query(t, h, "github.com")
	if r.Rcode != mdns.RcodeSuccess || len(r.Answer) != 1 || r.Answer[0].(*mdns.A).A.String() != "140.82.112.3" {
		t.Fatalf("github: %v", r)
	}
	if r := query(t, h, "example.com"); r.Rcode != mdns.RcodeNameError {
		t.Fatalf("example.com should be NXDOMAIN: %v", r)
	}
	if r := query(t, h, "rebind.example"); len(r.Answer) != 0 {
		t.Fatalf("rebinding answers must be dropped: %v", r)
	}
	if len(denied) != 3 {
		t.Fatalf("denied=%v", denied)
	}
}
