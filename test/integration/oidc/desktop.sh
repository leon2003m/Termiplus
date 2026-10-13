#!/usr/bin/env bash
# Browser sign-in test for desktop streams (deploy/docker-compose.desktop.yml)
# against a real IdP.
#
#   test/integration/oidc/desktop.sh
#   ECHO_IMAGE=ghcr.io/leon2003m/termiplus-terminal:it test/integration/oidc/desktop.sh
#
# Starts a throwaway Keycloak (realm.json), desk-auth exactly as the overlay
# configures it (image and flags read from the overlay; only --cookie-secure
# and --redirect-url are adapted to plain-HTTP localhost) and, in place of the
# gateway, an echo server (echo.py, run with the terminal image's Python). It
# logs in like a browser and checks that only signed-in browsers reach the
# stream path, that nothing else of the gateway is reachable, and that the
# browser's KasmVNC basic auth arrives unchanged while no token of ours does.
# Everything it creates is named tp-dskauth-it-* and removed on exit.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
HERE="$ROOT/test/integration/oidc"
P="tp-dskauth-it"
KC_IMAGE="quay.io/keycloak/keycloak:26.7.5@sha256:37dbaf6f0722c9ec246335f36e1ef8b2e6cb960f7c27e0d8c615121a3d475a85"
ECHO_IMAGE="${ECHO_IMAGE:-ghcr.io/leon2003m/termiplus-terminal:latest}"
ISSUER="http://keycloak:8080/realms/termiplus"
WORK="$(mktemp -d)"

cleanup() {
  docker rm -f "$P-kc" "$P-proxy" "$P-gw" >/dev/null 2>&1 || true
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
printf '%s\n' "${PROXY_ARGS[@]}" | grep -qx -- "--upstream=http://gateway:8080/stream/" ||
  { echo "overlay upstream changed; update this test"; exit 1; }

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

nets="$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$P-gw")"
check "the stream upstream sits only on the internal desk network" "$([ "$nets" = "$P-desk " ] && echo 0 || echo 1)" "$nets"

if [ "$FAILED" -ne 0 ]; then
  echo "desktop sign-in test FAILED"
  exit 1
fi
echo "desktop sign-in test passed"
