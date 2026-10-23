package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/internal/guestclient"
	"github.com/karthikponna/tryboxi/internal/ids"
	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/scheduler"
	"github.com/karthikponna/tryboxi/internal/store"
	"github.com/karthikponna/tryboxi/internal/store/db"
	"github.com/karthikponna/tryboxi/internal/telemetry"
	"github.com/karthikponna/tryboxi/internal/workspace"
	"github.com/karthikponna/tryboxi/pkg/client"
	"go.opentelemetry.io/otel/attribute"
)

const baseTemplateName = "base"

// TemplateSpec is what a template is built from; its hash identifies it.
type TemplateSpec struct {
	Base       string            `json:"base"`
	Install    []string          `json:"install,omitempty"`
	ReadyCheck string            `json:"ready_check,omitempty"`
	Lockfiles  map[string]string `json:"lockfiles,omitempty"` // path -> sha256
	RepoPath   string            `json:"repo_path,omitempty"`
	Kernel     string            `json:"kernel"`
	Agent      string            `json:"agent"`
}

func (s TemplateSpec) hash() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ensureBaseTemplate registers the base rootfs as a template and makes
// sure it has a golden snapshot at the default size.
func (m *Manager) ensureBaseTemplate(ctx context.Context) (db.Template, error) {
	mu := m.buildLock(baseTemplateName)
	mu.Lock()
	defer mu.Unlock()
	src := m.cfg.Dir("kernels", RootfsFile())
	shaB, _ := os.ReadFile(src + ".sha256")
	rootSHA := strings.Fields(string(shaB) + " unknown")[0]
	spec := TemplateSpec{Base: "rootfs:" + rootSHA, Kernel: m.kernelVersion}
	hash := spec.hash()
	tpl, err := m.st.R.GetTemplateBySpecHash(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := os.Stat(src); err != nil {
			return db.Template{}, fmt.Errorf("base rootfs missing at %s: run `sandboxd install --artifacts <dir>`", src)
		}
		id := ids.New(ids.Template)
		dir := m.cfg.Dir("templates", id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return db.Template{}, err
		}
		rootfs := filepath.Join(dir, "rootfs.ext4")
		if err := m.drv.CloneFile(src, rootfs); err != nil {
			return db.Template{}, fmt.Errorf("clone base rootfs: %w", err)
		}
		specJSON, _ := json.Marshal(spec)
		if err := m.st.W.InsertTemplate(ctx, db.InsertTemplateParams{
			ID: id, Name: baseTemplateName, SpecHash: hash, Spec: string(specJSON), BaseImage: "ubuntu:24.04",
			Arch: runtime.GOARCH, Backend: m.drv.Name(), KernelVersion: m.kernelVersion, RootfsPath: rootfs,
			Status: "building", CreatedAt: time.Now().UnixMilli(),
		}); err != nil {
			return db.Template{}, err
		}
		fi, _ := os.Stat(rootfs)
		_ = m.st.W.SetTemplateReady(ctx, db.SetTemplateReadyParams{RootfsPath: rootfs, RootfsSizeBytes: fi.Size(),
			KernelVersion: m.kernelVersion, AgentVersion: m.agentVersion, BuiltAt: nullTime(time.Now()), ID: id})
		_ = m.st.W.MarkOtherTemplatesStale(ctx, db.MarkOtherTemplatesStaleParams{Name: baseTemplateName, ID: id})
		m.bus.Emit("", "", "template.ready", map[string]string{"template": id, "name": baseTemplateName})
		tpl, err = m.st.R.GetTemplate(ctx, id)
	}
	if err != nil {
		return db.Template{}, err
	}
	if _, ok := m.findGolden(ctx, tpl.ID, m.cfg.Defaults.VCPU, m.cfg.Defaults.MemMB, m.cfg.Defaults.DiskMB); !ok {
		t0 := time.Now()
		if _, err := m.buildGolden(ctx, tpl, nil, nil); err != nil {
			_ = m.st.W.UpdateTemplateStatus(ctx, db.UpdateTemplateStatusParams{Status: "ready", StatusReason: "golden snapshot failed: " + err.Error(), ID: tpl.ID})
			return tpl, err
		}
		m.log.Info("base golden snapshot ready", "template", tpl.ID, "took", time.Since(t0).Round(time.Millisecond))
		tpl, _ = m.st.R.GetTemplate(ctx, tpl.ID)
	}
	return tpl, nil
}

