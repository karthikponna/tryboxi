// Package workspace moves repo files between the host and a sandbox:
// one-way delta sync into the VM, and diff/apply/pull back to the host
// with a backup taken before every host write.
package workspace

import (
	"archive/tar"
	"bufio"
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
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Entry is the last-synced state of one file.
type Entry struct {
	Size    int64  `json:"s"`
	MtimeNs int64  `json:"m"`
	Sha     string `json:"h"`
	Mode    uint32 `json:"p"`
	Link    string `json:"l,omitempty"`
}

// Manifest records what the VM's /workspace was synced from.
type Manifest struct {
	Root     string           `json:"root"`
	Commit   string           `json:"commit,omitempty"`
	SyncedAt int64            `json:"synced_at"`
	Files    map[string]Entry `json:"files"`
}

// NewManifest returns an empty manifest for root.
func NewManifest(root string) *Manifest { return &Manifest{Root: root, Files: map[string]Entry{}} }

// LoadManifest reads a manifest; a missing file yields an empty one.
func LoadManifest(p, root string) (*Manifest, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) || p == "" {
		return NewManifest(root), nil
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Files == nil {
		m.Files = map[string]Entry{}
	}
	if root != "" {
		m.Root = root
	}
	return &m, nil
}

// Save writes the manifest atomically.
func (m *Manifest) Save(p string) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Clone deep-copies the manifest.
func (m *Manifest) Clone() *Manifest {
	c := &Manifest{Root: m.Root, Commit: m.Commit, SyncedAt: m.SyncedAt, Files: make(map[string]Entry, len(m.Files))}
	for k, v := range m.Files {
		c.Files[k] = v
	}
	return c
}

// Options control what is synced.
type Options struct {
	Excludes       []string // directory/file base names skipped anywhere
	SecretExcludes []string // base-name globs never synced
	MaxFileBytes   int64
	// DenyContent: files containing any of these byte strings (stored
	// secret values) are never synced.
	DenyContent [][]byte
}

// Delta is the set of changes to push into the VM.
type Delta struct {
	Send    []string
	Delete  []string
	Skipped []string
	Bytes   int64
	Next    *Manifest
}

func (o Options) excluded(rel string) bool {
	segs := strings.Split(rel, "/")
	inGit := len(segs) > 0 && segs[0] == ".git"
	for _, s := range segs {
		if inGit {
			break
		}
		for _, e := range o.Excludes {
			if s == e {
				return true
			}
		}
		for _, pat := range o.SecretExcludes {
			if ok, _ := path.Match(pat, s); ok {
				return true
			}
		}
	}
	if inGit {
		base := segs[len(segs)-1]
		if strings.HasSuffix(base, ".lock") || strings.HasPrefix(base, "tmp_") {
			return true
		}
	}
	return false
}

func isGitRepo(root string) bool {
	fi, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil && fi.IsDir()
}

// candidates lists repo-relative paths that may be synced.
func candidates(ctx context.Context, root string, opt Options) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(rel string) {
		rel = filepath.ToSlash(rel)
		if rel == "" || seen[rel] || opt.excluded(rel) {
			return
		}
		seen[rel] = true
		out = append(out, rel)
	}
	if isGitRepo(root) {
		cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "-c", "-o", "--exclude-standard")
		b, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git ls-files: %w", err)
		}
		for _, p := range bytes.Split(b, []byte{0}) {
			if len(p) > 0 {
				add(string(p))
			}
		}
		err = filepath.WalkDir(filepath.Join(root, ".git"), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			add(rel)
			return nil
		})
		return out, err
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if opt.excluded(filepath.ToSlash(rel)) {
				return filepath.SkipDir
			}
			return nil
		}
		add(rel)
		return nil
	})
	return out, err
}

