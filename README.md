# tryboxi / sandboxd

Disposable Linux microVMs on your own machine, for AI agents.
"E2B, but on your computer": create a sandbox, run commands, read
stdout/stderr/exit code, throw it away. Code never leaves the machine and it
costs $0.

- **macOS (Apple Silicon, macOS 14+)**: Apple Virtualization.framework; VMs
  are hosted in the daemon process. Fully working.
- **Linux (KVM)**: Firecracker driver + per-VM network namespaces. Compiles
  and is unit-tested; real-VM testing happens in Lima (see below).

Start here: **[docs/TESTING.md](docs/TESTING.md)**, a step-by-step guide to
running it on a Mac from the CLI, from Cursor (MCP), and from your own
harness (HTTP API).

## Measured on an M4 MacBook (16 GB, macOS 26.5)

| Operation | Time |
| --- | --- |
| Cold boot to guest agent ready | 1.2 s |
| Golden snapshot of the base image | 0.3–0.8 s |
| Create sandbox from golden snapshot (incl. workspace sync) | 0.5–0.6 s |
| Create sandbox from the warm pool | 20–50 ms |
| Resume a sandbox paused to disk | 0.5–0.7 s |
| Fork a running sandbox (per clone, parallel) | ~0.8 s |
| `exec` round trip for a trivial command | a few ms |

## Components

| Binary | What it does |
| --- | --- |
| `sandboxd` | Host daemon: HTTP API on a unix socket (optional TCP + token), scheduler, VM drivers, snapshots, workspace sync, egress policy |
| `sandbox-agent` | Static Linux binary, PID 1 in every VM (delivered via a tiny initramfs), gRPC over vsock |
| `sandbox-mcp` | MCP stdio adapter with 10 tools for Cursor, Claude Code, Codex |
| `sandboxctl` | CLI: `ps create exec logs grep shell rm pause resume sync cp diff apply pull undo snapshot fork restore port recover templates top events gc doctor mcp artifacts` |

```text
Cursor / Claude Code / Codex --MCP stdio--> sandbox-mcp --HTTP/unix--> sandboxd
Custom harness / Go SDK / curl ------------------------HTTP/unix or TCP--> sandboxd
sandboxd --gRPC over vsock--> sandbox-agent (PID 1 in each microVM)
sandboxd --per-VM userspace network (gVisor netstack) / netns proxy--> allowlisted hosts only
```

## Repository layout

```text
cmd/            sandboxd, sandboxctl, sandbox-mcp, sandbox-agent
guest/          guest agent: initd (initramfs switch_root, mounts), proc (PID 1 reaper),
                execd (exec + PTY + timeouts), spool (unacked output), files, server (gRPC)
internal/
  api/          HTTP router, middleware (peer creds, token, Origin/Host checks), SSE
  service/      use cases: create/exec/sync/diff/apply/pull/undo/snapshots/fork/restore/
                recover, health monitor, reaper, warm pool, templates, GC
  scheduler/    vCPU/memory/count ledger, fair priority queue, memory pressure, pool reclaim
  vmm/          Driver interface; vz (macOS) and firecracker (Linux) backends
  network/      policy (allow/deny engine), dns (policy resolver), sniff (SNI/Host),
                egress (proxy), usernet (macOS gVisor stack), linuxnet (netns + nftables)
  workspace/    delta sync manifests, .git sanitizing, 3-way apply, pull, backup/undo
  output/       host log files, ring buffers, SSE fan-out, truncation, ANSI cleanup, grep
  store/        SQLite (WAL) + goose migrations + sqlc queries
  safefs/       the only code path that deletes or writes outside the data root
proto/          guest.proto (host <-> guest contract)
api/            openapi.yaml (HTTP contract)
pkg/client/     public Go SDK
agent-rules/    rule/skill text installed for harnesses
images/         base rootfs Dockerfile
scripts/        images/ (kernel fetch, rootfs build), dev/ (Lima + dev loop)
deploy/lima/    nested-virt Lima VM for the Firecracker backend
examples/       Go and Python external harnesses
test/           e2e tests, MCP smoke test, fixtures
```

## Data safety

These rules are enforced in code:

