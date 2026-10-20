package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	repo := t.TempDir()
	run(t, repo, "git", "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "a.txt"), "line1\nline2\nline3\n")
	write(t, filepath.Join(repo, "src", "main.go"), "package main\n")
	write(t, filepath.Join(repo, ".gitignore"), "ignored.log\n")
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-q", "-m", "init")
	return repo
}

var opts = Options{
	Excludes:       []string{"node_modules"},
	SecretExcludes: []string{".env", ".env.*", "*.pem"},
	MaxFileBytes:   1 << 20,
}

func TestPlanDelta(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "node_modules", "x", "i.js"), "x")
	write(t, filepath.Join(repo, ".env"), "SECRET=1")
	write(t, filepath.Join(repo, "certs", "k.pem"), "key")
	write(t, filepath.Join(repo, "ignored.log"), "noise")
	write(t, filepath.Join(repo, "new.txt"), "untracked")
	ctx := context.Background()
	d, err := Plan(ctx, repo, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	has := func(list []string, p string) bool {
		for _, x := range list {
			if x == p {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"a.txt", "src/main.go", "new.txt", ".gitignore", ".git/HEAD", ".git/config"} {
		if !has(d.Send, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, bad := range []string{".env", "certs/k.pem", "ignored.log", "node_modules/x/i.js"} {
		if has(d.Send, bad) {
			t.Errorf("should not send %s", bad)
		}
	}
	// Second plan with no changes sends nothing.
	d2, _ := Plan(ctx, repo, d.Next, opts)
	if len(d2.Send) != 0 || len(d2.Delete) != 0 {
		t.Fatalf("expected empty delta, got send=%v delete=%v", d2.Send, d2.Delete)
	}
	os.Remove(filepath.Join(repo, "new.txt"))
	write(t, filepath.Join(repo, "a.txt"), "changed\n")
	d3, _ := Plan(ctx, repo, d2.Next, opts)
	if !has(d3.Send, "a.txt") || !has(d3.Delete, "new.txt") {
		t.Fatalf("delta3 send=%v delete=%v", d3.Send, d3.Delete)
	}
	var buf bytes.Buffer
	if err := d3.WriteTar(&buf, repo); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeGitConfig(t *testing.T) {
	in := `[core]
	bare = false
[remote "origin"]
	url = https://user:ghp_secret@github.com/acme/repo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
[credential]
	helper = osxkeychain
[http "https://github.com/"]
	extraheader = AUTHORIZATION: basic abc
[include]
	path = ~/.gitconfig.secrets
`
	out := string(SanitizeGitConfig([]byte(in)))
	for _, bad := range []string{"ghp_secret", "user:", "osxkeychain", "AUTHORIZATION", "gitconfig.secrets", "[credential]"} {
		if strings.Contains(out, bad) {
			t.Fatalf("sanitized config still contains %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "url = https://github.com/acme/repo.git") {
		t.Fatalf("url mangled:\n%s", out)
	}
}

func TestApplyThreeWayAndUndo(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	head := strings.TrimSpace(func() string { b, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output(); return string(b) }())
	// Host edits line1, sandbox edits line3: a clean merge.
	write(t, filepath.Join(repo, "a.txt"), "LINE1\nline2\nline3\n")
	vm := map[string]FileVersion{
		"a.txt":       {Exists: true, Data: []byte("line1\nline2\nLINE3\n"), Mode: 0o644},
		"src/new.go":  {Exists: true, Data: []byte("package main\n// new\n"), Mode: 0o644},
		"../evil.txt": {Exists: true, Data: []byte("x")},
	}
	changes := []Change{{Path: "a.txt", Status: "M"}, {Path: "src/new.go", Status: "U"}, {Path: "src/main.go", Status: "D"}, {Path: "../evil.txt", Status: "U"}, {Path: ".git/config", Status: "M"}}
	paths := []string{"a.txt", "src/new.go", "src/main.go"}
	backup := filepath.Join(t.TempDir(), "backup.tar")
	meta, err := Backup(repo, paths, backup, "test")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Apply(ctx, repo, head, changes, vm)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(repo, "a.txt"))
	if string(got) != "LINE1\nline2\nLINE3\n" {
		t.Fatalf("merge result %q (%+v)", got, res)
	}
	if _, err := os.Stat(filepath.Join(repo, "src", "main.go")); !os.IsNotExist(err) {
		t.Fatal("deleted file still present")
	}
	if res.Skipped != 2 {
		t.Fatalf("expected traversal and .git paths skipped: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("wrote outside repo")
	}
	// Undo restores everything.
	if _, _, err := Undo(repo, backup, meta); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(repo, "a.txt"))
	if string(got) != "LINE1\nline2\nline3\n" {
		t.Fatalf("undo a.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "src", "main.go")); err != nil {
		t.Fatal("undo did not restore deleted file")
	}
	if _, err := os.Stat(filepath.Join(repo, "src", "new.go")); !os.IsNotExist(err) {
		t.Fatal("undo did not remove created file")
	}
}

func TestApplyConflict(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, "a.txt"), "HOST\nline2\nline3\n")
	vm := map[string]FileVersion{"a.txt": {Exists: true, Data: []byte("SANDBOX\nline2\nline3\n"), Mode: 0o644}}
	res, err := Apply(context.Background(), repo, "HEAD", []Change{{Path: "a.txt", Status: "M"}}, vm)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflicts != 1 {
		t.Fatalf("want conflict: %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(repo, "a.txt"))
	if !strings.Contains(string(got), "<<<<<<< host") || !strings.Contains(string(got), ">>>>>>> sandbox") {
		t.Fatalf("missing markers: %s", got)
	}
}

func TestPullRefusesHostChanges(t *testing.T) {
	repo := newRepo(t)
	d, _ := Plan(context.Background(), repo, nil, opts)
	m := d.Next
	write(t, filepath.Join(repo, "a.txt"), "edited on host\n")
	vm := map[string]FileVersion{"a.txt": {Exists: true, Data: []byte("formatted in vm\n"), Mode: 0o644}, "package-lock.json": {Exists: true, Data: []byte("{}"), Mode: 0o644}}
	res, _ := Pull(repo, []string{"a.txt", "package-lock.json"}, vm, m, false)
	if res.Applied != 1 || res.Skipped != 1 {
		t.Fatalf("res=%+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(repo, "a.txt"))
	if string(got) != "edited on host\n" {
		t.Fatal("pull overwrote a host change")
	}
	res, _ = Pull(repo, []string{"a.txt"}, vm, m, true)
	got, _ = os.ReadFile(filepath.Join(repo, "a.txt"))
	if res.Applied != 1 || string(got) != "formatted in vm\n" {
		t.Fatalf("force pull failed: %+v %q", res, got)
	}
}
