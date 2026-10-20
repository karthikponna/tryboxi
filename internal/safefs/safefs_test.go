package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func mkfile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveUnderInside(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a", "b", "c.txt")
	mkfile(t, p)
	if err := RemoveUnder(root, filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected removed, got %v", err)
	}
}

func TestRemoveUnderRefusesOutside(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	victim := filepath.Join(other, "keep.txt")
	mkfile(t, victim)
	for _, p := range []string{
		other,
		victim,
		filepath.Join(root, "..", filepath.Base(other)),
		filepath.Join(root, "x", "..", "..", filepath.Base(other), "keep.txt"),
	} {
		if err := RemoveUnder(root, p); err == nil {
			t.Fatalf("RemoveUnder(%q) should fail", p)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim deleted: %v", err)
	}
}

func TestRemoveUnderRefusesRoot(t *testing.T) {
	root := t.TempDir()
	if err := RemoveUnder(root, root); !errors.Is(err, ErrIsRoot) {
		t.Fatalf("want ErrIsRoot, got %v", err)
	}
	if err := RemoveUnder("/", "/tmp/whatever-not-real"); err == nil {
		t.Fatal("root '/' must be refused")
	}
}

func TestRemoveUnderSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	victim := filepath.Join(other, "keep.txt")
	mkfile(t, victim)
	// A directory symlink inside root that points outside.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	// Deleting through the link must be refused.
	if err := RemoveUnder(root, filepath.Join(link, "keep.txt")); err == nil {
		t.Fatal("expected refusal for path through escaping symlink")
	}
	// Deleting the link itself removes only the link.
	if err := RemoveUnder(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim deleted: %v", err)
	}
}

func TestRemoveUnderMissing(t *testing.T) {
	root := t.TempDir()
	if err := RemoveUnder(root, filepath.Join(root, "nope")); err != nil {
		t.Fatal(err)
	}
}

func TestCleanRel(t *testing.T) {
	good := map[string]string{
		"a.txt":       "a.txt",
		"src/./b.go":  "src/b.go",
		"dir/sub/c":   "dir/sub/c",
		".gitignore":  ".gitignore",
		".github/x.y": ".github/x.y",
	}
	for in, want := range good {
		got, err := CleanRel(in)
		if err != nil || got != want {
			t.Fatalf("CleanRel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", "a/..", ".git", ".git/config", "x\x00y", "."} {
		if _, err := CleanRel(bad); err == nil {
			t.Fatalf("CleanRel(%q) should fail", bad)
		}
	}
}

func TestJoinRepoSymlinkEscape(t *testing.T) {
	repo := t.TempDir()
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(repo, "out")); err != nil {
		t.Fatal(err)
	}
	if _, err := JoinRepo(repo, "out/file.txt"); err == nil {
		t.Fatal("expected refusal for symlink escape")
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, ".git"), filepath.Join(repo, "g")); err != nil {
		t.Fatal(err)
	}
	if _, err := JoinRepo(repo, "g/config"); err == nil {
		t.Fatal("expected refusal for path resolving into .git")
	}
	p, err := JoinRepo(repo, "src/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(repo, "src", "new.go") {
		t.Fatalf("unexpected %q", p)
	}
}

func TestWriteFileUnderReplacesSymlink(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	victim := filepath.Join(other, "keep.txt")
	mkfile(t, victim)
	link := filepath.Join(root, "f.txt")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	// The resolved target is outside root, so the write is refused.
	if err := WriteFileUnder(root, link, []byte("pwned"), 0o644); err == nil {
		t.Fatal("expected refusal")
	}
	b, _ := os.ReadFile(victim)
	if string(b) != "x" {
		t.Fatalf("victim modified: %q", b)
	}
	if err := WriteFileUnder(root, filepath.Join(root, "d", "ok.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileUnder(root, filepath.Join(other, "nope.txt"), []byte("hi"), 0o600); err == nil {
		t.Fatal("expected refusal outside root")
	}
}
