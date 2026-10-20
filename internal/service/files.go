package service

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	guestv1 "github.com/karthikponna/tryboxi/gen/guest/v1"
	"github.com/karthikponna/tryboxi/internal/guestclient"
	"github.com/karthikponna/tryboxi/internal/ids"
	"github.com/karthikponna/tryboxi/internal/store"
	"github.com/karthikponna/tryboxi/internal/store/db"
	"github.com/karthikponna/tryboxi/internal/workspace"
	"github.com/karthikponna/tryboxi/pkg/client"
)

const chunkSize = 256 << 10

// putTar streams a tar archive into dest in the guest.
func putTar(ctx context.Context, gc *guestclient.Client, dest string, deletes []string, r io.Reader) (*guestv1.PutFilesResponse, error) {
	st, err := gc.PutFiles(ctx)
	if err != nil {
		return nil, err
	}
	first := &guestv1.PutFilesChunk{DestDir: dest, Delete: deletes, Owner: "agent"}
	buf := make([]byte, chunkSize)
	sentFirst := false
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			c := &guestv1.PutFilesChunk{Data: append([]byte(nil), buf[:n]...)}
			if !sentFirst {
				first.Data = c.Data
				c = first
				sentFirst = true
			}
			if err := st.Send(c); err != nil {
				_, cerr := st.CloseAndRecv()
				if cerr != nil {
					return nil, cerr
				}
				return nil, err
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	if !sentFirst {
		if err := st.Send(first); err != nil {
			return nil, err
		}
	}
	return st.CloseAndRecv()
}

// getTar fetches files from the guest as a tar stream.
func getTar(ctx context.Context, gc *guestclient.Client, req *guestv1.GetFilesRequest) ([]byte, error) {
	st, err := gc.GetFiles(ctx, req)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for {
		c, err := st.Recv()
		if err == io.EOF {
			return buf.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
		buf.Write(c.Data)
	}
}

// syncWorkspace pushes host changes into /workspace (one-way).
func (m *Manager) syncWorkspace(ctx context.Context, b *Box) (*client.SyncResult, error) {
	if b.row.HostRepoPath == "" {
		return nil, errInvalid("sandbox %s has no host workspace", b.id)
	}
	b.syncMu.Lock()
	defer b.syncMu.Unlock()
	t0 := time.Now()
	prev := b.manifest
	if prev == nil {
		mf, err := workspace.LoadManifest(b.row.SyncManifestPath, b.row.HostRepoPath)
		if err != nil {
			return nil, err
		}
		prev = mf
	}
	d, err := workspace.Plan(ctx, b.row.HostRepoPath, prev, m.syncOptions())
	if err != nil {
		return nil, err
	}
	res := &client.SyncResult{Sent: len(d.Send), Deleted: len(d.Delete), Bytes: d.Bytes, Skipped: d.Skipped}
	if len(d.Send) > 0 || len(d.Delete) > 0 {
		gc := b.guest()
		if gc == nil {
			return nil, errState("sandbox not running")
		}
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(d.WriteTar(pw, b.row.HostRepoPath)) }()
		if _, err := putTar(ctx, gc, "/workspace", d.Delete, pr); err != nil {
			pr.CloseWithError(err)
			return nil, err
		}
	}
	b.manifest = d.Next
	if b.row.SyncManifestPath != "" {
		_ = d.Next.Save(b.row.SyncManifestPath)
	}
	res.DurationMs = time.Since(t0).Milliseconds()
	b.lastSync = res
	if res.Sent > 0 || res.Deleted > 0 {
		m.bus.Emit(b.id, "", "workspace.synced", res)
	}
	return res, nil
}

// Sync handles POST /sync.
func (m *Manager) Sync(ctx context.Context, id string) (*client.SyncResult, error) {
	b, err := m.box(id)
	if err != nil {
		return nil, err
	}
	if err := m.ensureRunning(ctx, b); err != nil {
		return nil, err
	}
	b.lc.RLock()
	defer b.lc.RUnlock()
	b.touch()
	return m.syncWorkspace(ctx, b)
}

func guestPath(p string) string {
	if p == "" {
		return "/workspace"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/workspace/" + p
	}
	return path.Clean(p)
}

// PutFile writes data (or a tar stream) into the sandbox.
func (m *Manager) PutFile(ctx context.Context, id, p string, r io.Reader, isTar bool) (map[string]any, error) {
	b, err := m.box(id)
	if err != nil {
		return nil, err
	}
	if err := m.ensureRunning(ctx, b); err != nil {
		return nil, err
	}
	b.lc.RLock()
	defer b.lc.RUnlock()
	b.touch()
	gc := b.guest()
	p = guestPath(p)
	var resp *guestv1.PutFilesResponse
	if isTar {
		resp, err = putTar(ctx, gc, p, nil, r)
	} else {
		data, rerr := io.ReadAll(io.LimitReader(r, 512<<20))
		if rerr != nil {
			return nil, rerr
		}
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: path.Base(p), Mode: 0o644, Size: int64(len(data)), ModTime: time.Now(), Typeflag: tar.TypeReg})
		_, _ = tw.Write(data)
		_ = tw.Close()
		resp, err = putTar(ctx, gc, path.Dir(p), nil, &buf)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": p, "files_written": resp.FilesWritten, "bytes": resp.BytesWritten}, nil
}

