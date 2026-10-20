// Package dns answers guest DNS queries according to the sandbox policy:
// allowed names are resolved on the host, everything else gets NXDOMAIN.
// Answers that point into blocked ranges are dropped (DNS rebinding).
package dns

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"

	mdns "github.com/miekg/dns"

	"github.com/karthikponna/tryboxi/internal/network/policy"
)

// Resolver looks names up on the host.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DenyFunc is called for every refused query.
type DenyFunc func(name, reason string)

// Handler answers DNS messages.
type Handler struct {
	Policy   *policy.Policy
	Resolver Resolver
	OnDeny   DenyFunc
	TTL      uint32
}

// New returns a handler using the host's resolver.
func New(p *policy.Policy, onDeny DenyFunc) *Handler {
	return &Handler{Policy: p, Resolver: net.DefaultResolver, OnDeny: onDeny, TTL: 30}
}

// Answer builds a response for the raw query in q.
func (h *Handler) Answer(ctx context.Context, q []byte) ([]byte, error) {
	var req mdns.Msg
	if err := req.Unpack(q); err != nil {
		return nil, err
	}
	resp := new(mdns.Msg)
	resp.SetReply(&req)
	resp.RecursionAvailable = true
	if len(req.Question) != 1 {
		resp.Rcode = mdns.RcodeFormatError
		return resp.Pack()
	}
	qq := req.Question[0]
	name := strings.TrimSuffix(strings.ToLower(qq.Name), ".")
	if !h.Policy.AllowHost(name) {
		if h.OnDeny != nil {
			h.OnDeny(name, "not in allowlist")
		}
		resp.Rcode = mdns.RcodeNameError
		return resp.Pack()
	}
	switch qq.Qtype {
	case mdns.TypeA, mdns.TypeAAAA:
	default:
		// Only address records are served; the guest stack is IPv4 only.
		return resp.Pack()
	}
	if qq.Qtype == mdns.TypeAAAA {
		return resp.Pack()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := h.Resolver.LookupNetIP(ctx, "ip4", name)
	if err != nil {
		if dnsErr, ok := err.(*net.DNSError); ok && dnsErr.IsNotFound {
			resp.Rcode = mdns.RcodeNameError
		} else {
			resp.Rcode = mdns.RcodeServerFailure
		}
		return resp.Pack()
	}
	for _, ip := range ips {
		if !h.Policy.AllowResolvedIP(ip) {
			if h.OnDeny != nil {
				h.OnDeny(name, "resolves to blocked address "+ip.String())
			}
			continue
		}
		resp.Answer = append(resp.Answer, &mdns.A{
			Hdr: mdns.RR_Header{Name: qq.Name, Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: h.TTL},
			A:   net.IP(ip.Unmap().AsSlice()),
		})
	}
	return resp.Pack()
}

// ResolveForDial resolves an allowed host for a proxy dial, returning the
// first address that passes the policy.
func (h *Handler) ResolveForDial(ctx context.Context, host string) (netip.Addr, error) {
	ips, err := h.Resolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return netip.Addr{}, err
	}
	for _, ip := range ips {
		if h.Policy.AllowResolvedIP(ip) {
			return ip.Unmap(), nil
		}
	}
	return netip.Addr{}, &net.DNSError{Err: "no allowed address", Name: host, IsNotFound: true}
}
