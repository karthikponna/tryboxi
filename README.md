# tryboxi / sandboxd

Disposable Linux microVMs on your own machine, for AI agents.
"E2B, but on your computer": create a sandbox, run commands, read stdout/stderr/exit code, throw it away. Code never leaves the machine and it costs $0.

- **macOS (Apple Silicon, macOS 14+)**: Apple Virtualization.framework, VMs hosted in the daemon process.
- **Linux (KVM)**: Firecracker + jailer (driver in progress; develop inside Lima with nested virtualization).

Components:

| Binary | What it does |
| --- | --- |
| `sandboxd` | Host daemon: HTTP API on a Unix socket, scheduler, VM drivers, snapshots, workspace sync |
| `sandbox-agent` | Static Linux binary, PID 1 in every VM, gRPC over vsock |
| `sandbox-mcp` | MCP stdio adapter for Cursor, Claude Code, Codex |
| `sandboxctl` | CLI: `ps`, `exec`, `logs`, `shell`, `snapshot`, `fork`, `diff`, `apply`, `undo`, `mcp install`, `doctor` |

See [docs/TESTING.md](docs/TESTING.md) for a step-by-step guide to running it on a Mac.

## Data safety

These rules are enforced in code:

- **Sync is one-way, host to VM.** Deleting files inside a VM never deletes anything on the host.
- The daemon writes to your repo **only** through `apply` and `pull`. Both save a backup tar of every file they are about to change first; `sandboxctl undo <artifact_id>` restores it.
- `apply` and `pull` reject absolute paths, `..`, symlinks that escape the repo, and anything under `.git/`. They never commit, switch branches, reset, or clean. `pull` refuses to overwrite a host file that changed since the last sync unless forced.
- Every delete inside the daemon goes through `safefs.RemoveUnder(root, path)`, which resolves symlinks and refuses anything outside the daemon's data root.
- No host folder is ever shared into a VM (no virtio-fs). Files move only by explicit copy over vsock.
- Secrets are not synced by default (`.env*`, `*.pem`, `*.key`, `id_*`, `.npmrc`), and the copied `.git/config` has credential helpers and URL credentials stripped.
- `sandboxd uninstall` keeps all data unless `--purge` is passed and confirmed.
- GC supports `--dry-run`, and every deletion is logged as an event.

Data lives in `~/Library/Application Support/sandboxd/` (macOS) or `/var/lib/sandboxd` (Linux), never in your repo.

## Build

```bash
make images        # kernel (Kata 6.12, sha256 pinned) + Ubuntu 24.04 rootfs via Docker
make build         # sandboxd (signed), sandboxctl, sandbox-mcp, sandbox-agent
make test          # unit tests
make e2e-darwin    # real microVM tests on macOS
```

License: not chosen yet.