// GetFile reads a file (raw) or directory (tar) from the sandbox.
func (m *Manager) GetFile(ctx context.Context, id, p string, forceTar bool, maxBytes int64) (data []byte, isTar bool, err error) {
	b, err := m.box(id)
	if err != nil {
		return nil, false, err
	}
	if err := m.ensureRunning(ctx, b); err != nil {
		return nil, false, err
	}
	b.lc.RLock()
	defer b.lc.RUnlock()
	b.touch()
	gc := b.guest()
	p = guestPath(p)
	st, err := gc.Stat(ctx, &guestv1.StatRequest{Path: p})
	if err != nil {
		return nil, false, err
	}
	if !st.Exists {
		return nil, false, errNotFound("file", p)
	}
	if maxBytes <= 0 {
		maxBytes = 256 << 20
	}
	raw, err := getTar(ctx, gc, &guestv1.GetFilesRequest{Path: p, MaxBytes: uint64(maxBytes), Exclude: []string{"node_modules", ".git"}})
	if err != nil {
		return nil, false, err
	}
	if st.IsDir || forceTar {
		return raw, true, nil
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	if _, err := tr.Next(); err != nil {
		return nil, false, err
	}
	out, err := io.ReadAll(tr)
	return out, false, err
}

func (m *Manager) newArtifact(ctx context.Context, sandboxID, execID, kind, ext string, data []byte, meta any, ttl time.Duration) (db.Artifact, error) {
	id := ids.New(ids.Artifact)
	p := m.cfg.Dir("artifacts", id+ext)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return db.Artifact{}, err
	}
	sum := sha256.Sum256(data)
	mj, _ := json.Marshal(meta)
	now := time.Now()
	a := db.InsertArtifactParams{ID: id, SandboxID: store.NS(sandboxID), ExecutionID: store.NS(execID), Kind: kind, Path: p,
		Sha256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(data)), Meta: string(mj), CreatedAt: now.UnixMilli()}
	if ttl > 0 {
		a.ExpiresAt = store.NI64(now.Add(ttl).UnixMilli())
	}
	if err := m.st.W.InsertArtifact(ctx, a); err != nil {
		return db.Artifact{}, err
	}
	m.bus.Emit(sandboxID, execID, "artifact.created", map[string]any{"artifact": id, "kind": kind, "bytes": len(data)})
	return m.st.R.GetArtifact(ctx, id)
}

type diffMeta struct {
	Head    string             `json:"head"`
	Changes []workspace.Change `json:"changes"`
	Repo    string             `json:"repo"`
	Files   string             `json:"files_tar"`
}