func (m *Manager) buildLock(name string) *sync.Mutex {
	v, _ := m.builds.LoadOrStore(name, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// builderBox makes an unregistered box for internal VMs (template builds,
// rescue VMs). Its working dir lives under tmp/.
func (m *Manager) builderBox(vcpu, memMB, diskMB int, nspec policy.Spec, hostPath string) *Box {
	id := ids.New(ids.Sandbox)
	nj, _ := json.Marshal(nspec)
	row := db.Sandbox{ID: id, State: StatePending, Vcpu: int64(vcpu), MemMb: int64(memMB), DiskMb: int64(diskMB),
		NetworkMode: string(nspec.Mode), NetworkPolicy: string(nj), WorkspaceMode: "host", HostRepoPath: hostPath,
		DirPath: m.cfg.Dir("tmp", id), Health: "healthy", Metadata: "{}"}
	_ = os.MkdirAll(row.DirPath, 0o700)
	return m.newBox(row)
}

func (m *Manager) discardBuilder(b *Box) {
	b.killVM()
	m.removeData(b.row.DirPath)
}

// reserveBuilder makes internal VMs count against the same budget as
// sandboxes, at a lower priority than user requests.
func (m *Manager) reserveBuilder(ctx context.Context, b *Box) error {
	res, err := m.sched.Reserve(ctx, scheduler.Request{ClientID: "internal", Priority: -1, VCPU: int(b.row.Vcpu), MemMB: int(b.row.MemMb)}, 5*time.Minute)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.res = res
	b.mu.Unlock()
	return nil
}

// buildGolden boots a template (optionally running steps) and captures its
// golden snapshot at the default size.
func (m *Manager) buildGolden(ctx context.Context, tpl db.Template, spec *TemplateSpec, logw io.Writer) (db.Snapshot, error) {
	d := m.cfg.Defaults
	nspec := policy.Spec{Mode: policy.ModeNone}
	hostPath := ""
	if spec != nil {
		nspec = policy.Spec{Mode: policy.ModeFull}
		hostPath = spec.RepoPath
	}
	b := m.builderBox(d.VCPU, d.MemMB, d.DiskMB, nspec, hostPath)
	defer m.discardBuilder(b)
	if err := m.reserveBuilder(ctx, b); err != nil {
		return db.Snapshot{}, err
	}
	if spec == nil {
		if err := m.coldBoot(ctx, b, tpl.RootfsPath, d.DiskMB); err != nil {
			return db.Snapshot{}, err
		}
		if err := m.configureGuest(ctx, b, false); err != nil {
			return db.Snapshot{}, err
		}
		return m.captureGolden(ctx, b, tpl)
	}
	// Repo template: start from the base golden, sync the repo, install.
	base, err := m.ensureBaseTemplate(ctx)
	if err != nil {
		return db.Snapshot{}, err
	}
	snap, ok := m.findGolden(ctx, base.ID, d.VCPU, d.MemMB, d.DiskMB)
	if ok {
		err = m.restoreInto(ctx, b, snap)
	} else {
		err = m.coldBoot(ctx, b, base.RootfsPath, d.DiskMB)
	}
	if err != nil {
		return db.Snapshot{}, err
	}
	if err := m.configureGuest(ctx, b, false); err != nil {
		return db.Snapshot{}, err
	}
	b.manifest = workspace.NewManifest(spec.RepoPath)
	if _, err := m.syncWorkspace(ctx, b); err != nil {
		return db.Snapshot{}, fmt.Errorf("sync: %w", err)
	}
	gc := b.guest()
	for _, step := range spec.Install {
		fmt.Fprintf(logw, "$ %s\n", step)
		code, err := guestRun(ctx, gc, step, "/workspace", 30*time.Minute, logw)
		if err != nil {
			return db.Snapshot{}, fmt.Errorf("install step %q: %w", step, err)
		}
		if code != 0 {
			return db.Snapshot{}, fmt.Errorf("install step %q exited %d", step, code)
		}
	}
	if spec.ReadyCheck != "" {
		deadline := time.Now().Add(5 * time.Minute)
		for {
			code, err := guestRun(ctx, gc, spec.ReadyCheck, "/workspace", time.Minute, logw)
			if err == nil && code == 0 {
				break
			}
			if time.Now().After(deadline) {
				return db.Snapshot{}, fmt.Errorf("ready check never passed")
			}
			time.Sleep(2 * time.Second)
		}
	}
	_, _ = guestRun(ctx, gc, "sudo rm -rf /tmp/* ~/.npm/_cacache 2>/dev/null; sync", "/", time.Minute, io.Discard)
	return m.captureGolden(ctx, b, tpl)
}

// captureGolden pauses b, snapshots it as tpl's golden snapshot, and
// resumes it.
func (m *Manager) captureGolden(ctx context.Context, b *Box, tpl db.Template) (db.Snapshot, error) {
	snap, err := m.takeSnapshot(ctx, b, "golden", m.goldenLabel(), tpl.ID)
	if err != nil {
		return snap, err
	}
	b.mu.Lock()
	vm := b.vm
	b.mu.Unlock()
	if err := vm.Resume(ctx); err != nil {
		return snap, err
	}
	d := m.cfg.Defaults
	if int(b.row.Vcpu) == d.VCPU && int(b.row.MemMb) == d.MemMB {
		fi, _ := os.Stat(tpl.RootfsPath)
		var size int64
		if fi != nil {
			size = fi.Size()
		}
		_ = m.st.W.SetTemplateReady(ctx, db.SetTemplateReadyParams{GoldenSnapshotID: store.NS(snap.ID), RootfsPath: tpl.RootfsPath,
			RootfsSizeBytes: size, KernelVersion: m.kernelVersion, AgentVersion: m.agentVersion, BuiltAt: nullTime(time.Now()), ID: tpl.ID})
	}
	m.bus.Emit("", "", "snapshot.golden", map[string]any{"template": tpl.ID, "snapshot": snap.ID, "vcpu": b.row.Vcpu, "mem_mb": b.row.MemMb})
	return snap, nil
}

// findGolden returns a compatible golden snapshot for the size, deleting
// incompatible ones (old agent/kernel/VMM) on the way.
func (m *Manager) findGolden(ctx context.Context, tplID string, vcpu, memMB, diskMB int) (db.Snapshot, bool) {
	snaps, err := m.st.R.ListSnapshots(ctx, db.ListSnapshotsParams{Kind: "golden"})
	if err != nil {
		return db.Snapshot{}, false
	}
	for _, s := range snaps {
		if !s.TemplateID.Valid || s.TemplateID.String != tplID {
			continue
		}
		if s.Backend != m.drv.Name() || s.VmmVersion != m.drv.Version() || s.AgentVersion != m.agentVersion || s.KernelVersion != m.kernelVersion || s.Label != m.goldenLabel() {
			m.deleteSnapshot(ctx, s, "incompatible with current agent/kernel/VMM/proxy CA")
			continue
		}
		if int(s.Vcpu) != vcpu || int(s.MemMb) != memMB || s.DiskSizeBytes != int64(diskMB)<<20 {
			continue
		}
		if _, err := os.Stat(s.VmstatePath); err != nil {
			m.deleteSnapshot(ctx, s, "files missing")
			continue
		}
		return s, true
	}
	return db.Snapshot{}, false
}

// resolveTemplate maps a request's template field to a ready template.
func (m *Manager) resolveTemplate(ctx context.Context, name, repo string) (db.Template, string, error) {
	switch name {
	case "", baseTemplateName:
		t, err := m.ensureBaseTemplate(ctx)
		return t, "", err
	case "auto":
		if repo == "" {
			t, err := m.ensureBaseTemplate(ctx)
			return t, "", err
		}
		return m.resolveRepoTemplate(ctx, repo)
	}
	if t, err := m.st.R.GetTemplate(ctx, name); err == nil && t.Status == "ready" {
		return t, "", nil
	}
	if t, err := m.st.R.GetReadyTemplateByName(ctx, name); err == nil {
		return t, "", nil
	}
	return db.Template{}, "", errNotFound("template", name)
}

// Detection describes a detected stack.
type Detection struct {
	Stack     string
	Install   []string
	Lockfiles []string
}

// DetectStack inspects a repo for known package managers.
func DetectStack(repo string) Detection {
	has := func(p string) bool { _, err := os.Stat(filepath.Join(repo, p)); return err == nil }
	var d Detection
	add := func(stack, cmd string, files ...string) {
		if d.Stack != "" {
			d.Stack += "+"
		}
		d.Stack += stack
		d.Install = append(d.Install, cmd)
		for _, f := range files {
			if has(f) {
				d.Lockfiles = append(d.Lockfiles, f)
			}
		}
	}
	switch {
	case has("pnpm-lock.yaml"):
		add("node-pnpm", "corepack enable --install-directory ~/.local/bin pnpm >/dev/null 2>&1; pnpm install --frozen-lockfile", "package.json", "pnpm-lock.yaml")
	case has("yarn.lock"):
		add("node-yarn", "corepack enable --install-directory ~/.local/bin yarn >/dev/null 2>&1; yarn install --immutable || yarn install --frozen-lockfile", "package.json", "yarn.lock")
	case has("package-lock.json"):
		add("node-npm", "npm ci --no-audit --no-fund", "package.json", "package-lock.json")
	case has("package.json"):
		add("node-npm", "npm install --no-audit --no-fund", "package.json")
	}
	switch {
	case has("uv.lock"):
		add("python-uv", "pip install --user --break-system-packages uv >/dev/null && ~/.local/bin/uv sync", "pyproject.toml", "uv.lock")
	case has("requirements.txt"):
		add("python-pip", "python3 -m venv .venv && .venv/bin/pip install -r requirements.txt", "requirements.txt")
	case has("pyproject.toml"):
		add("python-pip", "python3 -m venv .venv && .venv/bin/pip install -e .", "pyproject.toml")
	}
	if has("go.mod") {
		add("go", "command -v go >/dev/null || (curl -fsSL https://go.dev/dl/go1.24.4.linux-arm64.tar.gz | sudo tar -C /usr/local -xz && sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go); go mod download", "go.mod", "go.sum")
	}
	if has("Cargo.toml") {
		add("rust", "command -v cargo >/dev/null || (curl -fsSL https://sh.rustup.rs | sh -s -- -y --profile minimal); . ~/.cargo/env; cargo fetch", "Cargo.toml", "Cargo.lock")
	}
	sort.Strings(d.Lockfiles)
	return d
}

func repoTemplateName(repo string) string {
	h := sha256.Sum256([]byte(repo))
	base := strings.ToLower(filepath.Base(repo))
	var clean strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			clean.WriteRune(r)
		}
	}
	return "repo-" + clean.String() + "-" + hex.EncodeToString(h[:4])
}

