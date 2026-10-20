// Command sandbox-mcp is a thin MCP (stdio) adapter over the sandboxd HTTP
// API. Every tool maps to one or two API calls; there is no business logic.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/karthikponna/tryboxi/internal/version"
	"github.com/karthikponna/tryboxi/pkg/client"
)

type adapter struct {
	cl         *client.Client
	sessionKey string
	template   string

	mu        sync.Mutex
	sandboxID string
	workspace string
}

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("sandbox-mcp: ")
	name := "sandbox-mcp"
	if c := os.Getenv("SANDBOX_CLIENT"); c != "" {
		name += "/" + c
	}
	cl, err := client.New(client.Options{Name: name})
	if err != nil {
		log.Fatal(err)
	}
	a := &adapter{cl: cl, template: envOr("SANDBOX_TEMPLATE", "auto")}
	a.sessionKey = os.Getenv("SANDBOX_SESSION_KEY")
	if a.sessionKey == "" {
		h := sha256.Sum256([]byte(fmt.Sprintf("%d|%d|%s", os.Getppid(), os.Getpid(), time.Now().Format(time.RFC3339Nano))))
		a.sessionKey = "mcp-" + hex.EncodeToString(h[:8])
	}

	s := mcp.NewServer(&mcp.Implementation{Name: "sandboxd", Version: version.Version}, &mcp.ServerOptions{
		Instructions: "Disposable Linux microVM that mirrors this repo at /workspace. Use sandbox_exec for tests, builds, installs, servers, and untrusted code. Host files only change through sandbox_apply / sandbox_pull (backed up, undoable).",
	})
	a.register(s)
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func ptr[T any](v T) *T { return &v }

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// infraErr reports a sandbox/infrastructure problem (not a command failure).
func infraErr(err error) *mcp.CallToolResult {
	msg := err.Error()
	var ae *client.Error
	if errors.As(err, &ae) && ae.Code == "capacity_exhausted" {
		retry := ae.RetryAfterS
		if retry == 0 {
			retry = 20
		}
		msg = fmt.Sprintf("All sandboxes are busy (%s). Retry in %ds.", ae.Message, retry)
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "sandbox error: " + msg}}}
}

// resolveWorkspace picks the repo to mirror: MCP roots, then
// SANDBOX_WORKSPACE, then the working directory.
func (a *adapter) resolveWorkspace(ctx context.Context, ss *mcp.ServerSession) string {
	a.mu.Lock()
	ws := a.workspace
	a.mu.Unlock()
	if ws != "" {
		return ws
	}
	var cands []string
	if ss != nil {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if roots, err := ss.ListRoots(rctx, nil); err == nil {
			for _, r := range roots.Roots {
				if u, err := url.Parse(r.URI); err == nil && u.Scheme == "file" {
					cands = append(cands, u.Path)
				}
			}
		}
		cancel()
	}
	if v := os.Getenv("SANDBOX_WORKSPACE"); v != "" && !strings.Contains(v, "${") {
		cands = append(cands, v)
	}
	if wd, err := os.Getwd(); err == nil {
		cands = append(cands, wd)
	}
	home, _ := os.UserHomeDir()
	for _, c := range cands {
		abs, err := filepath.Abs(c)
		if err != nil || abs == "/" || abs == home {
			continue
		}
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			ws = abs
			break
		}
	}
	a.mu.Lock()
	a.workspace = ws
	a.mu.Unlock()
	return ws
}

// sandbox returns the session sandbox, creating it on first use (or after
// it was lost/destroyed). explicit overrides the session sandbox.
func (a *adapter) sandbox(ctx context.Context, ss *mcp.ServerSession, explicit string) (string, string, error) {
	if explicit != "" {
		return explicit, "", nil
	}
	a.mu.Lock()
	id := a.sandboxID
	a.mu.Unlock()
	if id != "" {
		sb, err := a.cl.GetSandbox(ctx, id)
		if err == nil && sb.State != "destroyed" && sb.State != "lost" && sb.State != "failed" {
			return id, "", nil
		}
	}
	req := client.CreateSandboxRequest{Template: a.template, SessionKey: a.sessionKey, WaitForCapacityS: 60,
		Metadata: map[string]string{"created_by": "sandbox-mcp", "client": os.Getenv("SANDBOX_CLIENT")}}
	if ws := a.resolveWorkspace(ctx, ss); ws != "" {
		req.Workspace = &client.WorkspaceSpec{HostPath: ws, Mode: "host"}
	}
	sb, err := a.cl.CreateSandbox(ctx, req)
	if err != nil {
		return "", "", err
	}
	a.mu.Lock()
	a.sandboxID = sb.ID
	a.mu.Unlock()
	note := ""
	if !sb.Reused {
		note = fmt.Sprintf("(started sandbox %s in %dms from %s", sb.ID, sb.BootMs, sb.BootedFrom)
		if sb.HostPath != "" {
			note += "; /workspace mirrors " + sb.HostPath
		}
		note += ")"
		if sb.Notice != "" {
			note += "\n(note: " + sb.Notice + ")"
		}
	}
	if id != "" && id != sb.ID {
		note = fmt.Sprintf("(previous sandbox %s was gone; started a fresh one: %s)", id, sb.ID)
	}
	return sb.ID, note, nil
}

