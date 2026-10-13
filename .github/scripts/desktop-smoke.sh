#!/usr/bin/env bash
# Smoke test for the desktop image (image/desktop), used by CI:
#
#   .github/scripts/desktop-smoke.sh IMAGE [RUNTIME]
#
# Starts the image once as a desktop (with OPEN_TERMINAL_DESKTOP_PASSWORD) and
# once as a plain terminal, checks Open Terminal and KasmVNC (basic auth,
# WebSocket), then runs the gateway's stream test against the real KasmVNC.
set -euo pipefail

IMAGE="$1"
RUNTIME="${2:-}"
P="tp-desktop-smoke"
PW="$(openssl rand -hex 32)"
KEY="$(openssl rand -hex 32)"
FAILED=0
runtime=()
[ -n "$RUNTIME" ] && runtime=(--runtime "$RUNTIME")

cleanup() { docker rm -f "$P" "$P-plain" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

check() { # name want got
  if [ "$2" = "$3" ]; then echo "PASS  $1"; else echo "FAIL  $1: want $2, got $3"; FAILED=1; fi
}
code() { curl -s -m 5 -o /dev/null -w '%{http_code}' "$@" || true; }

docker run -d --name "$P" "${runtime[@]}" -p 127.0.0.1::8000 -p 127.0.0.1::6901 \
  -e OPEN_TERMINAL_API_KEY="$KEY" -e OPEN_TERMINAL_DESKTOP_PASSWORD="$PW" "$IMAGE" >/dev/null
docker run -d --name "$P-plain" "${runtime[@]}" -p 127.0.0.1::8000 -p 127.0.0.1::6901 \
  -e OPEN_TERMINAL_API_KEY="$KEY" "$IMAGE" >/dev/null
T="$(docker port "$P" 8000/tcp | head -1)"
V="$(docker port "$P" 6901/tcp | head -1)"
TP="$(docker port "$P-plain" 8000/tcp | head -1)"
VP="$(docker port "$P-plain" 6901/tcp | head -1)"

for _ in $(seq 1 90); do
  [ "$(code "http://$T/health")" = 200 ] && [ "$(code "http://$V/")" = 401 ] && break
  sleep 1
done

check "desktop: Open Terminal health" 200 "$(code "http://$T/health")"
check "desktop: Open Terminal needs its key" 401 "$(code "http://$T/files/cwd")"
check "desktop: Open Terminal with its key" 200 "$(code -H "Authorization: Bearer $KEY" "http://$T/files/cwd")"
check "desktop: KasmVNC without password" 401 "$(code "http://$V/")"
check "desktop: KasmVNC with a wrong password" 401 "$(code -u user:wrong "http://$V/")"
check "desktop: KasmVNC with the password" 200 "$(code -u "user:$PW" "http://$V/")"
ws="$(curl -s -m 3 -i -o - -u "user:$PW" -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  -H 'Sec-WebSocket-Protocol: binary' -H "Origin: http://$V" "http://$V/websockify" | head -1 | tr -d '\r' || true)"
check "desktop: KasmVNC WebSocket" "HTTP/1.1 101 Switching Protocols" "$ws"
check "desktop: no lockout after failed logins" 200 "$(for _ in 1 2 3 4 5 6 7; do code -u user:x "http://$V/" >/dev/null; done; code -u "user:$PW" "http://$V/")"
leaked="$(docker exec "$P" sh -c 'for p in $(pgrep -f /opt/open-terminal/bin/open-terminal); do [ "$p" = 1 ] || [ "$p" = $$ ] || tr "\0" "\n" <"/proc/$p/environ"; done' 2>/dev/null | grep -c DESKTOP_PASSWORD || true)"
ot="$(docker exec "$P" pgrep -fc /opt/open-terminal/bin/open-terminal || true)"
check "desktop: Open Terminal is running" 1 "$([ "${ot:-0}" -ge 1 ] && echo 1 || echo 0)"
check "desktop: Open Terminal does not get the desktop password" 0 "$leaked"

for _ in $(seq 1 60); do [ "$(code "http://$TP/health")" = 200 ] && break; sleep 1; done
check "plain: Open Terminal health" 200 "$(code "http://$TP/health")"
check "plain: no desktop without the password" 000 "$(code "http://$VP/")"

echo "--- gateway stream test against this KasmVNC"
if ! TERMIPLUS_KASM_ADDR="$V" TERMIPLUS_KASM_PASSWORD="$PW" \
  go test -count=1 -run 'TestDesktopStreamRealKasmVNC' -v ./internal/gateway/ | grep -E '^(---|ok|FAIL|PASS)'; then
  FAILED=1
fi

if [ "$FAILED" -ne 0 ]; then
  docker logs "$P" 2>&1 | tail -30
  echo "desktop smoke test FAILED"
  exit 1
fi
echo "desktop smoke test passed"
