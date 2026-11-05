#!/usr/bin/env bash
# Browser sign-in test for desktop streams (deploy/docker-compose.desktop.yml)
# against a real IdP.
#
#   test/integration/oidc/desktop.sh
#   BUILD=0 ECHO_IMAGE=ghcr.io/leon2003m/termiplus-terminal:it test/integration/oidc/desktop.sh
#
# Starts isolated Keycloak, the real console/share oauth2-proxies and MCP,
# plus a gateway image fixture. Tests browser sign-in, CSRF, identity binding,
# origin separation and read-only expiring screen links. Native interactive
# Desktop/Browser protocols are covered by the separate image smoke test.
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
  docker rm -f "$P-kc" "$P-proxy" "$P-sproxy" "$P-gw" "$P-mcp" >/dev/null 2>&1 || true
  docker network rm "$P-net" "$P-desk" "$P-gwnet" >/dev/null 2>&1 || true
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

freeport() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])'; }
PORT="$(freeport)"
SPORT="$(freeport)"
PUBLIC="http://127.0.0.1:$PORT"   # console (desk-auth)
SPUBLIC="http://127.0.0.1:$SPORT" # desktops (desk-stream-auth), another origin

cfg="$(cd "$ROOT/deploy" && MCP_OIDC_ISSUER_URL="$ISSUER" DESKTOP_PUBLIC_URL="$PUBLIC" DESKTOP_STREAM_URL="$SPUBLIC" \
  TERMINAL_IMAGE=unused DOCKER_GID=0 \
  docker compose -p "$P" -f docker-compose.yml -f docker-compose.mcp.yml -f docker-compose.desktop.yml \
  config --format json)"
PROXY_IMAGE="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["services"]["desk-auth"]["image"])' <<<"$cfg")"
proxy_args() { # service -> its command, with the cookie made non-Secure for plain HTTP
  python3 -c '
import json, sys
for a in json.load(sys.stdin)["services"][sys.argv[1]]["command"]:
    print("--cookie-secure=false" if a == "--cookie-secure=true" else a)
' "$1" <<<"$cfg"
}
mapfile -t PROXY_ARGS < <(proxy_args desk-auth)
mapfile -t SPROXY_ARGS < <(proxy_args desk-stream-auth)
ups() { printf '%s\n' "$@" | grep -- '^--upstream=' | sort | tr '\n' ' '; }
[ "$(ups "${PROXY_ARGS[@]}")" = "--upstream=http://mcp:8091/ui/ " ] || { echo "desk-auth upstreams changed; update this test"; exit 1; }
[ "$(ups "${SPROXY_ARGS[@]}")" = "--upstream=http://mcp:8093/share/ " ] ||
  { echo "desk-stream-auth upstreams changed; update this test"; exit 1; }

printf 'desk-secret-it' >"$WORK/client_secret"
openssl rand -hex 16 | tr -d '\n' >"$WORK/cookie_secret"
openssl rand -hex 16 | tr -d '\n' >"$WORK/scookie_secret"
chmod 644 "$WORK/client_secret" "$WORK/cookie_secret" "$WORK/scookie_secret"

docker network create "$P-net" >/dev/null
docker network create --internal "$P-desk" >/dev/null
docker network create --internal "$P-gwnet" >/dev/null

docker run -d --name "$P-kc" --network "$P-net" --network-alias keycloak -p 127.0.0.1::8080 \
  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin-it \
  -e KC_HOSTNAME=http://keycloak:8080 \
  -v "$HERE/realm.json:/opt/keycloak/data/import/realm.json:ro" \
  "$KC_IMAGE" start-dev --import-realm >/dev/null
KCPORT="$(docker port "$P-kc" 8080/tcp | head -1 | cut -d: -f2)"

docker run -d --name "$P-gw" --network "$P-gwnet" --network-alias gateway \
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
printf '{"default":"allow","workspaces":{}}\n' >"$WORK/data/entitlements.json"
chmod 777 "$WORK/data"
chmod 644 "$WORK/data/identities.json" "$WORK/data/entitlements.json"
docker run -d --name "$P-mcp" --network "$P-desk" --network-alias mcp \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true -v "$WORK/data:/data" \
  -e MCP_AUTH=proxy -e MCP_STUB_BACKEND=1 -e MCP_IDENTITY_FILE=/data/identities.json -e UI_LISTEN_ADDR=:8091 \
  -e MCP_ENTITLEMENTS_FILE=/data/entitlements.json -e MCP_OWUI_PLUGIN_KEY=plugin-key-it-0123456789abcdef0123456789 \
  -e STREAM_LISTEN_ADDR=:8093 -e CONSOLE_PUBLIC_URL="$PUBLIC" -e STREAM_PUBLIC_URL="$SPUBLIC" \
  -e CONTROL_API_KEY=control-key-it-0123456789abcdef0123456789 \
  "$MCP_IMAGE" >/dev/null
docker network connect "$P-gwnet" "$P-mcp"

