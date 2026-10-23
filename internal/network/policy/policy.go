// Package policy decides which outbound connections a sandbox may make.
// It is shared by the Linux (nftables + proxy) and macOS (userspace stack)
// enforcement points.
package policy

import (
	"fmt"
	"net/netip"
	"strings"
)

// Mode is the network mode of a sandbox.
type Mode string

const (
	ModeNone      Mode = "none"
	ModeAllowlist Mode = "allowlist"
	ModeFull      Mode = "full"
)

// DefaultAllow is the built-in allowlist: package registries, GitHub, and
// Ubuntu mirrors.
var DefaultAllow = []string{
	"registry.npmjs.org", "*.npmjs.org", "registry.yarnpkg.com", "*.yarnpkg.com",
	"pypi.org", "files.pythonhosted.org",
	"github.com", "*.github.com", "*.githubusercontent.com", "codeload.github.com",
	"proxy.golang.org", "sum.golang.org", "storage.googleapis.com",
	"crates.io", "static.crates.io", "index.crates.io",
	"ports.ubuntu.com", "archive.ubuntu.com", "security.ubuntu.com",
	"deb.nodesource.com",
}

// Presets are named host lists for common ecosystems, referenced as
// "@name" in allow/deny lists. They include the CDNs real installs hit
// (prebuilt binaries, browser downloads), not just the registry API.
var Presets = map[string][]string{
	"node": {"registry.npmjs.org", "*.npmjs.org", "registry.yarnpkg.com", "*.yarnpkg.com", "repo.yarnpkg.com",
		"nodejs.org", "registry.npmmirror.com", "binaries.prisma.sh", "deb.nodesource.com", "bun.sh"},
	"python": {"pypi.org", "files.pythonhosted.org", "download.pytorch.org", "astral.sh", "*.anaconda.org", "conda.anaconda.org"},
	"go":     {"proxy.golang.org", "sum.golang.org", "go.dev", "golang.org", "dl.google.com", "storage.googleapis.com"},
	"rust":   {"crates.io", "static.crates.io", "index.crates.io", "static.rust-lang.org", "sh.rustup.rs"},
	"ubuntu": {"ports.ubuntu.com", "archive.ubuntu.com", "security.ubuntu.com", "ppa.launchpadcontent.net", "keyserver.ubuntu.com"},
	"github": {"github.com", "*.github.com", "*.githubusercontent.com", "codeload.github.com"},
	// Docker Hub serves layers from several CDN hosts under docker.com.
	"docker": {"docker.io", "*.docker.io", "*.docker.com", "ghcr.io", "pkg-containers.githubusercontent.com",
		"quay.io", "*.quay.io", "public.ecr.aws", "mcr.microsoft.com", "*.data.mcr.microsoft.com"},
	"browsers": {"playwright.azureedge.net", "playwright-akamai.azureedge.net", "playwright-verizon.azureedge.net",
		"cdn.playwright.dev", "googlechromelabs.github.io", "storage.googleapis.com", "edgedl.me.gvt1.com", "dl.google.com"},
	"huggingface": {"huggingface.co", "*.huggingface.co", "*.hf.co"},
}

// DefaultPresets are enabled unless config overrides them.
var DefaultPresets = []string{"node", "python", "go", "rust", "ubuntu", "github", "docker"}

// Expand replaces "@preset" entries with their host lists.
func Expand(entries []string) ([]string, error) {
	var out []string
	for _, e := range entries {
		name, ok := strings.CutPrefix(strings.TrimSpace(e), "@")
		if !ok {
			out = append(out, e)
			continue
		}
		hosts, found := Presets[strings.ToLower(name)]
		if !found {
			return nil, fmt.Errorf("unknown preset @%s", name)
		}
		out = append(out, hosts...)
	}
	return out, nil
}

// alwaysBlocked are destinations no sandbox may reach in any mode: the host,
// the LAN, link-local (cloud metadata), CGNAT, multicast, and IPv6 local.
var alwaysBlocked = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15",
	"224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
	"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "::ffff:0:0/96",
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// Policy is an immutable, compiled allow/deny policy.
type Policy struct {
	Mode       Mode
	allowNames []string
	allowCIDR  []netip.Prefix
	denyNames  []string
	denyCIDR   []netip.Prefix
	allowPorts map[uint16]bool // extra ports for allowlisted names (default 80, 443)
}

// Spec is the user-facing policy description.
type Spec struct {
	Mode  Mode     `json:"mode" yaml:"mode"`
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty" yaml:"deny,omitempty"`
	// Ports allowed for allowlisted hostnames besides 80 and 443 (e.g. 22
	// for git over SSH). Hostname matching needs TLS SNI or HTTP Host, so
	// other ports are only allowed for CIDR entries.
	Ports []uint16 `json:"ports,omitempty" yaml:"ports,omitempty"`
}

