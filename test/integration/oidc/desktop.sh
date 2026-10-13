#!/usr/bin/env bash
# Browser sign-in test for desktop streams (deploy/docker-compose.desktop.yml)
# against a real IdP.
#
#   test/integration/oidc/desktop.sh
#   BUILD=0 ECHO_IMAGE=ghcr.io/leon2003m/termiplus-terminal:it test/integration/oidc/desktop.sh
#
# Starts a throwaway Keycloak (realm.json), desk-auth exactly as the overlay
# configures it (image and flags read from the overlay; only --cookie-secure
# and --redirect-url are adapted to plain-HTTP localhost) and, in place of the
# gateway, an echo server (echo.py, run with the terminal image's Python), plus
# the real mcp image (stub backend) serving the management UI. It logs in like
# a browser and checks that only signed-in browsers reach the stream path and
# the UI, that nothing else is reachable, that the browser's KasmVNC basic
# auth arrives unchanged while no token of ours does, and that the UI acts as
# the Open WebUI user linked to the signed-in subject.
# Everything it creates is named tp-dskauth-it-* and removed on exit.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
HERE="$ROOT/test/integration/oidc"
P="tp-dskauth-it"
KC_IMAGE="quay.io/keycloak/keycloak:26.7.5@sha256:37dbaf6f0722c9ec246335f36e1ef8b2e6cb960f7c27e0d8c615121a3d475a85"
ECHO_IMAGE="${ECHO_IMAGE:-ghcr.io/leon2003m/termiplus-terminal:latest}"
MCP_IMAGE="termiplus-mcp:oidc-it"
ISSUER="http://keycloak:8080/realms/termiplus"
WORK="$(mktemp -d)"

cleanup() {
  docker rm -f "$P-kc" "$P-proxy" "$P-gw" "$P-mcp" >/dev/null 2>&1 || true
  docker network rm "$P-net" "$P-desk" >/dev/null 2>&1 || true
}
trap 'cleanup; rm -rf "$WORK"' EXIT
cleanup

FAILED=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; FAILED=1; }
check() { # name condition-result(0/1) detail
  if [ "$2" = 0 ]; then pass "$1"; else fail "$1: $3"; fi
}

if [ "${BUILD:-1}" = 1 ]; then
  docker build -q --target mcp -t "$MCP_IMAGE" "$ROOT" >/dev/null
fi

PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
PUBLIC="http://127.0.0.1:$PORT"

cfg="$(cd "$ROOT/deploy" && MCP_OIDC_ISSUER_URL="$ISSUER" DESKTOP_PUBLIC_URL="$PUBLIC" TERMINAL_IMAGE=unused \
  docker compose -p "$P" -f docker-compose.yml -f docker-compose.mcp.yml -f docker-compose.desktop.yml \
  config --format json)"
PROXY_IMAGE="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["services"]["desk-auth"]["image"])' <<<"$cfg")"
mapfile -t PROXY_ARGS < <(python3 -c '
import json, sys
for a in json.load(sys.stdin)["services"]["desk-auth"]["command"]:
    # Plain HTTP on localhost: the cookie cannot be Secure here.
    print("--cookie-secure=false" if a == "--cookie-secure=true" else a)
' <<<"$cfg")
for up in --upstream=http://gateway:8080/stream/ --upstream=http://mcp:8091/ui/; do
  printf '%s\n' "${PROXY_ARGS[@]}" | grep -qx -- "$up" || { echo "overlay upstreams changed; update this test"; exit 1; }
done

printf 'desk-secret-it' >"$WORK/client_secret"
openssl rand -hex 16 | tr -d '\n' >"$WORK/cookie_secret"
chmod 644 "$WORK/client_secret" "$WORK/cookie_secret"

docker network create "$P-net" >/dev/null
docker network create --internal "$P-desk" >/dev/null

docker run -d --name "$P-kc" --network "$P-net" --network-alias keycloak -p 127.0.0.1::8080 \
  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin-it \
  -e KC_HOSTNAME=http://keycloak:8080 \
  -v "$HERE/realm.json:/opt/keycloak/data/import/realm.json:ro" \
  "$KC_IMAGE" start-dev --import-realm >/dev/null
KCPORT="$(docker port "$P-kc" 8080/tcp | head -1 | cut -d: -f2)"

docker run -d --name "$P-gw" --network "$P-desk" --network-alias gateway \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  -v "$HERE/echo.py:/echo.py:ro" --entrypoint /opt/open-terminal/bin/python3 \
  "$ECHO_IMAGE" -I /echo.py >/dev/null

for _ in $(seq 1 90); do
  curl -sf -m 2 "http://127.0.0.1:$KCPORT/realms/termiplus/.well-known/openid-configuration" >/dev/null && break
  sleep 2
done

# The MCP server with the UI, the stub backend and alice's subject linked to
# the Open WebUI user "owui-alice" (as a plugin call would have recorded).
SUB_A="$(curl -s -m 10 -d grant_type=password -d client_id=termiplus-desktop -d client_secret=desk-secret-it \
  -d username=alice -d password=alice-pw "http://127.0.0.1:$KCPORT/realms/termiplus/protocol/openid-connect/token" |
  python3 -c 'import base64,json,sys; t=json.load(sys.stdin)["access_token"].split(".")[1]; print(json.loads(base64.urlsafe_b64decode(t+"="*(-len(t)%4)))["sub"])')"
