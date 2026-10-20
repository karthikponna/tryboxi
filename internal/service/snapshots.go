package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/karthikponna/tryboxi/internal/ids"
	"github.com/karthikponna/tryboxi/internal/safefs"
	"github.com/karthikponna/tryboxi/internal/scheduler"
	"github.com/karthikponna/tryboxi/internal/store"
	"github.com/karthikponna/tryboxi/internal/store/db"
	"github.com/karthikponna/tryboxi/internal/workspace"
	"github.com/karthikponna/tryboxi/pkg/client"
)

// takeSnapshot flushes the guest, pauses the VM, and writes a snapshot.
// The VM is left paused; the caller resumes or kills it.
func (m *Manager) takeSnapshot(ctx context.Context, b *Box, kind, label, templateID string) (db.Snapshot, error) {
	b.mu.Lock()
	vm, gc := b.vm, b.gc
	b.mu.Unlock()
	if vm == nil || gc == nil {
		return db.Snapshot{}, errState("sandbox %s has no running VM", b.id)
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, _ = guestRun(sctx, gc, "sync", "/", 30*time.Second, nil)
	cancel()
	if err := vm.Pause(ctx); err != nil {
		return db.Snapshot{}, fmt.Errorf("pause: %w", err)
	}
	id := ids.New(ids.Snapshot)
	dir := m.cfg.Dir("snapshots", id)
	t0 := time.Now()
	files, err := vm.Snapshot(ctx, dir)
	if err != nil {
		m.removeData(dir)
		return db.Snapshot{}, fmt.Errorf("snapshot: %w", err)
	}
	manifestPath := ""
	b.syncMu.Lock()
	if b.manifest != nil {
		manifestPath = filepath.Join(dir, "manifest.json")
		if err := b.manifest.Save(manifestPath); err != nil {
			manifestPath = ""
		}
	}
	b.syncMu.Unlock()
	now := time.Now().UnixMilli()
	p := db.InsertSnapshotParams{
		ID: id, Kind: kind, TemplateID: store.NS(templateID), VmstatePath: files.VMState, MemPath: files.Mem,
		DiskPath: files.Disk, DirPath: dir, MemSizeBytes: files.MemBytes, DiskSizeBytes: files.DiskBytes,
		Backend: m.drv.Name(), VmmVersion: m.drv.Version(), KernelVersion: m.kernelVersion, AgentVersion: m.agentVersion,
		Vcpu: b.row.Vcpu, MemMb: b.row.MemMb, NetworkEnabled: 1, MachineID: files.MachineID,
		SyncManifestPath: manifestPath, Label: label, CreatedAt: now, LastUsedAt: now,
	}
	if templateID == "" || kind != "golden" {
		p.SourceSandboxID = store.NS(b.id)
		p.TemplateID = store.NS(b.row.TemplateID)
	}
	if b.manifest != nil {
		p.WorkspaceCommit = b.manifest.Commit
	}
	if kind != "golden" && kind != "pause" {
		p.ExpiresAt = store.NI64(b.row.ExpiresAt + int64(m.cfg.Retention.SnapshotHours)*3600_000)
	}
	if err := m.st.W.InsertSnapshot(ctx, p); err != nil {
		m.removeData(dir)
		return db.Snapshot{}, err
	}
	m.bus.Emit(b.id, "", "snapshot.created", map[string]any{"snapshot": id, "kind": kind, "ms": time.Since(t0).Milliseconds(), "state_mb": files.MemBytes >> 20})
	return m.st.R.GetSnapshot(ctx, id)
}

func (m *Manager) deleteSnapshot(ctx context.Context, s db.Snapshot, reason string) {
	m.removeData(s.DirPath)
	_ = m.st.W.DeleteSnapshot(ctx, s.ID)
	m.bus.Emit(s.SourceSandboxID.String, "", "snapshot.deleted", map[string]string{"snapshot": s.ID, "kind": s.Kind, "reason": reason})
}

// pauseToDisk snapshots an idle sandbox, stops its VM, and frees its
// reservation. It resumes transparently on the next API call.
func (m *Manager) pauseToDisk(ctx context.Context, b *Box, reason string) error {
	b.lc.Lock()
	defer b.lc.Unlock()
	return m.pauseToDiskLocked(ctx, b, reason)
}

func (m *Manager) pauseToDiskLocked(ctx context.Context, b *Box, reason string) error {
	if st := b.State(); st != StateReady && st != StateBusy {
		return nil
	}
	b.setState(ctx, StatePausing, reason)
	b.stopMonitors()
	snap, err := m.takeSnapshot(ctx, b, "pause", reason, "")
	if err != nil {
		b.mu.Lock()
		vm := b.vm
		b.mu.Unlock()
		if vm != nil {
			_ = vm.Resume(ctx)
		}
		b.setState(ctx, StateReady, "")
		m.startMonitors(b)
		return err
	}
	_ = m.st.W.AddSnapshotRef(ctx, db.AddSnapshotRefParams{Refcount: 1, LastUsedAt: time.Now().UnixMilli(), ID: snap.ID})
	b.killVM()
	m.removeData(filepath.Join(b.row.DirPath, "disk.img"))
	now := time.Now()
	_ = m.st.W.SetSandboxPaused(ctx, db.SetSandboxPausedParams{StateReason: reason, PausedAt: nullTime(now), ID: b.id})
	b.mu.Lock()
	b.state = StatePaused
	b.row.State = StatePaused
	b.row.PausedAt = nullTime(now)
	b.mu.Unlock()
	m.bus.Emit(b.id, "", "sandbox.paused", map[string]any{"reason": reason, "snapshot": snap.ID})
	return nil
}

// resumeFromDisk restores a paused sandbox (lc must be held).
func (m *Manager) resumeFromDisk(ctx context.Context, b *Box) error {
	snaps, err := m.st.R.ListSnapshots(ctx, db.ListSnapshotsParams{SourceSandboxID: b.id, Kind: "pause"})
	if err != nil || len(snaps) == 0 {
		reason := "pause snapshot missing"
		b.setState(ctx, StateLost, reason)
		return errLost("sandbox %s cannot resume: %s", b.id, reason)
	}
	snap := snaps[0]
	t0 := time.Now()
	res, err := m.sched.Reserve(ctx, scheduler.Request{ClientID: b.row.ClientID, VCPU: int(b.row.Vcpu), MemMB: int(b.row.MemMb)},
		time.Duration(m.cfg.Queue.WaitForCapacityS)*time.Second)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.res = res
	b.mu.Unlock()
	b.setState(ctx, StateBooting, "resuming")
	if err := m.restoreInto(ctx, b, snap); err != nil {
		b.killVM()
		b.setState(ctx, StatePaused, "resume failed: "+err.Error())
		return fmt.Errorf("resume: %w", err)
	}
	if err := m.configureGuest(ctx, b, false); err != nil {
		b.killVM()
		b.setState(ctx, StatePaused, "resume failed: "+err.Error())
		return err
	}
	if b.manifest == nil && b.row.HostRepoPath != "" {
		if mf, err := workspace.LoadManifest(b.row.SyncManifestPath, b.row.HostRepoPath); err == nil {
			b.manifest = mf
		}
	}
	m.deleteSnapshot(ctx, snap, "resumed")
	b.mu.Lock()
	b.bootedFrom = "resume"
	b.bootMs = time.Since(t0).Milliseconds()
	b.mu.Unlock()
	b.setState(ctx, StateReady, "")
	b.touch()
	m.startMonitors(b)
	m.bus.Emit(b.id, "", "sandbox.resumed", map[string]any{"ms": time.Since(t0).Milliseconds()})
	return nil
}

// PauseSandbox handles POST /pause (to disk).
func (m *Manager) PauseSandbox(ctx context.Context, id string) (client.Sandbox, error) {
	b, err := m.box(id)
	if err != nil {
		return client.Sandbox{}, err
	}
	if err := m.pauseToDisk(ctx, b, "paused by client"); err != nil {
		return client.Sandbox{}, err
	}
	return b.view(), nil
}

// ResumeSandbox handles POST /resume.
func (m *Manager) ResumeSandbox(ctx context.Context, id string) (client.Sandbox, error) {
	b, err := m.box(id)
	if err != nil {
		return client.Sandbox{}, err
	}
	if err := m.ensureRunning(ctx, b); err != nil {
		return client.Sandbox{}, err
	}
	return b.view(), nil
}

// Checkpoint snapshots a running sandbox and keeps it running.
func (m *Manager) Checkpoint(ctx context.Context, id, label string) (client.Snapshot, error) {
	b, err := m.box(id)
	if err != nil {
		return client.Snapshot{}, err
	}
	if err := m.ensureRunning(ctx, b); err != nil {
		return client.Snapshot{}, err
	}
	b.lc.Lock()
	defer b.lc.Unlock()
	snap, err := m.takeSnapshot(ctx, b, "checkpoint", label, "")
	b.mu.Lock()
	vm := b.vm
	b.mu.Unlock()
	if vm != nil {
		if rerr := vm.Resume(ctx); rerr != nil && err == nil {
			err = rerr
		}
	}
	if err != nil {
		return client.Snapshot{}, err
	}
	b.touch()
	return snapView(snap), nil
}

// Fork snapshots a sandbox and starts count clones from it.
func (m *Manager) Fork(ctx context.Context, c Caller, id string, count int) (client.ForkResult, error) {
	if count < 1 || count > 8 {
		return client.ForkResult{}, errInvalid("count must be 1-8")
	}
	b, err := m.box(id)
	if err != nil {
		return client.ForkResult{}, err
	}
	if err := m.ensureRunning(ctx, b); err != nil {
		return client.ForkResult{}, err
	}
	b.lc.Lock()
	snap, err := m.takeSnapshot(ctx, b, "fork_base", fmt.Sprintf("fork x%d", count), "")
	b.mu.Lock()
	vm := b.vm
	b.mu.Unlock()
	if vm != nil {
		_ = vm.Resume(ctx)
	}
	b.lc.Unlock()
	if err != nil {
		return client.ForkResult{}, err
	}
	b.touch()
	out := client.ForkResult{SnapshotID: snap.ID, Sandboxes: make([]client.Sandbox, count)}
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out.Sandboxes[i], errs[i] = m.createFromSnapshot(ctx, c, b.row, snap, "fork of "+b.id)
		}(i)
	}
	wg.Wait()
	var ok []client.Sandbox
	var firstErr error
	for i, s := range out.Sandboxes {
		if errs[i] == nil {
			ok = append(ok, s)
		} else {
			out.Failed = append(out.Failed, errs[i].Error())
			if firstErr == nil {
				firstErr = errs[i]
			}
		}
	}
	out.Sandboxes = ok
	if len(ok) == 0 {
		return out, firstErr
	}
	m.bus.Emit(b.id, "", "sandbox.forked", map[string]any{"snapshot": snap.ID, "clones": len(ok)})
	return out, nil
}