- **Sync is one-way, host to VM.** Deleting files inside a VM never deletes anything on the host.
- The daemon writes to your repo **only** through `apply` and `pull`. Both save a backup tar of every file they are about to change first; `sandboxctl undo <artifact_id>` restores it.
- `apply` and `pull` reject absolute paths, `..`, symlinks that escape the repo, and anything under `.git/`. They never commit, switch branches, reset, clean, or touch the git index (3-way merges use `git merge-file` on temp files). `pull` refuses to overwrite a host file that changed since the last sync unless forced.
- Every delete inside the daemon goes through `safefs.RemoveUnder(root, path)`, which resolves symlinks and refuses anything outside the daemon's data root.
- No host folder is ever shared into a VM (no virtio-fs). Files move only by explicit copy over vsock.
- Secrets are not synced by default (`.env*`, `*.pem`, `*.key`, `id_*`, `.npmrc`, `.aws`, `.ssh`, ...), and the copied `.git/config` has credential helpers, auth headers, includes, and URL credentials stripped.
- **Credentials never enter VMs.** Stored secrets reach a sandbox only as placeholders; the egress proxy swaps in the real value for the secret's hosts (TLS terminated with a local CA), scrubs echoed values from responses, and blocks placeholders sent anywhere else. A git proxy adds the git token only for allowed repos and refuses pushes outside allowed branches. Commands, env, stdin, and synced files that contain a stored secret value are rejected.
- Egress is allowlist-only by default (package registries, GitHub, Ubuntu mirrors); host loopback, LAN, link-local/metadata, and CGNAT ranges are always blocked, and DNS answers pointing there are dropped.
- `sandboxd uninstall` keeps all data unless `--purge` is passed and confirmed.
- GC supports `--dry-run`, and every deletion is logged as an event.

Data lives in `~/Library/Application Support/sandboxd/` (macOS) or
`/var/lib/sandboxd` (Linux), never in your repo.

## Build

```bash
make images        # kernel (Kata 6.12, sha256 pinned) + Ubuntu 24.04 rootfs via Docker
make build         # sandboxd (signed), sandboxctl, sandbox-mcp, sandbox-agent
make test          # unit tests
SANDBOXD_E2E=1 go test ./test/e2e/   # real microVM tests (daemon running)
```

## Status against the plan

Done and verified with real VMs on macOS: guest agent (PID 1, exec streaming
with on-disk spool and re-attach, PTY, timeouts, OOM flag, files, diff,
changes, heartbeat, time sync, RNG reseed, dead-man switch), vz driver with
save/restore and APFS clones, golden snapshots per template and size, warm
pool with EWMA sizing and eviction under pressure, pause-to-disk on idle and on
daemon shutdown with transparent resume, checkpoint/fork/restore, lost-VM
detection and rescue-VM recovery, egress policy (DNS + SNI/Host), port
forwards, delta workspace sync, diff/apply/pull/undo, repo stack detection
with background template builds, HTTP API + SSE + auth, Go SDK, CLI, MCP
adapter, agent rules and `mcp install`, Prometheus metrics, events, GC,
host-side secrets with placeholder injection and response scrubbing, git
proxy (repo + push-branch rules), per-sandbox egress limits and
allowed-connection logs, OpenTelemetry tracing (`sandboxctl trace`, OTLP
export) and a Grafana dashboard (`deploy/observability`: Prometheus + Grafana
+ Jaeger), network presets (`@node`, `@docker`, `@browsers`, ...), a setup
phase with open network followed by lockdown, live "allow this host"
changes (`sandbox_network` MCP tool, `sandboxctl net`), blocked hosts reported
in exec results, auto-pull of manifests/lockfiles, and Docker inside sandboxes.

Not done yet / known gaps:

- Windows hosts have no backend (would need Hyper-V / WSL2).
- Linux backend not exercised on real KVM yet (`scripts/dev/lima-up.sh` +
  `lima-provision.sh` set it up); Firecracker runs without the jailer, so
  cgroup/seccomp confinement and daemon-restart reattach are still to do.
- Templates from arbitrary OCI images (builder-VM rootfs build) are not
  implemented; templates are the base image plus install steps.
- Python/TypeScript SDKs are not generated yet (see `examples/` for stdlib
  Python over the socket).
- Battery-aware budgets, egress rate limits, and VMGenID are not implemented.

License: not chosen yet.
