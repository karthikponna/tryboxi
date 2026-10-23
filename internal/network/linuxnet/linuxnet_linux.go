// Package linuxnet isolates each Firecracker VM in its own network
// namespace containing only a TAP device. nftables redirects all guest TCP
// to a proxy listener and DNS to a resolver listener, both created by the
// daemon inside that namespace. The namespace has no route out, so the only
// way off the VM is through the policy-checking proxy.
package linuxnet

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	sbxdns "github.com/karthikponna/tryboxi/internal/network/dns"
	"github.com/karthikponna/tryboxi/internal/network/egress"
	"github.com/karthikponna/tryboxi/internal/network/policy"
)

// Fixed addressing inside every namespace (snapshots bake it in).
const (
	TapName   = "tap0"
	HostIP    = "172.16.0.1"
	GuestIP   = "172.16.0.2"
	GuestCIDR = "172.16.0.2/30"
	GuestMAC  = "06:00:ac:10:00:02"
	MTU       = 1500
	proxyPort = 3128
)

// Ruleset is applied inside each namespace with `nft -f -`.
const Ruleset = `table inet sbx {
	chain prerouting {
		type nat hook prerouting priority dstnat; policy accept;
		iifname "tap0" udp dport 53 dnat ip to 172.16.0.1:53
		iifname "tap0" meta l4proto tcp dnat ip to 172.16.0.1:3128
	}
	chain input {
		type filter hook input priority filter; policy drop;
		iifname "lo" accept
		ct state established,related accept
		iifname "tap0" ip daddr 172.16.0.1 tcp dport 3128 accept
		iifname "tap0" ip daddr 172.16.0.1 udp dport 53 accept
	}
	chain forward {
		type filter hook forward priority filter; policy drop;
	}
}
`

func run(args ...string) error {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %v: %s", args, err, bytes.TrimSpace(out))
	}
	return nil
}

func inNS(name string, args ...string) error {
	return run(append([]string{"ip", "netns", "exec", name}, args...)...)
}

// createNS makes a namespace with tap0 and the default-deny ruleset.
func createNS() (string, error) {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "sbx-" + hex.EncodeToString(b)
	if err := run("ip", "netns", "add", name); err != nil {
		return "", err
	}
	steps := [][]string{
		{"ip", "link", "set", "lo", "up"},
		{"ip", "tuntap", "add", "dev", TapName, "mode", "tap"},
		{"ip", "addr", "add", HostIP + "/30", "dev", TapName},
		{"ip", "link", "set", TapName, "mtu", strconv.Itoa(MTU), "up"},
	}
	for _, s := range steps {
		if err := inNS(name, s...); err != nil {
			_ = run("ip", "netns", "del", name)
			return "", err
		}
	}
	cmd := exec.Command("ip", "netns", "exec", name, "nft", "-f", "-")
	cmd.Stdin = bytes.NewBufferString(Ruleset)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = run("ip", "netns", "del", name)
		return "", fmt.Errorf("nft: %v: %s", err, out)
	}
	return name, nil
}

// Pool keeps a few namespaces ready so VM starts avoid netlink contention.
type Pool struct {
	ch chan string
}

// NewPool starts refilling up to size namespaces.
func NewPool(ctx context.Context, size int, log *slog.Logger) *Pool {
	p := &Pool{ch: make(chan string, size)}
	go func() {
		for {
			name, err := createNS()
			if err != nil {
				log.Warn("netns pool refill failed", "err", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
				}
				continue
			}
			select {
			case p.ch <- name:
			case <-ctx.Done():
				_ = run("ip", "netns", "del", name)
				return
			}
		}
	}()
	return p
}

func (p *Pool) get() (string, error) {
	select {
	case n := <-p.ch:
		return n, nil
	default:
		return createNS()
	}
}

// withNS runs fn on a thread switched into the namespace.
func withNS(name string, fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	orig, err := netns.Get()
	if err != nil {
		return err
	}
	defer orig.Close()
	target, err := netns.GetFromName(name)
	if err != nil {
		return err
	}
	defer target.Close()
	if err := netns.Set(target); err != nil {
		return err
	}
	defer netns.Set(orig)
	return fn()
}

// Options configure a Network.
type Options struct {
	Policy     *policy.Policy
	OnEvent    func(egress.Event)
	Log        *slog.Logger
	CA         *egress.CA
	Limits     egress.Limits
	LogAllowed bool
}

// Network is one VM's namespace plus the daemon listeners inside it.
type Network struct {
	name     string
	pol      atomic.Pointer[policy.Policy]
	bindings atomic.Pointer[[]egress.Binding]
	dns      *sbxdns.Handler
	proxy    *egress.Proxy
	tcp      net.Listener
	udp      net.PacketConn
	ctx      context.Context
	stop     context.CancelFunc
	log      *slog.Logger

	mu       sync.Mutex
	forwards map[uint16]net.Listener
}

