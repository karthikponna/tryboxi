package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/internal/backend"
	"github.com/karthikponna/tryboxi/internal/guestclient"
	"github.com/karthikponna/tryboxi/internal/ids"
	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/scheduler"
	"github.com/karthikponna/tryboxi/internal/store"
	"github.com/karthikponna/tryboxi/internal/store/db"
	"github.com/karthikponna/tryboxi/internal/telemetry"
	"github.com/karthikponna/tryboxi/internal/vmm"
	"github.com/karthikponna/tryboxi/internal/workspace"
	"github.com/karthikponna/tryboxi/pkg/client"
	"go.opentelemetry.io/otel/attribute"
)

const cmdline = "console=hvc0 rdinit=/init sbx.root=/dev/vda quiet loglevel=3"

// Caller identifies the API client.
type Caller struct {
	ClientID string
	Priority int
}

func hostname(id string) string {
	if len(id) > 8 {
		return "sbx-" + id[len(id)-8:]
	}
	return "sbx-" + id
}

// CreateSandbox creates (or reuses, by session_key) a sandbox.
func (m *Manager) CreateSandbox(ctx context.Context, c Caller, req client.CreateSandboxRequest) (client.Sandbox, error) {
	cfg := m.cfg
	if req.VCPU <= 0 {
		req.VCPU = cfg.Defaults.VCPU
	}
	if req.MemMB <= 0 {
		req.MemMB = cfg.Defaults.MemMB
	}
	if req.DiskMB <= 0 {
		req.DiskMB = cfg.Defaults.DiskMB
	}
	if req.VCPU > 16 || req.MemMB < 256 || req.MemMB > 65536 || req.DiskMB < 1024 || req.DiskMB > 262144 {
		return client.Sandbox{}, errInvalid("vcpu must be 1-16, mem_mb 256-65536, disk_mb 1024-262144")
	}
	if req.IdleTimeoutS <= 0 {
		req.IdleTimeoutS = cfg.Defaults.IdleTimeoutS
	}
	if req.MaxLifetimeS <= 0 || req.MaxLifetimeS > cfg.Defaults.MaxLifetimeS*3 {
		req.MaxLifetimeS = cfg.Defaults.MaxLifetimeS
	}
	wait := time.Duration(cfg.Queue.WaitForCapacityS) * time.Second
	if req.WaitForCapacityS > 0 {
		wait = time.Duration(min(req.WaitForCapacityS, 600)) * time.Second
	}

	ws := client.WorkspaceSpec{Mode: "none"}
	if req.Workspace != nil {
		ws = *req.Workspace
	}
	if ws.HostPath != "" {
		abs, err := filepath.Abs(ws.HostPath)
		if err != nil {
			return client.Sandbox{}, errInvalid("bad workspace path: %v", err)
		}
		fi, err := os.Stat(abs)
		if err != nil || !fi.IsDir() {
			return client.Sandbox{}, errInvalid("workspace host_path %q is not a directory", ws.HostPath)
		}
		if abs == "/" || abs == os.Getenv("HOME") {
			return client.Sandbox{}, errInvalid("refusing to sync %q: point workspace.host_path at a project folder", abs)
		}
		ws.HostPath = abs
		if ws.Mode == "" || ws.Mode == "none" {
			ws.Mode = "host"
		}
	}
	switch ws.Mode {
	case "", "none":
		ws.Mode = "none"
		ws.HostPath = ""
	case "host", "sandbox":
		if ws.HostPath == "" {
			return client.Sandbox{}, errInvalid("workspace mode %q needs host_path", ws.Mode)
		}
	default:
		return client.Sandbox{}, errInvalid("workspace.mode must be host, sandbox, or none")
	}

	baseAllow := append(cfg.Network.PresetEntries(), cfg.Network.Allow...)
	baseAllow = append(baseAllow, stackPresets(ws.HostPath)...)
	nspec := policy.Spec{Mode: cfg.Network.DefaultMode, Allow: baseAllow, Deny: cfg.Network.Deny}
	if req.Network != nil {
		if req.Network.Mode != "" {
			nspec.Mode = policy.Mode(req.Network.Mode)
		}
		nspec.Allow = append(append([]string(nil), baseAllow...), req.Network.Allow...)
		nspec.Deny = append(append([]string(nil), cfg.Network.Deny...), req.Network.Deny...)
		nspec.Ports = req.Network.Ports
	}
	if len(ws.AutoPull) > 0 && ws.Mode != "host" {
		return client.Sandbox{}, errInvalid("workspace.auto_pull needs workspace mode host")
	}
	if _, err := policy.Compile(nspec); err != nil {
		return client.Sandbox{}, errInvalid("network: %v", err)
	}

	// Session reuse: one sandbox per (client, session_key).
	if req.SessionKey != "" {
		rows, err := m.st.R.ListSandboxes(ctx, db.ListSandboxesParams{ClientID: c.ClientID, SessionKey: req.SessionKey, Limit: 10})
		if err == nil {
			for _, r := range rows {
				if r.State == StateLost || r.State == StateFailed || r.State == StateDestroyed {
					continue
				}
				b, err := m.box(r.ID)
				if err != nil {
					continue
				}
				if err := m.ensureRunning(ctx, b); err != nil {
					return client.Sandbox{}, err
				}
				v := b.view()
				v.Reused = true
				return v, nil
			}
		}
	}

	tpl, notice, err := m.resolveTemplate(ctx, req.Template, ws.HostPath)
	if err != nil {
		return client.Sandbox{}, err
	}

	id := ids.New(ids.Sandbox)
	ctx, span := telemetry.Start(ctx, "sandbox.create", telemetry.Sandbox(id), attribute.String("template", tpl.Name))
	defer span.End()
	now := time.Now()
	meta := req.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	for k := range meta {
		if strings.HasPrefix(k, secretMetaPrefx) || strings.HasPrefix(k, "port:") {
			delete(meta, k)
		}
	}
	if err := m.secretMeta(req.Secrets, meta); err != nil {
		return client.Sandbox{}, err
	}
	if pats := expandAutoPull(ws.AutoPull); len(pats) > 0 {
		meta[autoPullMeta] = strings.Join(pats, ",")
	}
	metaJSON, _ := json.Marshal(meta)
	nspecJSON, _ := json.Marshal(nspec)
	row := db.InsertSandboxParams{
		ID: id, ClientID: c.ClientID, SessionKey: req.SessionKey, TemplateID: tpl.ID, State: StatePending,
		Vcpu: int64(req.VCPU), MemMb: int64(req.MemMB), DiskMb: int64(req.DiskMB), NetworkMode: string(nspec.Mode),
		NetworkPolicy: string(nspecJSON), WorkspaceMode: ws.Mode, HostRepoPath: ws.HostPath, DirPath: m.sandboxDir(id),
		IdleTimeoutS: int64(req.IdleTimeoutS), MaxLifetimeS: int64(req.MaxLifetimeS), CreatedAt: now.UnixMilli(),
		LastActiveAt: now.UnixMilli(), ExpiresAt: now.Add(time.Duration(req.MaxLifetimeS) * time.Second).UnixMilli(),
		Metadata: string(metaJSON),
	}
	if err := m.st.W.InsertSandbox(ctx, row); err != nil {
		return client.Sandbox{}, err
	}
	dbRow, err := m.st.R.GetSandbox(ctx, id)
	if err != nil {
		return client.Sandbox{}, err
	}
	b := m.newBox(dbRow)
	m.register(b)
	m.bus.Emit(id, "", "sandbox.created", map[string]any{"template": tpl.Name, "client": c.ClientID})

	b.lc.Lock()
	err = m.bootNew(ctx, b, c, tpl, wait)
	b.lc.Unlock()
	if err != nil {
		reason := err.Error()
		b.killVM()
		b.setState(context.Background(), StateFailed, reason)
		m.bus.Emit(id, "", "sandbox.failed", map[string]string{"reason": reason})
		if _, ok := scheduler.IsCapacity(err); ok {
			// Nothing to keep; the row was never usable.
			m.destroyRow(b, "capacity")
		}
		return client.Sandbox{}, err
	}
	var setup []client.SetupStep
	if len(req.Setup) > 0 {
		setupMode := ""
		if req.Network != nil {
			setupMode = req.Network.SetupMode
		}
		var serr error
		setup, serr = m.runSetup(ctx, b, req.Setup, setupMode)
		if serr != nil {
			notice = strings.TrimSpace(notice + " setup failed: " + serr.Error())
		}
	}
	v := b.view()
	v.Notice = notice
	v.Setup = setup
	return v, nil
}