func hashFile(p string) (string, error) {
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

// HashFile exposes the content hash used by manifests.
func HashFile(p string) (string, error) { return hashFile(p) }

// Plan compares the repo against prev and returns what must be sent.
func Plan(ctx context.Context, root string, prev *Manifest, opt Options) (*Delta, error) {
	if prev == nil {
		prev = NewManifest(root)
	}
	cands, err := candidates(ctx, root, opt)
	if err != nil {
		return nil, err
	}
	next := &Manifest{Root: root, Files: make(map[string]Entry, len(cands)), SyncedAt: time.Now().UnixMilli()}
	d := &Delta{Next: next}
	for _, rel := range cands {
		full := filepath.Join(root, filepath.FromSlash(rel))
		fi, err := os.Lstat(full)
		if err != nil {
			continue // listed by git but deleted on disk
		}
		e := Entry{Size: fi.Size(), MtimeNs: fi.ModTime().UnixNano(), Mode: uint32(fi.Mode())}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			e.Link, _ = os.Readlink(full)
			e.Sha = "link:" + e.Link
		case fi.Mode().IsRegular():
			if opt.MaxFileBytes > 0 && fi.Size() > opt.MaxFileBytes {
				d.Skipped = append(d.Skipped, rel+" (too large)")
				continue
			}
			old, ok := prev.Files[rel]
			if ok && old.Size == e.Size && old.MtimeNs == e.MtimeNs && old.Sha != "" {
				e.Sha = old.Sha
			} else {
				h, err := hashFile(full)
				if err != nil {
					continue
				}
				e.Sha = h
				if containsAny(full, opt.DenyContent) {
					d.Skipped = append(d.Skipped, rel+" (contains a stored secret value)")
					continue
				}
			}
		default:
			continue
		}
		next.Files[rel] = e
		old, ok := prev.Files[rel]
		if !ok || old.Sha != e.Sha || old.Mode != e.Mode {
			d.Send = append(d.Send, rel)
			d.Bytes += e.Size
		}
	}
	for rel := range prev.Files {
		if _, ok := next.Files[rel]; !ok {
			d.Delete = append(d.Delete, rel)
		}
	}
	sort.Strings(d.Send)
	sort.Strings(d.Delete)
	if isGitRepo(root) {
		if b, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output(); err == nil {
			next.Commit = strings.TrimSpace(string(b))
		}
	}
	return d, nil
}

func containsAny(path string, needles [][]byte) bool {
	if len(needles) == 0 {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, n := range needles {
		if len(n) >= 8 && bytes.Contains(b, n) {
			return true
		}
	}
	return false
}

// WriteTar streams the files in d.Send from root, sanitizing .git/config.
func (d *Delta) WriteTar(w io.Writer, root string) error {
	tw := tar.NewWriter(w)
	for _, rel := range d.Send {
		full := filepath.Join(root, filepath.FromSlash(rel))
		fi, err := os.Lstat(full)
		if err != nil {
			continue
		}
		link := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			link, _ = os.Readlink(full)
		}
		h, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		h.Name, h.Uname, h.Gname, h.Uid, h.Gid = rel, "", "", 0, 0
		var body io.Reader
		if fi.Mode().IsRegular() {
			if rel == ".git/config" {
				raw, err := os.ReadFile(full)
				if err != nil {
					return err
				}
				clean := SanitizeGitConfig(raw)
				h.Size = int64(len(clean))
				body = bytes.NewReader(clean)
			} else {
				f, err := os.Open(full)
				if err != nil {
					return err
				}
				defer f.Close()
				body = io.LimitReader(f, fi.Size())
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if body != nil {
			if _, err := io.Copy(tw, body); err != nil {
				return err
			}
		}
	}
	return tw.Close()
}

var (
	urlCredRE = regexp.MustCompile(`(?i)^(\s*(?:url|pushurl)\s*=\s*[a-z][a-z0-9+.-]*://)[^/@\s]+@`)
	sectionRE = regexp.MustCompile(`^\s*\[\s*([A-Za-z0-9.-]+)`)
)

// SanitizeGitConfig drops credential sections, includes, auth headers, and
// credentials embedded in remote URLs.
func SanitizeGitConfig(in []byte) []byte {
	var out bytes.Buffer
	skip := false
	sc := bufio.NewScanner(bytes.NewReader(in))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := sectionRE.FindStringSubmatch(line); m != nil {
			sec := strings.ToLower(m[1])
			skip = sec == "credential" || sec == "include" || sec == "includeif"
			if skip {
				continue
			}
		}
		if skip {
			continue
		}
		low := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(low, "extraheader") || strings.HasPrefix(low, "helper") || strings.HasPrefix(low, "askpass") ||
			strings.HasPrefix(low, "cookiefile") || strings.HasPrefix(low, "sshcommand") {
			continue
		}
		line = urlCredRE.ReplaceAllString(line, "$1")
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}
