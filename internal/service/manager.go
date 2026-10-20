// Package service implements the daemon's use cases: create, exec, sync,
// diff/apply/pull, snapshots, fork, restore, recover, and the background
// loops (health, reaper, warm pool, GC). HTTP handlers call only this layer.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/karthikponna/tryboxi/internal/backend"
	"github.com/karthikponna/tryboxi/internal/config"
	"github.com/karthikponna/tryboxi/internal/events"
	"github.com/karthikponna/tryboxi/internal/image"
	"github.com/karthikponna/tryboxi/internal/platform"
	"github.com/karthikponna/tryboxi/internal/safefs"
	"github.com/karthikponna/tryboxi/internal/scheduler"
	"github.com/karthikponna/tryboxi/internal/store"
	"github.com/karthikponna/tryboxi/internal/store/db"
	"github.com/karthikponna/tryboxi/internal/version"
	"github.com/karthikponna/tryboxi/internal/vmm"
	"github.com/karthikponna/tryboxi/internal/workspace"
)

// Manager owns every sandbox.
type Manager struct {
	cfg   *config.Config
	st    *store.Store
	be    backend.Backend
	drv   vmm.Driver
	sched *scheduler.Scheduler
	bus   *events.Bus
	log   *slog.Logger

	kernelPath    string
	initrdPath    string
	agentVersion  string
	kernelVersion string
	started       time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	boxes map[string]*Box

	pool   *pool
	builds sync.Map // template name -> *sync.Mutex (one build at a time)
}

// Artifact file names inside <data_root>/kernels.
func KernelFile() string { return "vmlinux-" + runtime.GOARCH }
func AgentFile() string  { return "sandbox-agent-linux-" + runtime.GOARCH }
func RootfsFile() string { return "base-rootfs-" + runtime.GOARCH + ".ext4" }

// New creates a manager. Call Start to begin background loops.
func New(ctx context.Context, cfg *config.Config, st *store.Store, be backend.Backend, log *slog.Logger) (*Manager, error) {
	m := &Manager{cfg: cfg, st: st, be: be, drv: be.Driver(), log: log, boxes: map[string]*Box{}, started: time.Now()}
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.bus = events.New(st, log)
	m.sched = scheduler.New(m.ctx, scheduler.Budget{
		VCPU:         float64(cfg.Budgets.MaxVCPU) * cfg.Budgets.VCPUOvercommit,
		MemMB:        cfg.Budgets.MaxMemMB,
		MaxSandboxes: cfg.Budgets.MaxSandboxes,
		MaxQueue:     cfg.Queue.MaxLen,
	}, platform.PressureFunc())
	for _, d := range []string{"kernels", "templates", "snapshots", "sandboxes", "executions", "artifacts", "tmp"} {
		if err := os.MkdirAll(cfg.Dir(d), 0o700); err != nil {
			return nil, err
		}
	}
	_ = os.Chmod(cfg.DataRoot, 0o700)
	if err := m.prepareBootArtifacts(); err != nil {
		return nil, err
	}
	m.pool = newPool(m)
	m.sched.SetReclaimer(m.pool.reclaim)
	return m, nil
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// prepareBootArtifacts checks the kernel and builds the agent initramfs.
func (m *Manager) prepareBootArtifacts() error {
	m.kernelPath = m.cfg.Dir("kernels", KernelFile())
	agent := m.cfg.Dir("kernels", AgentFile())
	if _, err := os.Stat(m.kernelPath); err != nil {
		return fmt.Errorf("guest kernel missing at %s: run `sandboxd install --artifacts <dir>` first", m.kernelPath)
	}
	asha, err := fileSHA(agent)
	if err != nil {
		return fmt.Errorf("guest agent missing at %s: run `sandboxd install --artifacts <dir>` first", agent)
	}
	ksha, err := fileSHA(m.kernelPath)
	if err != nil {
		return err
	}
	m.agentVersion = "agent-" + asha[:12]
	m.kernelVersion = "kernel-" + ksha[:12]
	m.initrdPath = m.cfg.Dir("kernels", "initrd-"+asha[:12]+".cpio")
	if _, err := os.Stat(m.initrdPath); err != nil {
		if err := image.BuildInitrd(agent, m.initrdPath); err != nil {
			return fmt.Errorf("build initrd: %w", err)
		}
	}
	return nil
}

// Start recovers state from the last run and starts background loops.
func (m *Manager) Start() error {
	if err := m.recoverOnStartup(); err != nil {
		return err
	}
	m.loop("health", time.Second, m.healthTick)
	m.loop("reaper", 15*time.Second, m.reapTick)
	m.loop("gc", time.Hour, func() { _, _ = m.GC(m.ctx, false) })
	m.loop("pool", 5*time.Second, m.pool.tick)
	m.loop("heartbeat-flush", 30*time.Second, m.flushHeartbeats)
	go func() {
		if _, err := m.ensureBaseTemplate(m.ctx); err != nil {
			m.log.Error("base template", "err", err)
		}
	}()
	return nil
}

func (m *Manager) loop(name string, every time.Duration, fn func()) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-t.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							m.log.Error("background loop panic", "loop", name, "panic", r)
						}
					}()
					fn()
				}()
			}
		}
	}()
}