func (m *Manager) repoSpec(ctx context.Context, repo string, d Detection) (TemplateSpec, error) {
	base, err := m.ensureBaseTemplate(ctx)
	if err != nil {
		return TemplateSpec{}, err
	}
	spec := TemplateSpec{Base: base.SpecHash, Install: d.Install, RepoPath: repo, Kernel: m.kernelVersion, Agent: m.agentVersion,
		Lockfiles: map[string]string{}}
	for _, f := range d.Lockfiles {
		h, err := workspace.HashFile(filepath.Join(repo, f))
		if err == nil {
			spec.Lockfiles[f] = h
		}
	}
	return spec, nil
}

func (m *Manager) resolveRepoTemplate(ctx context.Context, repo string) (db.Template, string, error) {
	d := DetectStack(repo)
	if len(d.Install) == 0 {
		t, err := m.ensureBaseTemplate(ctx)
		return t, "", err
	}
	spec, err := m.repoSpec(ctx, repo, d)
	if err != nil {
		return db.Template{}, "", err
	}
	name := repoTemplateName(repo)
	if t, err := m.st.R.GetTemplateBySpecHash(ctx, spec.hash()); err == nil && t.Status == "ready" && t.GoldenSnapshotID.Valid {
		return t, "", nil
	}
	m.startTemplateBuild(name, spec, d.Stack)
	if t, err := m.st.R.GetReadyTemplateByName(ctx, name); err == nil && t.GoldenSnapshotID.Valid {
		return t, fmt.Sprintf("dependencies changed; using previous %s template while a new one builds", d.Stack), nil
	}
	t, err := m.ensureBaseTemplate(ctx)
	return t, fmt.Sprintf("building a %s template with dependencies preinstalled in the background; this sandbox uses the base image", d.Stack), err
}