docker create --name "$P-proxy" --network "$P-net" -p "127.0.0.1:$PORT:4180" \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  -v "$WORK/client_secret:/run/secrets/desk_oidc_client_secret:ro" \
  -v "$WORK/cookie_secret:/run/secrets/desk_oidc_cookie_secret:ro" \
  "$PROXY_IMAGE" "${PROXY_ARGS[@]}" >/dev/null
docker network connect "$P-desk" "$P-proxy"
docker start "$P-proxy" >/dev/null
docker create --name "$P-sproxy" --network "$P-net" -p "127.0.0.1:$SPORT:4180" \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  -v "$WORK/client_secret:/run/secrets/desk_oidc_client_secret:ro" \
  -v "$WORK/scookie_secret:/run/secrets/dstream_oidc_cookie_secret:ro" \
  "$PROXY_IMAGE" "${SPROXY_ARGS[@]}" >/dev/null
docker network connect "$P-desk" "$P-sproxy"
docker start "$P-sproxy" >/dev/null
for base in "$PUBLIC" "$SPUBLIC"; do
  for _ in $(seq 1 30); do
    curl -sf -m 2 "$base/ping" >/dev/null && break
    sleep 1
  done
done

# The "browser": Keycloak's public name resolves to the published port.
browser() { curl -s -m 10 --connect-to "keycloak:8080:127.0.0.1:$KCPORT" "$@"; }

login() { # jar user password [url cookie] -> 0 when the session cookie was obtained
  local jar="$1" url="${4:-$SPUBLIC/share/invalid/}" cookie="${5:-_termiplus_dstream}" form action
  form="$(browser -L -c "$jar" -b "$jar" "$url")"
  action="$(python3 -c '
import html, re, sys
m = re.search(r"<form[^>]*id=\"kc-form-login\"[^>]*action=\"([^\"]+)\"", sys.stdin.read())
print(html.unescape(m.group(1)) if m else "")' <<<"$form")"
  [ -n "$action" ] || return 1
  browser -L -c "$jar" -b "$jar" -o /dev/null --data-urlencode "username=$2" \
    --data-urlencode "password=$3" "$action"
  grep -q "$cookie" "$jar"
}

# Not signed in: no request reaches the gateway.
code="$(browser -o /dev/null -w '%{http_code}' "$SPUBLIC/share/invalid/")"
loc="$(browser -o /dev/null -w '%{redirect_url}' "$SPUBLIC/share/invalid/")"
check "not signed in: redirected to the IdP, not proxied" "$([[ "$code" = 302 && "$loc" == http://keycloak:8080/realms/termiplus/* ]] && echo 0 || echo 1)" "$code $loc"
code="$(browser -o /dev/null -w '%{http_code}' -u user:pw "$SPUBLIC/share/invalid/")"
check "basic auth alone does not pass the proxy" "$([ "$code" = 302 ] && echo 0 || echo 1)" "$code"
TOKEN="$(browser -d grant_type=password -d client_id=termiplus-desktop -d client_secret=desk-secret-it \
  -d username=alice -d password=alice-pw "http://keycloak:8080/realms/termiplus/protocol/openid-connect/token" |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')"
code="$(browser -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$SPUBLIC/share/invalid/")"
check "a bearer token does not replace the browser sign-in" "$([ "$code" = 302 ] && echo 0 || echo 1)" "$code"

