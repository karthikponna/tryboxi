// Package safefs is the only place the daemon deletes files or writes into
// directories it does not fully own. Every path is resolved (including
// symlinks) and must stay inside an explicit root.
package safefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrOutsideRoot = errors.New("safefs: path is outside the allowed root")
	ErrIsRoot      = errors.New("safefs: refusing to operate on the root itself")
	ErrBadRelPath  = errors.New("safefs: invalid relative path")
)

// resolve returns the absolute, symlink-resolved form of p. If p (or a suffix
// of it) does not exist yet, the deepest existing ancestor is resolved and the
// remainder is appended, so callers can check paths they are about to create.
func resolve(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	var rest []string
	cur := abs
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				r = filepath.Join(r, rest[i])
			}
			return r, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// within reports whether child is root or lies beneath it. Both must already
// be resolved.
func within(root, child string) bool {
	if child == root {
		return true
	}
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Contains reports whether path resolves to a location inside root.
func Contains(root, path string) (bool, error) {
	r, err := resolve(root)
	if err != nil {
		return false, err
	}
	p, err := resolve(path)
	if err != nil {
		return false, err
	}
	return within(r, p), nil
}

// Check returns the resolved path if it lies strictly inside root.
func Check(root, path string) (string, error) {
	r, err := resolve(root)
	if err != nil {
		return "", err
	}
	if r == "/" || r == "" {
		return "", fmt.Errorf("%w: root %q is too broad", ErrOutsideRoot, root)
	}
	p, err := resolve(path)
	if err != nil {
		return "", err
	}
	if p == r {
		return "", ErrIsRoot
	}
	if !within(r, p) {
		return "", fmt.Errorf("%w: %s not under %s", ErrOutsideRoot, path, root)
	}
	return p, nil
}

// RemoveUnder deletes path (recursively) only if it resolves to a location
// strictly inside root. A missing path is not an error.
func RemoveUnder(root, path string) error {
	if r, err := resolve(root); err != nil {
		return err
	} else if r == "/" || r == "" {
		return fmt.Errorf("%w: root %q is too broad", ErrOutsideRoot, root)
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		// A symlink is removed as a link, never followed; only its parent
		// directory has to be inside root.
		if _, err := Check(root, filepath.Dir(path)); err != nil && !errors.Is(err, ErrIsRoot) {
			return err
		}
		return os.Remove(path)
	}
	p, err := Check(root, path)
	if err != nil {
		return err
	}
	return os.RemoveAll(p)
}

// CleanRel validates a slash-separated path relative to a repo root. It
// rejects absolute paths, any ".." segment, empty paths, and anything under
// .git/. It returns the cleaned relative path.
func CleanRel(rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", ErrBadRelPath
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: absolute path %q", ErrBadRelPath, rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: %q contains '..'", ErrBadRelPath, rel)
		}
	}
	c := filepath.ToSlash(filepath.Clean(rel))
	if c == "." || c == ".git" || strings.HasPrefix(c, ".git/") {
		return "", fmt.Errorf("%w: %q is not writable", ErrBadRelPath, rel)
	}
	return c, nil
}

// JoinRepo joins a validated relative path to repoRoot and verifies the
// resolved result (following any existing symlinks) is still inside the
// repo and not under .git.
func JoinRepo(repoRoot, rel string) (string, error) {
	c, err := CleanRel(rel)
	if err != nil {
		return "", err
	}
	full := filepath.Join(repoRoot, filepath.FromSlash(c))
	p, err := Check(repoRoot, full)
	if err != nil {
		return "", err
	}
	r, _ := resolve(repoRoot)
	if within(filepath.Join(r, ".git"), p) {
		return "", fmt.Errorf("%w: %q resolves into .git", ErrBadRelPath, rel)
	}
	return full, nil
}

// WriteFileUnder atomically writes data to path, which must be inside root.
// The file is written to a temp file in the same directory and renamed, so a
// symlink at path is replaced rather than followed.
func WriteFileUnder(root, path string, data []byte, perm os.FileMode) error {
	if _, err := Check(root, path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if _, err := Check(root, dir); err != nil && !errors.Is(err, ErrIsRoot) {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".sandboxd-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// CopyFile copies src to dst with the given permissions using
// WriteFileUnder semantics for the destination.
func CopyFile(root, src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	return WriteFileUnder(root, dst, data, perm)
}

// MkdirAllUnder creates dir (and parents) only if it is inside root.
func MkdirAllUnder(root, dir string, perm os.FileMode) error {
	if _, err := Check(root, dir); err != nil && !errors.Is(err, ErrIsRoot) {
		return err
	}
	return os.MkdirAll(dir, perm)
}
