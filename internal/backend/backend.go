// Package backend pairs a VM driver with the matching network
// implementation for the host OS.
package backend

import (
	"context"
	"log/slog"
	"net"

	"github.com/karthikponna/tryboxi/internal/network/egress"
	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/vmm"
)

// NetEvent is a network policy decision or audit record.
type NetEvent = egress.Event

// NetOptions configure a VM's network.
type NetOptions struct {
	Policy     *policy.Policy
	OnEvent    func(NetEvent)
	Log        *slog.Logger
	CA         *egress.CA
	Limits     egress.Limits
	LogAllowed bool
}

// Net is one VM's network attachment.
type Net interface {
	// VMNetwork is passed in vmm.Spec.Net.
	VMNetwork() vmm.Network
	GuestCIDR() string
	Gateway() string
	DNS() []string
	MTU() int
	// Forward exposes guestPort on 127.0.0.1 and returns the host port.
	Forward(guestPort uint16, hostPort int) (int, error)
	Forwards() map[uint16]int
	DialGuest(ctx context.Context, port uint16) (net.Conn, error)
	SetPolicy(p *policy.Policy)
	// SetSecrets binds host-side secrets (by placeholder) to this VM.
	SetSecrets(b []egress.Binding)
	Close() error
}

// Backend is everything OS-specific the daemon needs.
type Backend interface {
	Driver() vmm.Driver
	NewNet(o NetOptions) (Net, error)
}
