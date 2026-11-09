#!/usr/bin/env bash
# Headless-browser end-to-end test of termiplus through a real Open WebUI.
#
# Run on a throwaway host or VM (root: it applies deploy/firewall.sh), never on
# a host that serves a live deployment; it refuses to start next to one.
#
#   sudo RUNTIME=sysbox-runc test/integration/owui-browser/run.sh          # up + test + down
#   sudo test/integration/owui-browser/run.sh up                           # bring the stack up, leave it running
#   sudo test/integration/owui-browser/run.sh test [check ...]             # run checks (names: see e2e.py)
#   sudo test/integration/owui-browser/run.sh timing                       # cold-start timing of the spec sandbox
#   sudo test/integration/owui-browser/run.sh down                         # remove everything (compose down -v)
#
# From a developer machine with a VM, see remote.sh (copies the tree, runs this).
#
# The stack is test/integration/alpha-e2e.sh's (base + mcp + desktop + keycloak
# overlays as shipped, Keycloak behind TLS at https://sso.test) plus Open WebUI
# 0.13 signing in through that Keycloak at https://chat.test, a deterministic
# OpenAI-compatible stub model (stub_model.py) and Playwright (e2e.py) driving
# Chromium. Every secret is generated here; nothing is read from a live host.
#
# Images: ghcr.io/leon2003m/termiplus-*:$TAG (TAG=it, built by BUILD=1 or already
# loaded), $OWUI_IMAGE and $PLAYWRIGHT_IMAGE must be present (or pullable).

# Helpers are called through traps and subcommands.
# shellcheck disable=SC2317
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
RUNTIME="${RUNTIME:-sysbox-runc}"
TAG="${TAG:-it}"
REG="ghcr.io/leon2003m"
PROJECT="termiplus-owui"
STATE="${OWUI_E2E_STATE:-/var/tmp/owui-e2e}"
ARTIFACTS="${OWUI_E2E_ARTIFACTS:-$STATE/artifacts}"
OWUI_IMAGE="${OWUI_IMAGE:-ghcr.io/open-webui/open-webui:0.13.0-slim}"
PLAYWRIGHT_IMAGE="${PLAYWRIGHT_IMAGE:-mcr.microsoft.com/playwright/python:v1.55.0-noble}"
CADDY_IMAGE="caddy:2.10.2-alpine@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d"
EDGE=tp-owui-edge
OWUI=tp-owui
STUB=tp-owui-stub
OWUI_TOOL_TIMEOUT="${OWUI_TOOL_TIMEOUT:-60}"
# The shipped realm requires PKCE S256 (documented in deploy/setup-alpha.sh); set
# OWUI_PKCE= (empty) to leave OAUTH_CODE_CHALLENGE_METHOD out and see sign-in fail.
OWUI_PKCE="${OWUI_PKCE-1}"
WORK="$STATE/work"

[ "$(id -u)" = 0 ] || { echo "run as root (sudo): the firewall is part of the test" >&2; exit 1; }

log() { printf '\n==> %s\n' "$*"; }

compose() {
  docker compose -p "$PROJECT" --env-file "$WORK/deploy/.env" \
    -f "$WORK/deploy/docker-compose.yml" -f "$WORK/deploy/docker-compose.mcp.yml" \
    -f "$WORK/deploy/docker-compose.desktop.yml" -f "$WORK/deploy/docker-compose.keycloak.yml" \
    -f "$WORK/deploy/e2e.override.yml" "$@"
}

teardown() {
  log "teardown"
  [ ! -f "$STATE/watch.pid" ] || kill -- "-$(cat "$STATE/watch.pid")" >/dev/null 2>&1 || true
  rm -f "$STATE/watch.pid"
  docker rm -f "$OWUI" "$STUB" "$EDGE" tp-owui-client >/dev/null 2>&1 || true
  if [ -f "$WORK/deploy/.env" ]; then compose down -v >/dev/null 2>&1 || true; fi
  mapfile -t ctrs < <(docker ps -aq --filter label=termiplus.managed=true)
  if [ "${#ctrs[@]}" -gt 0 ]; then docker rm -f "${ctrs[@]}" >/dev/null 2>&1 || true; fi
  mapfile -t vols < <(docker volume ls -q --filter label=termiplus.managed=true)
  if [ "${#vols[@]}" -gt 0 ]; then docker volume rm "${vols[@]}" >/dev/null 2>&1 || true; fi
  docker volume rm tp-owui-data >/dev/null 2>&1 || true
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    docker network inspect ot-sandbox >/dev/null 2>&1 || break
    docker network rm ot-sandbox >/dev/null 2>&1 || sleep 3
  done
  if [ -f "$STATE/created-owui-network" ]; then docker network rm owui >/dev/null 2>&1 || true; fi
  rm -rf "$WORK" "$STATE/created-owui-network"
}

