package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/karthikponna/tryboxi/internal/safefs"
)

// FileVersion is a file's content in the sandbox.
type FileVersion struct {
	Exists bool
	Data   []byte
	Mode   fs.FileMode
	Link   string
}

// ReadTar parses a tar stream of sandbox files into a map.
func ReadTar(r io.Reader) (map[string]FileVersion, error) {
	out := map[string]FileVersion{}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(filepath.ToSlash(h.Name), "./")
		switch h.Typeflag {
		case tar.TypeReg:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			out[name] = FileVersion{Exists: true, Data: b, Mode: fs.FileMode(h.Mode).Perm()}
		case tar.TypeSymlink:
			out[name] = FileVersion{Exists: true, Link: h.Linkname, Mode: fs.ModeSymlink | 0o777}
		}
	}
}

// BackupMeta describes a backup artifact.
type BackupMeta struct {
	Repo    string   `json:"repo"`
	Saved   []string `json:"saved"`   // files that existed and are in the tar
	Created []string `json:"created"` // files that did not exist (undo deletes them)
	Reason  string   `json:"reason"`
}

// Backup saves the current host versions of paths into a tar at out.
func Backup(repo string, paths []string, out string, reason string) (BackupMeta, error) {
	meta := BackupMeta{Repo: repo, Reason: reason}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return meta, err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return meta, err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	for _, rel := range sorted {
		full, err := safefs.JoinRepo(repo, rel)
		if err != nil {
			continue
		}
		fi, err := os.Lstat(full)
		if errors.Is(err, fs.ErrNotExist) {
			meta.Created = append(meta.Created, rel)
			continue
		}
		if err != nil || !(fi.Mode().IsRegular() || fi.Mode()&fs.ModeSymlink != 0) {
			continue
		}
		link := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			link, _ = os.Readlink(full)
		}
		h, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return meta, err
		}
		h.Name = rel
		if err := tw.WriteHeader(h); err != nil {
			return meta, err
		}
		if fi.Mode().IsRegular() {
			src, err := os.Open(full)
			if err != nil {
				return meta, err
			}
			_, err = io.Copy(tw, src)
			src.Close()
			if err != nil {
				return meta, err
			}
		}
		meta.Saved = append(meta.Saved, rel)
	}
	if err := tw.Close(); err != nil {
		return meta, err
	}
	return meta, f.Sync()
}

// Undo restores a backup: saved files are written back and files the
// change created are removed.
func Undo(repo, tarPath string, meta BackupMeta) (restored, removed int, err error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	vers, err := ReadTar(f)
	if err != nil {
		return 0, 0, err
	}
	for rel, v := range vers {
		full, err := safefs.JoinRepo(repo, rel)
		if err != nil {
			return restored, removed, err
		}
		if err := writeVersion(repo, full, v); err != nil {
			return restored, removed, err
		}
		restored++
	}
	for _, rel := range meta.Created {
		full, err := safefs.JoinRepo(repo, rel)
		if err != nil {
			continue
		}
		if err := safefs.RemoveUnder(repo, full); err == nil {
			removed++
		}
	}
	return restored, removed, nil
}

func writeVersion(repo, full string, v FileVersion) error {
	if v.Link != "" {
		if err := safefs.RemoveUnder(repo, full); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		return os.Symlink(v.Link, full)
	}
	mode := v.Mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	if fi, err := os.Lstat(full); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		if err := safefs.RemoveUnder(repo, full); err != nil {
			return err
		}
	}
	return safefs.WriteFileUnder(repo, full, v.Data, mode)
}

// Change is one path changed in the sandbox relative to its git HEAD.
type Change struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // A, M, D, R, U (untracked)
	OldPath string `json:"old_path,omitempty"`
}

// FileResult is the outcome for one path.
type FileResult struct {
	Path   string `json:"path"`
	Action string `json:"action"` // written, deleted, merged, conflict, skipped, unchanged
	Reason string `json:"reason,omitempty"`
}