// Diff computes the sandbox's changes vs its git HEAD and stores them (plus
// the changed files themselves) as an artifact.
func (m *Manager) Diff(ctx context.Context, id string) (client.DiffResult, error) {
	b, err := m.box(id)
	if err != nil {
		return client.DiffResult{}, err
	}
	if err := m.ensureRunning(ctx, b); err != nil {
		return client.DiffResult{}, err
	}
	b.lc.RLock()
	defer b.lc.RUnlock()
	b.touch()
	gc := b.guest()
	d, err := gc.Diff(ctx, &guestv1.DiffRequest{Dir: "/workspace"})
	if err != nil {
		return client.DiffResult{}, err
	}
	var changes []workspace.Change
	var paths []string
	for _, c := range d.Changes {
		changes = append(changes, workspace.Change{Path: c.Path, Status: c.Status, OldPath: c.OldPath})
		if c.Status != "D" {
			paths = append(paths, c.Path)
		}
	}
	for _, u := range d.Untracked {
		changes = append(changes, workspace.Change{Path: u, Status: "U"})
		paths = append(paths, u)
	}
	filesTar := []byte{}
	if len(paths) > 0 {
		filesTar, err = getTar(ctx, gc, &guestv1.GetFilesRequest{Path: "/workspace", Paths: paths, MaxBytes: 256 << 20})
		if err != nil {
			return client.DiffResult{}, fmt.Errorf("fetch changed files: %w", err)
		}
	}
	ttl := time.Duration(m.cfg.Retention.ArtifactsDays) * 24 * time.Hour
	ftar, err := m.newArtifact(ctx, b.id, "", "tar", ".tar", filesTar, map[string]string{"for": "diff"}, ttl)
	if err != nil {
		return client.DiffResult{}, err
	}
	art, err := m.newArtifact(ctx, b.id, "", "diff", ".patch", d.Patch, diffMeta{Head: d.Head, Changes: changes, Repo: b.row.HostRepoPath, Files: ftar.ID}, ttl)
	if err != nil {
		return client.DiffResult{}, err
	}
	res := client.DiffResult{ArtifactID: art.ID, Head: d.Head, PatchBytes: len(d.Patch), Changes: []client.Change{}}
	for _, c := range changes {
		res.Changes = append(res.Changes, client.Change{Path: c.Path, Status: c.Status, OldPath: c.OldPath})
	}
	patch := string(d.Patch)
	if len(patch) > 64<<10 {
		patch = patch[:64<<10] + "\n... patch truncated; fetch the artifact for the full diff ...\n"
		res.Truncated = true
	}
	res.Patch = patch
	return res, nil
}

// Apply merges a diff artifact into the host repo after backing up every
// host file it touches.
func (m *Manager) Apply(ctx context.Context, id string, req client.ApplyRequest) (client.ApplyResult, error) {
	b, err := m.box(id)
	if err != nil {
		return client.ApplyResult{}, err
	}
	if b.row.HostRepoPath == "" {
		return client.ApplyResult{}, errInvalid("sandbox %s has no host workspace to apply to", id)
	}
	artID := req.ArtifactID
	if artID == "" {
		d, err := m.Diff(ctx, id)
		if err != nil {
			return client.ApplyResult{}, err
		}
		artID = d.ArtifactID
	}
	art, err := m.st.R.GetArtifact(ctx, artID)
	if err != nil || art.Kind != "diff" || art.SandboxID.String != id {
		return client.ApplyResult{}, errNotFound("diff artifact for this sandbox", artID)
	}
	var meta diffMeta
	if err := json.Unmarshal([]byte(art.Meta), &meta); err != nil {
		return client.ApplyResult{}, err
	}
	ftar, err := m.st.R.GetArtifact(ctx, meta.Files)
	if err != nil {
		return client.ApplyResult{}, errNotFound("diff files artifact", meta.Files)
	}
	f, err := os.Open(ftar.Path)
	if err != nil {
		return client.ApplyResult{}, err
	}
	vm, err := workspace.ReadTar(f)
	f.Close()
	if err != nil {
		return client.ApplyResult{}, err
	}
	changes := meta.Changes
	if len(req.Paths) > 0 {
		want := map[string]bool{}
		for _, p := range req.Paths {
			want[p] = true
		}
		var sub []workspace.Change
		for _, c := range changes {
			if want[c.Path] {
				sub = append(sub, c)
			}
		}
		changes = sub
	}
	var touched []string
	for _, c := range changes {
		touched = append(touched, c.Path)
		if c.OldPath != "" {
			touched = append(touched, c.OldPath)
		}
	}
	backup, err := m.backup(ctx, b, touched, "apply "+artID)
	if err != nil {
		return client.ApplyResult{}, err
	}
	res, err := workspace.Apply(ctx, b.row.HostRepoPath, meta.Head, changes, vm)
	if err != nil {
		return client.ApplyResult{}, err
	}
	out := applyView(res, backup)
	m.bus.Emit(b.id, "", "workspace.applied", map[string]any{"artifact": artID, "applied": res.Applied, "conflicts": res.Conflicts, "backup": backup})
	// Host now matches; refresh the manifest so the next sync is a no-op.
	if b.row.WorkspaceMode == "host" && b.State() != StatePaused {
		_, _ = m.Sync(ctx, id)
	}
	return out, nil
}

