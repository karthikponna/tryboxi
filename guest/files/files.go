// Package files moves file trees in and out of the guest as tar streams.
package files

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Result summarizes an extraction.
type Result struct {
	Written int
	Deleted int
	Bytes   uint64
}

func cleanRel(name string) (string, error) {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	if name == "" || name == "." {
		return "", nil
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("absolute path %q", name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path %q escapes destination", name)
		}
	}
	return path.Clean(name), nil
}

// noSymlinkParents verifies that no existing component between dest and
// target (exclusive) is a symlink, so writes can't be redirected.
func noSymlinkParents(dest, rel string) error {
	cur := dest
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("parent %q is a symlink", cur)
		}
	}
	return nil
}

// Extract removes the deletes list, then unpacks the tar stream r into dest.
// Ownership of created entries is set to uid/gid when uid >= 0.
func Extract(r io.Reader, dest string, deletes []string, uid, gid int) (Result, error) {
	var res Result
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return res, err
	}
	for _, d := range deletes {
		rel, err := cleanRel(d)
		if err != nil || rel == "" {
			continue
		}
		if err := noSymlinkParents(dest, rel); err != nil {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dest, rel)); err == nil {
			res.Deleted++
		}
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return res, nil
		}
		if err != nil {
			return res, err
		}
		rel, err := cleanRel(h.Name)
		if err != nil {
			return res, err
		}
		if rel == "" {
			continue
		}
		if err := noSymlinkParents(dest, rel); err != nil {
			return res, err
		}
		target := filepath.Join(dest, rel)
		if err := mkParents(dest, filepath.Dir(rel), uid, gid); err != nil {
			return res, err
		}
		mode := fs.FileMode(h.Mode).Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			if fi, err := os.Lstat(target); err == nil && !fi.IsDir() {
				_ = os.Remove(target)
			}
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return res, err
			}
			_ = os.Chmod(target, mode|0o700)
		case tar.TypeReg:
			if fi, err := os.Lstat(target); err == nil && (fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0) {
				if err := os.RemoveAll(target); err != nil {
					return res, err
				}
			}
			tmp := target + ".sbx-tmp"
			f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return res, err
			}
			n, err := io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				_ = os.Remove(tmp)
				return res, err
			}
			_ = os.Chmod(tmp, mode)
			if err := os.Rename(tmp, target); err != nil {
				return res, err
			}
			res.Bytes += uint64(n)
			res.Written++
		case tar.TypeSymlink:
			_ = os.RemoveAll(target)
			if err := os.Symlink(h.Linkname, target); err != nil {
				return res, err
			}
			res.Written++
		default:
			continue
		}
		if uid >= 0 {
			_ = os.Lchown(target, uid, gid)
		}
		if !h.ModTime.IsZero() && h.Typeflag != tar.TypeSymlink {
			_ = os.Chtimes(target, h.ModTime, h.ModTime)
		}
	}
}

func mkParents(dest, relDir string, uid, gid int) error {
	if relDir == "." || relDir == "" {
		return nil
	}
	cur := dest
	for _, p := range strings.Split(filepath.ToSlash(relDir), "/") {
		cur = filepath.Join(cur, p)
		if fi, err := os.Lstat(cur); err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("%q exists and is not a directory", cur)
			}
			continue
		}
		if err := os.Mkdir(cur, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if uid >= 0 {
			_ = os.Lchown(cur, uid, gid)
		}
	}
	return nil
}

// ErrTooLarge is returned when an archive would exceed maxBytes.
var ErrTooLarge = errors.New("files: archive exceeds size limit")

// Archive writes p (a file or directory) to w as a tar stream. Entry names
// are relative to p's parent for files and to p itself for directories.
func Archive(w io.Writer, p string, exclude []string, maxBytes uint64) error {
	tw := tar.NewWriter(w)
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	var total uint64
	add := func(full, name string, fi fs.FileInfo) error {
		link := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			link, _ = os.Readlink(full)
		}
		h, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		h.Name = name
		h.Uname, h.Gname = "", ""
		if fi.IsDir() {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			total += uint64(fi.Size())
			if maxBytes > 0 && total > maxBytes {
				return ErrTooLarge
			}
			f, err := os.Open(full)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			return err
		}
		return nil
	}
	if !fi.IsDir() {
		if err := add(p, filepath.Base(p), fi); err != nil {
			return err
		}
		return tw.Close()
	}
	err = filepath.WalkDir(p, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(p, full)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		for _, pat := range exclude {
			if ok, _ := path.Match(pat, rel); ok {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if ok, _ := path.Match(pat, path.Base(rel)); ok {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return add(full, rel, info)
	})
	if err != nil {
		return err
	}
	return tw.Close()
}