// bootNew reserves resources, obtains a VM, and syncs the workspace.
func (m *Manager) bootNew(ctx context.Context, b *Box, c Caller, tpl db.Template, wait time.Duration) (err error) {
	ctx, span := telemetry.Start(ctx, "sandbox.boot", telemetry.Sandbox(b.id), attribute.String("template", tpl.Name))
	defer func() { telemetry.End(span, err) }()
	t0 := time.Now()
	r := b.row
	_, rspan := telemetry.Start(ctx, "scheduler.reserve", telemetry.Sandbox(b.id))
	res, err := m.sched.Reserve(ctx, scheduler.Request{ClientID: c.ClientID, Priority: c.Priority, VCPU: int(r.Vcpu), MemMB: int(r.MemMb)}, wait)
	telemetry.End(rspan, err)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.res = res
	b.mu.Unlock()
	b.setState(ctx, StateBooting, "")
	if err := os.MkdirAll(r.DirPath, 0o700); err != nil {
		return err
	}

	var manifest *workspace.Manifest
	bootedFrom := ""
	var snapID string
	// 1. Warm pool.
	if pv := m.pool.claim(tpl.ID, int(r.Vcpu), int(r.MemMb), int(r.DiskMb)); pv != nil {
		if err := m.adoptPoolVM(ctx, b, pv); err == nil {
			bootedFrom, snapID = "pool", pv.snapID
		} else {
			m.log.Warn("pool VM unusable, falling back", "err", err)
			pv.discard()
		}
	}
	// 2. Golden snapshot restore.
	if bootedFrom == "" {
		if snap, ok := m.findGolden(ctx, tpl.ID, int(r.Vcpu), int(r.MemMb), int(r.DiskMb)); ok {
			if err := m.restoreInto(ctx, b, snap); err == nil {
				bootedFrom, snapID = "snapshot", snap.ID
			} else {
				m.log.Warn("golden restore failed, cold booting", "snapshot", snap.ID, "err", err)
				b.killVMKeepRes()
			}
		}
	}
	// 3. Cold boot from the template rootfs, capturing a golden snapshot.
	if bootedFrom == "" {
		rootfs := tpl.RootfsPath
		if rootfs == "" {
			base, err := m.ensureBaseTemplate(ctx)
			if err != nil {
				return err
			}
			rootfs = base.RootfsPath
			m.bus.Emit(b.id, "", "sandbox.notice", map[string]string{"message": "template has no rootfs for this size; booted base"})
		}
		if err := m.coldBoot(ctx, b, rootfs, int(r.DiskMb)); err != nil {
			return err
		}
		bootedFrom = "cold"
		if tpl.RootfsPath != "" {
			if snap, err := m.captureGolden(ctx, b, tpl); err != nil {
				m.log.Warn("golden capture failed", "template", tpl.Name, "err", err)
			} else {
				snapID = snap.ID
			}
		}
	}
	if snapID != "" {
		if snap, err := m.st.R.GetSnapshot(ctx, snapID); err == nil && snap.SyncManifestPath != "" && r.HostRepoPath != "" {
			if mf, err := workspace.LoadManifest(snap.SyncManifestPath, r.HostRepoPath); err == nil && mf.Root == r.HostRepoPath {
				manifest = mf
			}
		}
	}
	if err := m.configureGuest(ctx, b, true); err != nil {
		return err
	}
	b.mu.Lock()
	b.bootedFrom = bootedFrom
	b.mu.Unlock()

	// Workspace: initial sync (delta against the template's manifest).
	manifestPath := filepath.Join(r.DirPath, "manifest.json")
	if r.HostRepoPath != "" {
		if manifest == nil {
			manifest = workspace.NewManifest(r.HostRepoPath)
		}
		b.syncMu.Lock()
		b.manifest = manifest
		b.syncMu.Unlock()
		if _, err := m.syncWorkspace(ctx, b); err != nil {
			return fmt.Errorf("workspace sync: %w", err)
		}
	}
	now := time.Now()
	b.mu.Lock()
	b.bootMs = time.Since(t0).Milliseconds()
	b.row.ReadyAt = nullTime(now)
	b.mu.Unlock()
	if err := m.st.W.SetSandboxReady(ctx, db.SetSandboxReadyParams{
		BootedFromSnapshotID: store.NS(snapID), ReadyAt: nullTime(now), LastActiveAt: now.UnixMilli(),
		VmmPid: int64(b.vmPID()), SyncManifestPath: manifestPath, ID: b.id,
	}); err != nil {
		return err
	}
	b.mu.Lock()
	b.row.SyncManifestPath = manifestPath
	b.mu.Unlock()
	b.setState(ctx, StateReady, "")
	b.touch()
	m.bus.Emit(b.id, "", "sandbox.ready", map[string]any{"booted_from": bootedFrom, "boot_ms": b.bootMs})
	m.startMonitors(b)
	if bootedFrom != "pool" {
		m.pool.noteDemand(tpl.ID)
	}
	return nil
}

