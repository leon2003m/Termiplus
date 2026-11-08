#!/usr/bin/env bash
# Persistent preview of the whole termiplus stack (gateway, sockproxy, mcp,
# oauth2-proxies, Keycloak, a Caddy TLS edge, native desktop) inside the
# ISOLATED nested Docker daemon, plus a "live" panel harness that serves the
# real open_workspaces panel against the real backend as test user alice.
#
#   test/mcp-app/preview-up.sh            # build (cached), bring up, seed
#   BUILD=0 test/mcp-app/preview-up.sh    # reuse images tagged :preview
#   test/mcp-app/preview-down.sh          # tear it down again
#
# Every docker command targets the nested daemon (DOCKER_HOST below). Never
# run this against the host daemon: it is not the production stack's daemon.
# deploy/firewall.sh is deliberately NOT applied (it edits host-global
# nftables); the preview runs on plain bridges with runc, NOT isolated like a
# real deployment. The preview needs no cleanup trap and leaves everything up.
set -euo pipefail

export DOCKER_HOST="${PREVIEW_DOCKER_HOST:-tcp://127.0.0.1:2376}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$here/../.." && pwd)"
WORK="$here/.preview"
PROJECT=termiplus-preview
REG=ghcr.io/leon2003m
TAG=preview
CADDY_IMAGE="caddy:2.10.2-alpine@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d"
NODE_IMAGE="node:22-alpine"
EDGE_PORT=8443
SSO="https://sso.test:$EDGE_PORT"
DESK="https://desk.test:$EDGE_PORT"
STREAM="https://stream.test:$EDGE_PORT"
CHAT="https://chat.test:$EDGE_PORT"
PANEL_HOST="panel.test"
LAN_IP="${PREVIEW_LAN_IP:-192.168.178.190}"

log() { printf '\n==> %s\n' "$*"; }
die() { echo "preview-up: $*" >&2; exit 1; }

log "checking the nested daemon at $DOCKER_HOST"
runtimes="$(docker info --format '{{json .Runtimes}}' 2>/dev/null)" || die "nested Docker daemon not reachable at $DOCKER_HOST"
case "$runtimes" in *'"runc"'*) ;; *) die "nested daemon has no runc runtime: $runtimes" ;; esac
case "$runtimes" in *runsc* | *sysbox*) die "this looks like the host daemon ($runtimes); refusing" ;; esac
docker info --format 'daemon {{.Name}}'
mkdir -p "$WORK"
chmod 0700 "$WORK"