mkdir "$WORK/data"
printf '{"subjects":{"%s":"owui-alice"}}\n' "$SUB_A" >"$WORK/data/identities.json"
chmod 755 "$WORK/data"
chmod 644 "$WORK/data/identities.json"
docker run -d --name "$P-mcp" --network "$P-desk" --network-alias mcp \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true -v "$WORK/data:/data:ro" \
  -e MCP_AUTH=proxy -e MCP_STUB_BACKEND=1 -e MCP_IDENTITY_FILE=/data/identities.json -e UI_LISTEN_ADDR=:8091 \
  "$MCP_IMAGE" >/dev/null

docker create --name "$P-proxy" --network "$P-net" -p "127.0.0.1:$PORT:4180" \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  -v "$WORK/client_secret:/run/secrets/desk_oidc_client_secret:ro" \
  -v "$WORK/cookie_secret:/run/secrets/desk_oidc_cookie_secret:ro" \
  "$PROXY_IMAGE" "${PROXY_ARGS[@]}" >/dev/null
docker network connect "$P-desk" "$P-proxy"
docker start "$P-proxy" >/dev/null
for _ in $(seq 1 30); do
  curl -sf -m 2 "$PUBLIC/ping" >/dev/null && break
  sleep 1
done

# The "browser": Keycloak's public name resolves to the published port.
browser() { curl -s -m 10 --connect-to "keycloak:8080:127.0.0.1:$KCPORT" "$@"; }

login() { # jar user password -> 0 when the session cookie was obtained
  local jar="$1" form action
  form="$(browser -L -c "$jar" -b "$jar" "$PUBLIC/stream/x/")"
  action="$(python3 -c '
import html, re, sys
m = re.search(r"<form[^>]*id=\"kc-form-login\"[^>]*action=\"([^\"]+)\"", sys.stdin.read())
print(html.unescape(m.group(1)) if m else "")' <<<"$form")"
  [ -n "$action" ] || return 1
  browser -L -c "$jar" -b "$jar" -o /dev/null --data-urlencode "username=$2" \
    --data-urlencode "password=$3" "$action"
  grep -q _termiplus_desk "$jar"
}

# Not signed in: no request reaches the gateway.
code="$(browser -o /dev/null -w '%{http_code}' "$PUBLIC/stream/x/")"
loc="$(browser -o /dev/null -w '%{redirect_url}' "$PUBLIC/stream/x/")"
check "not signed in: redirected to the IdP, not proxied" "$([[ "$code" = 302 && "$loc" == http://keycloak:8080/realms/termiplus/* ]] && echo 0 || echo 1)" "$code $loc"
code="$(browser -o /dev/null -w '%{http_code}' -u user:pw "$PUBLIC/stream/x/")"
check "basic auth alone does not pass the proxy" "$([ "$code" = 302 ] && echo 0 || echo 1)" "$code"
TOKEN="$(browser -d grant_type=password -d client_id=termiplus-desktop -d client_secret=desk-secret-it \
  -d username=alice -d password=alice-pw "http://keycloak:8080/realms/termiplus/protocol/openid-connect/token" |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')"
code="$(browser -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$PUBLIC/stream/x/")"
check "a bearer token does not replace the browser sign-in" "$([ "$code" = 302 ] && echo 0 || echo 1)" "$code"

JAR="$WORK/jar"
if login "$JAR" alice alice-pw; then pass "browser sign-in via Keycloak sets the session cookie"; else fail "browser sign-in"; fi

seen="$(browser -b "$JAR" -u user:kasm-pw "$PUBLIC/stream/ot-u-1-desk/websockify?x=1")"
py() { python3 -c "import json,sys; d=json.loads(sys.argv[1]); print($1)" "$seen"; }
check "signed in: request reaches the stream path" "$([ "$(py 'd["path"]')" = "/stream/ot-u-1-desk/websockify?x=1" ] && echo 0 || echo 1)" "$seen"
basic="Basic $(printf 'user:kasm-pw' | base64)"
check "KasmVNC basic auth arrives unchanged" "$([ "$(py 'd["headers"].get("authorization","")')" = "$basic" ] && echo 0 || echo 1)" "$seen"
check "no access token or id token is forwarded" \
  "$([ "$(py '",".join(k for k in d["headers"] if "token" in k or k.startswith("x-auth-request"))')" = "" ] && echo 0 || echo 1)" "$seen"
