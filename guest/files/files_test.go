package files

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func mkTar(t *testing.T, entries map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	return &buf
}

func TestExtractAndDelete(t *testing.T) {
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Extract(mkTar(t, map[string]string{"a/b.txt": "hello", "c.txt": "c"}), dest, []string{"old.txt", "../escape"}, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 2 || res.Deleted != 1 {
		t.Fatalf("res=%+v", res)
	}
	b, _ := os.ReadFile(filepath.Join(dest, "a", "b.txt"))
	if string(b) != "hello" {
		t.Fatalf("got %q", b)
	}
	if _, err := os.Stat(filepath.Join(dest, "old.txt")); !os.IsNotExist(err) {
		t.Fatal("old.txt not deleted")
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	dest := t.TempDir()
	if _, err := Extract(mkTar(t, map[string]string{"../evil.txt": "x"}), dest, nil, -1, -1); err == nil {
		t.Fatal("expected traversal error")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dest, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(mkTar(t, map[string]string{"link/evil.txt": "x"}), dest, nil, -1, -1); err == nil {
		t.Fatal("expected symlink parent error")
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("wrote through symlink")
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "d", "node_modules"), 0o755)
	os.WriteFile(filepath.Join(src, "d", "x.go"), []byte("package x"), 0o644)
	os.WriteFile(filepath.Join(src, "d", "node_modules", "big"), []byte("zzz"), 0o644)
	var buf bytes.Buffer
	if err := Archive(&buf, src, []string{"node_modules"}, 0); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, err := Extract(&buf, dest, nil, -1, -1); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "d", "x.go")); string(b) != "package x" {
		t.Fatalf("got %q", b)
	}
	if _, err := os.Stat(filepath.Join(dest, "d", "node_modules")); !os.IsNotExist(err) {
		t.Fatal("excluded dir was archived")
	}
}