kc() { docker exec "$PROJECT-keycloak-1" /opt/keycloak/bin/kcadm.sh "$@"; }

do_up() {
  if [ "$(docker ps -aq --filter label=termiplus.managed=true | wc -l)" -gt 0 ] ||
    [ "$(docker volume ls -q --filter label=termiplus.managed=true | wc -l)" -gt 0 ] ||
    docker network inspect ot-sandbox >/dev/null 2>&1; then
    echo "Refusing to run: this host has termiplus containers, home volumes or ot-sandbox." >&2
    exit 1
  fi
  # Armed only here: a refused start must never tear down what it refused for.
  if [ "${AUTO_TEARDOWN:-0}" = 1 ]; then trap teardown EXIT; fi
  rm -rf "$ARTIFACTS"; mkdir -p "$STATE" "$ARTIFACTS"
  rm -rf "$WORK"; mkdir -p "$WORK"

  if [ "${BUILD:-0}" = 1 ]; then
    log "build images"
    for t in gateway sockproxy mcp; do docker build -q --target "$t" -t "$REG/termiplus-$t:$TAG" "$ROOT" >/dev/null; done
    docker build -q -t "$REG/termiplus-terminal:$TAG" "$ROOT/image" >/dev/null
    docker build -q --build-arg BASE="$REG/termiplus-terminal:$TAG" -t "$REG/termiplus-desktop:$TAG" "$ROOT/image/desktop" >/dev/null
  fi

  log "prepare deployment (runtime $RUNTIME)"
  cp -r "$ROOT/deploy" "$WORK/deploy"
  rm -rf "$WORK/deploy/secrets" "$WORK/deploy/.env" "$WORK/deploy/mcp-data" "$WORK/deploy/backups"
  mkdir -p "$WORK/deploy/secrets"
  for s in proxy_token gateway_api_key key_secret; do
    openssl rand -hex 32 | tr -d '\n' >"$WORK/deploy/secrets/$s"
  done
  chmod 0644 "$WORK"/deploy/secrets/*
  WEBUI_URL=https://chat.test KEYCLOAK_PUBLIC_URL=https://sso.test DESKTOP_PUBLIC_URL=https://desk.test \
    DESKTOP_STREAM_URL=https://stream.test "$WORK/deploy/setup-alpha.sh" >/dev/null
  # Test-only: every workspace open; the entitlement scenarios narrow it.
  printf '{"default":"allow","workspaces":{}}\n' >"$WORK/deploy/mcp-data/entitlements.json"
  chown -R 65534:65534 "$WORK/deploy/mcp-data" "$WORK/deploy/backups"
  cat >"$WORK/deploy/policies.json" <<JSON
{
  "default": { "label": "Desktop", "runtime": "$RUNTIME", "memory_mb": 1536, "cpus": 1, "pids": 1024, "desktop": true },
  "scratch": { "label": "Temporary desktop", "runtime": "$RUNTIME", "memory_mb": 1536, "cpus": 1, "pids": 1024,
               "storage": "ephemeral", "idle_timeout": "15m", "max_per_user": 3, "desktop": true },
  "i18n":    { "label": "German desktop", "runtime": "$RUNTIME", "memory_mb": 1536, "cpus": 1, "pids": 1024,
               "desktop": true, "locale": "de_DE.UTF-8", "keyboard": "de", "record": true }
}
JSON
  cat >>"$WORK/deploy/.env" <<ENV
TERMINAL_IMAGE=$REG/termiplus-desktop:$TAG
TERMIPLUS_VERSION=$TAG
DOCKER_GID=$(stat -c %g /var/run/docker.sock)
MCP_SHARES=${MCP_SHARES:-}
ENV
  cat >"$WORK/deploy/e2e.override.yml" <<YAML
services:
  sockproxy:
    environment:
      ALLOWED_RUNTIMES: "$RUNTIME"
  mcp-auth:
    environment: { SSL_CERT_FILE: /etc/e2e/root.crt }
    volumes: ["$WORK/edge-root.crt:/etc/e2e/root.crt:ro"]
  desk-auth:
    environment: { SSL_CERT_FILE: /etc/e2e/root.crt }
    volumes: ["$WORK/edge-root.crt:/etc/e2e/root.crt:ro"]
  desk-stream-auth:
    environment: { SSL_CERT_FILE: /etc/e2e/root.crt }
    volumes: ["$WORK/edge-root.crt:/etc/e2e/root.crt:ro"]
YAML

  if ! docker network inspect owui >/dev/null 2>&1; then
    docker network create owui >/dev/null
    : >"$STATE/created-owui-network"
  fi

  log "start Keycloak and the TLS edge"
  compose up -d --no-build --pull missing --wait --wait-timeout 300 keycloak
  cat >"$WORK/Caddyfile" <<'CADDY'
sso.test {
  tls internal
  reverse_proxy keycloak:8080
}
chat.test {
  tls internal
  reverse_proxy tp-owui:8080
}
desk.test {
  tls internal
  reverse_proxy desk-auth:4180
}
stream.test {
  tls internal
  reverse_proxy desk-stream-auth:4180
}
CADDY
  docker run -d --name "$EDGE" --network owui --network-alias sso.test --network-alias chat.test \
    --network-alias desk.test --network-alias stream.test \
    -v "$WORK/Caddyfile:/etc/caddy/Caddyfile:ro" "$CADDY_IMAGE" >/dev/null
  for _ in $(seq 1 30); do
    docker cp "$EDGE:/data/caddy/pki/authorities/local/root.crt" "$WORK/edge-root.crt" 2>/dev/null && break
    sleep 1
  done
  [ -s "$WORK/edge-root.crt" ] || { echo "Caddy root CA missing" >&2; exit 1; }
  chmod 0644 "$WORK/edge-root.crt"

  log "Keycloak test users"
  # Inside the owui network the "browser" is the Playwright image; for the
  # driver's own calls curl in a helper container is enough.
  for _ in $(seq 1 60); do
    docker run --rm --network owui -v "$WORK/edge-root.crt:/ca.crt:ro" curlimages/curl:latest -sf --cacert /ca.crt \
      https://sso.test/realms/termiplus/.well-known/openid-configuration >/dev/null 2>&1 && break
    sleep 2
  done
  kc config credentials --server http://localhost:8080 --realm master --user admin \
    --password "$(cat "$WORK/deploy/secrets/keycloak_admin_password")" >/dev/null 2>&1
  mkuser alice termiplus-persist
  mkuser bob
  mkuser root termiplus-admin
  # Test-only: the driver gets tokens for direct protocol checks by password grant.
  local cid
  cid="$(kc get clients -r termiplus -q clientId=owui --fields id --format csv --noquotes)"
  kc update "clients/$cid" -r termiplus -s directAccessGrantsEnabled=true

  log "start the full stack"
  compose up -d --no-build --pull missing --wait --wait-timeout 300
  "$WORK/deploy/firewall.sh" >/dev/null
  # Own process group: the watcher forks a monitor and a worker.
  setsid "$WORK/deploy/firewall.sh" --watch-ports >/dev/null 2>&1 &
  echo $! >"$STATE/watch.pid"

  log "stub model"
  mkdir -p "$ARTIFACTS/stub"
  docker run -d --name "$STUB" --network owui --network-alias stub --memory 128m \
    -v "$HERE/stub_model.py:/stub_model.py:ro" -v "$ARTIFACTS/stub:/logs" \
    "$PLAYWRIGHT_IMAGE" python3 /stub_model.py >/dev/null

  log "Open WebUI $OWUI_IMAGE (tool-server data timeout ${OWUI_TOOL_TIMEOUT}s)"
  start_owui
  docker run -d --name tp-owui-client --network owui "$REG/termiplus-terminal:$TAG" sleep infinity >/dev/null
  log "stack is up"
}

mkuser() { # name group...
  local u="$1"; shift
  kc create users -r termiplus -s "username=$u" -s enabled=true -s "email=$u@example.test" -s emailVerified=true \
    -s firstName="$u" -s lastName=Test >/dev/null
  kc set-password -r termiplus --username "$u" --new-password "$u-pw-e2e"
  local id g gid
  id="$(kc get users -r termiplus -q "username=$u" --fields id --format csv --noquotes)"
  for g in "$@"; do
    gid="$(kc get groups -r termiplus -q "search=$g" --fields id --format csv --noquotes | head -1)"
    kc update "users/$id/groups/$gid" -r termiplus -s realm=termiplus -s "userId=$id" -s "groupId=$gid" -n >/dev/null
  done
}

start_owui() {
  docker rm -f "$OWUI" >/dev/null 2>&1 || true
  local s="$WORK/deploy/secrets"
  [ -f "$WORK/webui_secret" ] || openssl rand -hex 32 | tr -d '\n' >"$WORK/webui_secret"
  # Environment as deploy/setup-alpha.sh documents it, plus test-only settings
  # (stub model, a first-signup admin, pre-granted default role).
  docker run -d --name "$OWUI" --network owui --network-alias open-webui --memory 1700m \
    -v tp-owui-data:/app/backend/data -v "$WORK/edge-root.crt:/etc/e2e/root.crt:ro" \
    -e SSL_CERT_FILE=/etc/e2e/root.crt -e REQUESTS_CA_BUNDLE=/etc/e2e/root.crt \
    -e "WEBUI_SECRET_KEY=$(cat "$WORK/webui_secret")" -e WEBUI_URL=https://chat.test \
    -e OAUTH_CLIENT_ID=owui -e "OAUTH_CLIENT_SECRET=$(cat "$s/owui_oidc_client_secret")" \
    -e OPENID_PROVIDER_URL=https://sso.test/realms/termiplus/.well-known/openid-configuration \
    -e OAUTH_PROVIDER_NAME=Keycloak -e "OAUTH_SCOPES=openid email profile offline_access" \
    -e ENABLE_OAUTH_SIGNUP=true -e OAUTH_MERGE_ACCOUNTS_BY_EMAIL=true \
    ${OWUI_PKCE:+-e OAUTH_CODE_CHALLENGE_METHOD=S256} \
    -e "OAUTH_SESSION_TOKEN_ENCRYPTION_KEY=$(cat "$s/owui_oauth_session_key")" \
    -e DEFAULT_USER_ROLE=user -e ENABLE_SIGNUP=true \
    -e ENABLE_OLLAMA_API=false -e ENABLE_OPENAI_API=true \
    -e OPENAI_API_BASE_URL=http://stub:9000/v1 -e OPENAI_API_KEY=stub-key \
    -e ENABLE_VERSION_UPDATE_CHECK=false -e ANONYMIZED_TELEMETRY=false -e SCARF_NO_ANALYTICS=true \
    -e DO_NOT_TRACK=true -e ENABLE_TOOL_SERVERS=true \
    -e "AIOHTTP_CLIENT_TIMEOUT_TOOL_SERVER_DATA=$OWUI_TOOL_TIMEOUT" \
    "$OWUI_IMAGE" >/dev/null
  for _ in $(seq 1 90); do
    [ "$(docker exec "$OWUI" python -c 'import urllib.request;print(urllib.request.urlopen("http://127.0.0.1:8080/health").status)' 2>/dev/null)" = 200 ] && return 0
    sleep 2
  done
  echo "Open WebUI did not start" >&2; docker logs --tail 30 "$OWUI" >&2; return 1
}

# Everything the Playwright container needs; the e2e script reads these.
e2e_env() {
  local s="$WORK/deploy/secrets"
  cat <<ENV
E2E_GATEWAY_KEY=$(cat "$s/gateway_api_key")
E2E_PLUGIN_KEY=$(cat "$s/mcp_owui_plugin_key")
E2E_OWUI_SECRET=$(cat "$s/owui_oidc_client_secret")
E2E_RUNTIME=$RUNTIME
E2E_PROJECT=$PROJECT
E2E_OWUI_TOOL_TIMEOUT=$OWUI_TOOL_TIMEOUT
E2E_MCP_SHARES=${MCP_SHARES:-on}
ENV
}

do_test() {
  [ -d "$WORK/deploy" ] || { echo "stack is not up (run: $0 up)" >&2; exit 1; }
  mkdir -p "$ARTIFACTS/shots"
  e2e_env >"$STATE/e2e.env"
  chmod 0600 "$STATE/e2e.env"
  # Playwright's Python package is not in the image; cache it under $STATE.
  if [ ! -d "$STATE/pylib/playwright" ]; then
    docker run --rm -v "$STATE/pylib:/pylib" "$PLAYWRIGHT_IMAGE" pip install -q --target /pylib playwright==1.55.0
  fi
  # The test drives the VM's docker for what a browser cannot do (restart
  # services, inspect containers) by calling this script's "op" commands, so the
  # repo and $STATE are mounted at their own paths together with docker and
  # its compose plugin. Test-only: the VM is disposable.
  local plugins=/usr/libexec/docker/cli-plugins
  docker run --rm --name tp-owui-e2e --network owui --ipc=host \
    -v "$ROOT:$ROOT:ro" -v "$STATE:$STATE" -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$(command -v docker):/usr/bin/docker:ro" -v "$plugins:$plugins:ro" \
    -e PYTHONPATH="$STATE/pylib" -e E2E_ENVFILE="$STATE/e2e.env" -e E2E_RUN_SH="$HERE/run.sh" \
    -e OWUI_E2E_STATE="$STATE" -e E2E_OUT="$ARTIFACTS" -e RUNTIME="$RUNTIME" -e E2E_TAG="$TAG" \
    "$PLAYWRIGHT_IMAGE" python3 -u "$HERE/e2e.py" "$@"
}

# Operations the test asks for while it runs.
do_op() {
  [ -d "$WORK/deploy" ] || { echo "stack is not up" >&2; exit 1; }
  local op="$1"; shift
  case "$op" in
    owui-restart) # [VAR=value ...]: OWUI_TOOL_TIMEOUT, OWUI_PKCE
      local kv
      for kv in "$@"; do export "${kv?}"; done
      start_owui ;;
    gateway-restart) compose restart gateway >/dev/null ;;
    shares) # on|off: MCP_SHARES kill-switch, recreates mcp
      if [ "$1" = off ]; then sed -i 's/^MCP_SHARES=.*/MCP_SHARES=off/' "$WORK/deploy/.env"; else sed -i 's/^MCP_SHARES=.*/MCP_SHARES=/' "$WORK/deploy/.env"; fi
      compose up -d --no-build --force-recreate --wait --wait-timeout 120 mcp >/dev/null ;;
    stack-restart) # every service at once, as after a host reboot (Open WebUI included)
      docker rm -f "$OWUI" >/dev/null 2>&1 || true
      local c
      mapfile -t c < <(docker ps -aq --filter label=termiplus.managed=true)
      if [ "${#c[@]}" -gt 0 ]; then docker rm -f "${c[@]}" >/dev/null; fi
      if [ "${COLD_STRESS:-0}" = 1 ]; then
        # A host that has just booted: cold page cache and every CPU busy.
        docker run --rm --privileged alpine:3 sh -c 'sync; echo 3 >/proc/sys/vm/drop_caches'
        for _ in 1 2 3; do (timeout 90 sh -c 'while :; do :; done' >/dev/null 2>&1 &); done
      fi
      compose restart >/dev/null
      start_owui ;;
    home-remove) # on|off: the opt-in ALLOW_HOME_REMOVE for sockproxy, gateway and mcp (wipe_home, departed users)
      if grep -q '^ALLOW_HOME_REMOVE=' "$WORK/deploy/.env"; then sed -i "s/^ALLOW_HOME_REMOVE=.*/ALLOW_HOME_REMOVE=$([ "$1" = on ] && echo 1)/" "$WORK/deploy/.env"
      else echo "ALLOW_HOME_REMOVE=$([ "$1" = on ] && echo 1)" >>"$WORK/deploy/.env"; fi
      # The gateway and the MCP server read the same variable (clear refusal, panel).
      compose up -d --no-build --force-recreate --wait --wait-timeout 180 sockproxy gateway mcp >/dev/null ;;
    policies) # file: replace policies.json and restart the gateway
      cp "$1" "$WORK/deploy/policies.json"; compose restart gateway >/dev/null ;;
    *) echo "unknown op $op" >&2; exit 2 ;;
  esac
}

do_timing() { # cold-start matrix of the spec sandbox (host level: drops caches, burns CPU)
  [ -d "$WORK/deploy" ] || { echo "stack is not up" >&2; exit 1; }
  local scen n cc ld
  for scen in "warm:0:0" "pagecache:1:0" "cpuload:0:3" "pagecache+cpuload:1:3"; do
    IFS=: read -r n cc ld <<<"$scen"
    echo "### $n"
    TIMING_LABEL="$n" COLD_CACHES="$cc" COLD_LOAD="$ld" bash "$HERE/timing.sh" default | grep -v 'exec_'
  done | tee "$ARTIFACTS/timing-matrix.txt"
}

case "${1:-all}" in
  up) do_up ;;
  test) shift; do_test "$@" ;;
  op) shift; do_op "$@" ;;
  down) teardown ;;
  timing) do_timing ;;
  all)
    AUTO_TEARDOWN=1
    do_up
    rc=0
    do_test || rc=$?
    do_timing || true
    exit "$rc"
    ;;
  *) echo "usage: $0 [all|up|test [check...]|timing|down]" >&2; exit 2 ;;
esac