// Compile validates spec. Entries are domain names, "*.domain" wildcards,
// IP addresses, or CIDRs.
func Compile(spec Spec) (*Policy, error) {
	p := &Policy{Mode: spec.Mode, allowPorts: map[uint16]bool{80: true, 443: true}}
	switch p.Mode {
	case "":
		p.Mode = ModeAllowlist
	case ModeNone, ModeAllowlist, ModeFull:
	default:
		return nil, fmt.Errorf("unknown network mode %q", spec.Mode)
	}
	for _, port := range spec.Ports {
		p.allowPorts[port] = true
	}
	add := func(entries []string, names *[]string, cidrs *[]netip.Prefix) error {
		entries, err := Expand(entries)
		if err != nil {
			return err
		}
		for _, e := range entries {
			e = strings.ToLower(strings.TrimSpace(e))
			if e == "" {
				continue
			}
			if pfx, err := netip.ParsePrefix(e); err == nil {
				*cidrs = append(*cidrs, pfx.Masked())
				continue
			}
			if ip, err := netip.ParseAddr(e); err == nil {
				*cidrs = append(*cidrs, netip.PrefixFrom(ip, ip.BitLen()))
				continue
			}
			name := strings.TrimSuffix(e, ".")
			if strings.ContainsAny(name, " /:") || (strings.Contains(name, "*") && !strings.HasPrefix(name, "*.")) {
				return fmt.Errorf("invalid policy entry %q", e)
			}
			*names = append(*names, name)
		}
		return nil
	}
	if err := add(spec.Allow, &p.allowNames, &p.allowCIDR); err != nil {
		return nil, err
	}
	if err := add(spec.Deny, &p.denyNames, &p.denyCIDR); err != nil {
		return nil, err
	}
	return p, nil
}

// MustCompile panics on error; for defaults and tests.
func MustCompile(spec Spec) *Policy {
	p, err := Compile(spec)
	if err != nil {
		panic(err)
	}
	return p
}

func matchName(pattern, name string) bool {
	if strings.HasPrefix(pattern, "*.") {
		return strings.HasSuffix(name, pattern[1:]) && len(name) > len(pattern)-1
	}
	return pattern == name
}

func normalize(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

// AllowHost reports whether a hostname may be resolved and connected to.
// Deny entries beat allow entries.
func (p *Policy) AllowHost(name string) bool {
	if p.Mode == ModeNone {
		return false
	}
	n := normalize(name)
	if n == "" || n == "localhost" || strings.HasSuffix(n, ".localhost") || strings.HasSuffix(n, ".local") || strings.HasSuffix(n, ".internal") {
		return false
	}
	for _, d := range p.denyNames {
		if matchName(d, n) {
			return false
		}
	}
	if p.Mode == ModeFull {
		return true
	}
	for _, a := range p.allowNames {
		if matchName(a, n) {
			return true
		}
	}
	return false
}

// AllowHostPort reports whether a hostname-matched connection on port is OK.
func (p *Policy) AllowHostPort(name string, port uint16) bool {
	if !p.AllowHost(name) {
		return false
	}
	return p.Mode == ModeFull || p.allowPorts[port]
}

// HostPort reports whether hostname-matched connections may use port.
func (p *Policy) HostPort(port uint16) bool {
	return p.Mode == ModeFull || (p.Mode == ModeAllowlist && p.allowPorts[port])
}

// Blocked reports whether ip is in an always-blocked range.
func Blocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return true
	}
	for _, b := range alwaysBlocked {
		if b.Contains(ip) {
			return true
		}
	}
	return false
}

// AllowIP reports whether a connection to ip:port may be made without a
// hostname (raw IP), e.g. for non-HTTP protocols.
func (p *Policy) AllowIP(ip netip.Addr, port uint16) bool {
	if p.Mode == ModeNone || Blocked(ip) {
		return false
	}
	ip = ip.Unmap()
	for _, d := range p.denyCIDR {
		if d.Contains(ip) {
			return false
		}
	}
	if p.Mode == ModeFull {
		return true
	}
	for _, a := range p.allowCIDR {
		if a.Contains(ip) {
			return true
		}
	}
	return false
}

// AllowResolvedIP reports whether ip may be returned in a DNS answer or
// dialed after resolving an allowed name (blocks DNS rebinding).
func (p *Policy) AllowResolvedIP(ip netip.Addr) bool {
	if Blocked(ip) {
		return false
	}
	ip = ip.Unmap()
	for _, d := range p.denyCIDR {
		if d.Contains(ip) {
			return false
		}
	}
	return true
}
