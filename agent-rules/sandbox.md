# Sandbox (sandboxd microVM)

You have a disposable Linux microVM (Ubuntu 24.04, you are user `agent` with
passwordless sudo) that mirrors this repo at `/workspace`. It is a separate
computer: its own kernel, filesystem, and network. Nothing you do there can
touch the host except through the explicit `sandbox_apply` / `sandbox_pull`
tools, which back up host files first and can be undone.

Use `sandbox_exec` for:
- running tests, builds, linters, type checks, and package installs
  (`npm install`, `pip install`, `apt-get install` with sudo)
- running unknown scripts or code you just wrote
- starting servers (`background: true`, then `sandbox_port` to reach them)
- anything that needs Linux, root, Docker-style setup, or would be risky on the host

Keep using your normal tools to read and edit code on the host. Before each
`sandbox_exec`, the host repo is synced into the VM automatically (one-way,
host -> VM), so your latest edits are always what runs.

Notes:
- Output is trimmed to the head and tail; use `sandbox_exec` with `grep` or
  re-run with narrower commands when you need a specific part.
- A non-zero `exit_code` means the command failed, not the sandbox. Read the
  output and fix the code.
- `changed_files` lists files the command modified inside the VM (formatters,
  lockfiles, codegen). Use `sandbox_pull` to copy them to the host when wanted.
- Network access is limited to package registries and GitHub by default.
- `.env` files and keys are never copied into the VM. Pass needed values with
  the `env` argument of `sandbox_exec` only if the user asks.
- To try risky changes, `sandbox_snapshot` first; `sandbox_snapshot` with
  `action: "restore"` brings back a clean copy.