// startTemplateBuild builds a repo template in the background (deduped).
func (m *Manager) startTemplateBuild(name string, spec TemplateSpec, detected string) {
	go func() {
		if _, err := m.BuildTemplate(m.ctx, name, spec, detected); err != nil {
			m.log.Warn("template build failed", "name", name, "err", err)
		}
	}()
}

// BuildTemplate builds (or returns) the template for spec.
func (m *Manager) BuildTemplate(ctx context.Context, name string, spec TemplateSpec, detected string) (db.Template, error) {
	ctx, span := telemetry.Start(ctx, "template.build", attribute.String("template", name), attribute.String("stack", detected))
	defer span.End()
	mu := m.buildLock(name)
	mu.Lock()
	defer mu.Unlock()
	hash := spec.hash()
	if t, err := m.st.R.GetTemplateBySpecHash(ctx, hash); err == nil {
		if t.Status == "ready" || t.Status == "building" {
			return t, nil
		}
		// A failed build is retried.
		_ = m.st.W.DeleteTemplate(ctx, t.ID)
	}
	id := ids.New(ids.Template)
	dir := m.cfg.Dir("templates", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return db.Template{}, err
	}
	logPath := filepath.Join(dir, "build.log")
	specJSON, _ := json.Marshal(spec)
	commit := ""
	if mf, err := workspace.Plan(ctx, spec.RepoPath, nil, m.syncOptions()); err == nil {
		commit = mf.Next.Commit
	}
	if err := m.st.W.InsertTemplate(ctx, db.InsertTemplateParams{
		ID: id, Name: name, SpecHash: hash, Spec: string(specJSON), BaseImage: spec.Base, Arch: runtime.GOARCH,
		Backend: m.drv.Name(), KernelVersion: m.kernelVersion, AgentVersion: m.agentVersion, RepoPath: spec.RepoPath,
		RepoCommit: commit, DetectedFrom: detected, Status: "building", BuildLogPath: logPath, CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		return db.Template{}, err
	}
	m.bus.Emit("", "", "template.building", map[string]string{"template": id, "name": name, "stack": detected})
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return db.Template{}, err
	}
	defer logf.Close()
	tpl, _ := m.st.R.GetTemplate(ctx, id)
	t0 := time.Now()
	if _, err := m.buildGolden(ctx, tpl, &spec, logf); err != nil {
		fmt.Fprintf(logf, "\nBUILD FAILED: %v\n", err)
		_ = m.st.W.UpdateTemplateStatus(ctx, db.UpdateTemplateStatusParams{Status: "failed", StatusReason: err.Error(), ID: id})
		m.bus.Emit("", "", "template.failed", map[string]string{"template": id, "reason": err.Error()})
		return tpl, err
	}
	fmt.Fprintf(logf, "\nBUILD OK in %s\n", time.Since(t0).Round(time.Second))
	_ = m.st.W.MarkOtherTemplatesStale(ctx, db.MarkOtherTemplatesStaleParams{Name: name, ID: id})
	m.bus.Emit("", "", "template.ready", map[string]any{"template": id, "name": name, "build_s": int(time.Since(t0).Seconds())})
	return m.st.R.GetTemplate(ctx, id)
}

