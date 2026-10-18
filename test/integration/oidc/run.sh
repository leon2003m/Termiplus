#!/usr/bin/env bash
# OIDC integration test for the MCP control plane against a real IdP.
#
#   test/integration/oidc/run.sh
#   BUILD=0 test/integration/oidc/run.sh    # reuse termiplus-mcp:oidc-it
#
# Starts a throwaway Keycloak (realm.json), the oauth2-proxy exactly as
# deploy/docker-compose.mcp.yml configures it (image and flags are read from
# the overlay, so they cannot drift) and the real mcp image (stub backend),
# then checks that only valid tokens get through and that forged identity
# headers are never trusted. The mcp server runs with the Open WebUI plugin key,
# as the overlay deploys it, so sessions are keyed by the Open WebUI user id. Everything it creates is named tp-oidc-it-* and
# removed on exit; it needs no gateway and touches no other containers.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
HERE="$ROOT/test/integration/oidc"
P="tp-oidc-it"
KC_IMAGE="quay.io/keycloak/keycloak:26.7.5@sha256:37dbaf6f0722c9ec246335f36e1ef8b2e6cb960f7c27e0d8c615121a3d475a85"
MCP_IMAGE="termiplus-mcp:oidc-it"
ISSUER="http://keycloak:8080/realms/termiplus"
WORK="$(mktemp -d)"

cleanup() {
  docker rm -f "$P-kc" "$P-mcp" "$P-proxy" >/dev/null 2>&1 || true
  docker network rm "$P-net" "$P-auth" "$P-dir" >/dev/null 2>&1 || true
}
trap 'cleanup; rm -rf "$WORK"' EXIT
cleanup

FAILED=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; FAILED=1; }

if [ "${BUILD:-1}" = 1 ]; then
  docker build -q --target mcp -t "$MCP_IMAGE" "$ROOT" >/dev/null
fi

# The proxy image and command come from the deployment overlay itself.
cfg="$(cd "$ROOT/deploy" && MCP_OIDC_ISSUER_URL="$ISSUER" TERMINAL_IMAGE=unused \
  docker compose -p "$P" -f docker-compose.yml -f docker-compose.mcp.yml config --format json)"
PROXY_IMAGE="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["services"]["mcp-auth"]["image"])' <<<"$cfg")"
mapfile -t PROXY_ARGS < <(python3 -c 'import json,sys; print("\n".join(json.load(sys.stdin)["services"]["mcp-auth"]["command"]))' <<<"$cfg")

printf 'mcp-secret-it' >"$WORK/client_secret"
openssl rand -hex 16 | tr -d '\n' >"$WORK/cookie_secret"
OWUI_KEY="$(openssl rand -hex 32)"
printf '%s' "$OWUI_KEY" >"$WORK/owui_key"
chmod 644 "$WORK/client_secret" "$WORK/cookie_secret" "$WORK/owui_key"

# $P-net reaches Keycloak; $P-auth is internal and shared only by proxy and
# mcp, as ot-mcp-auth is in the overlay.
docker network create "$P-net" >/dev/null
docker network create --internal "$P-auth" >/dev/null
# $P-dir is internal and shared only by mcp and Keycloak, for the admin API
# (manage_user / manage_group), as ot-mcp-kc in docker-compose.mcp-users.yml.
docker network create --internal "$P-dir" >/dev/null
printf 'mcp-admin-secret-it-0123456789abcdef' >"$WORK/kc_admin_secret"
chmod 644 "$WORK/kc_admin_secret"

docker run -d --name "$P-kc" --network "$P-net" --network-alias keycloak -p 127.0.0.1::8080 \
  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin-it \
  -e KC_HOSTNAME=http://keycloak:8080 \
  -v "$HERE/realm.json:/opt/keycloak/data/import/realm.json:ro" \
  "$KC_IMAGE" start-dev --import-realm >/dev/null
KC="http://$(docker port "$P-kc" 8080/tcp | head -1)"
docker network connect --alias keycloak "$P-dir" "$P-kc"

docker run -d --name "$P-mcp" --network "$P-auth" --network-alias mcp \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  -v "$WORK/owui_key:/run/secrets/mcp_owui_plugin_key:ro" \
  -e MCP_OWUI_PLUGIN_KEY_FILE=/run/secrets/mcp_owui_plugin_key \
  -v "$WORK/kc_admin_secret:/run/secrets/mcp_kc_admin_client_secret:ro" \
  -e MCP_KC_ADMIN_URL=http://keycloak:8080 \
  -e MCP_KC_CLIENT_SECRET_FILE=/run/secrets/mcp_kc_admin_client_secret \
  -e MCP_AUTH=proxy -e MCP_STUB_BACKEND=1 "$MCP_IMAGE" >/dev/null
