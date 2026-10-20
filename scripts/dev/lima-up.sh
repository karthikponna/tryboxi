#!/usr/bin/env bash
# Creates and starts the Lima dev VM for the Firecracker backend.
# Requires: brew install lima; Apple M3 or newer; macOS 15 or newer.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
NAME="${LIMA_NAME:-sandboxd-dev}"
command -v limactl >/dev/null || { echo "install Lima first: brew install lima"; exit 1; }
if limactl list -q 2>/dev/null | grep -qx "$NAME"; then
  limactl start "$NAME"
  exit 0
fi
tmp="$(mktemp -t sandboxd-lima).yaml"
sed "s#REPO_DIR#${ROOT}#g" "$ROOT/deploy/lima/sandboxd-dev.yaml" > "$tmp"
limactl create --name "$NAME" "$tmp"
limactl start "$NAME"
echo
echo "Next: limactl shell $NAME sudo $ROOT/scripts/dev/lima-provision.sh"
