import type { Line } from "@/components/ui/command-line";
import { site } from "@/site.config";

export type Snippet = {
  value: string;
  label: string;
  copy: string;
  copyLabel: string;
  // "agent" tabs install MCP into a harness; "api" tabs call sandboxd directly
  group: "agent" | "api";
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

const { cursor: CURSOR, claude: CLAUDE, codex: CODEX } = site.install.mcp;
const P = site.install.project;

export const SNIPPETS: Snippet[] = [
  {
    value: "cursor",
    group: "agent",
    label: "Cursor",
    kind: "shell",
    lines: [
      { kind: "cmd", text: CURSOR },
      { kind: "out", text: `wrote ${P}/.cursor/mcp.json` },
      { kind: "out", text: `wrote ${P}/.cursor/rules/sandbox.mdc` },
      { kind: "out", text: "Next: open Cursor Settings -> MCP and enable the 'sandbox' server" },
    ],
    copy: CURSOR,
    copyLabel: "Cursor install command",
  },
  {
    value: "claude",
    group: "agent",
    label: "Claude Code",
    kind: "shell",
    lines: [
      { kind: "cmd", text: CLAUDE },
      { kind: "out", text: `wrote ${P}/.claude/skills/sandbox/SKILL.md` },
      { kind: "out", text: "then runs: claude mcp add --scope project sandbox -- sandbox-mcp" },
    ],
    copy: CLAUDE,
    copyLabel: "Claude Code install command",
  },
  {
    value: "codex",
    group: "agent",
    label: "Codex",
    kind: "shell",
    lines: [
      { kind: "cmd", text: CODEX },
      { kind: "out", text: "updated ~/.codex/config.toml" },
      { kind: "out", text: `wrote ${P}/AGENTS.md` },
    ],
    copy: CODEX,
    copyLabel: "Codex install command",
  },
  {
    value: "python",
    group: "api",
    label: "Python",
    kind: "code",
    code: PY,
    copy: PY,
    copyLabel: "Python example",
  },
  {
    value: "go",
    group: "api",
    label: "Go",
    kind: "code",
    code: GO,
    copy: GO,
    copyLabel: "Go example",
  },
  {
    value: "curl",
    group: "api",
    label: "curl",
    kind: "code",
    code: CURL,
    copy: CURL,
    copyLabel: "curl example",
  },
];
