package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/karthikponna/tryboxi/internal/backend"
	"github.com/karthikponna/tryboxi/internal/guestclient"
	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/scheduler"
	"github.com/karthikponna/tryboxi/internal/store"
	"github.com/karthikponna/tryboxi/internal/store/db"
	"github.com/karthikponna/tryboxi/internal/vmm"
	"github.com/karthikponna/tryboxi/internal/workspace"
	"github.com/karthikponna/tryboxi/pkg/client"
)

// Sandbox states.
const (
	StatePending   = "pending"
	StateBooting   = "booting"
	StateReady     = "ready"
	StateBusy      = "busy"
	StatePausing   = "pausing"
	StatePaused    = "paused"
	StateLost      = "lost"
	StateFailed    = "failed"
	StateDestroyed = "destroyed"
)

var errNoRows = sql.ErrNoRows

// Box is the in-memory side of a sandbox. lc serializes lifecycle changes
// (boot, pause, resume, snapshot, destroy); ordinary operations hold it in
// read mode only while starting.
type Box struct {
	id string
	m  *Manager

	lc sync.RWMutex

	mu         sync.Mutex
	row        db.Sandbox
	state      string
	health     string
	vm         vmm.Machine
	net        backend.Net
	gc         *guestclient.Client
	res        *scheduler.Reservation
	running    map[string]*runExec
	busy       int
	lastActive time.Time
	bootedFrom string
	bootMs     int64
	ports      map[uint16]int // guest port -> host port (re-established on resume)
	hbCancel   context.CancelFunc
	warned     bool
	gone       chan struct{} // closed when the VM process is gone

	lastBeat atomic.Int64 // unix nanos (monotonic-derived) of last heartbeat

	syncMu   sync.Mutex
	manifest *workspace.Manifest
	lastSync *client.SyncResult
}

func (m *Manager) newBox(r db.Sandbox) *Box {
	b := &Box{id: r.ID, m: m, row: r, state: r.State, health: r.Health, running: map[string]*runExec{},
		lastActive: time.Now(), ports: map[uint16]int{}}
	if b.health == "" {
		b.health = "healthy"
	}
	var meta map[string]string
	_ = json.Unmarshal([]byte(r.Metadata), &meta)
	for k, v := range meta {
		if gp, ok := cutPrefix(k, "port:"); ok {
			g, _ := strconv.Atoi(gp)
			h, _ := strconv.Atoi(v)
			if g > 0 {
				b.ports[uint16(g)] = h
			}
		}
	}
	b.touchBeat()
	return b
}

func cutPrefix(s, p string) (string, bool) {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return "", false
}

func (b *Box) touchBeat() { b.lastBeat.Store(time.Now().UnixNano()) }

// State returns the current state.
func (b *Box) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *Box) setState(ctx context.Context, state, reason string) {
	b.mu.Lock()
	prev := b.state
	b.state = state
	b.row.State = state
	b.row.StateReason = reason
	b.mu.Unlock()
	if prev == state {
		return
	}
	_ = b.m.st.W.UpdateSandboxState(ctx, db.UpdateSandboxStateParams{State: state, StateReason: reason, ID: b.id})
	data := map[string]string{"from": prev, "to": state}
	if reason != "" {
		data["reason"] = reason
	}
	b.m.bus.Emit(b.id, "", "sandbox.state", data)
}

func (b *Box) touch() {
	b.mu.Lock()
	b.lastActive = time.Now()
	b.mu.Unlock()
	_ = b.m.st.W.TouchSandbox(context.Background(), db.TouchSandboxParams{LastActiveAt: time.Now().UnixMilli(), ID: b.id})
}

func (b *Box) guest() *guestclient.Client {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gc
}

func (b *Box) policy() *policy.Policy {
	var spec policy.Spec
	_ = json.Unmarshal([]byte(b.row.NetworkPolicy), &spec)
	if spec.Mode == "" {
		spec.Mode = policy.Mode(b.row.NetworkMode)
	}
	p, err := policy.Compile(spec)
	if err != nil {
		return policy.MustCompile(policy.Spec{Mode: policy.ModeNone})
	}
	return p
}

// killVM force-stops the VM and frees runtime resources (not the DB row).
func (b *Box) killVM() {
	b.mu.Lock()
	vm, nw, gc, res, cancel := b.vm, b.net, b.gc, b.res, b.hbCancel
	b.vm, b.net, b.gc, b.res, b.hbCancel = nil, nil, nil, nil, nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if gc != nil {
		gc.Close()
	}
	if vm != nil {
		_ = vm.Kill()
	}
	if nw != nil {
		nw.Close()
	}
	res.Release()
}

// view renders the public representation.
func (b *Box) view() client.Sandbox {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.row
	s := client.Sandbox{
		ID: r.ID, State: b.state, Health: b.health, StateReason: r.StateReason, TemplateID: r.TemplateID,
		SessionKey: r.SessionKey, VCPU: int(r.Vcpu), MemMB: int(r.MemMb), DiskMB: int(r.DiskMb),
		NetworkMode: r.NetworkMode, WorkspaceMode: r.WorkspaceMode, HostPath: r.HostRepoPath,
		BootedFrom: b.bootedFrom, BootMs: b.bootMs, ExecCount: int(r.ExecCount), CreatedAt: r.CreatedAt,
		LastActiveAt: b.lastActive.UnixMilli(), ExpiresAt: r.ExpiresAt,
	}
	if r.ReadyAt.Valid {
		s.ReadyAt = r.ReadyAt.Int64
	}
	if len(b.ports) > 0 {
		s.Ports = map[string]int{}
		for g, h := range b.ports {
			s.Ports[strconv.Itoa(int(g))] = h
		}
	}
	var meta map[string]string
	if json.Unmarshal([]byte(r.Metadata), &meta) == nil && len(meta) > 0 {
		s.Metadata = map[string]string{}
		for k, v := range meta {
			if _, isPort := cutPrefix(k, "port:"); !isPort {
				s.Metadata[k] = v
			}
		}
		if len(s.Metadata) == 0 {
			s.Metadata = nil
		}
	}
	return s
}

func viewRow(r db.Sandbox) client.Sandbox {
	s := client.Sandbox{
		ID: r.ID, State: r.State, Health: r.Health, StateReason: r.StateReason, TemplateID: r.TemplateID,
		SessionKey: r.SessionKey, VCPU: int(r.Vcpu), MemMB: int(r.MemMb), DiskMB: int(r.DiskMb),
		NetworkMode: r.NetworkMode, WorkspaceMode: r.WorkspaceMode, HostPath: r.HostRepoPath,
		ExecCount: int(r.ExecCount), CreatedAt: r.CreatedAt, LastActiveAt: r.LastActiveAt, ExpiresAt: r.ExpiresAt,
	}
	if r.ReadyAt.Valid {
		s.ReadyAt = r.ReadyAt.Int64
	}
	return s
}

func nullTime(t time.Time) sql.NullInt64 { return store.NI64(t.UnixMilli()) }