// Shutdown pauses running sandboxes to disk (so they resume after a
// restart), stops VMs, and waits for loops.
func (m *Manager) Shutdown(ctx context.Context) {
	m.log.Info("shutting down: pausing sandboxes to disk")
	m.pool.drain()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, b := range m.liveBoxes() {
		wg.Add(1)
		go func(b *Box) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if b.State() == StateReady || b.State() == StateBusy {
				if err := m.pauseToDisk(ctx, b, "daemon shutdown"); err != nil {
					m.log.Warn("pause on shutdown failed", "sandbox", b.id, "err", err)
					b.killVM()
				}
			} else {
				b.killVM()
			}
		}(b)
	}
	wg.Wait()
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) liveBoxes() []*Box {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Box, 0, len(m.boxes))
	for _, b := range m.boxes {
		out = append(out, b)
	}
	return out
}

// recoverOnStartup reconciles DB rows with reality: VMs never survive a
// daemon restart on macOS, so running sandboxes become lost; paused ones
// stay resumable from their pause snapshots.
func (m *Manager) recoverOnStartup() error {
	ctx := m.ctx
	rows, err := m.st.R.ListLiveSandboxes(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, r := range rows {
		switch r.State {
		case StatePaused:
			b := m.newBox(r)
			b.state = StatePaused
			m.register(b)
		case StateLost, StateFailed:
			b := m.newBox(r)
			b.state = r.State
			m.register(b)
		default:
			reason := "daemon restarted while sandbox was " + r.State + " (VMs stop with the daemon on this backend)"
			if !m.drv.Capabilities().SurvivesDaemonRestart || r.State == StatePending || r.State == StateBooting {
				_ = m.st.W.UpdateSandboxState(ctx, db.UpdateSandboxStateParams{State: StateLost, StateReason: reason, ID: r.ID})
				_ = m.st.W.MarkRunningExecutionsLost(ctx, db.MarkRunningExecutionsLostParams{Reason: reason, FinishedAt: store.NI64(now), SandboxID: r.ID})
				m.bus.Emit(r.ID, "", "sandbox.lost", map[string]string{"reason": reason})
				r.State = StateLost
				r.StateReason = reason
				b := m.newBox(r)
				b.state = StateLost
				m.register(b)
			}
		}
	}
	// Orphaned working dirs: sandboxes/<id> with no live row.
	entries, _ := os.ReadDir(m.cfg.Dir("sandboxes"))
	for _, e := range entries {
		if _, err := m.st.R.GetSandbox(ctx, e.Name()); errors.Is(err, errNoRows) {
			p := m.cfg.Dir("sandboxes", e.Name())
			if err := safefs.RemoveUnder(m.cfg.DataRoot, p); err == nil {
				m.bus.Emit("", "", "gc.deleted", map[string]string{"path": p, "reason": "orphaned sandbox dir"})
			}
		}
	}
	return nil
}

func (m *Manager) register(b *Box) {
	m.mu.Lock()
	m.boxes[b.id] = b
	m.mu.Unlock()
}

func (m *Manager) unregister(id string) {
	m.mu.Lock()
	delete(m.boxes, id)
	m.mu.Unlock()
}

// box returns the in-memory sandbox or a not_found error.
func (m *Manager) box(id string) (*Box, error) {
	m.mu.Lock()
	b := m.boxes[id]
	m.mu.Unlock()
	if b == nil {
		if r, err := m.st.R.GetSandbox(m.ctx, id); err == nil && r.State == StateDestroyed {
			return nil, errf(410, "sandbox_lost", "sandbox %s was destroyed (%s)", id, r.StateReason)
		}
		return nil, errNotFound("sandbox", id)
	}
	return b, nil
}

// Health reports daemon status.
func (m *Manager) HealthInfo(ctx context.Context) map[string]any {
	base := "missing"
	if t, err := m.st.R.GetLatestTemplateByName(ctx, baseTemplateName); err == nil {
		base = t.Status
	}
	return map[string]any{
		"status": "ok", "version": version.Version, "backend": m.drv.Name(), "vmm_version": m.drv.Version(),
		"agent_version": m.agentVersion, "kernel_version": m.kernelVersion, "min_client_version": version.MinClient,
		"base_template_status": base, "uptime_s": int64(time.Since(m.started).Seconds()), "data_root": m.cfg.DataRoot,
	}
}

// Capacity returns the scheduler ledger plus warm pool counts.
func (m *Manager) Capacity() (scheduler.Capacity, map[string]int) {
	return m.sched.Capacity(), m.pool.counts()
}

// Events exposes the event bus.
func (m *Manager) Events() *events.Bus { return m.bus }

// Config exposes the active configuration.
func (m *Manager) Config() *config.Config { return m.cfg }

func (m *Manager) syncOptions() workspace.Options {
	return workspace.Options{
		Excludes:       m.cfg.Sync.Excludes,
		SecretExcludes: m.cfg.Sync.SecretExcludes,
		MaxFileBytes:   int64(m.cfg.Sync.MaxFileMB) << 20,
	}
}

func (m *Manager) sandboxDir(id string) string { return m.cfg.Dir("sandboxes", id) }

func (m *Manager) removeData(p string) {
	if p == "" {
		return
	}
	if err := safefs.RemoveUnder(m.cfg.DataRoot, p); err != nil {
		m.log.Warn("remove failed", "path", p, "err", err)
	}
}

func relToRoot(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}
