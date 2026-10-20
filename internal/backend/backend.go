// Package backend pairs a VM driver with the matching network
// implementation for the host OS.
package backend

import (
	"context"
	"log/slog"
	"net"

	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/vmm"
)

// NetEvent is a network policy decision.
type NetEvent struct {
	Type   string `json:"type"`
	Host   string `json:"host,omitempty"`
	Dest   string `json:"dest,omitempty"`
	Reason string `json:"reason"`
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
	Close() error
}

// Backend is everything OS-specific the daemon needs.
type Backend interface {
	Driver() vmm.Driver
	NewNet(p *policy.Policy, onEvent func(NetEvent), log *slog.Logger) (Net, error)
}