compose() {
  local f
  for f in "$WORK"/deploy/secrets/*; do
    case "$(basename "$f")" in realm-*.json | keycloak.env) continue ;; esac
    export "SECRET_$(basename "$f")=$(cat "$f")"
  done
  docker compose -p "$PROJECT" --env-file "$WORK/deploy/.env" \
    -f "$WORK/deploy/docker-compose.yml" -f "$WORK/deploy/docker-compose.mcp.yml" \
    -f "$WORK/deploy/docker-compose.desktop.yml" -f "$WORK/deploy/docker-compose.keycloak.yml" \
    -f "$WORK/deploy/preview.override.yml" "$@"
}

# put_volume VOLUME UID:GID FILE... : (re)fill a named volume on the nested
# daemon. Bind mounts of local files cannot work against a remote daemon.
put_volume() {
  local vol="$1" owner="$2"
  shift 2
  docker volume create "$vol" >/dev/null
  docker rm -f tp-preview-fill >/dev/null 2>&1 || true
  docker create --name tp-preview-fill -v "$vol:/d" alpine:3.20 true >/dev/null
  local f
  for f in "$@"; do docker cp "$f" tp-preview-fill:/d/"$(basename "$f")"; done
  docker rm tp-preview-fill >/dev/null
  docker run --rm -v "$vol:/d" alpine:3.20 sh -c "chown -R $owner /d && chmod -R a+rX /d"
}

if [ "${BUILD:-1}" = 1 ]; then
  log "build images into the nested daemon"
  for t in gateway sockproxy mcp; do
    docker build -q --target "$t" -t "$REG/termiplus-$t:$TAG" "$ROOT" >/dev/null
  done
  docker build -q -t "$REG/termiplus-terminal:$TAG" "$ROOT/image" >/dev/null
  docker build -q --build-arg BASE="$REG/termiplus-terminal:$TAG" -t "$REG/termiplus-desktop:$TAG" "$ROOT/image/desktop" >/dev/null
fi

log "prepare deployment in $WORK/deploy (existing secrets are kept)"
mkdir -p "$WORK/deploy"
cp -r "$ROOT"/deploy/. "$WORK/deploy/"
rm -f "$WORK/deploy/.env"
mkdir -p "$WORK/deploy/secrets"
for s in proxy_token gateway_api_key key_secret; do
  [ -s "$WORK/deploy/secrets/$s" ] || openssl rand -hex 32 | tr -d '\n' >"$WORK/deploy/secrets/$s"
done
chmod 0644 "$WORK"/deploy/secrets/*
WEBUI_URL="$CHAT" KEYCLOAK_PUBLIC_URL="$SSO" DESKTOP_PUBLIC_URL="$DESK" DESKTOP_STREAM_URL="$STREAM" \
  "$WORK/deploy/setup-alpha.sh" >/dev/null
DOCKER_GID="$(docker run --rm -v /var/run/docker.sock:/s alpine:3.20 stat -c %g /s)"
cat >>"$WORK/deploy/.env" <<ENV
TERMINAL_IMAGE=$REG/termiplus-desktop:$TAG
TERMIPLUS_VERSION=$TAG
DOCKER_GID=$DOCKER_GID
ENV

cat >"$WORK/deploy/policies.json" <<'JSON'
{
  "default": { "label": "Persistent terminal", "description": "Terminal with a home that is kept",
               "runtime": "runc", "memory_mb": 1024, "cpus": 1, "pids": 512 },
  "desk":    { "label": "Persistent desktop", "description": "Desktop with a home that is kept",
               "runtime": "runc", "memory_mb": 2048, "cpus": 2, "pids": 1024,
               "home": "default", "desktop": true },
  "scratch": { "label": "Temporary terminal", "description": "Throwaway terminal, nothing is kept",
               "runtime": "runc", "memory_mb": 1024, "cpus": 1, "pids": 512,
               "storage": "ephemeral", "idle_timeout": "30m", "max_per_user": 2 }
}
JSON
printf '{"default":"allow","workspaces":{}}\n' >"$WORK/deploy/mcp-data/entitlements.json"

# The nested daemon cannot see local files: policies, realm, data directories
# and the edge CA live in named volumes; secrets are passed as environment
# secrets (compose copies them into the containers).
put_volume termiplus-preview-policies 0:0 "$WORK/deploy/policies.json"
put_volume termiplus-preview-kc-import 0:0 "$WORK/deploy/secrets/realm-termiplus.json"
put_volume termiplus-preview-mcp-data 65534:65534 "$WORK/deploy/mcp-data/entitlements.json"
docker volume create termiplus-preview-backups >/dev/null
docker run --rm -v termiplus-preview-backups:/d alpine:3.20 sh -c 'chown 65534:65534 /d && chmod 700 /d'
docker volume create termiplus-preview-edge-data >/dev/null

secret_decl() { printf '  %s: !override { environment: SECRET_%s }\n' "$1" "$1"; }
{
  cat <<'YAML'
# read_only is off for the services that get environment secrets: compose
# cannot copy a secret into a read-only container. Preview only.
services:
  sockproxy:
    read_only: false
    environment:
      ALLOWED_RUNTIMES: runc
      ALLOW_RUNC_INSECURE: "1"
  gateway:
    read_only: false
    volumes: !override
      - policies:/etc/termiplus:ro
      - backups:/data/backups
  mcp:
    read_only: false
    volumes: !override [ "mcp-data:/data" ]
  keycloak:
    volumes: !override [ "kc-import:/opt/keycloak/data/import:ro" ]
  mcp-auth:
    read_only: false
    # Session and reset starts take longer than oauth2-proxy's 30 s default
    # under nested runc.
    environment: { SSL_CERT_FILE: /etc/edge/root.crt, OAUTH2_PROXY_UPSTREAM_TIMEOUT: 180s }
    volumes: [ "edge-ca:/etc/edge:ro" ]
  desk-auth:
    read_only: false
    environment: { SSL_CERT_FILE: /etc/edge/root.crt }
    volumes: [ "edge-ca:/etc/edge:ro" ]
  desk-stream-auth:
    read_only: false
    environment: { SSL_CERT_FILE: /etc/edge/root.crt }
    volumes: [ "edge-ca:/etc/edge:ro" ]
networks:
  # No firewall.sh here, which is what re-permits gateway->terminal traffic on
  # the otherwise ICC-disabled sandbox bridge. Preview only.
  ot-sandbox:
    driver_opts: !override
      com.docker.network.bridge.name: ot-sandbox
      com.docker.network.bridge.enable_icc: "true"
volumes:
  policies: { external: true, name: termiplus-preview-policies }
  backups: { external: true, name: termiplus-preview-backups }
  mcp-data: { external: true, name: termiplus-preview-mcp-data }
  kc-import: { external: true, name: termiplus-preview-kc-import }
  edge-ca: { external: true, name: termiplus-preview-edge-ca }
secrets:
YAML
  for s in proxy_token gateway_api_key key_secret control_api_key mcp_oidc_client_secret mcp_oidc_cookie_secret \
    mcp_owui_plugin_key desk_oidc_client_secret desk_oidc_cookie_secret dstream_oidc_cookie_secret keycloak_db_password; do
    secret_decl "$s"
  done
} >"$WORK/deploy/preview.override.yml"

docker network inspect owui >/dev/null 2>&1 || docker network create owui >/dev/null
docker volume create termiplus-preview-edge-ca >/dev/null

log "start Keycloak"
# The edge CA volume must exist with a placeholder before Keycloak (unused
# there) so every service can mount it; the real CA is copied in below.
compose up -d --no-build --pull missing --wait --wait-timeout 300 keycloak keycloak-db

log "start the TLS edge (Caddy, internal CA) on port $EDGE_PORT"
cat >"$WORK/Caddyfile" <<CADDY
{
  skip_install_trust
}
sso.test:$EDGE_PORT {
  tls internal
  reverse_proxy keycloak:8080
}
desk.test:$EDGE_PORT {
  tls internal
  reverse_proxy desk-auth:4180
}
stream.test:$EDGE_PORT {
  tls internal
  reverse_proxy desk-stream-auth:4180
}
$PANEL_HOST:$EDGE_PORT {
  tls internal
  reverse_proxy tp-preview-harness:8080
}
CADDY
docker rm -f tp-preview-edge >/dev/null 2>&1 || true
docker create --name tp-preview-edge --restart unless-stopped --network owui \
  --network-alias sso.test --network-alias desk.test --network-alias stream.test --network-alias "$PANEL_HOST" \
  -p "0.0.0.0:$EDGE_PORT:$EDGE_PORT" -v termiplus-preview-edge-data:/data "$CADDY_IMAGE" >/dev/null
docker cp "$WORK/Caddyfile" tp-preview-edge:/etc/caddy/Caddyfile
docker start tp-preview-edge >/dev/null
for _ in $(seq 1 60); do
  docker cp tp-preview-edge:/data/caddy/pki/authorities/local/root.crt "$here/preview-ca.crt" 2>/dev/null && break
  sleep 1
done
[ -s "$here/preview-ca.crt" ] || die "edge CA was not generated"
chmod 0644 "$here/preview-ca.crt"
put_volume termiplus-preview-edge-ca 0:0 "$here/preview-ca.crt"
# oauth2-proxy reads root.crt from the volume.
docker run --rm -v termiplus-preview-edge-ca:/d alpine:3.20 sh -c 'cp /d/preview-ca.crt /d/root.crt && chmod a+r /d/*'

# browser: what the user's browser does, from inside the nested daemon.
edge() {
  docker run --rm --network owui -v termiplus-preview-edge-ca:/ca:ro curlimages/curl:8.11.1 \
    -s -m 20 --cacert /ca/root.crt "$@"
}
for _ in $(seq 1 60); do
  edge -f "$SSO/realms/termiplus/.well-known/openid-configuration" >/dev/null && break
  sleep 2
done
edge -f "$SSO/realms/termiplus/.well-known/openid-configuration" | grep -q "\"issuer\":\"$SSO/realms/termiplus\"" ||
  die "Keycloak does not publish the public issuer through the edge"

log "Keycloak test users"
kc() { docker exec "$PROJECT-keycloak-1" /opt/keycloak/bin/kcadm.sh "$@"; }
kc config credentials --server http://localhost:8080 --realm master --user admin \
  --password "$(cat "$WORK/deploy/secrets/keycloak_admin_password")" >/dev/null 2>&1
mkuser() { # name group...
  local u="$1"
  shift
  if [ -n "$(kc get users -r termiplus -q "username=$u" --fields id --format csv --noquotes)" ]; then
    echo "keep user $u"
    return
  fi
  kc create users -r termiplus -s "username=$u" -s enabled=true -s "email=$u@example.test" -s emailVerified=true \
    -s firstName="$u" -s lastName=Test >/dev/null
  kc set-password -r termiplus --username "$u" --new-password "$u-pw-a2e"
  local id g gid
  id="$(kc get users -r termiplus -q "username=$u" --fields id --format csv --noquotes)"
  for g in "$@"; do
    gid="$(kc get groups -r termiplus -q "search=$g" --fields id --format csv --noquotes | head -1)"
    kc update "users/$id/groups/$gid" -r termiplus -s realm=termiplus -s "userId=$id" -s "groupId=$gid" -n >/dev/null
  done
  echo "created user $u"
}
mkuser alice termiplus-persist
mkuser bob
mkuser root termiplus-admin
OWUI_CID="$(kc get clients -r termiplus -q clientId=owui --fields id --format csv --noquotes)"
kc update "clients/$OWUI_CID" -r termiplus -s directAccessGrantsEnabled=true

log "start the full stack"
compose up -d --no-build --pull missing --wait --wait-timeout 300

log "live panel harness"
docker rm -f tp-preview-harness >/dev/null 2>&1 || true
[ -s "$WORK/panel-key" ] || openssl rand -hex 16 | tr -d '\n' >"$WORK/panel-key"
docker create --name tp-preview-harness --restart unless-stopped --network owui --user 1000:1000 \
  -e LIVE=1 -e PORT=8080 -e BIND=0.0.0.0 \
  -e MCP_URL=http://mcp-auth:4180/ \
  -e TOKEN_URL="$SSO/realms/termiplus/protocol/openid-connect/token" \
  -e OWUI_CLIENT_SECRET="$(cat "$WORK/deploy/secrets/owui_oidc_client_secret")" \
  -e PLUGIN_KEY="$(cat "$WORK/deploy/secrets/mcp_owui_plugin_key")" \
  -e LIVE_USER=alice -e LIVE_PASSWORD=alice-pw-a2e -e LIVE_OWUI_USER=owui-alice \
  -e PANEL_KEY="$(cat "$WORK/panel-key")" \
  -e PANEL_FILE=/app/workspaces.html \
  -e NODE_EXTRA_CA_CERTS=/ca/root.crt \
  -v termiplus-preview-edge-ca:/ca:ro -v termiplus-preview-harness-app:/app "$NODE_IMAGE" node /app/harness.mjs >/dev/null
docker cp "$here/harness.mjs" tp-preview-harness:/app/harness.mjs
docker cp "$here/host.html" tp-preview-harness:/app/host.html
docker cp "$ROOT/internal/mcp/app/workspaces.html" tp-preview-harness:/app/workspaces.html
docker start tp-preview-harness >/dev/null
for _ in $(seq 1 30); do
  docker exec tp-preview-harness node /app/harness.mjs call list_workspaces '{}' 2>/dev/null | grep -q '"name":"desk"' && break
  sleep 2
done

log "seed alice's sessions"
sessions="$(docker exec tp-preview-harness node /app/harness.mjs call list_sessions '{}')"
case "$sessions" in
  *'"desktop":true'*) echo "desktop session already present" ;;
  *) docker exec tp-preview-harness node /app/harness.mjs call request_session \
    '{"workspace":"desk","persistent":true,"label":"My desktop","_confirmed":true}' || echo "(slow first start: the session continues in the background)" ;;
esac
case "$sessions" in
  *'"Scratch term"'*) ;;
  *) docker exec tp-preview-harness node /app/harness.mjs call request_session \
    '{"workspace":"scratch","persistent":false,"label":"Scratch term"}' || true ;;
esac

cat <<EOF

preview is up (nested daemon $DOCKER_HOST, project $PROJECT)

  panel URL   https://$PANEL_HOST:$EDGE_PORT/?k=$(cat "$WORK/panel-key")
  hosts file  $LAN_IP sso.test desk.test stream.test $PANEL_HOST
  CA to trust $here/preview-ca.crt
  users       alice / alice-pw-a2e (persist), bob / bob-pw-a2e, root / root-pw-a2e (admin)
  console     $DESK/ui/
EOF