// CreateTemplate handles POST /v1/templates.
func (m *Manager) CreateTemplate(ctx context.Context, req client.TemplateRequest) (client.Template, error) {
	if req.RepoPath == "" && len(req.Install) == 0 {
		t, err := m.ensureBaseTemplate(ctx)
		return templateView(t), err
	}
	if req.RepoPath == "" {
		return client.Template{}, errInvalid("custom install steps need repo_path (the files they run against)")
	}
	repo, err := filepath.Abs(req.RepoPath)
	if err != nil {
		return client.Template{}, errInvalid("bad repo_path")
	}
	d := DetectStack(repo)
	if len(req.Install) > 0 {
		d.Install, d.Stack = req.Install, "custom"
	}
	if len(d.Install) == 0 {
		return client.Template{}, errInvalid("no known stack detected in %s; pass install steps", repo)
	}
	spec, err := m.repoSpec(ctx, repo, d)
	if err != nil {
		return client.Template{}, err
	}
	spec.ReadyCheck = req.ReadyCheck
	name := req.Name
	if name == "" {
		name = repoTemplateName(repo)
	}
	if req.Wait {
		t, err := m.BuildTemplate(ctx, name, spec, d.Stack)
		return templateView(t), err
	}
	m.startTemplateBuild(name, spec, d.Stack)
	time.Sleep(100 * time.Millisecond)
	if t, err := m.st.R.GetTemplateBySpecHash(ctx, spec.hash()); err == nil {
		return templateView(t), nil
	}
	return client.Template{Name: name, Status: "building", SpecHash: spec.hash()}, nil
}