func (b *Box) vmPID() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.vm == nil {
		return 0
	}
	return b.vm.PID()
}

// killVMKeepRes stops a half-started VM but keeps the reservation.
func (b *Box) killVMKeepRes() {
	b.mu.Lock()
	vm, nw, gc := b.vm, b.net, b.gc
	b.vm, b.net, b.gc = nil, nil, nil
	b.mu.Unlock()
	if gc != nil {
		gc.Close()
	}
	if vm != nil {
		_ = vm.Kill()
	}
	if nw != nil {
		nw.Close()
	}
}

func (m *Manager) netEventSink(id string) func(backend.NetEvent) {
	return func(e backend.NetEvent) {
		telemetry.NetEvents.WithLabelValues(e.Type).Inc()
		if e.Type == "net.denied" || e.Type == "secret.blocked" || e.Type == "net.ratelimited" {
			if b, err := m.box(id); err == nil {
				host := e.Host
				if host == "" {
					host = e.Dest
				}
				b.noteDenial(host, e.Reason)
			}
		}
		if e.BytesUp > 0 || e.BytesDown > 0 {
			telemetry.NetBytes.WithLabelValues("up").Add(float64(e.BytesUp))
			telemetry.NetBytes.WithLabelValues("down").Add(float64(e.BytesDown))
		}
		m.bus.Emit(id, "", e.Type, e)
	}
}