func fmtExec(e client.Execution, note string) string {
	var b strings.Builder
	if note != "" {
		b.WriteString(note + "\n")
	}
	switch {
	case e.Status == "running":
		fmt.Fprintf(&b, "status: running in background (exec_id %s). Check later with sandbox_exec `cat`/`curl`, or sandbox_status.\n", e.ID)
		return b.String()
	case e.ExitCode == nil:
		fmt.Fprintf(&b, "exit_code: none (%s: %s)\n", e.Status, e.Reason)
	default:
		word := "ok"
		if *e.ExitCode != 0 {
			word = "failed"
		}
		if e.TimedOut {
			word = "timed out"
		}
		if e.OOMKilled {
			word = "killed: out of memory"
		}
		fmt.Fprintf(&b, "exit_code: %d (%s in %.1fs)\n", *e.ExitCode, word, float64(e.DurationMs)/1000)
	}
	if e.Stdout != "" {
		b.WriteString("--- stdout ---\n" + strings.TrimRight(e.Stdout, "\n") + "\n")
	}
	if e.Stderr != "" {
		b.WriteString("--- stderr ---\n" + strings.TrimRight(e.Stderr, "\n") + "\n")
	}
	if e.Stdout == "" && e.Stderr == "" {
		b.WriteString("(no output)\n")
	}
	if e.StdoutTruncated || e.StderrTruncated {
		fmt.Fprintf(&b, "(output trimmed; full log is exec %s: %d stdout bytes, %d stderr bytes)\n", e.ID, e.StdoutBytes, e.StderrBytes)
	}
	if len(e.ChangedFiles) > 0 {
		shown := e.ChangedFiles
		if len(shown) > 30 {
			shown = shown[:30]
		}
		fmt.Fprintf(&b, "changed_files (in sandbox only; sandbox_pull copies them to the host): %s", strings.Join(shown, ", "))
		if len(e.ChangedFiles) > 30 {
			fmt.Fprintf(&b, " ... and %d more", len(e.ChangedFiles)-30)
		}
		b.WriteString("\n")
	}
	return b.String()
}

type execIn struct {
	Command    string            `json:"command" jsonschema:"Shell command, run with bash -lc in /workspace as user agent (sudo available)"`
	Cwd        string            `json:"cwd,omitempty" jsonschema:"Working directory inside the sandbox (default /workspace)"`
	TimeoutS   int               `json:"timeout_s,omitempty" jsonschema:"Timeout in seconds (default 600)"`
	Background bool              `json:"background,omitempty" jsonschema:"Run without waiting (servers, watchers)"`
	Env        map[string]string `json:"env,omitempty" jsonschema:"Extra environment variables"`
	SandboxID  string            `json:"sandbox_id,omitempty" jsonschema:"Target a specific sandbox (e.g. a fork); default is this session's sandbox"`
}

type pathIn struct {
	Path      string `json:"path" jsonschema:"File path inside the sandbox (absolute, or relative to /workspace)"`
	MaxBytes  int    `json:"max_bytes,omitempty" jsonschema:"Maximum bytes to return (default 200000)"`
	SandboxID string `json:"sandbox_id,omitempty"`
}

type writeIn struct {
	Path      string `json:"path" jsonschema:"File path inside the sandbox (absolute, or relative to /workspace)"`
	Content   string `json:"content" jsonschema:"File contents"`
	SandboxID string `json:"sandbox_id,omitempty"`
}

type idIn struct {
	SandboxID string `json:"sandbox_id,omitempty" jsonschema:"Default is this session's sandbox"`
}

type pathsIn struct {
	Paths     []string `json:"paths,omitempty" jsonschema:"Repo-relative paths (default: all changes / files changed by the last command)"`
	Force     bool     `json:"force,omitempty" jsonschema:"pull only: overwrite host files changed since the last sync"`
	SandboxID string   `json:"sandbox_id,omitempty"`
}

