import type { Line } from "@/components/ui/command-line";

export type Snippet = {
  value: string;
  label: string;
  caption: string;
  footer: string;
  copy: string;
  copyLabel: string;
} & ({ kind: "shell"; lines: Line[] } | { kind: "code"; code: string });

const PY = `import os
import httpx

SOCK = os.path.expanduser("~/Library/Application Support/sandboxd/sandboxd.sock")
api = httpx.Client(
    transport=httpx.HTTPTransport(uds=SOCK),
    base_url="http://localhost",
    timeout=900,
)

sbx = api.post("/v1/sandboxes", json={
    "workspace": {"host_path": os.getcwd()},
    "template": "auto",
}).json()

run = api.post(f"/v1/sandboxes/{sbx['id']}/exec", json={"cmd": "npm test"}).json()
print(run["exit_code"], run["stdout"])

api.delete(f"/v1/sandboxes/{sbx['id']}")`;

const GO = `package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/karthikponna/tryboxi/pkg/client"
)

func main() {
	ctx := context.Background()
	c, err := client.New(client.Options{Name: "my-harness"})
	if err != nil {
		log.Fatal(err)
	}
	repo, _ := os.Getwd()
	sb, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{
		Template:  "auto",
		Workspace: &client.WorkspaceSpec{HostPath: repo},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer c.DestroySandbox(ctx, sb.ID)

	e, err := c.Exec(ctx, sb.ID, client.ExecRequest{Cmd: "npm test"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(*e.ExitCode, e.Stdout)
}`;

const CURL = `S="$HOME/Library/Application Support/sandboxd/sandboxd.sock"

curl -s --unix-socket "$S" -X POST http://localhost/v1/sandboxes \\
  -d '{"workspace":{"host_path":"'$PWD'"},"template":"auto"}'

curl -s --unix-socket "$S" -X POST http://localhost/v1/sandboxes/$ID/exec \\
  -d '{"cmd":"npm test"}'

curl -s --unix-socket "$S" -X DELETE http://localhost/v1/sandboxes/$ID`;

const CURSOR = "sandboxctl mcp install cursor --project . --auto-pull";
const CLAUDE = "sandboxctl mcp install claude --project .";
const CODEX = "sandboxctl mcp install codex --project .";

export const SNIPPETS: Snippet[] = [
  {
    value: "cursor",
    label: "Cursor",
    caption: "cursor · mcp",
    kind: "shell",
    lines: [
      { kind: "cmd", text: CURSOR },
      { kind: "out", text: "wrote ~/code/app/.cursor/mcp.json" },
      { kind: "out", text: "wrote ~/code/app/.cursor/rules/sandbox.mdc" },
      { kind: "out", text: "Next: open Cursor Settings -> MCP and enable the 'sandbox' server" },
    ],
    footer: "sandboxctl events",
    copy: CURSOR,
    copyLabel: "Cursor install command",
  },
  {
    value: "claude",
    label: "Claude Code",
    caption: "claude code · mcp",
    kind: "shell",
    lines: [
      { kind: "cmd", text: CLAUDE },
      { kind: "out", text: "wrote ~/code/app/.claude/skills/sandbox/SKILL.md" },
      { kind: "out", text: "then runs: claude mcp add --scope project sandbox -- sandbox-mcp" },
    ],
    footer: "sandboxctl events",
    copy: CLAUDE,
    copyLabel: "Claude Code install command",
  },
  {
    value: "codex",
    label: "Codex",
    caption: "codex · mcp",
    kind: "shell",
    lines: [
      { kind: "cmd", text: CODEX },
      { kind: "out", text: "updated ~/.codex/config.toml" },
      { kind: "out", text: "wrote ~/code/app/AGENTS.md" },
    ],
    footer: "sandboxctl events",
    copy: CODEX,
    copyLabel: "Codex install command",
  },
  {
    value: "python",
    label: "Python",
    caption: "harness.py · httpx over the unix socket",
    kind: "code",
    code: PY,
    footer: "python3 harness.py",
    copy: PY,
    copyLabel: "Python example",
  },
  {
    value: "go",
    label: "Go",
    caption: "main.go · pkg/client",
    kind: "code",
    code: GO,
    footer: "go run .",
    copy: GO,
    copyLabel: "Go example",
  },
  {
    value: "curl",
    label: "curl",
    caption: "curl · HTTP on the unix socket",
    kind: "code",
    code: CURL,
    footer: 'curl -N --unix-socket "$S" http://localhost/v1/events',
    copy: CURL,
    copyLabel: "curl example",
  },
];
