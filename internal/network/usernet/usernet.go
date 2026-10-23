// Package usernet gives each VM its own userspace network stack (gVisor
// netstack) attached through a unix datagram socket carrying raw ethernet
// frames. It is the macOS enforcement point for the egress policy: every
// outbound TCP connection and DNS query passes through policy checks here.
package usernet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/tap"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	sbxdns "github.com/karthikponna/tryboxi/internal/network/dns"
	"github.com/karthikponna/tryboxi/internal/network/egress"
	"github.com/karthikponna/tryboxi/internal/network/policy"
)

// Fixed addressing: identical in every VM, because snapshots bake the guest
// IP and gateway MAC into memory.
const (
	Subnet     = "192.168.127.0/24"
	GatewayIP  = "192.168.127.1"
	GuestIP    = "192.168.127.2"
	GuestCIDR  = "192.168.127.2/24"
	GatewayMAC = "5a:94:ef:e4:0c:dd"
	GuestMAC   = "5a:94:ef:e4:0c:ee"
	MTU        = 1500
)

// Event is a policy decision or audit record (shared with Linux).
type Event = egress.Event

// Options configure a Network.
type Options struct {
	Policy     *policy.Policy
	OnEvent    func(Event)
	Log        *slog.Logger
	CA         *egress.CA
	Limits     egress.Limits
	LogAllowed bool
}

// Network is one VM's virtual network.
type Network struct {
	pol      atomic.Pointer[policy.Policy]
	bindings atomic.Pointer[[]egress.Binding]
	dns      *sbxdns.Handler
	proxy    *egress.Proxy
	stack    *stack.Stack
	sw       *tap.Switch
	host     *os.File // our end of the socketpair
	guest    *os.File // VM end, handed to Virtualization.framework
	onEv     func(Event)
	log      *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	forwards map[uint16]net.Listener
}

// New creates the network and returns it; GuestFile must be given to the VM.
func New(o Options) (*Network, error) {
	p, onEvent, log := o.Policy, o.OnEvent, o.Log
	if log == nil {
		log = slog.Default()
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		return nil, fmt.Errorf("socketpair: %w", err)
	}
	for _, fd := range fds {
		_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 1<<20)
		_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 4<<20)
	}
	n := &Network{
		host:     os.NewFile(uintptr(fds[0]), "usernet-host"),
		guest:    os.NewFile(uintptr(fds[1]), "usernet-guest"),
		onEv:     onEvent,
		log:      log,
		forwards: map[uint16]net.Listener{},
	}
	n.pol.Store(p)
	n.bindings.Store(&[]egress.Binding{})
	n.dns = sbxdns.New(p, func(name, reason string) {
		n.emit(Event{Type: "net.denied", Host: name, Reason: "dns: " + reason})
	})
	n.proxy = &egress.Proxy{
		Policy:     func() *policy.Policy { return n.pol.Load() },
		DNS:        n.dns,
		Emit:       n.emit,
		Bindings:   func() []egress.Binding { return *n.bindings.Load() },
		CA:         o.CA,
		LogAllowed: o.LogAllowed,
		Dialer:     net.Dialer{Timeout: 15 * time.Second},
	}
	n.proxy.SetLimits(o.Limits)
	n.dns.Extra = n.proxy.SecretHost
	n.ctx, n.cancel = context.WithCancel(context.Background())

	ep, err := tap.NewLinkEndpoint(false, MTU, GatewayMAC, GatewayIP, nil)
	if err != nil {
		n.closeFiles()
		return nil, err
	}
	n.sw = tap.NewSwitch(false)
	ep.Connect(n.sw)
	n.sw.Connect(ep)

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, arp.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	if terr := s.CreateNIC(1, ep); terr != nil {
		n.closeFiles()
		return nil, errors.New(terr.String())
	}
	if terr := s.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4(netip.MustParseAddr(GatewayIP).As4()).WithPrefix(),
	}, stack.AddressProperties{}); terr != nil {
		n.closeFiles()
		return nil, errors.New(terr.String())
	}
	s.SetSpoofing(1, true)
	s.SetPromiscuousMode(1, true)
	pfx := netip.MustParsePrefix(Subnet)
	sub, _ := tcpip.NewSubnet(tcpip.AddrFrom4(pfx.Addr().As4()), tcpip.MaskFromBytes(net.CIDRMask(pfx.Bits(), 32)))
	s.SetRouteTable([]tcpip.Route{{Destination: sub, NIC: 1}})
	n.stack = s

	tcpFwd := tcp.NewForwarder(s, 0, 512, n.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpFwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) bool {
		id := r.ID()
		if n.pol.Load().Mode != policy.ModeNone {
			n.emit(Event{Type: "net.denied", Dest: fmt.Sprintf("udp %s:%d", id.LocalAddress, id.LocalPort), Reason: "udp is only allowed for DNS"})
		}
		return true
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
	if err := n.serveDNS(); err != nil {
		n.Close()
		return nil, err
	}

	conn, err := net.FileConn(n.host)
	if err != nil {
		n.Close()
		return nil, err
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		if err := n.sw.Accept(n.ctx, conn, types.VfkitProtocol); err != nil && n.ctx.Err() == nil {
			n.log.Debug("usernet switch stopped", "err", err)
		}
	}()
	return n, nil
}

