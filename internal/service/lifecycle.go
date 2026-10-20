package service

import (
	"context"
	"time"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/internal/store"
	"github.com/karthikponna/tryboxi/internal/store/db"
)

// startMonitors opens the heartbeat stream and watches the VMM.
func (m *Manager) startMonitors(b *Box) {
	ctx, cancel := context.WithCancel(m.ctx)
	b.mu.Lock()
	if b.hbCancel != nil {
		b.hbCancel()
	}
	b.hbCancel = cancel
	vm := b.vm
	b.mu.Unlock()
	b.touchBeat()
	go func() {
		iv := uint32(m.cfg.Health.HeartbeatMS)
		for ctx.Err() == nil {
			gc := b.guest()
			if gc == nil {
				return
			}
			st, err := gc.Heartbeat(ctx, &guestv1.HeartbeatRequest{IntervalMs: iv})
			if err == nil {
				for {
					beat, err := st.Recv()
					if err != nil {
						break
					}
					b.touchBeat()
					if beat.OomKills > 0 {
						b.mu.Lock()
						warned := b.warned
						b.warned = true
						b.mu.Unlock()
						if !warned {
							m.bus.Emit(b.id, "", "sandbox.oom", map[string]uint32{"oom_kills": beat.OomKills})
						}
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	if vm != nil {
		go func() {
			select {
			case <-ctx.Done():
			case info := <-vm.Done():
				if ctx.Err() != nil {
					return
				}
				st := b.State()
				if st == StateReady || st == StateBusy {
					reason := "VM stopped unexpectedly: " + info.Reason
					if info.Err != nil {
						reason += " (" + info.Err.Error() + ")"
					}
					if tail := consoleTail(b.row.DirPath); tail != "" {
						reason += "; console: " + truncateStr(tail, 400)
					}
					m.markLost(b, reason)
				}
			}
		}()
	}
}

func (b *Box) stopMonitors() {
	b.mu.Lock()
	c := b.hbCancel
	b.hbCancel = nil
	b.mu.Unlock()
	if c != nil {
		c()
	}
}

// markLost stops the VM, keeps the disk and logs for recovery, and marks
// running executions lost.
func (m *Manager) markLost(b *Box, reason string) {
	b.lc.Lock()
	defer b.lc.Unlock()
	if st := b.State(); st != StateReady && st != StateBusy {
		return
	}
	m.log.Warn("sandbox lost", "sandbox", b.id, "reason", reason)
	b.stopMonitors()
	b.killVM()
	b.failRunning(reason)
	_ = m.st.W.MarkRunningExecutionsLost(context.Background(), db.MarkRunningExecutionsLostParams{Reason: reason, FinishedAt: nullTime(time.Now()), SandboxID: b.id})
	b.setState(context.Background(), StateLost, reason)
	m.bus.Emit(b.id, "", "sandbox.lost", map[string]string{"reason": reason})
}

var lastTickWall, lastTickMono time.Time

// healthTick checks heartbeats every second and detects host sleep.
func (m *Manager) healthTick() {
	now := time.Now()
	if !lastTickWall.IsZero() {
		wallGap := now.Round(0).Sub(lastTickWall)
		monoGap := now.Sub(lastTickMono)
		if wallGap-monoGap > 10*time.Second || monoGap > 30*time.Second {
			m.log.Info("host sleep/wake detected; resetting heartbeat timers", "wall_gap", wallGap, "mono_gap", monoGap)
			for _, b := range m.liveBoxes() {
				b.touchBeat()
				if gc := b.guest(); gc != nil {
					go func() {
						ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
						defer cancel()
						_ = gc.Refresh(ctx)
					}()
				}
			}
			m.bus.Emit("", "", "host.wake", map[string]string{"gap": wallGap.String()})
		}
	}
	lastTickWall, lastTickMono = now.Round(0), now
	unhealthy := time.Duration(m.cfg.Health.UnhealthyAfterS) * time.Second
	lost := time.Duration(m.cfg.Health.LostAfterS) * time.Second
	for _, b := range m.liveBoxes() {
		st := b.State()
		if st != StateReady && st != StateBusy {
			continue
		}
		since := time.Duration(now.UnixNano() - b.lastBeat.Load())
		b.mu.Lock()
		health := b.health
		b.mu.Unlock()
		switch {
		case since > lost:
			go m.markLost(b, "no heartbeat for "+since.Round(time.Second).String())
		case since > unhealthy:
			if health != "unhealthy" {
				b.mu.Lock()
				b.health = "unhealthy"
				b.mu.Unlock()
				_ = m.st.W.SetSandboxHealth(m.ctx, db.SetSandboxHealthParams{Health: "unhealthy", ID: b.id})
				m.bus.Emit(b.id, "", "sandbox.unhealthy", map[string]string{"since_last_beat": since.String()})
			}
			if gc := b.guest(); gc != nil {
				go func() {
					ctx, cancel := context.WithTimeout(m.ctx, 2*time.Second)
					defer cancel()
					if _, err := gc.Ping(ctx, &guestv1.PingRequest{}); err == nil {
						b.touchBeat()
					}
				}()
			}
		default:
			if health != "healthy" {
				b.mu.Lock()
				b.health = "healthy"
				b.mu.Unlock()
				_ = m.st.W.SetSandboxHealth(m.ctx, db.SetSandboxHealthParams{Health: "healthy", ID: b.id})
				m.bus.Emit(b.id, "", "sandbox.healthy", nil)
			}
		}
		// Stall warning: long-running exec with no output.
		b.mu.Lock()
		for _, re := range b.running {
			re.mu.Lock()
			if !re.stalled && !re.background && time.Since(re.lastOutput) > time.Duration(m.cfg.Defaults.StallWarnS)*time.Second {
				re.stalled = true
				m.bus.Emit(b.id, re.id, "exec.stalled", map[string]string{"no_output_for": time.Since(re.lastOutput).Round(time.Second).String()})
			}
			re.mu.Unlock()
		}
		b.mu.Unlock()
	}
}

// flushHeartbeats persists last-heartbeat times (never per beat).
func (m *Manager) flushHeartbeats() {
	for _, b := range m.liveBoxes() {
		if st := b.State(); st == StateReady || st == StateBusy {
			_ = m.st.W.SetSandboxHeartbeat(m.ctx, db.SetSandboxHeartbeatParams{LastHeartbeatAt: store.NI64(b.lastBeat.Load() / 1e6), ID: b.id})
		}
	}
}

// reapTick pauses idle sandboxes, enforces lifetimes, and expires paused
// and lost sandboxes.
func (m *Manager) reapTick() {
	now := time.Now()
	for _, b := range m.liveBoxes() {
		b.mu.Lock()
		st, busy, last := b.state, b.busy, b.lastActive
		idle := time.Duration(b.row.IdleTimeoutS) * time.Second
		expires := time.UnixMilli(b.row.ExpiresAt)
		pausedAt := b.row.PausedAt
		warned := b.warned
		b.mu.Unlock()

		if now.After(expires) && st != StateDestroyed {
			go func(b *Box) { _ = m.DestroySandbox(m.ctx, b.id, "max lifetime reached") }(b)
			continue
		}
		if !warned && expires.Sub(now) < 30*time.Minute && (st == StateReady || st == StateBusy) {
			b.mu.Lock()
			b.warned = true
			b.mu.Unlock()
			m.bus.Emit(b.id, "", "sandbox.expiring", map[string]string{"expires_in": expires.Sub(now).Round(time.Minute).String()})
		}
		switch st {
		case StateReady:
			if busy == 0 && idle > 0 && now.Sub(last) > idle {
				if b.lc.TryLock() {
					go func(b *Box) {
						defer b.lc.Unlock()
						if err := m.pauseToDiskLocked(m.ctx, b, "idle for "+idle.String()); err != nil {
							m.log.Warn("idle pause failed", "sandbox", b.id, "err", err)
						}
					}(b)
				}
			}
		case StatePaused:
			if pausedAt.Valid && now.Sub(time.UnixMilli(pausedAt.Int64)) > time.Duration(m.cfg.Defaults.PausedTTLS)*time.Second {
				go func(b *Box) { _ = m.DestroySandbox(m.ctx, b.id, "paused longer than the paused TTL") }(b)
			}
		case StateLost, StateFailed:
			if now.Sub(last) > time.Duration(m.cfg.Retention.RecoveryHours)*time.Hour {
				go func(b *Box) { _ = m.DestroySandbox(m.ctx, b.id, "recovery window expired") }(b)
			}
		}
	}
}