type snapIn struct {
	Action     string `json:"action" jsonschema:"checkpoint | restore | fork | list"`
	Label      string `json:"label,omitempty" jsonschema:"checkpoint label"`
	SnapshotID string `json:"snapshot_id,omitempty" jsonschema:"restore: snapshot to restore (default latest checkpoint)"`
	Count      int    `json:"count,omitempty" jsonschema:"fork: number of clones (1-8)"`
	SandboxID  string `json:"sandbox_id,omitempty"`
}

type portIn struct {
	GuestPort int    `json:"guest_port" jsonschema:"TCP port a server listens on inside the sandbox"`
	SandboxID string `json:"sandbox_id,omitempty"`
}

func (a *adapter) register(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "sandbox_exec", Description: "Run a shell command in the disposable Linux microVM (repo mirrored at /workspace, synced from the host before every run). Use for tests, builds, installs, servers, and untrusted code. Returns exit code, trimmed stdout/stderr, and files the command changed.",
		Annotations: &mcp.ToolAnnotations{Title: "Run in sandbox", DestructiveHint: ptr(false), OpenWorldHint: ptr(true)}},
		func(ctx context.Context, req *mcp.CallToolRequest, in execIn) (*mcp.CallToolResult, any, error) {
			if strings.TrimSpace(in.Command) == "" {
				return infraErr(errors.New("command is required")), nil, nil
			}
			id, note, err := a.sandbox(ctx, req.Session, in.SandboxID)
			if err != nil {
				return infraErr(err), nil, nil
			}
			e, err := a.cl.Exec(ctx, id, client.ExecRequest{Cmd: in.Command, Cwd: in.Cwd, TimeoutS: in.TimeoutS, Background: in.Background, Env: in.Env})
			if client.IsCode(err, "sandbox_lost") || client.IsCode(err, "not_found") {
				a.mu.Lock()
				a.sandboxID = ""
				a.mu.Unlock()
				if id, note, err = a.sandbox(ctx, req.Session, ""); err == nil {
					e, err = a.cl.Exec(ctx, id, client.ExecRequest{Cmd: in.Command, Cwd: in.Cwd, TimeoutS: in.TimeoutS, Background: in.Background, Env: in.Env})
				}
			}
			if err != nil {
				return infraErr(err), nil, nil
			}
			return text(fmtExec(e, note)), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sandbox_read_file", Description: "Read a file from inside the sandbox (e.g. build output or files created by commands).",
		Annotations: &mcp.ToolAnnotations{Title: "Read sandbox file", ReadOnlyHint: true}},
		func(ctx context.Context, req *mcp.CallToolRequest, in pathIn) (*mcp.CallToolResult, any, error) {
			id, _, err := a.sandbox(ctx, req.Session, in.SandboxID)
			if err != nil {
				return infraErr(err), nil, nil
			}
			b, err := a.cl.ReadFile(ctx, id, in.Path)
			if err != nil {
				return infraErr(err), nil, nil
			}
			max := in.MaxBytes
			if max <= 0 {
				max = 200_000
			}
			out := string(b)
			if len(b) > max {
				out = string(b[:max]) + fmt.Sprintf("\n... (%d more bytes)", len(b)-max)
			}
			return text(strings.ToValidUTF8(out, "\uFFFD")), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sandbox_write_file", Description: "Write a file inside the sandbox only (the host is not touched). For repo files, prefer editing on the host; they sync automatically.",
		Annotations: &mcp.ToolAnnotations{Title: "Write sandbox file", DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}},
		func(ctx context.Context, req *mcp.CallToolRequest, in writeIn) (*mcp.CallToolResult, any, error) {
			id, _, err := a.sandbox(ctx, req.Session, in.SandboxID)
			if err != nil {
				return infraErr(err), nil, nil
			}
			if err := a.cl.WriteFile(ctx, id, in.Path, []byte(in.Content)); err != nil {
				return infraErr(err), nil, nil
			}
			return text(fmt.Sprintf("wrote %d bytes to %s in sandbox %s", len(in.Content), in.Path, id)), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sandbox_diff", Description: "Show changes inside the sandbox's /workspace relative to its git HEAD (what sandbox_apply would bring to the host).",
		Annotations: &mcp.ToolAnnotations{Title: "Sandbox diff", ReadOnlyHint: true}},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
			id, _, err := a.sandbox(ctx, req.Session, in.SandboxID)
			if err != nil {
				return infraErr(err), nil, nil
			}
			d, err := a.cl.Diff(ctx, id)
			if err != nil {
				return infraErr(err), nil, nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "diff artifact %s (base %s), %d change(s):\n", d.ArtifactID, d.Head, len(d.Changes))
			for _, c := range d.Changes {
				fmt.Fprintf(&b, "  %s %s\n", c.Status, c.Path)
			}
			b.WriteString(d.Patch)
			return text(b.String()), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sandbox_apply", Description: "Write the sandbox's workspace changes into the HOST repo with a 3-way merge. Backs up every touched host file first (undo with `sandboxctl undo <backup id>`). Never commits or touches .git. Ask the user before calling.",
		Annotations: &mcp.ToolAnnotations{Title: "Apply sandbox changes to host", DestructiveHint: ptr(true)}},
		func(ctx context.Context, req *mcp.CallToolRequest, in pathsIn) (*mcp.CallToolResult, any, error) {
			id, _, err := a.sandbox(ctx, req.Session, in.SandboxID)
			if err != nil {
				return infraErr(err), nil, nil
			}
			r, err := a.cl.Apply(ctx, id, client.ApplyRequest{Paths: in.Paths})
			if err != nil {
				return infraErr(err), nil, nil
			}
			return text(fmtApply(r)), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sandbox_pull", Description: "Copy files that commands changed inside the sandbox (formatters, lockfiles, codegen) to the HOST repo. Backs up host files first; refuses to overwrite host files edited since the last sync unless force. Ask the user before calling.",
		Annotations: &mcp.ToolAnnotations{Title: "Pull files to host", DestructiveHint: ptr(true)}},
		func(ctx context.Context, req *mcp.CallToolRequest, in pathsIn) (*mcp.CallToolResult, any, error) {
			id, _, err := a.sandbox(ctx, req.Session, in.SandboxID)
			if err != nil {
				return infraErr(err), nil, nil
			}
			r, err := a.cl.Pull(ctx, id, client.PullRequest{Paths: in.Paths, Force: in.Force})
			if err != nil {
				return infraErr(err), nil, nil
			}
			return text(fmtApply(r)), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sandbox_snapshot", Description: "Snapshots of the running sandbox. action=checkpoint saves state; action=restore switches this session to a fresh sandbox from a snapshot (default latest checkpoint); action=fork clones the sandbox N times for parallel attempts (returns ids usable as sandbox_id); action=list shows snapshots.",
		Annotations: &mcp.ToolAnnotations{Title: "Snapshot / restore / fork", DestructiveHint: ptr(false)}},
		func(ctx context.Context, req *mcp.CallToolRequest, in snapIn) (*mcp.CallToolResult, any, error) {
			id, _, err := a.sandbox(ctx, req.Session, in.SandboxID)
			if err != nil {
				return infraErr(err), nil, nil
			}
			switch in.Action {
			case "checkpoint", "":
				s, err := a.cl.Checkpoint(ctx, id, in.Label)
				if err != nil {
					return infraErr(err), nil, nil
				}
				return text(fmt.Sprintf("checkpoint %s saved for sandbox %s. Restore with action=restore.", s.ID, id)), nil, nil
			case "restore":
				sb, err := a.cl.Restore(ctx, id, in.SnapshotID)
				if err != nil {
					return infraErr(err), nil, nil
				}
				if in.SandboxID == "" {
					a.mu.Lock()
					a.sandboxID = sb.ID
					a.mu.Unlock()
					_ = a.cl.DestroySandbox(ctx, id)
				}
				return text(fmt.Sprintf("restored into new sandbox %s (session now uses it; workspace mode %s: host files are not auto-synced, use sandbox_apply to bring changes back).", sb.ID, sb.WorkspaceMode)), nil, nil
			case "fork":
				n := in.Count
				if n <= 0 {
					n = 2
				}
				r, err := a.cl.Fork(ctx, id, n)
				if err != nil {
					return infraErr(err), nil, nil
				}
				var ids []string
				for _, s := range r.Sandboxes {
					ids = append(ids, s.ID)
				}
				msg := fmt.Sprintf("forked %s into %d sandboxes: %s. Pass sandbox_id to target one; each can be applied independently.", id, len(ids), strings.Join(ids, ", "))
				if len(r.Failed) > 0 {
					msg += fmt.Sprintf("\n%d clone(s) could not start: %s", len(r.Failed), strings.Join(r.Failed, "; "))
				}
				return text(msg), nil, nil
			case "list":
				list, err := a.cl.ListSnapshots(ctx, id)
				if err != nil {
					return infraErr(err), nil, nil
				}
				var b strings.Builder
				for _, s := range list {
					fmt.Fprintf(&b, "%s %s %s %s\n", s.ID, s.Kind, time.UnixMilli(s.CreatedAt).Format(time.Kitchen), s.Label)
				}
				if b.Len() == 0 {
					b.WriteString("no snapshots")
				}
				return text(b.String()), nil, nil
			}
			return infraErr(fmt.Errorf("unknown action %q", in.Action)), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sandbox_port", Description: "Expose a TCP port from a server running in the sandbox on the host's 127.0.0.1 (returns the URL).",
		Annotations: &mcp.ToolAnnotations{Title: "Expose port", DestructiveHint: ptr(false)}},
		func(ctx context.Context, req *mcp.CallToolRequest, in portIn) (*mcp.CallToolResult, any, error) {
			id, _, err := a.sandbox(ctx, req.Session, in.SandboxID)
			if err != nil {
				return infraErr(err), nil, nil
			}
			p, err := a.cl.ForwardPort(ctx, id, in.GuestPort)
			if err != nil {
				return infraErr(err), nil, nil
			}
			return text(fmt.Sprintf("sandbox port %d is reachable at %s", in.GuestPort, p.URL)), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sandbox_status", Description: "Show this session's sandbox (state, size, workspace, recent commands) and host capacity.",
		Annotations: &mcp.ToolAnnotations{Title: "Sandbox status", ReadOnlyHint: true}},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
			var b strings.Builder
			id := in.SandboxID
			if id == "" {
				a.mu.Lock()
				id = a.sandboxID
				a.mu.Unlock()
			}
			if id == "" {
				b.WriteString("no sandbox yet for this session (one starts on the first sandbox_exec)\n")
			} else if sb, err := a.cl.GetSandbox(ctx, id); err == nil {
				fmt.Fprintf(&b, "sandbox %s: %s/%s, %d vCPU, %d MB, network %s, workspace %s %s\n", sb.ID, sb.State, sb.Health, sb.VCPU, sb.MemMB, sb.NetworkMode, sb.WorkspaceMode, sb.HostPath)
				for gp, hp := range sb.Ports {
					fmt.Fprintf(&b, "  port %s -> http://127.0.0.1:%d\n", gp, hp)
				}
				if execs, err := a.cl.ListExecutions(ctx, id); err == nil {
					for i, e := range execs {
						if i >= 8 {
							break
						}
						code := "-"
						if e.ExitCode != nil {
							code = fmt.Sprint(*e.ExitCode)
						}
						fmt.Fprintf(&b, "  %s %-9s exit=%s %s\n", e.ID, e.Status, code, truncate(e.Command, 80))
					}
				}
			} else {
				fmt.Fprintf(&b, "sandbox %s: %v\n", id, err)
			}
			if c, err := a.cl.Capacity(ctx); err == nil {
				fmt.Fprintf(&b, "host: %d/%d sandboxes, %d MB memory budget free, queue %d\n", c.Sandboxes, c.MaxSandbox, c.MemFreeMB, c.QueueLen)
			}
			return text(b.String()), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sandbox_destroy", Description: "Destroy the sandbox (frees memory). The next sandbox_exec starts a fresh one.",
		Annotations: &mcp.ToolAnnotations{Title: "Destroy sandbox", DestructiveHint: ptr(true), IdempotentHint: true}},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
			id := in.SandboxID
			if id == "" {
				a.mu.Lock()
				id = a.sandboxID
				a.sandboxID = ""
				a.mu.Unlock()
			}
			if id == "" {
				return text("no sandbox to destroy"), nil, nil
			}
			if err := a.cl.DestroySandbox(ctx, id); err != nil {
				return infraErr(err), nil, nil
			}
			return text("destroyed " + id), nil, nil
		})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func fmtApply(r client.ApplyResult) string {
	var b strings.Builder
	for _, f := range r.Files {
		fmt.Fprintf(&b, "%-9s %s", f.Action, f.Path)
		if f.Reason != "" {
			b.WriteString("  (" + f.Reason + ")")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "applied %d, conflicts %d, skipped %d", r.Applied, r.Conflicts, r.Skipped)
	if r.BackupArtifactID != "" {
		fmt.Fprintf(&b, "\nbackup: %s (undo: sandboxctl undo %s)", r.BackupArtifactID, r.BackupArtifactID)
	}
	return b.String()
}
