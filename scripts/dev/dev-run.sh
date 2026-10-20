#!/usr/bin/env bash
# macOS dev loop: rebuild everything, reinstall the guest agent, restart the
# daemon in the foreground (Ctrl-C pauses sandboxes to disk and exits).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
make build
pkill -TERM -f "_out/bin/sandboxd serve" 2>/dev/null && sleep 3 || true
./_out/bin/sandboxd install --artifacts _out --no-service
exec ./_out/bin/sandboxd serve "$@"
