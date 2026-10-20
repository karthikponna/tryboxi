// Package e2e runs real microVM scenarios against a running sandboxd.
// Enable with SANDBOXD_E2E=1 (see `make e2e-darwin`).
package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/karthikponna/tryboxi/pkg/client"
)

func setup(t *testing.T) (*client.Client, context.Context) {
	t.Helper()
	if os.Getenv("SANDBOXD_E2E") == "" {
		t.Skip("set SANDBOXD_E2E=1 with a running sandboxd")
	}
	c, err := client.New(client.Options{Name: "e2e"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	if _, err := c.Health(ctx); err != nil {
		t.Fatalf("daemon not reachable: %v", err)
	}
	return c, ctx
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@x", "GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("..", "fixtures", "node-app")
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(src, e.Name()))
		_ = os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

func exec1(t *testing.T, c *client.Client, ctx context.Context, id, cmd string) client.Execution {
	t.Helper()
	e, err := c.Exec(ctx, id, client.ExecRequest{Cmd: cmd, TimeoutS: 120})
	if err != nil {
		t.Fatalf("exec %q: %v", cmd, err)
	}
	return e
}

func TestCreateExecDestroy(t *testing.T) {
	c, ctx := setup(t)
	dir := repo(t)
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=1"), 0o600)
	sb, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Workspace: &client.WorkspaceSpec{HostPath: dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.DestroySandbox(context.Background(), sb.ID)
	t.Logf("created %s from %s in %dms", sb.ID, sb.BootedFrom, sb.BootMs)

	e := exec1(t, c, ctx, sb.ID, "npm test")
	if e.ExitCode == nil || *e.ExitCode != 0 {
		t.Fatalf("npm test failed: %+v", e)
	}
	e = exec1(t, c, ctx, sb.ID, "echo out; echo err >&2; exit 3")
	if *e.ExitCode != 3 || e.Status != "failed" || strings.TrimSpace(e.Stdout) != "out" || strings.TrimSpace(e.Stderr) != "err" {
		t.Fatalf("exit/streams wrong: %+v", e)
	}
	e = exec1(t, c, ctx, sb.ID, "test -e .env && echo LEAKED || echo absent")
	if strings.TrimSpace(e.Stdout) != "absent" {
		t.Fatal(".env was synced into the VM")
	}
	e, _ = c.Exec(ctx, sb.ID, client.ExecRequest{Cmd: "sleep 30", TimeoutS: 2})
	if !e.TimedOut || *e.ExitCode != 124 {
		t.Fatalf("timeout not enforced: %+v", e)
	}
	// Host edit is synced before the next exec.
	_ = os.WriteFile(filepath.Join(dir, "host.txt"), []byte("from host"), 0o644)
	e = exec1(t, c, ctx, sb.ID, "cat host.txt")
	if strings.TrimSpace(e.Stdout) != "from host" {
		t.Fatalf("delta sync failed: %+v", e)
	}
	// Deleting inside the VM never deletes on the host.
	exec1(t, c, ctx, sb.ID, "rm -f host.txt sum.js")
	if _, err := os.Stat(filepath.Join(dir, "sum.js")); err != nil {
		t.Fatal("deletion in VM reached the host")
	}
	// Large output is truncated head+tail and greppable.
	e = exec1(t, c, ctx, sb.ID, "seq 1 100000")
	if !e.StdoutTruncated || !strings.Contains(e.Stdout, "100000") {
		t.Fatalf("truncation wrong")
	}
	g, err := c.Grep(ctx, e.ID, "^77777$", "stdout")
	if err != nil || len(g.Matches) != 1 {
		t.Fatalf("grep: %v %+v", err, g)
	}
}

func TestDiffApplyUndo(t *testing.T) {
	c, ctx := setup(t)
	dir := repo(t)
	sb, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Workspace: &client.WorkspaceSpec{HostPath: dir, Mode: "sandbox"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.DestroySandbox(context.Background(), sb.ID)
	exec1(t, c, ctx, sb.ID, "sed -i 's/a + b/b + a/' sum.js && echo new > added.txt")
	d, err := c.Diff(ctx, sb.ID)
	if err != nil || len(d.Changes) != 2 {
		t.Fatalf("diff: %v %+v", err, d)
	}
	r, err := c.Apply(ctx, sb.ID, client.ApplyRequest{ArtifactID: d.ArtifactID})
	if err != nil || r.Applied != 2 || r.BackupArtifactID == "" {
		t.Fatalf("apply: %v %+v", err, r)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "sum.js"))
	if !strings.Contains(string(b), "b + a") {
		t.Fatal("apply did not write sum.js")
	}
	if _, err := c.Undo(ctx, r.BackupArtifactID); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "sum.js"))
	if strings.Contains(string(b), "b + a") {
		t.Fatal("undo did not restore sum.js")
	}
	if _, err := os.Stat(filepath.Join(dir, "added.txt")); !os.IsNotExist(err) {
		t.Fatal("undo did not remove added.txt")
	}
}

func TestSnapshotForkRestorePause(t *testing.T) {
	c, ctx := setup(t)
	sb, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.DestroySandbox(context.Background(), sb.ID)
	exec1(t, c, ctx, sb.ID, "echo A > /tmp/state")
	if _, err := c.Checkpoint(ctx, sb.ID, "A"); err != nil {
		t.Fatal(err)
	}
	exec1(t, c, ctx, sb.ID, "echo B > /tmp/state")
	r, err := c.Restore(ctx, sb.ID, "latest")
	if err != nil {
		t.Fatal(err)
	}
	defer c.DestroySandbox(context.Background(), r.ID)
	if e := exec1(t, c, ctx, r.ID, "cat /tmp/state"); strings.TrimSpace(e.Stdout) != "A" {
		t.Fatalf("restore got %q", e.Stdout)
	}
	f, err := c.Fork(ctx, sb.ID, 2)
	if err != nil || len(f.Sandboxes) == 0 {
		t.Fatalf("fork: %v %+v", err, f)
	}
	seen := map[string]bool{}
	for _, s := range f.Sandboxes {
		defer c.DestroySandbox(context.Background(), s.ID)
		e := exec1(t, c, ctx, s.ID, "cat /tmp/state; cat /proc/sys/kernel/random/uuid")
		lines := strings.Fields(e.Stdout)
		if lines[0] != "B" || seen[lines[1]] {
			t.Fatalf("fork state/rng wrong: %q", e.Stdout)
		}
		seen[lines[1]] = true
	}
	if _, err := c.Pause(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	if e := exec1(t, c, ctx, sb.ID, "cat /tmp/state"); strings.TrimSpace(e.Stdout) != "B" {
		t.Fatalf("resume lost state: %q", e.Stdout)
	}
}

func TestNetworkPolicy(t *testing.T) {
	c, ctx := setup(t)
	sb, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.DestroySandbox(context.Background(), sb.ID)
	e := exec1(t, c, ctx, sb.ID, "curl -sS -m 15 -o /dev/null -w '%{http_code}' https://registry.npmjs.org/")
	if strings.TrimSpace(e.Stdout) != "200" {
		t.Fatalf("allowlisted host failed: %+v", e)
	}
	e = exec1(t, c, ctx, sb.ID, "curl -sS -m 5 https://example.com >/dev/null && echo reached || echo blocked")
	if !strings.Contains(e.Stdout, "blocked") {
		t.Fatalf("non-allowlisted host reachable: %+v", e)
	}
	e = exec1(t, c, ctx, sb.ID, "curl -sS -m 5 http://169.254.169.254/ >/dev/null && echo reached || echo blocked")
	if !strings.Contains(e.Stdout, "blocked") {
		t.Fatal("metadata address reachable")
	}
	none, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Network: &client.NetworkSpec{Mode: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.DestroySandbox(context.Background(), none.ID)
	e = exec1(t, c, ctx, none.ID, "curl -sS -m 5 https://registry.npmjs.org/ >/dev/null && echo reached || echo blocked")
	if !strings.Contains(e.Stdout, "blocked") {
		t.Fatal("network mode none still has egress")
	}
}
