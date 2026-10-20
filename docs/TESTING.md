# Testing sandboxd on your Mac

Everything below runs on an Apple Silicon Mac with macOS 14+ (tested on an
M4, macOS 26.5). VMs run on Apple's Virtualization.framework inside the
`sandboxd` process; no Lima, Docker, or root is needed at runtime.

Paths assume the repo root (`tryboxi/`) as the working directory.

## 1. Build (once)

```bash
# Guest kernel (Kata 6.12, sha256 pinned) and the Ubuntu 24.04 rootfs.
# The rootfs build needs Docker Desktop running; it takes ~3 minutes.
make images

# sandboxd (signed with the virtualization entitlement), sandboxctl,
# sandbox-mcp, and the static Linux guest agent.
make build
```

Artifacts land in `_out/` (git-ignored): `_out/images/vmlinux-arm64`,
`_out/images/base-rootfs-arm64.ext4`, `_out/bin/*`.

## 2. Install and start the daemon

Option A, foreground (best while testing; Ctrl-C pauses sandboxes to disk):

```bash
./_out/bin/sandboxd install --artifacts _out --no-service
./_out/bin/sandboxd serve            # add --debug to see every event
```

Option B, background service that starts at login (LaunchAgent):

```bash
./_out/bin/sandboxd install --artifacts _out
tail -f ~/Library/Application\ Support/sandboxd/sandboxd.log
```

Data lives in `~/Library/Application Support/sandboxd/` (state.db, kernels,
templates, snapshots, sandboxes, execution logs, artifacts). The first start
builds the base golden snapshot (~2 s).

Check it:

```bash
./_out/bin/sandboxd doctor        # host checks: macOS, entitlement, APFS, artifacts, socket
./_out/bin/sandboxctl doctor      # daemon health + capacity
```

Tip: put the binaries on your PATH, e.g.
`ln -s "$PWD/_out/bin/"{sandboxd,sandboxctl,sandbox-mcp} ~/.local/bin/`.

## 3. Five-minute CLI tour

```bash
C=./_out/bin/sandboxctl
REPO=$PWD/test/fixtures/node-app            # or any repo of yours

$C create -w $REPO                          # prints sbx_... (boots in ~0.5 s)
ID=sbx_...                                  # paste the id
$C exec $ID -- npm test                     # host repo is synced before every exec
$C exec $ID -- 'sudo apt-get update -q && sudo apt-get install -y cowsay && /usr/games/cowsay hi'
$C exec $ID -- 'curl -sI https://example.com'      # blocked: not in the allowlist
$C exec $ID -d -- 'python3 -m http.server 8000'    # background server
$C port $ID 8000                                    # -> http://127.0.0.1:NNNNN
$C shell $ID                                        # interactive bash (exit to leave)

# VM -> host, always with a backup:
$C exec $ID -- "sed -i 's/a + b/b + a/' sum.js"
$C diff $ID
$C apply $ID sum.js                         # prints "undo with: sandboxctl undo art_..."
$C undo art_...

# Snapshots
$C snapshot $ID --label clean               # checkpoint
$C fork $ID -n 2                            # two clones for parallel attempts
$C restore $ID                              # new sandbox from the latest checkpoint
$C pause $ID && $C exec $ID -- uptime       # pause to disk, transparent resume

$C ps ; $C top ; $C events                  # observe
$C rm $ID
```

## 4. Use it from Cursor (MCP)

1. Make sure the daemon is running (step 2).
2. In the project you want agents to test (any repo, not necessarily this one):

   ```bash
   /path/to/tryboxi/_out/bin/sandboxctl mcp install cursor --project /path/to/your/repo
   # or for every project: sandboxctl mcp install cursor --global
   ```

   This writes `.cursor/mcp.json` (server `sandbox` -> `sandbox-mcp` with
   `SANDBOX_WORKSPACE=${workspaceFolder}`) and `.cursor/rules/sandbox.mdc`
   (always-applied rule telling the agent when to use the sandbox).
3. In Cursor: Settings -> MCP (Tools & Integrations) -> enable `sandbox`.
   Reload the window if it doesn't appear. You should see 10 tools:
   `sandbox_exec, sandbox_read_file, sandbox_write_file, sandbox_diff,
   sandbox_apply, sandbox_pull, sandbox_snapshot, sandbox_port,
   sandbox_status, sandbox_destroy`.
4. Try prompts in Agent mode:
   - "Run the test suite in the sandbox and fix any failures."
   - "Install the dependencies and start the dev server in the sandbox, then give me the URL."
   - "In the sandbox, run `npm run lint -- --fix` and pull the changed files back."
   - "Checkpoint the sandbox, try upgrading all dependencies, run the tests; if they fail, restore."
5. What to expect: the first call starts a sandbox for this Cursor window
   (usually from the warm pool in tens of ms). If the repo has a lockfile,
   a template with dependencies preinstalled is built in the background and
   used by later sandboxes. `sandbox_apply` / `sandbox_pull` write to your
   repo, so Cursor asks for approval; every write is backed up
   (`sandboxctl artifacts --kind backup`, `sandboxctl undo <id>`).