func (m *Manager) baseSpec(b *Box, disk string, nw backend.Net) vmm.Spec {
	r := b.row
	return vmm.Spec{
		ID: b.id, Dir: r.DirPath, Kernel: m.kernelPath, Initrd: m.initrdPath, Cmdline: cmdline,
		RootDisk: disk, VCPU: int(r.Vcpu), MemMB: int(r.MemMb), Net: nw.VMNetwork(),
		ConsoleLog: filepath.Join(r.DirPath, "console.log"),
	}
}

func (m *Manager) dialGuest(vm vmm.Machine) (*guestclient.Client, error) {
	return guestclient.Dial(func(ctx context.Context) (net.Conn, error) { return vm.DialVsock(ctx, vmm.GuestPort) })
}

func (m *Manager) waitGuest(ctx context.Context, vm vmm.Machine, gc *guestclient.Client, d time.Duration) error {
	wctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	type res struct{ err error }
	ch := make(chan res, 1)
	go func() {
		_, err := gc.WaitReady(wctx)
		ch <- res{err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
	case info := <-vm.Done():
		return fmt.Errorf("VM stopped during boot: %s", info.Reason)
	}
	if _, err := gc.CheckVersion(ctx); err != nil {
		return err
	}
	return nil
}

func consoleTail(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "console.log"))
	if err != nil {
		return ""
	}
	if len(b) > 2000 {
		b = b[len(b)-2000:]
	}
	return strings.TrimSpace(string(b))
}

// coldBoot boots a fresh VM from a rootfs image.
func (m *Manager) coldBoot(ctx context.Context, b *Box, rootfs string, diskMB int) (err error) {
	ctx, span := telemetry.Start(ctx, "vm.cold_boot", telemetry.Sandbox(b.id))
	defer func() { telemetry.End(span, err) }()
	r := b.row
	disk := filepath.Join(r.DirPath, "disk.img")
	_ = os.Remove(disk)
	if err := m.drv.CloneFile(rootfs, disk); err != nil {
		return fmt.Errorf("clone rootfs: %w", err)
	}
	if fi, err := os.Stat(disk); err == nil && fi.Size() < int64(diskMB)<<20 {
		if err := os.Truncate(disk, int64(diskMB)<<20); err != nil {
			return err
		}
	}
	nw, err := m.newNet(b, true)
	if err != nil {
		return err
	}
	vm, err := m.drv.Boot(ctx, m.baseSpec(b, disk, nw))
	if err != nil {
		nw.Close()
		return fmt.Errorf("boot: %w", err)
	}
	gc, err := m.dialGuest(vm)
	if err != nil {
		_ = vm.Kill()
		nw.Close()
		return err
	}
	b.mu.Lock()
	b.vm, b.net, b.gc = vm, nw, gc
	b.mu.Unlock()
	if err := m.waitGuest(ctx, vm, gc, 60*time.Second); err != nil {
		return fmt.Errorf("guest agent: %w\nconsole: %s", err, consoleTail(r.DirPath))
	}
	// Grow the filesystem to the disk size; "sandbox" hostname for goldens.
	if _, err := gc.Configure(ctx, &guestv1.ConfigureRequest{Hostname: "sandbox", GrowRootfs: true}); err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	return nil
}