// RestoreSandbox creates a new sandbox from a snapshot of id ("latest"
// picks the newest checkpoint or fork base).
func (m *Manager) RestoreSandbox(ctx context.Context, c Caller, id, snapshotID string) (client.Sandbox, error) {
	src, err := m.st.R.GetSandbox(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return client.Sandbox{}, errNotFound("sandbox", id)
	}
	if err != nil {
		return client.Sandbox{}, err
	}
	var snap db.Snapshot
	if snapshotID == "" || snapshotID == "latest" {
		snap, err = m.st.R.LatestSnapshotForSandbox(ctx, store.NS(id))
		if err != nil {
			return client.Sandbox{}, errNotFound("checkpoint for sandbox", id)
		}
	} else {
		snap, err = m.st.R.GetSnapshot(ctx, snapshotID)
		if err != nil {
			return client.Sandbox{}, errNotFound("snapshot", snapshotID)
		}
	}
	return m.createFromSnapshot(ctx, c, src, snap, "restored from "+snap.ID)
}

// createFromSnapshot starts a new sandbox from snap, inheriting settings
// from parent. Its workspace mode becomes "sandbox" when the parent synced
// a host repo, so the clone never auto-overwrites its own changes.
func (m *Manager) createFromSnapshot(ctx context.Context, c Caller, parent db.Sandbox, snap db.Snapshot, why string) (client.Sandbox, error) {
	id := ids.New(ids.Sandbox)
	now := time.Now()
	mode := parent.WorkspaceMode
	if mode == "host" {
		mode = "sandbox"
	}
	meta, _ := json.Marshal(map[string]string{"parent": parent.ID, "snapshot": snap.ID, "origin": why})
	if err := m.st.W.InsertSandbox(ctx, db.InsertSandboxParams{
		ID: id, ClientID: c.ClientID, TemplateID: parent.TemplateID, State: StatePending, Vcpu: snap.Vcpu, MemMb: snap.MemMb,
		DiskMb: parent.DiskMb, NetworkMode: parent.NetworkMode, NetworkPolicy: parent.NetworkPolicy, WorkspaceMode: mode,
		HostRepoPath: parent.HostRepoPath, DirPath: m.sandboxDir(id), IdleTimeoutS: parent.IdleTimeoutS,
		MaxLifetimeS: parent.MaxLifetimeS, CreatedAt: now.UnixMilli(), LastActiveAt: now.UnixMilli(),
		ExpiresAt: now.Add(time.Duration(parent.MaxLifetimeS) * time.Second).UnixMilli(), Metadata: string(meta),
	}); err != nil {
		return client.Sandbox{}, err
	}
	row, err := m.st.R.GetSandbox(ctx, id)
	if err != nil {
		return client.Sandbox{}, err
	}
	b := m.newBox(row)
	m.register(b)
	m.bus.Emit(id, "", "sandbox.created", map[string]string{"from_snapshot": snap.ID, "parent": parent.ID})
	b.lc.Lock()
	defer b.lc.Unlock()
	t0 := time.Now()
	fail := func(err error) (client.Sandbox, error) {
		b.killVM()
		b.setState(context.Background(), StateFailed, err.Error())
		return client.Sandbox{}, err
	}
	res, err := m.sched.Reserve(ctx, scheduler.Request{ClientID: c.ClientID, Priority: c.Priority, VCPU: int(snap.Vcpu), MemMB: int(snap.MemMb)},
		time.Duration(m.cfg.Queue.WaitForCapacityS)*time.Second)
	if err != nil {
		m.destroyRow(b, "capacity")
		return client.Sandbox{}, err
	}
	b.mu.Lock()
	b.res = res
	b.mu.Unlock()
	b.setState(ctx, StateBooting, "")
	if err := os.MkdirAll(row.DirPath, 0o700); err != nil {
		return fail(err)
	}
	if err := m.restoreInto(ctx, b, snap); err != nil {
		return fail(err)
	}
	if err := m.configureGuest(ctx, b, true); err != nil {
		return fail(err)
	}
	manifestPath := filepath.Join(row.DirPath, "manifest.json")
	if snap.SyncManifestPath != "" && row.HostRepoPath != "" {
		if mf, err := workspace.LoadManifest(snap.SyncManifestPath, row.HostRepoPath); err == nil {
			b.manifest = mf
			_ = mf.Save(manifestPath)
		}
	}
	if err := m.st.W.SetSandboxReady(ctx, db.SetSandboxReadyParams{BootedFromSnapshotID: store.NS(snap.ID), ReadyAt: nullTime(time.Now()),
		LastActiveAt: time.Now().UnixMilli(), SyncManifestPath: manifestPath, ID: id}); err != nil {
		return fail(err)
	}
	b.mu.Lock()
	b.bootedFrom = "snapshot"
	b.bootMs = time.Since(t0).Milliseconds()
	b.row.SyncManifestPath = manifestPath
	b.mu.Unlock()
	b.setState(ctx, StateReady, "")
	b.touch()
	m.startMonitors(b)
	return b.view(), nil
}

