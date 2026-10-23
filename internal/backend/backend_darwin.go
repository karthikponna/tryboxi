package backend

import (
	"log/slog"

	"github.com/karthikponna/tryboxi/internal/network/usernet"
	"github.com/karthikponna/tryboxi/internal/vmm"
	"github.com/karthikponna/tryboxi/internal/vmm/vz"
)

type darwinBackend struct{ d *vz.Driver }

// New returns the Virtualization.framework backend.
func New(dataRoot string, log *slog.Logger) (Backend, error) {
	return &darwinBackend{d: vz.New()}, nil
}

func (b *darwinBackend) Driver() vmm.Driver { return b.d }

func (b *darwinBackend) NewNet(o NetOptions) (Net, error) {
	n, err := usernet.New(usernet.Options{Policy: o.Policy, OnEvent: o.OnEvent, Log: o.Log, CA: o.CA, Limits: o.Limits, LogAllowed: o.LogAllowed})
	if err != nil {
		return nil, err
	}
	return &userNet{n}, nil
}

type userNet struct{ *usernet.Network }

func (u *userNet) VMNetwork() vmm.Network {
	return vmm.Network{Enabled: true, MAC: usernet.GuestMAC, File: u.GuestFile()}
}
func (u *userNet) GuestCIDR() string { return usernet.GuestCIDR }
func (u *userNet) Gateway() string   { return usernet.GatewayIP }
func (u *userNet) DNS() []string     { return []string{usernet.GatewayIP} }
func (u *userNet) MTU() int          { return usernet.MTU }
