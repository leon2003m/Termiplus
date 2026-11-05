#!/usr/bin/env bash
# Native Open Terminal desktop/browser smoke test. No second desktop server.
# Usage: desktop-smoke.sh IMAGE [RUNTIME]
set -euo pipefail
IMAGE="$1"
RUNTIME="${2:-}"
P="tp-native-desktop-smoke"
KEY="$(openssl rand -hex 32)"
runtime=()
[ -n "$RUNTIME" ] && runtime=(--runtime "$RUNTIME")
cleanup() { docker rm -f "$P" "$P-plain" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
for mode in desktop plain; do
  name="$P"; enabled=true
  if [ "$mode" = plain ]; then name="$P-plain"; enabled=false; fi
  docker run -d --name "$name" "${runtime[@]}" --memory=2g --cpus=2 --pids-limit=512 \
    -e OPEN_TERMINAL_API_KEY="$KEY" -e OPEN_TERMINAL_ENABLE_DESKTOP="$enabled" \
    -e OPEN_TERMINAL_ENABLE_BROWSER="$enabled" "$IMAGE" >/dev/null
  for _ in $(seq 1 90); do
    state="$(docker inspect -f '{{.State.Health.Status}}' "$name")"
    [ "$state" = healthy ] && break
    sleep 1
  done
  if [ "$state" != healthy ]; then echo "FAIL $mode health"; exit 1; fi
  docker exec -i "$name" /opt/open-terminal/bin/python3 - "$mode" < "$(dirname "$0")/native-smoke.py"
done
echo "native desktop smoke test passed"