docker network connect "$P-dir" "$P-mcp"

for _ in $(seq 1 90); do
  curl -sf -m 2 "$KC/realms/termiplus/.well-known/openid-configuration" >/dev/null && break
  sleep 2
done

docker create --name "$P-proxy" --network "$P-net" -p 127.0.0.1::4180 \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  -v "$WORK/client_secret:/run/secrets/mcp_oidc_client_secret:ro" \
  -v "$WORK/cookie_secret:/run/secrets/mcp_oidc_cookie_secret:ro" \
  "$PROXY_IMAGE" "${PROXY_ARGS[@]}" >/dev/null
docker network connect "$P-auth" "$P-proxy"
docker start "$P-proxy" >/dev/null
PROXY="http://$(docker port "$P-proxy" 4180/tcp | head -1)"
for _ in $(seq 1 30); do
  curl -sf -m 2 "$PROXY/ping" >/dev/null && break
  sleep 1
done

token() { # client secret user password
  curl -s "$KC/realms/termiplus/protocol/openid-connect/token" -d grant_type=password \
    -d client_id="$1" -d client_secret="$2" -d username="$3" -d password="$4" |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])'
}

# rpc TOKEN TOOL ARGS_JSON [extra curl args...] -> "<http status> <rpc error code or ok> <result text>"
# Sends the plugin key and AS (the Open WebUI user id) like the plugin does,
# unless AS is empty.
AS=""
rpc() {
  local tok="$1" tool="$2" args="$3"
  shift 3
  local auth=() owui=()
  [ -n "$tok" ] && auth=(-H "Authorization: Bearer $tok")
  [ -n "$AS" ] && owui=(-H "X-Termiplus-Owui-Key: $OWUI_KEY" -H "X-Termiplus-Owui-User: $AS")
  curl -s -o "$WORK/body" -w '%{http_code}' "${auth[@]}" "${owui[@]}" "$@" -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args}}" \
    "$PROXY/" >"$WORK/code"
  python3 - "$(cat "$WORK/code")" "$WORK/body" <<'EOF'
import json, sys
code, body = sys.argv[1], open(sys.argv[2]).read()
try:
    d = json.loads(body)
except ValueError:
    print(code, "-", ""); sys.exit()
if isinstance(d, dict) and d.get("error"):
    print(code, d["error"]["code"], "")
elif isinstance(d, dict) and "result" in d:
    print(code, "ok", d["result"]["content"][0]["text"])
else:
    print(code, "-", "")
EOF
}

expect() { # name want got
  if [ "${3#"$2"}" != "$3" ]; then pass "$1"; else fail "$1: want '$2…', got '$3'"; fi
}

A="$(token owui owui-secret-it alice alice-pw)"   # group termiplus-persist
B="$(token owui owui-secret-it bob bob-pw)"       # no groups
O="$(token other other-secret-it alice alice-pw)" # no aud=termiplus-mcp
SUB_A="$(cut -d. -f2 <<<"$A" | python3 -c 'import base64,json,sys; s=sys.stdin.read().strip(); print(json.loads(base64.urlsafe_b64decode(s+"="*(-len(s)%4)))["sub"])')"

AS="owui-alice"
expect "no token is rejected by the proxy" "401" "$(rpc "" list_sessions '{}')"
expect "forged identity without a token is rejected" "401" \
  "$(rpc "" list_sessions '{}' -H "X-Forwarded-User: $SUB_A" -H 'X-Forwarded-Groups: termiplus-admin')"
expect "token for another audience is rejected" "401" "$(rpc "$O" list_sessions '{}')"
expect "tampered signature is rejected" "401" "$(rpc "${A%.*}.AAAA" list_sessions '{}')"
NONE="$(python3 -c 'import base64,sys; print(base64.urlsafe_b64encode(b"{\"alg\":\"none\",\"typ\":\"JWT\"}").decode().rstrip("=")+"."+sys.argv[1].split(".")[1]+".")' "$A")"
expect "alg=none is rejected" "401" "$(rpc "$NONE" list_sessions '{}')"

expect "valid token reaches the server" "200 ok" "$(rpc "$A" list_workspaces '{}')"
expect "group termiplus-persist grants a confirmed persistent session" "200 ok" \
  "$(rpc "$A" request_session '{"workspace":"desk","persistent":true,"_confirmed":true}')"
expect "user without the group is forbidden" "200 -32003" \
  "$(rpc "$B" request_session '{"workspace":"desk","persistent":true,"_confirmed":true}')"