func applyView(res *workspace.ApplyResult, backup string) client.ApplyResult {
	out := client.ApplyResult{BackupArtifactID: backup, Applied: res.Applied, Conflicts: res.Conflicts, Skipped: res.Skipped, Files: []client.FileResult{}}
	for _, f := range res.Files {
		out.Files = append(out.Files, client.FileResult{Path: f.Path, Action: f.Action, Reason: f.Reason})
	}
	return out
}

func (m *Manager) backup(ctx context.Context, b *Box, paths []string, reason string) (string, error) {
	id := ids.New(ids.Artifact)
	p := m.cfg.Dir("artifacts", id+".tar")
	meta, err := workspace.Backup(b.row.HostRepoPath, paths, p, reason)
	if err != nil {
		return "", fmt.Errorf("backup before writing host files: %w", err)
	}
	fi, _ := os.Stat(p)
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	now := time.Now()
	if err := m.st.W.InsertArtifact(ctx, db.InsertArtifactParams{ID: id, SandboxID: store.NS(b.id), Kind: "backup", Path: p, SizeBytes: size,
		Meta: meta.MarshalMeta(), CreatedAt: now.UnixMilli(), ExpiresAt: store.NI64(now.Add(time.Duration(m.cfg.Retention.ArtifactsDays) * 24 * time.Hour).UnixMilli())}); err != nil {
		return "", err
	}
	m.bus.Emit(b.id, "", "artifact.created", map[string]any{"artifact": id, "kind": "backup", "files": len(paths)})
	return id, nil
}

// Pull copies files changed inside the sandbox back to the host.
func (m *Manager) Pull(ctx context.Context, id string, req client.PullRequest) (client.ApplyResult, error) {
	b, err := m.box(id)
	if err != nil {
		return client.ApplyResult{}, err
	}
	if b.row.HostRepoPath == "" {
		return client.ApplyResult{}, errInvalid("sandbox %s has no host workspace", id)
	}
	if err := m.ensureRunning(ctx, b); err != nil {
		return client.ApplyResult{}, err
	}
	paths := req.Paths
	if len(paths) == 0 {
		execs, err := m.st.R.ListExecutions(ctx, db.ListExecutionsParams{SandboxID: id, Limit: 1})
		if err == nil && len(execs) > 0 {
			_ = json.Unmarshal([]byte(execs[0].ChangedFiles), &paths)
		}
	}
	if len(paths) == 0 {
		return client.ApplyResult{Files: []client.FileResult{}}, nil
	}
	b.lc.RLock()
	defer b.lc.RUnlock()
	b.touch()
	raw, err := getTar(ctx, b.guest(), &guestv1.GetFilesRequest{Path: "/workspace", Paths: paths, MaxBytes: 256 << 20})
	if err != nil {
		return client.ApplyResult{}, err
	}
	vm, err := workspace.ReadTar(bytes.NewReader(raw))
	if err != nil {
		return client.ApplyResult{}, err
	}
	backup, err := m.backup(ctx, b, paths, "pull")
	if err != nil {
		return client.ApplyResult{}, err
	}
	b.syncMu.Lock()
	if b.manifest == nil {
		b.manifest, _ = workspace.LoadManifest(b.row.SyncManifestPath, b.row.HostRepoPath)
	}
	res, err := workspace.Pull(b.row.HostRepoPath, paths, vm, b.manifest, req.Force)
	if err == nil && b.row.SyncManifestPath != "" {
		_ = b.manifest.Save(b.row.SyncManifestPath)
	}
	b.syncMu.Unlock()
	if err != nil {
		return client.ApplyResult{}, err
	}
	m.bus.Emit(b.id, "", "workspace.pulled", map[string]any{"applied": res.Applied, "skipped": res.Skipped, "backup": backup})
	return applyView(res, backup), nil
}

