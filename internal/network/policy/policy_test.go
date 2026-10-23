package policy

import (
	"net/netip"
	"testing"
)

func TestAllowlist(t *testing.T) {
	p := MustCompile(Spec{Mode: ModeAllowlist, Allow: append(DefaultAllow, "203.0.113.0/24"), Deny: []string{"evil.npmjs.org"}})
	cases := map[string]bool{
		"registry.npmjs.org":        true,
		"REGISTRY.NPMJS.ORG.":       true,
		"foo.npmjs.org":             true,
		"npmjs.org":                 false, // wildcard does not match apex
		"evil.npmjs.org":            false, // deny beats allow
		"github.com":                true,
		"raw.githubusercontent.com": true,
		"example.com":               false,
		"localhost":                 false,
		"metadata.google.internal":  false,
	}
	for host, want := range cases {
		if got := p.AllowHost(host); got != want {
			t.Errorf("AllowHost(%q)=%v want %v", host, got, want)
		}
	}
	if !p.AllowHostPort("github.com", 443) || p.AllowHostPort("github.com", 22) {
		t.Error("port rules wrong")
	}
	if !p.AllowIP(netip.MustParseAddr("203.0.113.9"), 5432) {
		t.Error("CIDR allow failed")
	}
	if p.AllowIP(netip.MustParseAddr("8.8.8.8"), 53) {
		t.Error("unlisted IP allowed")
	}
}

func TestAlwaysBlocked(t *testing.T) {
	full := MustCompile(Spec{Mode: ModeFull})
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "172.20.0.1", "169.254.169.254", "::1", "fe80::1", "fd00::1", "::ffff:127.0.0.1", "100.100.100.100"} {
		a := netip.MustParseAddr(ip)
		if full.AllowIP(a, 80) || full.AllowResolvedIP(a) {
			t.Errorf("%s should always be blocked", ip)
		}
	}
	if !full.AllowIP(netip.MustParseAddr("1.1.1.1"), 443) {
		t.Error("full mode should allow public IPs")
	}
	if !full.AllowHost("anything.example") {
		t.Error("full mode should allow any public name")
	}
}

func TestNone(t *testing.T) {
	p := MustCompile(Spec{Mode: ModeNone, Allow: []string{"github.com"}})
	if p.AllowHost("github.com") || p.AllowIP(netip.MustParseAddr("1.1.1.1"), 443) {
		t.Error("none mode must deny everything")
	}
}

func TestPresets(t *testing.T) {
	p := MustCompile(Spec{Mode: ModeAllowlist, Allow: []string{"@docker", "@browsers"}})
	for _, h := range []string{"registry-1.docker.io", "ghcr.io", "cdn.playwright.dev"} {
		if !p.AllowHost(h) {
			t.Errorf("%s should be allowed by presets", h)
		}
	}
	if p.AllowHost("pypi.org") {
		t.Error("python preset not requested")
	}
	if _, err := Compile(Spec{Allow: []string{"@nope"}}); err == nil {
		t.Error("unknown preset must fail")
	}
}

func TestInvalid(t *testing.T) {
	for _, e := range []string{"a*b.com", "http://x.com", "foo bar"} {
		if _, err := Compile(Spec{Allow: []string{e}}); err == nil {
			t.Errorf("expected error for %q", e)
		}
	}
	if _, err := Compile(Spec{Mode: "weird"}); err == nil {
		t.Error("expected error for bad mode")
	}
}