// restoreInto starts the box from a snapshot (golden, pause, checkpoint).
func (m *Manager) restoreInto(ctx context.Context, b *Box, snap db.Snapshot) (err error) {
	ctx, span := telemetry.Start(ctx, "vm.restore", telemetry.Sandbox(b.id), attribute.String("snapshot.kind", snap.Kind))
	defer func() { telemetry.End(span, err) }()
	r := b.row
	disk := filepath.Join(r.DirPath, "disk.img")
	_ = os.Remove(disk)
	if err := m.drv.CloneFile(snap.DiskPath, disk); err != nil {
		return fmt.Errorf("clone snapshot disk: %w", err)
	}
	nw, err := m.newNet(b, true)
	if err != nil {
		return err
	}
	spec := m.baseSpec(b, disk, nw)
	spec.VCPU, spec.MemMB = int(snap.Vcpu), int(snap.MemMb)
	vm, err := m.drv.Restore(ctx, snapFiles(snap), spec)
	if err != nil {
		nw.Close()
		return err
	}
	if err := vm.Resume(ctx); err != nil {
		_ = vm.Kill()
		nw.Close()
		return err
	}
	gc, err := m.dialGuest(vm)
	if err != nil {
		_ = vm.Kill()
		nw.Close()
		return err
	}
	b.mu.Lock()
	b.vm, b.net, b.gc = vm, nw, gc
	b.mu.Unlock()
	if err := m.waitGuest(ctx, vm, gc, 20*time.Second); err != nil {
		return fmt.Errorf("guest agent after restore: %w", err)
	}
	if err := gc.Refresh(ctx); err != nil {
		return err
	}
	_ = m.st.W.AddSnapshotRef(ctx, db.AddSnapshotRefParams{Refcount: 0, LastUsedAt: time.Now().UnixMilli(), ID: snap.ID})
	return nil
}

func snapFiles(s db.Snapshot) vmm.SnapshotFiles {
	return vmm.SnapshotFiles{VMState: s.VmstatePath, Mem: s.MemPath, Disk: s.DiskPath, MachineID: s.MachineID,
		MemBytes: s.MemSizeBytes, DiskBytes: s.DiskSizeBytes}
}

// configureGuest sets identity, network, dead-man switch, and port forwards.
func (m *Manager) configureGuest(ctx context.Context, b *Box, identity bool) (err error) {
	ctx, span := telemetry.Start(ctx, "guest.configure", telemetry.Sandbox(b.id))
	defer func() { telemetry.End(span, err) }()
	b.mu.Lock()
	gc, nw := b.gc, b.net
	b.mu.Unlock()
	if err := m.ensureCA(ctx, gc); err != nil {
		return err
	}
	req := &guestv1.ConfigureRequest{
		IpCidr: nw.GuestCIDR(), Gateway: nw.Gateway(), Dns: nw.DNS(), Mtu: uint32(nw.MTU()),
		DeadmanS: uint32(m.cfg.Health.DeadmanS), SandboxId: b.id,
		Env: m.guestEnv(b),
	}
	if identity {
		req.Hostname = hostname(b.id)
	}
	if _, err := gc.Configure(ctx, req); err != nil {
		return fmt.Errorf("configure guest: %w", err)
	}
	b.mu.Lock()
	ports := make(map[uint16]int, len(b.ports))
	for g, h := range b.ports {
		ports[g] = h
	}
	b.mu.Unlock()
	for g, h := range ports {
		if hp, err := nw.Forward(g, h); err == nil {
			b.mu.Lock()
			b.ports[g] = hp
			b.mu.Unlock()
		} else if hp, err := nw.Forward(g, 0); err == nil {
			b.mu.Lock()
			b.ports[g] = hp
			b.mu.Unlock()
		}
	}
	return nil
}