expect "forged X-Forwarded-Groups does not grant the role" "200 -32003" \
  "$(rpc "$B" request_session '{"workspace":"desk","persistent":true,"_confirmed":true}' -H 'X-Forwarded-Groups: termiplus-persist')"
expect "alice sees her session" "200 ok [{" "$(rpc "$A" list_sessions '{}')"
AS="owui-bob"
expect "forged X-Forwarded-User does not impersonate" "200 ok []" \
  "$(rpc "$B" list_sessions '{}' -H "X-Forwarded-User: $SUB_A")"

# Open WebUI user keying: the plugin key and user id pass through oauth2-proxy.
AS=""
expect "valid token without the plugin key is unauthenticated" "200 -32001" "$(rpc "$A" list_sessions '{}')"
expect "wrong plugin key is unauthenticated" "200 -32001" \
  "$(rpc "$A" list_sessions '{}' -H 'X-Termiplus-Owui-Key: wrong' -H 'X-Termiplus-Owui-User: owui-alice')"
expect "plugin key without a token is rejected by the proxy" "401" \
  "$(rpc "" list_sessions '{}' -H "X-Termiplus-Owui-Key: $OWUI_KEY" -H 'X-Termiplus-Owui-User: owui-alice')"
AS="owui-alice-2"
expect "sessions are keyed by the Open WebUI user, not the token subject" "200 ok []" "$(rpc "$A" list_sessions '{}')"
AS="owui-alice"

# User management through Keycloak's admin API (service account of
# termiplus-mcp-admin). carol is the only member of termiplus-admin.
C="$(token owui owui-secret-it carol carol-pw)"
AS="owui-carol"
expect "admin lists the termiplus groups" '200 ok {"groups":["termiplus-admin","termiplus-persist"]}' \
  "$(rpc "$C" manage_group '{"action":"list"}')"
expect "a group outside the prefix is refused" '200 ok group "ops-admins" is not managed' \
  "$(rpc "$C" manage_user '{"action":"add_to_group","username":"bob","group":"ops-admins","_confirmed":true}')"
expect "membership changes need confirmation" "200 -32010" \
  "$(rpc "$C" manage_user '{"action":"add_to_group","username":"bob","group":"termiplus-persist"}')"
AS="owui-bob"
expect "a non-admin cannot manage users" "200 -32003" "$(rpc "$B" manage_user '{"action":"show","username":"bob"}')"
AS="owui-carol"
expect "admin adds bob to termiplus-persist" '200 ok {"changed":true' \
  "$(rpc "$C" manage_user '{"action":"add_to_group","username":"bob","group":"termiplus-persist","_confirmed":true}')"
AS="owui-bob"
expect "bob's next token carries the new role" "200 ok" \
  "$(rpc "$(token owui owui-secret-it bob bob-pw)" request_session '{"workspace":"desk","persistent":true,"_confirmed":true}')"
AS="owui-carol"
expect "the last admin cannot be removed" '200 ok refusing to remove the last enabled member' \
  "$(rpc "$C" manage_user '{"action":"remove_from_group","username":"carol","group":"termiplus-admin","_confirmed":true}')"
expect "admin removes bob again" '200 ok {"changed":true' \
  "$(rpc "$C" manage_user '{"action":"remove_from_group","username":"bob","group":"termiplus-persist","_confirmed":true}')"
AS="owui-bob"
expect "bob's next token no longer carries it" "200 -32003" \
  "$(rpc "$(token owui owui-secret-it bob bob-pw)" request_session '{"workspace":"desk","persistent":true,"_confirmed":true}')"
AS="owui-alice"

# Expiry: shorten the realm's token lifespan, then outwait a fresh token.
kc() { docker exec "$P-kc" /opt/keycloak/bin/kcadm.sh "$@"; }
kc config credentials --server http://localhost:8080 --realm master --user admin --password admin-it >/dev/null 2>&1
kc update realms/termiplus -s accessTokenLifespan=5
SHORT="$(token owui owui-secret-it alice alice-pw)"
AS="owui-alice"
expect "short-lived token works while valid" "200 ok" "$(rpc "$SHORT" list_workspaces '{}')"
sleep 8
expect "expired token is rejected" "401" "$(rpc "$SHORT" list_workspaces '{}')"

nets="$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$P-mcp")"
if [ "$nets" = "$P-auth $P-dir " ]; then pass "mcp is attached only to the internal auth and directory networks"; else fail "mcp networks: $nets"; fi

if [ "$FAILED" -ne 0 ]; then
  echo "OIDC integration test FAILED"
  exit 1
fi
echo "OIDC integration test passed"