6. Watch it live in a terminal: `sandboxctl events`.

Claude Code: `sandboxctl mcp install claude --project <repo>` (writes a skill
and runs `claude mcp add`). Codex: `sandboxctl mcp install codex --project <repo>`
(adds `[mcp_servers.sandbox]` to `~/.codex/config.toml` and the rules to `AGENTS.md`).

## 5. Use it from your own harness (HTTP API)

The API is plain HTTP/JSON on the unix socket; the contract is in
`api/openapi.yaml` (also served at `GET /v1/openapi.yaml`).

### curl

```bash
S="$HOME/Library/Application Support/sandboxd/sandboxd.sock"
curl -s --unix-socket "$S" http://localhost/v1/health
curl -s --unix-socket "$S" -X POST http://localhost/v1/sandboxes \
  -d '{"workspace":{"host_path":"'$PWD'/test/fixtures/node-app"},"template":"auto"}'
curl -s --unix-socket "$S" -X POST http://localhost/v1/sandboxes/$ID/exec -d '{"cmd":"npm test"}'
curl -s --unix-socket "$S" -X POST http://localhost/v1/sandboxes/$ID/exec -d '{"cmd":"sleep 3; echo done","wait":false}'
curl -N --unix-socket "$S" http://localhost/v1/executions/$EXEC_ID/stream     # SSE: stdout/stderr/exit
curl -N --unix-socket "$S" http://localhost/v1/events                         # SSE: all daemon events
curl -s --unix-socket "$S" -X DELETE http://localhost/v1/sandboxes/$ID
```

Responses for `exec` include `exit_code`, `status`, head+tail truncated
`stdout`/`stderr`, byte counts, `changed_files`, and `log_urls` for paging
(`/output?offset=`) and searching (`/grep?pattern=`). Retries are safe with an
`Idempotency-Key` header. Capacity errors are `503 capacity_exhausted` with
`Retry-After`.

### Python (stdlib only)

```bash
python3 examples/python-harness/harness.py test/fixtures/node-app
```

### Go SDK

```bash
go run ./examples/go-harness -repo test/fixtures/node-app
```

`pkg/client` covers every endpoint (create, exec, stream, files, diff, apply,
pull, undo, checkpoint, fork, restore, ports, recover, events, gc).

### Over TCP (containers, other users, other languages)

```bash
# in ~/Library/Application Support/sandboxd/config.yaml
tcp_listen: 127.0.0.1:7070
# restart the daemon, then:
TOKEN=$(./_out/bin/sandboxd token)
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:7070/v1/health
SANDBOXD_URL=http://127.0.0.1:7070 SANDBOXD_TOKEN=$TOKEN python3 examples/python-harness/harness.py
```

Requests with an `Origin` header or a non-localhost `Host` are rejected, so
web pages can't drive the daemon.

## 6. Automated tests

```bash
make test                                     # unit tests (no VMs)
SANDBOXD_E2E=1 go test -v ./test/e2e/         # real VMs; daemon must be running
python3 test/mcp_smoke.py $PWD/_out/bin/sandbox-mcp $PWD/test/fixtures/node-app   # MCP over stdio
make test-linux                               # guest process tests in a Linux container (Docker)
```

## 7. Failure drills

```bash
# Crash a VM and recover its workspace through a rescue VM:
$C exec $ID -- 'echo work > important.txt; sync'
$C exec $ID -- 'sudo python3 -c "import ctypes;ctypes.CDLL(None).reboot(0x4321fedc)"'
$C ps                                         # state: lost (reason includes the console tail)
$C recover $ID -o /tmp/recovery.tar && tar -tf /tmp/recovery.tar

# Daemon restart: running sandboxes are paused to disk on SIGTERM and resume on next use.
pkill -TERM -f "sandboxd serve"; ./_out/bin/sandboxd serve &; $C exec $ID -- uptime

# Idle pause: sandboxctl create --idle 10 ... then wait 25 s and run exec again.
$C gc --dry-run
```

## 8. Uninstall

```bash
./_out/bin/sandboxd uninstall            # removes the LaunchAgent, keeps all data
./_out/bin/sandboxd uninstall --purge    # also deletes the data root (asks you to type "purge")
```

## Troubleshooting

- `sandboxd unreachable`: start the daemon (`sandboxd serve`) or check
  `~/Library/Application Support/sandboxd/sandboxd.log`.
- `Virtualization ... entitlement` errors: run `make sign` (macOS kills
  unsigned binaries that create VMs).
- A sandbox is slow to start the first time after an agent/kernel rebuild:
  golden snapshots are rebuilt automatically (~2 s).
- `capacity_exhausted`: the default budget is half your RAM (8 GB on a 16 GB
  Mac, i.e. four 2 GB sandboxes). Tune `budgets.max_mem_mb` in config.yaml.
- Memory pressure "warn" pauses warm-pool refills; close heavy apps (Docker
  Desktop reserves several GB) for the fastest creates.