// ensureRunning resumes a paused sandbox and rejects unusable states.
func (m *Manager) ensureRunning(ctx context.Context, b *Box) error {
	switch b.State() {
	case StateReady, StateBusy:
		return nil
	case StatePaused:
		b.lc.Lock()
		defer b.lc.Unlock()
		if b.State() != StatePaused {
			return nil
		}
		return m.resumeFromDisk(ctx, b)
	case StatePending, StateBooting, StatePausing:
		// Wait for the lifecycle operation in progress.
		b.lc.Lock()
		b.lc.Unlock()
		return m.ensureRunning(ctx, b)
	case StateLost:
		return errLost("sandbox %s is lost (%s); use recover to extract its workspace", b.id, b.row.StateReason)
	case StateFailed:
		return errState("sandbox %s failed to start: %s", b.id, b.row.StateReason)
	default:
		return errState("sandbox %s is %s", b.id, b.State())
	}
}

// GetSandbox returns a sandbox by id (including destroyed rows).
func (m *Manager) GetSandbox(ctx context.Context, id string) (client.Sandbox, error) {
	if b, err := m.box(id); err == nil {
		return b.view(), nil
	}
	r, err := m.st.R.GetSandbox(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return client.Sandbox{}, errNotFound("sandbox", id)
	}
	if err != nil {
		return client.Sandbox{}, err
	}
	return viewRow(r), nil
}

// ListSandboxes lists sandboxes (live by default).
func (m *Manager) ListSandboxes(ctx context.Context, clientID, sessionKey, state string, all bool) ([]client.Sandbox, error) {
	p := db.ListSandboxesParams{Limit: 500}
	if clientID != "" {
		p.ClientID = clientID
	}
	if sessionKey != "" {
		p.SessionKey = sessionKey
	}
	if state != "" {
		p.State = state
		all = true
	}
	if all {
		p.IncludeDestroyed = 1
	}
	rows, err := m.st.R.ListSandboxes(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]client.Sandbox, 0, len(rows))
	for _, r := range rows {
		if b, err := m.box(r.ID); err == nil {
			out = append(out, b.view())
		} else {
			out = append(out, viewRow(r))
		}
	}
	return out, nil
}

// Keepalive resets the idle timer.
func (m *Manager) Keepalive(ctx context.Context, id string) (client.Sandbox, error) {
	b, err := m.box(id)
	if err != nil {
		return client.Sandbox{}, err
	}
	if err := m.ensureRunning(ctx, b); err != nil {
		return client.Sandbox{}, err
	}
	b.touch()
	return b.view(), nil
}

// DestroySandbox stops the VM and deletes its working files. Logs, diffs,
// and backups stay as artifacts until retention expires.
func (m *Manager) DestroySandbox(ctx context.Context, id, reason string) error {
	b, err := m.box(id)
	if err != nil {
		return err
	}
	if reason == "" {
		reason = "deleted by client"
	}
	b.killVM() // unblocks any in-flight exec streams
	b.lc.Lock()
	defer b.lc.Unlock()
	if b.State() == StateDestroyed {
		return nil
	}
	b.failRunning("sandbox destroyed")
	m.destroyRow(b, reason)
	return nil
}

func (m *Manager) destroyRow(b *Box, reason string) {
	ctx := context.Background()
	b.killVM()
	_ = m.st.W.SetSandboxDestroyed(ctx, db.SetSandboxDestroyedParams{StateReason: reason, DestroyedAt: nullTime(time.Now()), ID: b.id})
	b.mu.Lock()
	b.state = StateDestroyed
	b.mu.Unlock()
	m.bus.Emit(b.id, "", "sandbox.destroyed", map[string]string{"reason": reason})
	// Pause snapshots belong to the sandbox; checkpoints and fork bases
	// expire through GC so restore/fork can still use them for a while.
	snaps, _ := m.st.R.ListSnapshots(ctx, db.ListSnapshotsParams{SourceSandboxID: b.id})
	for _, s := range snaps {
		if s.Kind == "pause" {
			m.deleteSnapshot(ctx, s, "owner destroyed")
		} else {
			exp := time.Now().Add(time.Duration(m.cfg.Retention.SnapshotHours) * time.Hour).UnixMilli()
			_ = m.st.W.SetSnapshotExpiry(ctx, db.SetSnapshotExpiryParams{ExpiresAt: store.NI64(exp), ID: s.ID})
		}
	}
	m.removeData(m.sandboxDir(b.id))
	m.unregister(b.id)
}