check "the browser's Host is forwarded (same-origin check)" "$([ "$(py 'd["headers"].get("host","")')" = "127.0.0.1:$PORT" ] && echo 0 || echo 1)" "$seen"

for path in /control/v1/workspaces /p/default/openapi.json /admin/v1/policy /healthz /; do
  out="$(browser -b "$JAR" -w ' %{http_code}' "$PUBLIC$path")"
  check "signed in: $path is not proxied" "$([[ "$out" != *'"path"'* && "${out##* }" = 404 ]] && echo 0 || echo 1)" "$out"
done

ws="$(browser -b "$JAR" -u user:kasm-pw -i -o - -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H "Origin: $PUBLIC" \
  "$PUBLIC/stream/ot-u-1-desk/websockify" | head -1 | tr -d '\r' || true)"
check "WebSocket upgrade passes for a signed-in browser" "$([[ "$ws" == *" 101 "* ]] && echo 0 || echo 1)" "$ws"
ws="$(browser -i -o - -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' \
  -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H "Origin: $PUBLIC" -u user:kasm-pw \
  "$PUBLIC/stream/ot-u-1-desk/websockify" | head -1 | tr -d '\r' || true)"
check "WebSocket upgrade without sign-in is refused" "$([[ "$ws" != *" 101 "* ]] && echo 0 || echo 1)" "$ws"

cookie="$(curl -s -o /dev/null -D - "$PUBLIC/oauth2/start?rd=/stream/x/" | grep -i '^set-cookie: _termiplus_desk' | tr -d '\r')"
check "session cookies are HttpOnly and SameSite=Lax" "$([[ "$cookie" == *HttpOnly* && "$cookie" == *SameSite=Lax* ]] && echo 0 || echo 1)" "$cookie"

# Management UI.
ui() { # jar method path [body] [extra curl args...] -> "<status> <body>"
  local jar="$1" method="$2" p="$3" body="${4:-}"
  shift 4 2>/dev/null || shift 3
  local data=()
  [ -n "$body" ] && data=(--data "$body")
  browser -b "$jar" -X "$method" -w ' %{http_code}' "${data[@]}" "$@" "$PUBLIC/ui/$p"
}
write() { ui "$1" "$2" "$3" "${4:-}" -H 'X-Termiplus-UI: 1' -H "Origin: ${ORIGIN:-$PUBLIC}" -H 'Content-Type: application/json'; }
endswith() { [[ "$1" == *" $2" ]] && echo 0 || echo 1; }

out="$(ui "$JAR" GET "")"
check "signed in: the UI page loads" "$([[ "$out" == *app.js* && "$out" == *" 200" ]] && echo 0 || echo 1)" "$out"
out="$(ui "$JAR" GET api/me)"
check "UI: alice is linked and has her groups" "$([[ "$out" == *'"linked":true'* && "$out" == *termiplus-persist* ]] && echo 0 || echo 1)" "$out"
out="$(write "$JAR" POST api/sessions '{"workspace":"default"}')"
check "UI: alice starts a session" "$(endswith "$out" 200)" "$out"
out="$(write "$JAR" POST api/sessions '{"workspace":"desk","persistent":true}')"
check "UI: the persist group allows a persistent session" "$(endswith "$out" 200)" "$out"
out="$(ui "$JAR" POST api/sessions '{"workspace":"default"}' -H 'Content-Type: application/json' -H "Origin: $PUBLIC")"
check "UI: a write without the UI header is refused" "$(endswith "$out" 403)" "$out"
out="$(ORIGIN=https://evil.example write "$JAR" POST api/sessions '{"workspace":"default"}')"
check "UI: a cross-site write is refused" "$(endswith "$out" 403)" "$out"
sessions="$(ui "$JAR" GET api/sessions)"
check "UI: alice lists her two sessions" "$([ "$(grep -o session_id <<<"$sessions" | wc -l)" = 2 ] && echo 0 || echo 1)" "$sessions"
out="$(browser -w ' %{http_code}' "$PUBLIC/ui/api/sessions")"
check "UI: not signed in, the API is not reachable" "$(endswith "$out" 302)" "$out"

JARB="$WORK/jarb"
if login "$JARB" bob bob-pw; then pass "bob signs in"; else fail "bob sign-in"; fi
out="$(ui "$JARB" GET api/me)"
check "UI: bob (never used the plugin) is not linked" "$([[ "$out" == *'"linked":false'* ]] && echo 0 || echo 1)" "$out"
out="$(ui "$JARB" GET api/sessions "" -H "X-Forwarded-User: $SUB_A")"
check "UI: a forged X-Forwarded-User does not make bob alice" "$(endswith "$out" 409)" "$out"

nets="$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$P-gw")"
check "the stream upstream sits only on the internal desk network" "$([ "$nets" = "$P-desk " ] && echo 0 || echo 1)" "$nets"

if [ "$FAILED" -ne 0 ]; then
  echo "desktop sign-in test FAILED"
  exit 1
fi
echo "desktop sign-in test passed"