// ListSnapshots lists snapshots, optionally for one sandbox.
func (m *Manager) ListSnapshots(ctx context.Context, sandboxID string) ([]client.Snapshot, error) {
	p := db.ListSnapshotsParams{}
	if sandboxID != "" {
		p.SourceSandboxID = sandboxID
	}
	rows, err := m.st.R.ListSnapshots(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]client.Snapshot, 0, len(rows))
	for _, s := range rows {
		out = append(out, snapView(s))
	}
	return out, nil
}

// snapView reports the space actually used: disks are sparse CoW clones,
// so their logical size says little.
func snapView(s db.Snapshot) client.Snapshot {
	return client.Snapshot{ID: s.ID, Kind: s.Kind, SandboxID: s.SourceSandboxID.String, TemplateID: s.TemplateID.String,
		Label: s.Label, CreatedAt: s.CreatedAt, SizeBytes: s.MemSizeBytes + allocatedBytes(s.DiskPath)}
}

func allocatedBytes(p string) int64 {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return 0
	}
	return int64(st.Blocks) * 512
}

// GC applies retention rules. With dryRun nothing is deleted.
func (m *Manager) GC(ctx context.Context, dryRun bool) (client.GCResult, error) {
	res := client.GCResult{DryRun: dryRun}
	now := time.Now()
	del := func(path, why string, size int64, fn func()) {
		res.Deleted = append(res.Deleted, fmt.Sprintf("%s (%s)", relToRoot(m.cfg.DataRoot, path), why))
		res.Bytes += size
		if !dryRun {
			fn()
			m.bus.Emit("", "", "gc.deleted", map[string]string{"path": path, "reason": why})
		}
	}
	cands, _ := m.st.R.ListSnapshotGCCandidates(ctx, store.NI64(now.UnixMilli()))
	for _, s := range cands {
		s := s
		del(s.DirPath, "expired "+s.Kind+" snapshot", s.MemSizeBytes+s.DiskSizeBytes, func() { m.deleteSnapshot(ctx, s, "gc: expired") })
	}
	// Quota uses allocated blocks: disks are sparse clones, so their logical
	// size overstates usage by an order of magnitude.
	if all, err := m.st.R.ListSnapshots(ctx, db.ListSnapshotsParams{}); err == nil {
		var total int64
		used := map[string]int64{}
		for _, s := range all {
			u := s.MemSizeBytes + allocatedBytes(s.DiskPath)
			used[s.ID] = u
			total += u
		}
		quota := int64(m.cfg.Budgets.MaxSnapshotGB) << 30
		if total > quota {
			lru, _ := m.st.R.ListSnapshotsLRU(ctx)
			for _, s := range lru {
				if total <= quota {
					break
				}
				if s.Kind == "pause" {
					continue
				}
				s := s
				total -= used[s.ID]
				del(s.DirPath, "snapshot quota", used[s.ID], func() { m.deleteSnapshot(ctx, s, "gc: quota") })
			}
		}
	}
	execCut := now.Add(-time.Duration(m.cfg.Retention.ExecLogsDays) * 24 * time.Hour).UnixMilli()
	if execs, err := m.st.R.ListFinishedExecutionsBefore(ctx, store.NI64(execCut)); err == nil {
		for _, e := range execs {
			e := e
			dir := m.cfg.Dir("executions", e.ID)
			del(dir, "execution logs past retention", e.StdoutBytes+e.StderrBytes, func() {
				m.removeData(dir)
				_ = m.st.W.DeleteExecution(ctx, e.ID)
			})
		}
	}
	if arts, err := m.st.R.ListExpiredArtifacts(ctx, store.NI64(now.UnixMilli())); err == nil {
		for _, a := range arts {
			a := a
			del(a.Path, "expired "+a.Kind+" artifact", a.SizeBytes, func() {
				m.removeData(a.Path)
				_ = m.st.W.DeleteArtifact(ctx, a.ID)
			})
		}
	}
	rowCut := now.Add(-time.Duration(m.cfg.Retention.DestroyedRowsDays) * 24 * time.Hour).UnixMilli()
	if rows, err := m.st.R.ListDestroyedSandboxesBefore(ctx, store.NI64(rowCut)); err == nil {
		for _, r := range rows {
			r := r
			del("db:sandboxes/"+r.ID, "destroyed sandbox row", 0, func() { _ = m.st.W.DeleteSandboxRow(ctx, r.ID) })
		}
	}
	if !dryRun {
		_ = m.st.W.DeleteEventsBefore(ctx, now.Add(-time.Duration(m.cfg.Retention.EventsDays)*24*time.Hour).UnixMilli())
	}
	// Temp dirs from interrupted builds.
	entries, _ := os.ReadDir(m.cfg.Dir("tmp"))
	for _, e := range entries {
		p := m.cfg.Dir("tmp", e.Name())
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > 6*time.Hour {
			del(p, "stale temp dir", 0, func() { _ = safefs.RemoveUnder(m.cfg.DataRoot, p) })
		}
	}
	return res, nil
}
