#!/usr/bin/env bash
# End-to-end test of the alpha stack as deployed by docs/deploy-alpha.md, on a
# throwaway host or VM (needs root: it applies deploy/firewall.sh).
#
#   sudo RUNTIME=sysbox-runc test/integration/alpha-e2e.sh
#   sudo RUNTIME=runsc       test/integration/alpha-e2e.sh
#   BUILD=0 ...   # reuse images tagged :it
#
# Brings up base + mcp + desktop + keycloak overlays exactly as shipped (with
# secrets and realm from deploy/setup-alpha.sh), puts Caddy with an internal CA
# in front as the TLS reverse proxy (sso.test, desk.test), creates test users
# in Keycloak, then plays Open WebUI (plugin calls with a real access token and
# the plugin key) and a browser (sign-in through Keycloak, console, desktop
# stream through desk-auth, gateway and KasmVNC in a real sandbox).
# Refuses to run next to an existing deployment.

# Helpers are called through check and the EXIT trap.
# shellcheck disable=SC2317
set -euo pipefail

RUNTIME="${RUNTIME:-runsc}"
TAG="it"
REG="ghcr.io/leon2003m"
PROJECT="termiplus-a2e"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d)"
CADDY_IMAGE="caddy:2.10.2-alpine@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d"
EDGE_PORT=18443
CREATED_OWUI=0

[ "$(id -u)" = 0 ] || { echo "run as root (sudo): the firewall is part of the test" >&2; exit 1; }

log() { printf '\n==> %s\n' "$*"; }
FAILED=0
pass() { echo "PASS  $*"; }
fail() { echo "FAIL  $*"; FAILED=1; }
check() { # name condition(0/1) detail
  if [ "$2" = 0 ]; then pass "$1"; else fail "$1: $3"; fi
}
is() { [ "$1" = "$2" ] && echo 0 || echo 1; }
has() { [[ "$1" == *"$2"* ]] && echo 0 || echo 1; }
lacks() { [[ "$1" == *"$2"* ]] && echo 1 || echo 0; }

compose() {
  docker compose -p "$PROJECT" --env-file "$WORK/deploy/.env" \
    -f "$WORK/deploy/docker-compose.yml" -f "$WORK/deploy/docker-compose.mcp.yml" \
    -f "$WORK/deploy/docker-compose.desktop.yml" -f "$WORK/deploy/docker-compose.keycloak.yml" \
    -f "$WORK/deploy/a2e.override.yml" "$@"
}