// Undo restores a backup artifact.
func (m *Manager) Undo(ctx context.Context, artifactID string) (client.UndoResult, error) {
	a, err := m.st.R.GetArtifact(ctx, artifactID)
	if err != nil || a.Kind != "backup" {
		return client.UndoResult{}, errNotFound("backup artifact", artifactID)
	}
	meta, err := workspace.ParseMeta(a.Meta)
	if err != nil {
		return client.UndoResult{}, err
	}
	restored, removed, err := workspace.Undo(meta.Repo, a.Path, meta)
	if err != nil {
		return client.UndoResult{}, err
	}
	m.bus.Emit(a.SandboxID.String, "", "workspace.undo", map[string]any{"artifact": artifactID, "restored": restored, "removed": removed})
	return client.UndoResult{Restored: restored, Removed: removed}, nil
}

// ListArtifacts lists artifacts for a sandbox (or all).
func (m *Manager) ListArtifacts(ctx context.Context, sandboxID, kind string) ([]client.Artifact, error) {
	p := db.ListArtifactsParams{Limit: 200}
	if sandboxID != "" {
		p.SandboxID = sandboxID
	}
	if kind != "" {
		p.Kind = kind
	}
	rows, err := m.st.R.ListArtifacts(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]client.Artifact, 0, len(rows))
	for _, a := range rows {
		v := client.Artifact{ID: a.ID, SandboxID: a.SandboxID.String, ExecutionID: a.ExecutionID.String, Kind: a.Kind,
			SizeBytes: a.SizeBytes, CreatedAt: a.CreatedAt, Meta: json.RawMessage(a.Meta)}
		if a.ExpiresAt.Valid {
			v.ExpiresAt = a.ExpiresAt.Int64
		}
		out = append(out, v)
	}
	return out, nil
}

// ArtifactPath returns the file behind an artifact.
func (m *Manager) ArtifactPath(ctx context.Context, id string) (string, string, error) {
	a, err := m.st.R.GetArtifact(ctx, id)
	if err != nil {
		return "", "", errNotFound("artifact", id)
	}
	return a.Path, a.Kind, nil
}

// ForwardPort exposes a guest TCP port on 127.0.0.1.
func (m *Manager) ForwardPort(ctx context.Context, id string, req client.PortRequest) (client.PortResult, error) {
	if req.GuestPort <= 0 || req.GuestPort > 65535 {
		return client.PortResult{}, errInvalid("guest_port must be 1-65535")
	}
	b, err := m.box(id)
	if err != nil {
		return client.PortResult{}, err
	}
	if err := m.ensureRunning(ctx, b); err != nil {
		return client.PortResult{}, err
	}
	b.mu.Lock()
	nw := b.net
	b.mu.Unlock()
	if nw == nil {
		return client.PortResult{}, errState("sandbox has no network")
	}
	hp, err := nw.Forward(uint16(req.GuestPort), req.HostPort)
	if err != nil {
		return client.PortResult{}, err
	}
	b.mu.Lock()
	b.ports[uint16(req.GuestPort)] = hp
	var meta map[string]string
	_ = json.Unmarshal([]byte(b.row.Metadata), &meta)
	if meta == nil {
		meta = map[string]string{}
	}
	meta["port:"+strconv.Itoa(req.GuestPort)] = strconv.Itoa(hp)
	mj, _ := json.Marshal(meta)
	b.row.Metadata = string(mj)
	b.mu.Unlock()
	b.touch()
	m.bus.Emit(b.id, "", "port.forwarded", map[string]int{"guest_port": req.GuestPort, "host_port": hp})
	return client.PortResult{GuestPort: req.GuestPort, HostPort: hp, URL: fmt.Sprintf("http://127.0.0.1:%d", hp)}, nil
}

var _ = filepath.Join
