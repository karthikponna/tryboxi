# Contributing to boxi

Thanks for helping. This guide covers how to set up the project, make a change, and get it merged.

- [Ground rules](#ground-rules)
- [Set up](#set-up)
- [Where things live](#where-things-live)
- [Make a change](#make-a-change)
- [Tests](#tests)
- [Generated code](#generated-code)
- [The website](#the-website)
- [Pull requests](#pull-requests)
- [Reporting bugs and security issues](#reporting-bugs-and-security-issues)

## Ground rules

boxi runs untrusted code next to people's repos and credentials, so a few rules are not negotiable:

- **Never weaken the data-safety guarantees** listed in the [README](README.md#data-safety). Sync stays one-way, writes to the host go only through `apply` / `pull` with a backup, and secrets never enter a VM.
- **All deletes outside the data root go through `internal/safefs`.** Do not call `os.RemoveAll` on paths you did not create.
- **No new host folders shared into VMs.** Files move by explicit copy over vsock.
- **Egress stays allowlist-only by default.** Loopback, LAN, link-local / metadata, and CGNAT must remain blocked.

If a change touches one of these areas, say so in the pull request and explain why it is safe.

## Set up

You need:

| Tool | Why |
| --- | --- |
| Apple Silicon Mac, macOS 14+ | The working backend (Virtualization.framework) |
| Go (version in `go.mod`) | Builds everything |
| Docker Desktop | Builds the base rootfs (`make images`) and runs `make test-linux` |
| `buf`, `sqlc` | Only if you change `proto/` or `queries/` |
| Lima | Only for the Linux / Firecracker backend |

```bash
git clone https://github.com/karthikponna/tryboxi && cd tryboxi
make images                                  # kernel + Ubuntu 24.04 rootfs (~3 min)
make build                                   # all binaries into _out/bin
./_out/bin/sandboxd install --artifacts _out --no-service
./_out/bin/sandboxd serve --debug            # foreground daemon with every event logged
```

In another terminal, check it with `./_out/bin/sandboxctl doctor`.
[docs/TESTING.md](docs/TESTING.md) walks through the CLI, MCP, and HTTP API end to end.

## Where things live

| You want to change | Look in |
| --- | --- |
| The HTTP API | `api/openapi.yaml`, `internal/api/`, `internal/service/` |
| The Go SDK | `pkg/client/` |
| CLI commands | `cmd/sandboxctl/` |
| MCP tools | `cmd/sandbox-mcp/`, rule text in `agent-rules/` |
| VM drivers | `internal/vmm/vz/` (macOS), `internal/vmm/firecracker/` (Linux) |
| Network policy, egress proxy, secrets | `internal/network/`, `internal/secrets/` |
| Workspace sync, diff, apply, pull, undo | `internal/workspace/` |
| The guest agent | `guest/`, `cmd/sandbox-agent/`, contract in `proto/guest/v1/` |
| Database schema and queries | `migrations/`, `queries/`, generated code in `internal/store/db/` |

The [repository layout](README.md#repository-layout) in the README shows the full tree.

## Make a change

1. Create a branch from `main`.
2. Keep changes focused; one concern per pull request.
3. Format and vet before you push:

   ```bash
   gofmt -w .
   make lint    # go vet for darwin and linux/arm64, plus a gofmt check
   ```

4. Add or update tests for behavior you change.
5. Update the README or `docs/TESTING.md` when you change commands, flags, or user-facing behavior.

Code style:

- Follow standard Go conventions; `gofmt` is the formatter.
- Return errors with context instead of logging and continuing.
- Comments explain *why* something is done when the code cannot; avoid restating the code.

## Tests

| Command | What it runs | Needs |
| --- | --- | --- |
| `make test` | Unit tests | Nothing extra |
| `make test-linux` | Guest process tests in a Linux container | Docker |
| `make e2e-darwin` | Real microVM tests on this Mac | `make images` |
| `SANDBOXD_E2E=1 go test -v ./test/e2e/` | E2E tests against a running daemon | Daemon running |
| `python3 test/mcp_smoke.py $PWD/_out/bin/sandbox-mcp $PWD/test/fixtures/node-app` | MCP over stdio | Daemon running |

CI runs `go vet` (host and linux/arm64), `go test -race ./...`, `make agent`, and `make build` on Linux and macOS.

For the Linux backend, `scripts/dev/lima-up.sh` and `scripts/dev/lima-provision.sh` set up a nested-virtualization Lima VM.

## Generated code

Do not edit generated files by hand.

| Source | Command | Output |
| --- | --- | --- |
| `proto/guest/v1/guest.proto` | `make proto` | `gen/guest/v1/` |
| `queries/queries.sql` + `migrations/` | `make sqlc` | `internal/store/db/` |

Commit the regenerated files together with the source change.

## The website

The landing page for tryboxi.dev lives in `web/` (Next.js, Tailwind CSS, bun).

```bash
cd web
bun install
bun run dev      # http://localhost:3000
bun run lint
bun run build
```

Install strings and links live in `web/src/site.config.ts`. The `web/go.mod` file keeps Go tooling from scanning `node_modules`; leave it in place.

## Pull requests

- Describe what changed and why, and how you tested it (commands and results).
- Link the issue it fixes, if there is one.
- Make sure `make lint` and `make test` pass locally.
- Screenshots or terminal output help for CLI and website changes.

## Reporting bugs and security issues

- **Bugs:** open an issue with your macOS version, chip, the commands you ran, and the output of `sandboxd doctor` and `sandboxctl doctor`. Logs live in `~/Library/Application Support/sandboxd/sandboxd.log`.
- **Security issues** (a way out of the VM, a secret leak, a bypass of the network policy or of `safefs`): please do not open a public issue. Contact the maintainer privately through GitHub first.