// ListTemplates returns all templates.
func (m *Manager) ListTemplates(ctx context.Context) ([]client.Template, error) {
	rows, err := m.st.R.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]client.Template, 0, len(rows))
	for _, t := range rows {
		out = append(out, templateView(t))
	}
	return out, nil
}

// GetTemplate returns one template by id or name.
func (m *Manager) GetTemplate(ctx context.Context, id string) (client.Template, error) {
	t, err := m.st.R.GetTemplate(ctx, id)
	if err != nil {
		t, err = m.st.R.GetLatestTemplateByName(ctx, id)
	}
	if err != nil {
		return client.Template{}, errNotFound("template", id)
	}
	return templateView(t), nil
}

// TemplateLogPath returns the build log path.
func (m *Manager) TemplateLogPath(ctx context.Context, id string) (string, error) {
	t, err := m.st.R.GetTemplate(ctx, id)
	if err != nil {
		return "", errNotFound("template", id)
	}
	return t.BuildLogPath, nil
}

func templateView(t db.Template) client.Template {
	v := client.Template{ID: t.ID, Name: t.Name, Status: t.Status, StatusReason: t.StatusReason, SpecHash: t.SpecHash,
		DetectedFrom: t.DetectedFrom, RepoPath: t.RepoPath, GoldenID: t.GoldenSnapshotID.String, CreatedAt: t.CreatedAt}
	if t.BuiltAt.Valid {
		v.BuiltAt = t.BuiltAt.Int64
	}
	return v
}

// goldenLabel ties golden snapshots to the proxy CA baked into them.
func (m *Manager) goldenLabel() string {
	if m.ca == nil {
		return ""
	}
	return "ca:" + m.ca.Fingerprint()
}

// guestRun runs a shell command in the guest to completion, streaming
// combined output to w. Used for internal steps (builds, rescue).
func guestRun(ctx context.Context, gc *guestclient.Client, cmd, cwd string, timeout time.Duration, w io.Writer) (int, error) {
	return guestRunAs(ctx, gc, "", cmd, cwd, timeout, w)
}

func guestRunAs(ctx context.Context, gc *guestclient.Client, user, cmd, cwd string, timeout time.Duration, w io.Writer) (int, error) {
	id := ids.New(ids.Execution)
	st, err := gc.Exec(ctx, &guestv1.ExecRequest{ExecId: id, Command: cmd, Cwd: cwd, User: user, TimeoutS: uint32(timeout.Seconds())})
	if err != nil {
		return -1, err
	}
	for {
		f, err := st.Recv()
		if err != nil {
			return -1, err
		}
		switch k := f.Kind.(type) {
		case *guestv1.ExecFrame_Output:
			if w != nil {
				_, _ = w.Write(k.Output.Data)
			}
		case *guestv1.ExecFrame_Exit:
			_, _ = gc.Ack(ctx, &guestv1.AckRequest{ExecId: id, Seq: f.Seq})
			if k.Exit.Error != "" {
				return int(k.Exit.ExitCode), errors.New(k.Exit.Error)
			}
			return int(k.Exit.ExitCode), nil
		}
	}
}