JAR="$WORK/jar"
if login "$JAR" alice alice-pw; then pass "browser sign-in via Keycloak sets the desktops' session cookie"; else fail "browser sign-in (desktops)"; fi
JARC="$WORK/jarc"
if login "$JARC" alice alice-pw "$PUBLIC/ui/" _termiplus_desk; then pass "browser sign-in via Keycloak sets the console's session cookie"; else fail "browser sign-in (console)"; fi
# Each origin serves only its own part.
for path in /stream/ot-u-1-desk/ /share/x/; do
  out="$(browser -b "$JARC" -w ' %{http_code}' "$PUBLIC$path")"
  check "console origin: $path is not proxied" "$([[ "$out" != *'"path"'* && "${out##* }" = 404 ]] && echo 0 || echo 1)" "$out"
done
out="$(browser -b "$JAR" -w ' %{http_code}' "$SPUBLIC/share/invalid/sw.js" -H 'Service-Worker: script')"
check "share origin: service workers are refused" "$([[ "$out" == *" 403" ]] && echo 0 || echo 1)" "$out"
for path in /stream/ot-u-1-desk/ /control/v1/workspaces /p/default/openapi.json /admin/v1/policy /healthz / /ui/ /ui/api/sessions; do
  out="$(browser -b "$JAR" -w ' %{http_code}' "$SPUBLIC$path")"
  check "share origin: $path is not proxied" "$([[ "$out" != *'\"path\"'* && "$out" == *" 404" ]] && echo 0 || echo 1)" "$out"
done

for c in "$PUBLIC/oauth2/start?rd=/ui/ _termiplus_desk" "$SPUBLIC/oauth2/start?rd=/share/invalid/ _termiplus_dstream"; do
  cookie="$(curl -s -o /dev/null -D - "${c% *}" | grep -i "^set-cookie: ${c#* }" | tr -d '\r')"
  check "${c#* } cookies are HttpOnly and SameSite=Lax" "$([[ "$cookie" == *HttpOnly* && "$cookie" == *SameSite=Lax* ]] && echo 0 || echo 1)" "$cookie"
done

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

out="$(ui "$JARC" GET "")"
check "signed in: the UI page loads" "$([[ "$out" == *app.js* && "$out" == *" 200" ]] && echo 0 || echo 1)" "$out"
out="$(ui "$JARC" GET api/me)"
check "UI: alice is linked and has her groups" "$([[ "$out" == *'"linked":true'* && "$out" == *termiplus-persist* ]] && echo 0 || echo 1)" "$out"
out="$(write "$JARC" POST api/sessions '{"workspace":"default"}')"
check "UI: alice starts a session" "$(endswith "$out" 200)" "$out"
out="$(write "$JARC" POST api/sessions '{"workspace":"desk","persistent":true}')"
check "UI: the persist group allows a persistent session" "$(endswith "$out" 200)" "$out"
out="$(ui "$JARC" POST api/sessions '{"workspace":"default"}' -H 'Content-Type: application/json' -H "Origin: $PUBLIC")"
check "UI: a write without the UI header is refused" "$(endswith "$out" 403)" "$out"
out="$(ORIGIN=https://evil.example write "$JARC" POST api/sessions '{"workspace":"default"}')"
check "UI: a cross-site write is refused" "$(endswith "$out" 403)" "$out"
sessions="$(ui "$JARC" GET api/sessions)"
check "UI: alice lists her two sessions" "$([ "$(grep -o session_id <<<"$sessions" | wc -l)" = 2 ] && echo 0 || echo 1)" "$sessions"
out="$(ui "$JARC" GET api/admin/entitlements)"
check "UI: admin pages need the admin group" "$(endswith "$out" 403)" "$out"
out="$(browser -w ' %{http_code}' "$PUBLIC/ui/api/sessions")"
check "UI: not signed in, the API is not reachable" "$(endswith "$out" 302)" "$out"

JARB="$WORK/jarb"
if login "$JARB" bob bob-pw "$PUBLIC/ui/" _termiplus_desk; then pass "bob signs in"; else fail "bob sign-in"; fi
JARBS="$WORK/jarbs"
login "$JARBS" bob bob-pw || fail "bob sign-in (desktops)"
out="$(ui "$JARB" GET api/me)"
check "UI: bob (never used the plugin) is not linked" "$([[ "$out" == *'"linked":false'* ]] && echo 0 || echo 1)" "$out"
out="$(ui "$JARB" GET api/sessions "" -H "X-Forwarded-User: $SUB_A")"
check "UI: a forged X-Forwarded-User does not make bob alice" "$(endswith "$out" 409)" "$out"
# Owner creates a named share through the human console. The token does not
# grant input or override its recipient identity.
sid="$(python3 -c 'import json,sys; print(next(x["session_id"] for x in json.loads(sys.argv[1].rsplit(" ",1)[0]) if x.get("desktop")))' "$sessions")"
shared="$(write "$JARC" POST api/shares "{\"session_id\":\"$sid\",\"minutes\":10}")"
share_url="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1].rsplit(" ",1)[0])["url"])' "$shared")"
share_id="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1].rsplit(" ",1)[0])["share_id"])' "$shared")"
out="$(browser -b "$JAR" -w ' %{http_code}' "$share_url")"
check "signed-in recipient sees the image-only viewer" "$([[ "$out" == *"view only"* && "$out" == *" 200" ]] && echo 0 || echo 1)" "$out"
code="$(browser -b "$JAR" -o /dev/null -w '%{http_code}' "${share_url}screen.png")"
check "native screenshot is relayed as PNG" "$([ "$code" = 200 ] && echo 0 || echo 1)" "$code"
for suffix in clipboard desktop/action websockify; do
  code="$(browser -b "$JAR" -o /dev/null -w '%{http_code}' "${share_url}$suffix")"
  check "share cannot access $suffix" "$([ "$code" = 404 ] && echo 0 || echo 1)" "$code"
done
code="$(browser -b "$JAR" -X POST -o /dev/null -w '%{http_code}' "${share_url}screen.png")"
check "share cannot send input" "$([ "$code" = 403 ] && echo 0 || echo 1)" "$code"
ws="$(browser -b "$JAR" -i -o - -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' "$share_url" | head -1 | tr -d '\r' || true)"
check "share cannot open a WebSocket" "$([[ "$ws" == *" 403 "* ]] && echo 0 || echo 1)" "$ws"
write "$JARC" DELETE "api/shares/$share_id" >/dev/null
code="$(browser -b "$JAR" -o /dev/null -w '%{http_code}' "${share_url}screen.png")"
check "revocation prevents subsequent images" "$([ "$code" = 404 ] && echo 0 || echo 1)" "$code"

nets="$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$P-gw")"
check "the gateway sits only on the internal network shared with the MCP server" "$([ "$nets" = "$P-gwnet " ] && echo 0 || echo 1)" "$nets"

if [ "$FAILED" -ne 0 ]; then
  echo "desktop sign-in test FAILED"
  exit 1
fi
echo "desktop sign-in test passed"
