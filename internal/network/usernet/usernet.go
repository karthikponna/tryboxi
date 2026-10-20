// Package usernet gives each VM its own userspace network stack (gVisor
// netstack) attached through a unix datagram socket carrying raw ethernet
// frames. It is the macOS enforcement point for the egress policy: every
// outbound TCP connection and DNS query passes through policy checks here.
package usernet

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/network/sniff"
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

// Event describes a policy decision worth recording.
type Event struct {
	Type   string // net.denied | net.allowed
	Host   string
	Dest   string
	Reason string
}

// Network is one VM's virtual network.
type Network struct {
	pol   atomic.Pointer[policy.Policy]
	dns   *sbxdns.Handler
	stack *stack.Stack
	sw    *tap.Switch
	host  *os.File // our end of the socketpair
	guest *os.File // VM end, handed to Virtualization.framework
	onEv  func(Event)
	log   *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	forwards map[uint16]net.Listener
}

// New creates the network and returns it; GuestFile must be given to the VM.
func New(p *policy.Policy, onEvent func(Event), log *slog.Logger) (*Network, error) {
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
	n.dns = sbxdns.New(p, func(name, reason string) {
		n.emit(Event{Type: "net.denied", Host: name, Reason: "dns: " + reason})
	})
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
	destStr := net.JoinHostPort(dst.String(), strconv.Itoa(int(port)))

	pol := n.pol.Load()
	if dst.String() == GatewayIP || pol.Mode == policy.ModeNone {
		r.Complete(true)
		return
	}
	if policy.Blocked(dst) {
		n.emit(Event{Type: "net.denied", Dest: destStr, Reason: "blocked address range"})
		r.Complete(true)
		return
	}

	// Raw-IP rules (full mode, or CIDR allow entries) dial immediately.
	if pol.AllowIP(dst, port) {
		up, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(n.ctx, "tcp", destStr)
		if err != nil {
			r.Complete(true)
			return
		}
		guest, err := n.accept(r)
		if err != nil {
			up.Close()
			return
		}
		splice(guest, up)
		return
	}

	// Allowlist mode: only hostname-identified HTTP(S) can pass.
	if !pol.HostPort(port) {
		n.emit(Event{Type: "net.denied", Dest: destStr, Reason: "port not allowed for hostname rules"})
		r.Complete(true)
		return
	}
	guest, err := n.accept(r)
	if err != nil {
		return
	}
	res, _ := sniff.Peek(guest, 5*time.Second)
	if res.Host == "" {
		n.emit(Event{Type: "net.denied", Dest: destStr, Reason: "no TLS SNI or HTTP Host to match against the allowlist"})
		guest.Close()
		return
	}
	if !pol.AllowHostPort(res.Host, port) {
		n.emit(Event{Type: "net.denied", Host: res.Host, Dest: destStr, Reason: "host not in allowlist"})
		guest.Close()
		return
	}
	ctx, cancel := context.WithTimeout(n.ctx, 15*time.Second)
	defer cancel()
	ip, err := n.dns.ResolveForDial(ctx, res.Host)
	if err != nil {
		n.emit(Event{Type: "net.denied", Host: res.Host, Dest: destStr, Reason: "resolve: " + err.Error()})
		guest.Close()
		return
	}
	up, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(int(port))))
	if err != nil {
		guest.Close()
		return
	}
	if _, err := up.Write(res.Buffered); err != nil {
		guest.Close()
		up.Close()
		return
	}
	splice(guest, up)
}

func (n *Network) accept(r *tcp.ForwarderRequest) (net.Conn, error) {
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		r.Complete(true)
		return nil, errors.New(terr.String())
	}
	r.Complete(false)
	return gonet.NewTCPConn(&wq, ep), nil
}

type closeWriter interface{ CloseWrite() error }

func splice(a, b net.Conn) {
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
				splice(c, g)
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