// SetPolicy swaps the egress policy; new connections use it immediately.
func (n *Network) SetPolicy(p *policy.Policy) {
	n.pol.Store(p)
	n.dns.SetPolicy(p)
}

// SetSecrets swaps the secrets this sandbox may use (by placeholder).
func (n *Network) SetSecrets(b []egress.Binding) {
	cp := append([]egress.Binding(nil), b...)
	n.bindings.Store(&cp)
}

// GuestFile is the socket the VM's NIC attaches to.
func (n *Network) GuestFile() *os.File { return n.guest }

func (n *Network) emit(e Event) {
	if n.onEv != nil {
		n.onEv(e)
	}
}

func (n *Network) closeFiles() {
	if n.host != nil {
		n.host.Close()
	}
	if n.guest != nil {
		n.guest.Close()
	}
}

// Close tears the network down.
func (n *Network) Close() error {
	n.cancel()
	n.mu.Lock()
	for p, l := range n.forwards {
		l.Close()
		delete(n.forwards, p)
	}
	n.mu.Unlock()
	n.closeFiles()
	if n.stack != nil {
		n.stack.Close()
	}
	return nil
}

func (n *Network) serveDNS() error {
	addr := tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(netip.MustParseAddr(GatewayIP).As4()), Port: 53}
	conn, err := gonet.DialUDP(n.stack, &addr, nil, ipv4.ProtocolNumber)
	if err != nil {
		return fmt.Errorf("dns listen: %w", err)
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		<-n.ctx.Done()
		conn.Close()
	}()
	go func() {
		buf := make([]byte, 65535)
		for {
			k, raddr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:k]...)
			go func(q []byte, raddr net.Addr) {
				resp, err := n.dns.Answer(n.ctx, q)
				if err != nil {
					return
				}
				_, _ = conn.WriteTo(resp, raddr)
			}(q, raddr)
		}
	}()
	return nil
}

func (n *Network) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	dst, _ := netip.AddrFromSlice(id.LocalAddress.AsSlice())
	port := id.LocalPort
	pol := n.pol.Load()
	// Refuse cheaply (RST) what can never be allowed; everything else is
	// accepted and decided by the shared egress proxy.
	if dst.String() == GatewayIP || pol.Mode == policy.ModeNone {
		r.Complete(true)
		return
	}
	if policy.Blocked(dst) || (!pol.AllowIP(dst, port) && !pol.HostPort(port)) {
		reason := "port not allowed for hostname rules"
		if policy.Blocked(dst) {
			reason = "blocked address range"
		}
		n.emit(Event{Type: "net.denied", Dest: net.JoinHostPort(dst.String(), strconv.Itoa(int(port))), Reason: reason})
		r.Complete(true)
		return
	}
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		r.Complete(true)
		return
	}
	r.Complete(false)
	n.proxy.Handle(n.ctx, gonet.NewTCPConn(&wq, ep), dst, port)
}

// DialGuest opens a TCP connection from the host to the guest.
func (n *Network) DialGuest(ctx context.Context, port uint16) (net.Conn, error) {
	addr := tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(netip.MustParseAddr(GuestIP).As4()), Port: port}
	return gonet.DialContextTCP(ctx, n.stack, addr, ipv4.ProtocolNumber)
}

// Forward listens on 127.0.0.1 (random port unless hostPort > 0) and relays
// connections to guestPort. It returns the host port.
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

// Forwards lists active guest->host port forwards.
func (n *Network) Forwards() map[uint16]int {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[uint16]int{}
	for gp, l := range n.forwards {
		out[gp] = l.Addr().(*net.TCPAddr).Port
	}
	return out
}