// ApplyResult summarizes an apply or pull.
type ApplyResult struct {
	Files     []FileResult `json:"files"`
	Applied   int          `json:"applied"`
	Conflicts int          `json:"conflicts"`
	Skipped   int          `json:"skipped"`
}

func (r *ApplyResult) add(fr FileResult) {
	r.Files = append(r.Files, fr)
	switch fr.Action {
	case "written", "deleted", "merged":
		r.Applied++
	case "conflict":
		r.Conflicts++
	case "skipped":
		r.Skipped++
	}
}

func gitShow(ctx context.Context, repo, commit, rel string) ([]byte, bool) {
	if commit == "" {
		return nil, false
	}
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "show", commit+":"+rel).Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

func commitExists(ctx context.Context, repo, commit string) bool {
	if commit == "" {
		return false
	}
	return exec.CommandContext(ctx, "git", "-C", repo, "cat-file", "-e", commit+"^{commit}").Run() == nil
}

func mergeFile(ctx context.Context, ours, base, theirs []byte) ([]byte, bool, error) {
	dir, err := os.MkdirTemp("", "sandboxd-merge-")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(dir)
	paths := []string{filepath.Join(dir, "ours"), filepath.Join(dir, "base"), filepath.Join(dir, "theirs")}
	for i, b := range [][]byte{ours, base, theirs} {
		if err := os.WriteFile(paths[i], b, 0o600); err != nil {
			return nil, false, err
		}
	}
	cmd := exec.CommandContext(ctx, "git", "merge-file", "-p", "-L", "host", "-L", "base", "-L", "sandbox", paths[0], paths[1], paths[2])
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() > 0 {
			return out, true, nil
		}
		return nil, false, err
	}
	return out, false, nil
}

// Apply merges sandbox versions of changed files into the host repo using a
// 3-way merge against the sandbox's base commit. It never touches .git.
func Apply(ctx context.Context, repo, baseCommit string, changes []Change, vm map[string]FileVersion) (*ApplyResult, error) {
	res := &ApplyResult{}
	if !commitExists(ctx, repo, baseCommit) {
		baseCommit = "HEAD"
	}
	for _, ch := range changes {
		full, err := safefs.JoinRepo(repo, ch.Path)
		if err != nil {
			res.add(FileResult{Path: ch.Path, Action: "skipped", Reason: err.Error()})
			continue
		}
		hostData, hostErr := os.ReadFile(full)
		hostExists := hostErr == nil
		base, baseExists := gitShow(ctx, repo, baseCommit, ch.Path)
		theirs, inVM := vm[ch.Path]

		if ch.Status == "D" {
			switch {
			case !hostExists:
				res.add(FileResult{Path: ch.Path, Action: "unchanged"})
			case baseExists && bytes.Equal(hostData, base):
				if err := safefs.RemoveUnder(repo, full); err != nil {
					res.add(FileResult{Path: ch.Path, Action: "skipped", Reason: err.Error()})
					continue
				}
				res.add(FileResult{Path: ch.Path, Action: "deleted"})
			default:
				res.add(FileResult{Path: ch.Path, Action: "conflict", Reason: "deleted in sandbox but changed on host; host file kept"})
			}
			continue
		}
		if !inVM || !theirs.Exists {
			res.add(FileResult{Path: ch.Path, Action: "skipped", Reason: "file missing from sandbox snapshot"})
			continue
		}
		if ch.OldPath != "" {
			// Rename: delete the old path if the host still has the base version.
			if oldFull, err := safefs.JoinRepo(repo, ch.OldPath); err == nil {
				if oldHost, err := os.ReadFile(oldFull); err == nil {
					if oldBase, ok := gitShow(ctx, repo, baseCommit, ch.OldPath); ok && bytes.Equal(oldHost, oldBase) {
						_ = safefs.RemoveUnder(repo, oldFull)
						res.add(FileResult{Path: ch.OldPath, Action: "deleted", Reason: "renamed to " + ch.Path})
					}
				}
			}
		}
		if theirs.Link != "" {
			if err := writeVersion(repo, full, theirs); err != nil {
				res.add(FileResult{Path: ch.Path, Action: "skipped", Reason: err.Error()})
			} else {
				res.add(FileResult{Path: ch.Path, Action: "written"})
			}
			continue
		}
		switch {
		case hostExists && bytes.Equal(hostData, theirs.Data):
			res.add(FileResult{Path: ch.Path, Action: "unchanged"})
		case !hostExists || (baseExists && bytes.Equal(hostData, base)):
			if err := writeVersion(repo, full, theirs); err != nil {
				res.add(FileResult{Path: ch.Path, Action: "skipped", Reason: err.Error()})
				continue
			}
			res.add(FileResult{Path: ch.Path, Action: "written"})
		default:
			merged, conflict, err := mergeFile(ctx, hostData, base, theirs.Data)
			if err != nil {
				res.add(FileResult{Path: ch.Path, Action: "skipped", Reason: "merge: " + err.Error()})
				continue
			}
			v := theirs
			v.Data = merged
			if err := writeVersion(repo, full, v); err != nil {
				res.add(FileResult{Path: ch.Path, Action: "skipped", Reason: err.Error()})
				continue
			}
			if conflict {
				res.add(FileResult{Path: ch.Path, Action: "conflict", Reason: "merged with conflict markers"})
			} else {
				res.add(FileResult{Path: ch.Path, Action: "merged"})
			}
		}
	}
	return res, nil
}