// New prepares a namespace (from pool when given) and starts listeners.
func New(pool *Pool, o Options) (*Network, error) {
	p, onEvent, log := o.Policy, o.OnEvent, o.Log
	if onEvent == nil {
		onEvent = func(egress.Event) {}
	}
	var name string
	var err error
	if pool != nil {
		name, err = pool.get()
	} else {
		name, err = createNS()
	}
	if err != nil {
		return nil, err
	}
	n := &Network{name: name, log: log, forwards: map[uint16]net.Listener{}}
	n.pol.Store(p)
	n.dns = sbxdns.New(p, func(host, reason string) {
		onEvent(egress.Event{Type: "net.denied", Host: host, Reason: "dns: " + reason})
	})
	n.proxy = &egress.Proxy{Policy: func() *policy.Policy { return n.pol.Load() }, DNS: n.dns, Emit: onEvent}
	n.ctx, n.stop = context.WithCancel(context.Background())
	err = withNS(name, func() error {
		var err error
		if n.tcp, err = net.Listen("tcp4", net.JoinHostPort(HostIP, strconv.Itoa(proxyPort))); err != nil {
			return err
		}
		n.udp, err = net.ListenPacket("udp4", net.JoinHostPort(HostIP, "53"))
		return err
	})
	if err != nil {
		n.Close()
		return nil, err
	}
	go n.serveTCP()
	go n.serveDNS()
	return n, nil
}

// originalDst reads the pre-DNAT destination of an accepted connection.
func originalDst(c net.Conn) (netip.Addr, uint16, error) {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return netip.Addr{}, 0, fmt.Errorf("not tcp")
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		return netip.Addr{}, 0, err
	}
	var addr netip.Addr
	var port uint16
	var gerr error
	_ = raw.Control(func(fd uintptr) {
		m, err := unix.GetsockoptIPv6Mreq(int(fd), unix.IPPROTO_IP, unix.SO_ORIGINAL_DST)
		if err != nil {
			gerr = err
			return
		}
		port = binary.BigEndian.Uint16(m.Multiaddr[2:4])
		addr = netip.AddrFrom4([4]byte{m.Multiaddr[4], m.Multiaddr[5], m.Multiaddr[6], m.Multiaddr[7]})
	})
	return addr, port, gerr
}

func (n *Network) serveTCP() {
	for {
		c, err := n.tcp.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			dst, port, err := originalDst(c)
			if err != nil {
				c.Close()
				return
			}
			// Outbound dials run on ordinary goroutines, i.e. in the host
			// namespace; only the accepted socket lives in the sandbox one.
			n.proxy.Handle(n.ctx, c, dst, port)
		}(c)
	}
}

func (n *Network) serveDNS() {
	buf := make([]byte, 65535)
	for {
		k, addr, err := n.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		q := append([]byte(nil), buf[:k]...)
		go func() {
			if resp, err := n.dns.Answer(n.ctx, q); err == nil {
				_, _ = n.udp.WriteTo(resp, addr)
			}
		}()
	}
}

// Name is the namespace name (Firecracker runs inside it).
func (n *Network) Name() string { return n.name }

// SetSecrets swaps the secrets this sandbox may use.
func (n *Network) SetSecrets(b []egress.Binding) {
	cp := append([]egress.Binding(nil), b...)
	n.bindings.Store(&cp)
}

// SetPolicy swaps the egress policy.
func (n *Network) SetPolicy(p *policy.Policy) {
	n.pol.Store(p)
	n.dns.SetPolicy(p)
}

// DialGuest connects to the guest from inside the namespace.
func (n *Network) DialGuest(ctx context.Context, port uint16) (net.Conn, error) {
	var c net.Conn
	err := withNS(n.name, func() error {
		var d net.Dialer
		var err error
		c, err = d.DialContext(ctx, "tcp4", net.JoinHostPort(GuestIP, strconv.Itoa(int(port))))
		return err
	})
	return c, err
}

// Forward exposes guestPort on 127.0.0.1 in the host namespace.
func (n *Network) Forward(guestPort uint16, hostPort int) (int, error) {
	n.mu.Lock()
	if l, ok := n.forwards[guestPort]; ok {
		n.mu.Unlock()
		return l.Addr().(*net.TCPAddr).Port, nil
	}
	n.mu.Unlock()
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)))
	if err != nil {
		return 0, err
	}
	n.mu.Lock()
	n.forwards[guestPort] = l
	n.mu.Unlock()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
				g, err := n.DialGuest(ctx, guestPort)
				cancel()
				if err != nil {
					c.Close()
					return
				}
				egress.Splice(c, g)
			}(c)
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Forwards lists active forwards.
func (n *Network) Forwards() map[uint16]int {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[uint16]int{}
	for g, l := range n.forwards {
		out[g] = l.Addr().(*net.TCPAddr).Port
	}
	return out
}

// Close removes listeners and the namespace.
func (n *Network) Close() error {
	if n.stop != nil {
		n.stop()
	}
	if n.tcp != nil {
		n.tcp.Close()
	}
	if n.udp != nil {
		n.udp.Close()
	}
	n.mu.Lock()
	for g, l := range n.forwards {
		l.Close()
		delete(n.forwards, g)
	}
	n.mu.Unlock()
	return run("ip", "netns", "del", n.name)
}
