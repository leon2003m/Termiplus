#!/usr/bin/env bash
# Browser validation of the MCP App panel: builds the mcp server, runs it with
# the in-memory stub backend, serves the panel through a host harness and
# drives it in headless chromium. Exits non-zero if any assertion fails.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
work="$(mktemp -d)"
pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  rm -rf "$work"
}
trap cleanup EXIT

free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])'; }
wait_http() {
  for _ in $(seq 1 100); do
    curl -fsS -o /dev/null "$1" 2>/dev/null && return 0
    sleep 0.1
  done
  echo "timed out waiting for $1" >&2
  return 1
}

(cd "$root" && go build -o "$work/mcp" ./cmd/mcp)

mcp_port="$(free_port)"
harness_port="$(free_port)"
token="dev-$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"

MCP_STUB_BACKEND=1 MCP_AUTH=dev MCP_DEV_TOKEN="$token" MCP_DEV_USER=alice \
  MCP_DEV_ROLES=termiplus-persist,termiplus-admin LISTEN_ADDR="127.0.0.1:$mcp_port" \
  "$work/mcp" >"$work/mcp.log" 2>&1 &
pids+=("$!")
wait_http "http://127.0.0.1:$mcp_port/healthz" || { cat "$work/mcp.log" >&2; exit 1; }

cd "$here"
if [ ! -d node_modules/playwright ]; then
  npm install --no-audit --no-fund
fi

# No-op when the matching chromium is already in the playwright cache.
npx playwright install chromium >/dev/null

MCP_URL="http://127.0.0.1:$mcp_port/" MCP_TOKEN="$token" PORT="$harness_port" \
  node harness.mjs >"$work/harness.log" 2>&1 &
pids+=("$!")
wait_http "http://127.0.0.1:$harness_port/host.html" || { cat "$work/harness.log" >&2; exit 1; }

HARNESS_URL="http://127.0.0.1:$harness_port" node panel.spec.mjs
