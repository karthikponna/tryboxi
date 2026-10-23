package backend

import (
	"context"
	"log/slog"

	"github.com/karthikponna/tryboxi/internal/network/linuxnet"
	"github.com/karthikponna/tryboxi/internal/vmm"
	"github.com/karthikponna/tryboxi/internal/vmm/firecracker"
)

type linuxBackend struct {
	d    *firecracker.Driver
	pool *linuxnet.Pool
}

// New returns the Firecracker backend with a pre-created netns pool.
func New(dataRoot string, log *slog.Logger) (Backend, error) {
	d, err := firecracker.New()
	if err != nil {
		return nil, err
	}
	return &linuxBackend{d: d, pool: linuxnet.NewPool(context.Background(), 4, log)}, nil
}

func (b *linuxBackend) Driver() vmm.Driver { return b.d }

func (b *linuxBackend) NewNet(o NetOptions) (Net, error) {
	n, err := linuxnet.New(b.pool, linuxnet.Options{Policy: o.Policy, OnEvent: o.OnEvent, Log: o.Log, CA: o.CA, Limits: o.Limits, LogAllowed: o.LogAllowed})
	if err != nil {
		return nil, err
	}
	return &nsNet{n}, nil
}

type nsNet struct{ *linuxnet.Network }

func (n *nsNet) VMNetwork() vmm.Network {
	return vmm.Network{Enabled: true, MAC: linuxnet.GuestMAC, TapName: linuxnet.TapName, NetNS: n.Name()}
}
func (n *nsNet) GuestCIDR() string { return linuxnet.GuestCIDR }
func (n *nsNet) Gateway() string   { return linuxnet.HostIP }
func (n *nsNet) DNS() []string     { return []string{linuxnet.HostIP} }
func (n *nsNet) MTU() int          { return linuxnet.MTU }
