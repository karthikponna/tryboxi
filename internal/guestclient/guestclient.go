// Package guestclient connects to sandbox-agent over vsock using gRPC.
package guestclient

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
)

// ProtocolVersion must match the guest agent's.
const ProtocolVersion = 1

// Dialer opens a raw vsock connection to the guest.
type Dialer func(ctx context.Context) (net.Conn, error)

// Client wraps the gRPC connection.
type Client struct {
	guestv1.GuestClient
	conn *grpc.ClientConn
}

// Dial creates a client; connections are established lazily by gRPC and
// re-established through dial after VM pause/restore.
func Dial(dial Dialer) (*Client, error) {
	cc, err := grpc.NewClient("passthrough:///guest",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return dial(ctx) }),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64<<20), grpc.MaxCallSendMsgSize(64<<20)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 10 * time.Second, Timeout: 5 * time.Second, PermitWithoutStream: true}),
	)
	if err != nil {
		return nil, err
	}
	return &Client{GuestClient: guestv1.NewGuestClient(cc), conn: cc}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// WaitReady pings until the agent answers or ctx expires.
func (c *Client) WaitReady(ctx context.Context) (*guestv1.PingResponse, error) {
	var last error
	for {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		resp, err := c.Ping(pctx, &guestv1.PingRequest{}, grpc.WaitForReady(true))
		cancel()
		if err == nil {
			return resp, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("guest agent not ready: %w", last)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// CheckVersion fails on protocol mismatch.
func (c *Client) CheckVersion(ctx context.Context) (*guestv1.VersionResponse, error) {
	v, err := c.Version(ctx, &guestv1.VersionRequest{})
	if err != nil {
		return nil, err
	}
	if v.ProtocolVersion != ProtocolVersion {
		return v, fmt.Errorf("guest protocol %d, host expects %d", v.ProtocolVersion, ProtocolVersion)
	}
	return v, nil
}

// Refresh syncs the guest clock and reseeds its RNG; required after every
// snapshot restore so clones don't share time skew or random state.
func (c *Client) Refresh(ctx context.Context) error {
	if _, err := c.SyncTime(ctx, &guestv1.SyncTimeRequest{UnixNs: time.Now().UnixNano()}); err != nil {
		return fmt.Errorf("sync time: %w", err)
	}
	ent := make([]byte, 64)
	if _, err := rand.Read(ent); err != nil {
		return err
	}
	if _, err := c.Reseed(ctx, &guestv1.ReseedRequest{Entropy: ent}); err != nil {
		return fmt.Errorf("reseed: %w", err)
	}
	return nil
}