cleanup() {
  log "cleanup"
  docker rm -f tp-a2e-edge tp-a2e-client >/dev/null 2>&1 || true
  compose down -v >/dev/null 2>&1 || true
  mapfile -t ctrs < <(docker ps -aq --filter label=termiplus.managed=true)
  if [ "${#ctrs[@]}" -gt 0 ]; then docker rm -f "${ctrs[@]}" >/dev/null 2>&1 || true; fi
  mapfile -t vols < <(docker volume ls -q --filter label=termiplus.managed=true)
  if [ "${#vols[@]}" -gt 0 ]; then docker volume rm "${vols[@]}" >/dev/null 2>&1 || true; fi
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    docker network inspect ot-sandbox >/dev/null 2>&1 || break
    docker network rm ot-sandbox >/dev/null 2>&1 || sleep 3
  done
  if [ "$CREATED_OWUI" = 1 ]; then docker network rm owui >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

if [ "$(docker ps -aq --filter label=termiplus.managed=true | wc -l)" -gt 0 ] ||
  [ "$(docker volume ls -q --filter label=termiplus.managed=true | wc -l)" -gt 0 ] ||
  docker network inspect ot-sandbox >/dev/null 2>&1; then
  echo "Refusing to run: this host has termiplus containers, home volumes or ot-sandbox." >&2
  trap - EXIT
  rm -rf "$WORK"
  exit 1
fi

if [ "${BUILD:-1}" = 1 ]; then
  log "build images"
  for t in gateway sockproxy mcp; do
    docker build -q --target "$t" -t "$REG/termiplus-$t:$TAG" "$ROOT" >/dev/null
  done
  docker build -q -t "$REG/termiplus-terminal:$TAG" "$ROOT/image" >/dev/null
  docker build -q --build-arg BASE="$REG/termiplus-terminal:$TAG" -t "$REG/termiplus-desktop:$TAG" "$ROOT/image/desktop" >/dev/null
fi

log "prepare deployment (runtime $RUNTIME)"
cp -r "$ROOT/deploy" "$WORK/deploy"
rm -rf "$WORK/deploy/secrets" "$WORK/deploy/.env" "$WORK/deploy/mcp-data"
mkdir -p "$WORK/deploy/secrets"
for s in proxy_token gateway_api_key key_secret; do
  openssl rand -hex 32 | tr -d '\n' >"$WORK/deploy/secrets/$s"
done
chmod 0644 "$WORK"/deploy/secrets/*
WEBUI_URL=https://chat.test KEYCLOAK_PUBLIC_URL=https://sso.test DESKTOP_PUBLIC_URL=https://desk.test \
  "$WORK/deploy/setup-alpha.sh" >/dev/null
chown 65534:65534 "$WORK/deploy/mcp-data"
cat >"$WORK/deploy/policies.json" <<JSON
{
  "default": { "runtime": "$RUNTIME", "memory_mb": 1024, "cpus": 1, "pids": 512 },
  "desk":    { "runtime": "$RUNTIME", "memory_mb": 2048, "cpus": 2, "pids": 1024,
               "home": "default", "desktop": true }
}
JSON
cat >>"$WORK/deploy/.env" <<ENV
TERMINAL_IMAGE=$REG/termiplus-desktop:$TAG
TERMIPLUS_VERSION=$TAG
DOCKER_GID=$(stat -c %g /var/run/docker.sock)
ENV
# Test-only: the oauth2-proxies trust Caddy's internal CA, and the sandbox
# runtime is pinned.
cat >"$WORK/deploy/a2e.override.yml" <<YAML
services:
  sockproxy:
    environment:
      ALLOWED_RUNTIMES: "$RUNTIME"
  mcp-auth:
    environment: { SSL_CERT_FILE: /etc/a2e/root.crt }
    volumes: ["$WORK/edge-root.crt:/etc/a2e/root.crt:ro"]
  desk-auth:
    environment: { SSL_CERT_FILE: /etc/a2e/root.crt }
    volumes: ["$WORK/edge-root.crt:/etc/a2e/root.crt:ro"]
YAML

if ! docker network inspect owui >/dev/null 2>&1; then
  docker network create owui >/dev/null
  CREATED_OWUI=1
fi

log "start Keycloak and the TLS edge"
compose up -d --no-build --pull missing --wait --wait-timeout 300 keycloak
cat >"$WORK/Caddyfile" <<'CADDY'
sso.test {
  tls internal
  reverse_proxy keycloak:8080
}
desk.test {
  tls internal
  reverse_proxy desk-auth:4180
}
CADDY
docker run -d --name tp-a2e-edge --network owui --network-alias sso.test --network-alias desk.test \
  -p "127.0.0.1:$EDGE_PORT:443" -v "$WORK/Caddyfile:/etc/caddy/Caddyfile:ro" "$CADDY_IMAGE" >/dev/null
for _ in $(seq 1 30); do
  docker cp tp-a2e-edge:/data/caddy/pki/authorities/local/root.crt "$WORK/edge-root.crt" 2>/dev/null && break
  sleep 1
done
chmod 0644 "$WORK/edge-root.crt"

# The "browser": both names resolve to the published edge, Caddy's CA is trusted.
browser() {
  curl -s -m 20 --cacert "$WORK/edge-root.crt" \
    --connect-to "sso.test:443:127.0.0.1:$EDGE_PORT" --connect-to "desk.test:443:127.0.0.1:$EDGE_PORT" "$@"
}
for _ in $(seq 1 60); do
  browser -f https://sso.test/realms/termiplus/.well-known/openid-configuration >/dev/null && break
  sleep 2
done
issuer="$(browser https://sso.test/realms/termiplus/.well-known/openid-configuration | python3 -c 'import json,sys; print(json.load(sys.stdin)["issuer"])')"
check "Keycloak behind TLS publishes the public issuer" "$(is "$issuer" https://sso.test/realms/termiplus)" "$issuer"

log "test users"
kc() { docker exec "$PROJECT-keycloak-1" /opt/keycloak/bin/kcadm.sh "$@"; }
kc config credentials --server http://localhost:8080 --realm master --user admin \
  --password "$(cat "$WORK/deploy/secrets/keycloak_admin_password")" >/dev/null 2>&1
mkuser() { # name group...
  local u="$1"; shift
  kc create users -r termiplus -s "username=$u" -s enabled=true -s "email=$u@example.test" -s emailVerified=true \
    -s firstName="$u" -s lastName=Test >/dev/null
  kc set-password -r termiplus --username "$u" --new-password "$u-pw-a2e"
  local id
  id="$(kc get users -r termiplus -q "username=$u" --fields id --format csv --noquotes)"
  local g gid
  for g in "$@"; do
    gid="$(kc get groups -r termiplus -q "search=$g" --fields id --format csv --noquotes | head -1)"
    kc update "users/$id/groups/$gid" -r termiplus -s realm=termiplus -s "userId=$id" -s "groupId=$gid" -n >/dev/null
  done
}
mkuser alice termiplus-persist
mkuser bob
mkuser root termiplus-admin
# Test-only: the "Open WebUI" side gets its tokens by password grant.
OWUI_CID="$(kc get clients -r termiplus -q clientId=owui --fields id --format csv --noquotes)"
kc update "clients/$OWUI_CID" -r termiplus -s directAccessGrantsEnabled=true

log "start the full stack"
compose up -d --no-build --pull missing --wait --wait-timeout 300
"$WORK/deploy/firewall.sh" >/dev/null

token() { # user -> access token of the owui client, as Open WebUI would hold it
  browser -d grant_type=password -d client_id=owui -d "client_secret=$(cat "$WORK/deploy/secrets/owui_oidc_client_secret")" \
    -d "username=$1" -d "password=$1-pw-a2e" -d scope=openid https://sso.test/realms/termiplus/protocol/openid-connect/token |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])'
}
docker run -d --name tp-a2e-client --network owui "$REG/termiplus-terminal:$TAG" sleep infinity >/dev/null
PLUGIN_KEY="$(cat "$WORK/deploy/secrets/mcp_owui_plugin_key")"
mcp() { # token owui-user tool args-json -> tool result text or "ERROR <code>"
  local body="{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$3\",\"arguments\":$4}}"
  docker exec tp-a2e-client curl -s -m 180 -H "Authorization: Bearer $1" -H "X-Termiplus-Owui-Key: $PLUGIN_KEY" \
    -H "X-Termiplus-Owui-User: $2" -H 'Content-Type: application/json' -d "$body" http://mcp-auth:4180/ |
    python3 -c '
import json, sys
d = json.loads(sys.stdin.read() or "{}")
if d.get("error"): print("ERROR", d["error"]["code"])
else: print(d.get("result", {}).get("content", [{}])[0].get("text", ""))'
}

log "Open WebUI plugin path (mcp-auth with a real token)"
TA="$(token alice)"
TB="$(token bob)"
out="$(mcp "$TA" owui-alice list_workspaces '{}')"
check "alice lists the desk workspace (desktop)" "$(has "$out" '"name":"desk"')" "$out"
out="$(mcp "$TA" owui-alice request_session '{"workspace":"desk","persistent":true}')"
check "persistent desk needs confirmation" "$(is "$out" "ERROR -32010")" "$out"
out="$(mcp "$TB" owui-bob request_session '{"workspace":"desk","persistent":true,"_confirmed":true}')"
check "bob (no persist group) is forbidden" "$(is "$out" "ERROR -32003")" "$out"
out="$(mcp "$TA" owui-alice request_session '{"workspace":"desk","persistent":true,"_confirmed":true}')"
check "alice gets a desktop session" "$(has "$out" '"stream_url":"https://desk.test/stream/')" "$out"
STREAM_URL="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["stream_url"])' "$out" 2>/dev/null || true)"
STREAM_PW="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["stream_password"])' "$out" 2>/dev/null || true)"
SID="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["session_id"])' "$out" 2>/dev/null || true)"
rt="$(docker inspect -f '{{.HostConfig.Runtime}}' "$SID" 2>/dev/null || true)"
check "the desktop runs under $RUNTIME" "$(is "$rt" "$RUNTIME")" "$rt"
out="$(docker exec tp-a2e-client curl -s -m 10 -H "Authorization: Bearer $TA" -H 'X-Termiplus-Owui-User: owui-alice' \
  -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_sessions","arguments":{}}}' \
  http://mcp-auth:4180/)"
check "without the plugin key the call is unauthenticated" "$(has "$out" '-32001')" "$out"

log "browser path (desk-auth sign-in, console, desktop stream)"
login() { # jar user
  local form action
  form="$(browser -L -c "$1" -b "$1" https://desk.test/ui/)"
  action="$(python3 -c '
import html, re, sys
m = re.search(r"<form[^>]*id=\"kc-form-login\"[^>]*action=\"([^\"]+)\"", sys.stdin.read())
print(html.unescape(m.group(1)) if m else "")' <<<"$form")"
  [ -n "$action" ] || return 1
  browser -L -c "$1" -b "$1" -o /dev/null --data-urlencode "username=$2" --data-urlencode "password=$2-pw-a2e" "$action"
  grep -q _termiplus_desk "$1"
}
JA="$WORK/jar-alice"
if login "$JA" alice; then pass "alice signs in through Keycloak (TLS, PKCE)"; else fail "alice browser sign-in"; fi
out="$(browser -b "$JA" https://desk.test/ui/api/me)"
check "console: alice is linked to owui-alice" "$(has "$out" '"linked":true')" "$out"
out="$(browser -b "$JA" https://desk.test/ui/api/sessions)"
check "console: lists alice's desktop" "$(has "$out" "$SID")" "$out"
code="$(browser -o /dev/null -w '%{http_code}' -u "user:$STREAM_PW" "$STREAM_URL")"
check "stream without sign-in goes to the IdP" "$(is "$code" 302)" "$code"
code="$(browser -b "$JA" -o /dev/null -w '%{http_code}' "$STREAM_URL")"
check "signed in, no desktop password: KasmVNC says 401" "$(is "$code" 401)" "$code"
for _ in $(seq 1 60); do
  code="$(browser -b "$JA" -o /dev/null -w '%{http_code}' -u "user:$STREAM_PW" "$STREAM_URL")"
  [ "$code" = 200 ] && break
  sleep 2
done
check "signed in with the desktop password: the desktop page loads" "$(is "$code" 200)" "$code"
ws() { # origin -> status line
  browser --http1.1 -b "$JA" -u "user:$STREAM_PW" -i -o - -m 5 -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
    -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H 'Sec-WebSocket-Protocol: binary' \
    -H "Origin: $1" "${STREAM_URL}websockify" | head -1 | tr -d '\r' || true
}
out="$(ws https://desk.test)"
check "desktop WebSocket through TLS, desk-auth and the gateway" "$(has "$out" " 101 ")" "$out"
out="$(ws https://evil.test)"
check "cross-site desktop WebSocket is refused" "$(has "$out" " 403 ")" "$out"
JB="$WORK/jar-bob"
login "$JB" bob || fail "bob browser sign-in"
code="$(browser -b "$JB" -o /dev/null -w '%{http_code}' -u "user:wrong" "$STREAM_URL")"
check "bob without alice's password cannot open her desktop" "$(is "$code" 401)" "$code"

log "admin console"
mcp "$(token root)" owui-root list_sessions '{}' >/dev/null
JR="$WORK/jar-root"
login "$JR" root || fail "root browser sign-in"
code="$(browser -b "$JR" -o /dev/null -w '%{http_code}' -X PUT -H 'X-Termiplus-UI: 1' -H 'Origin: https://desk.test' \
  -H 'Content-Type: application/json' -d '{"default":"allow","workspaces":{"desk":["termiplus-admin"]}}' \
  https://desk.test/ui/api/admin/entitlements)"
check "admin saves entitlements in the console" "$(is "$code" 200)" "$code"
out="$(mcp "$TA" owui-alice list_workspaces '{}')"
check "alice no longer sees desk" "$(lacks "$out" '"desk"')" "$out"
check "entitlements persisted in mcp-data" "$(test -s "$WORK/deploy/mcp-data/entitlements.json" && echo 0 || echo 1)" "missing"

log "Open WebUI terminal path to the same home"
out="$(docker exec tp-a2e-client curl -s -m 120 -H "Authorization: Bearer $(cat "$WORK/deploy/secrets/gateway_api_key")" \
  -H 'X-User-Id: owui-alice' http://gateway:8080/files/cwd)"
check "Open WebUI's terminal for owui-alice works" "$(has "$out" cwd)" "$out"
homes="$(docker volume ls -q --filter label=termiplus.user=owui-alice | wc -l)"
check "terminal and desktop share one home volume" "$(is "$homes" 1)" "$homes volumes"

if [ "$FAILED" -ne 0 ]; then
  log "alpha end-to-end test FAILED (runtime $RUNTIME)"
  exit 1
fi
log "alpha end-to-end test passed (runtime $RUNTIME)"