// Pull copies sandbox versions of paths to the host. A host file that
// changed since the last sync is left alone unless force is set.
func Pull(repo string, paths []string, vm map[string]FileVersion, m *Manifest, force bool) (*ApplyResult, error) {
	res := &ApplyResult{}
	for _, rel := range paths {
		full, err := safefs.JoinRepo(repo, rel)
		if err != nil {
			res.add(FileResult{Path: rel, Action: "skipped", Reason: err.Error()})
			continue
		}
		v, ok := vm[rel]
		if !ok || !v.Exists {
			res.add(FileResult{Path: rel, Action: "skipped", Reason: "not present in sandbox (deletions are never pulled)"})
			continue
		}
		synced, wasSynced := m.Files[rel]
		cur, err := hashFile(full)
		hostExists := err == nil
		if !force {
			if hostExists && wasSynced && cur != synced.Sha {
				res.add(FileResult{Path: rel, Action: "skipped", Reason: "host file changed since last sync (use force)"})
				continue
			}
			if hostExists && !wasSynced {
				if cur == sha(v.Data) {
					res.add(FileResult{Path: rel, Action: "unchanged"})
				} else {
					res.add(FileResult{Path: rel, Action: "skipped", Reason: "host file exists but was never synced (use force)"})
				}
				continue
			}
			if !hostExists && wasSynced {
				res.add(FileResult{Path: rel, Action: "skipped", Reason: "host file was deleted since last sync (use force)"})
				continue
			}
		}
		if hostExists && cur == sha(v.Data) {
			res.add(FileResult{Path: rel, Action: "unchanged"})
			continue
		}
		if err := writeVersion(repo, full, v); err != nil {
			res.add(FileResult{Path: rel, Action: "skipped", Reason: err.Error()})
			continue
		}
		if fi, err := os.Lstat(full); err == nil {
			m.Files[rel] = Entry{Size: fi.Size(), MtimeNs: fi.ModTime().UnixNano(), Sha: sha(v.Data), Mode: uint32(fi.Mode())}
		}
		res.add(FileResult{Path: rel, Action: "written"})
	}
	return res, nil
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// MarshalMeta renders backup metadata for the artifacts table.
func (b BackupMeta) MarshalMeta() string {
	out, _ := json.Marshal(b)
	return string(out)
}

// ParseMeta reads backup metadata.
func ParseMeta(s string) (BackupMeta, error) {
	var m BackupMeta
	err := json.Unmarshal([]byte(s), &m)
	if err != nil {
		return m, fmt.Errorf("bad backup metadata: %w", err)
	}
	return m, nil
}
