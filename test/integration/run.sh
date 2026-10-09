#!/usr/bin/env bash
# End-to-end test against a real Docker daemon.
#
#   test/integration/run.sh                 # gVisor (needs runsc registered with Docker)
#   RUNTIME=sysbox-runc test/integration/run.sh
#   RUNTIME=runc test/integration/run.sh    # local smoke test only, NOT isolated
#   BUILD=0 ...                             # use images already tagged :it
#
# Builds all images, starts the stack from deploy/docker-compose.yml, applies
# deploy/firewall.sh and runs test/integration/checks.py from a client
# container that plays the Open WebUI backend.
set -euo pipefail

RUNTIME="${RUNTIME:-runsc}"
TAG="it"
REG="ghcr.io/leon2003m"
PROJECT="termiplus-it"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d)"
CREATED_OWUI=0

log() { printf '\n==> %s\n' "$*"; }

cleanup() {
  log "cleanup"
  docker rm -f tp-it-client >/dev/null 2>&1 || true
  docker compose -p "$PROJECT" -f "$WORK/deploy/docker-compose.yml" -f "$WORK/deploy/it.override.yml" down -v >/dev/null 2>&1 || true
  mapfile -t ctrs < <(docker ps -aq --filter label=termiplus.managed=true)
  if [ "${#ctrs[@]}" -gt 0 ]; then docker rm -f "${ctrs[@]}" >/dev/null 2>&1 || true; fi
  mapfile -t vols < <(docker volume ls -q --filter label=termiplus.managed=true)
  if [ "${#vols[@]}" -gt 0 ]; then docker volume rm "${vols[@]}" >/dev/null 2>&1 || true; fi
  if [ "$CREATED_OWUI" = 1 ]; then docker network rm owui >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

log "runtime: $RUNTIME"
if [ "$RUNTIME" != runc ] && ! docker info --format '{{json .Runtimes}}' | grep -q "\"$RUNTIME\""; then
  echo "Docker does not know runtime $RUNTIME" >&2
  exit 1
fi
# The test stack uses fixed names (network ot-sandbox, gateway 172.30.0.2,
# container names derived from user ids) and its cleanup removes every
# termiplus container and home volume. Refuse to run next to anything that
# already exists, so a real deployment's homes can never be deleted.
existing_ctrs="$(docker ps -aq --filter label=termiplus.managed=true | wc -l)"
existing_vols="$(docker volume ls -q --filter label=termiplus.managed=true | wc -l)"
if [ "$existing_ctrs" -gt 0 ] || [ "$existing_vols" -gt 0 ] || docker network inspect ot-sandbox >/dev/null 2>&1; then
  echo "Refusing to run: found $existing_ctrs termiplus container(s), $existing_vols home volume(s)" >&2
  echo "or the ot-sandbox network on this host. The integration test needs a host without a" >&2
  echo "termiplus deployment, because its cleanup removes all of them." >&2
  trap - EXIT
  rm -rf "$WORK"
  exit 1
fi


if [ "${BUILD:-1}" = 1 ]; then
log "build images"
docker build -q --target gateway   -t "$REG/termiplus-gateway:$TAG"   "$ROOT" >/dev/null
docker build -q --target sockproxy -t "$REG/termiplus-sockproxy:$TAG" "$ROOT" >/dev/null
docker build -q ${TERMINAL_BASE:+--build-arg BASE="$TERMINAL_BASE"} -t "$REG/termiplus-terminal:$TAG" "$ROOT/image" >/dev/null
else
  log "using prebuilt images tagged :$TAG (BUILD=0)"
fi

log "prepare deployment in $WORK"
cp -r "$ROOT/deploy" "$WORK/deploy"
mkdir -p "$WORK/deploy/secrets"
for s in proxy_token gateway_api_key key_secret; do
  head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$WORK/deploy/secrets/$s"
done
chmod 0644 "$WORK"/deploy/secrets/*   # readable by the non-root services and the test client
cat > "$WORK/deploy/policies.json" <<JSON
{
  "default": { "runtime": "$RUNTIME", "memory_mb": 1024, "cpus": 1, "pids": 512 },
  "scratch": { "runtime": "$RUNTIME", "memory_mb": 1024, "cpus": 1, "pids": 512,
               "storage": "ephemeral", "idle_timeout": "1m", "max_per_user": 2 }
}
JSON
cat > "$WORK/deploy/it.override.yml" <<YAML
services:
  sockproxy:
    environment:
      ALLOWED_RUNTIMES: "$RUNTIME"
      ALLOW_RUNC_INSECURE: "$([ "$RUNTIME" = runc ] && echo 1 || echo 0)"
YAML

if ! docker network inspect owui >/dev/null 2>&1; then
  docker network create owui >/dev/null
  CREATED_OWUI=1
fi

export TERMINAL_IMAGE="$REG/termiplus-terminal:$TAG" TERMIPLUS_VERSION="$TAG"
DOCKER_GID="$(stat -c %g /var/run/docker.sock)"
export DOCKER_GID

log "start stack"
docker compose -p "$PROJECT" -f "$WORK/deploy/docker-compose.yml" -f "$WORK/deploy/it.override.yml" \
  up -d --no-build --pull never --wait --wait-timeout 120

log "check service hardening"
for svc in gateway sockproxy; do
  id="$(docker compose -p "$PROJECT" -f "$WORK/deploy/docker-compose.yml" ps -q "$svc")"
  user="$(docker inspect -f '{{.Config.User}}' "$id")"
  ro="$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$id")"
  caps="$(docker inspect -f '{{.HostConfig.CapDrop}}' "$id")"
  echo "$svc: user=$user readonly=$ro capdrop=$caps"
  if [ "$user" != "65534:65534" ] || [ "$ro" != true ] || [[ "$caps" != *ALL* ]]; then
    echo "FAIL: $svc not hardened"; exit 1
  fi
done

if [ "${SKIP_FIREWALL:-0}" != 1 ]; then
  log "apply firewall"
  sudo "$ROOT/deploy/firewall.sh"
fi

log "start test client (plays the Open WebUI backend)"
docker run -d --name tp-it-client --network owui \
  -v "$WORK/deploy/secrets:/s:ro" -v "$ROOT/test/integration:/t:ro" \
  "$TERMINAL_IMAGE" sleep infinity >/dev/null

log "functional and isolation checks"
docker exec -e RUNTIME="$RUNTIME" tp-it-client python3 /t/checks.py

log "terminal containers use runtime $RUNTIME"
mapfile -t terms < <(docker ps -q --filter label=termiplus.managed=true)
[ "${#terms[@]}" -gt 0 ] || { echo "FAIL: no terminal containers"; exit 1; }
for c in "${terms[@]}"; do
  rt="$(docker inspect -f '{{.HostConfig.Runtime}}' "$c")"
  priv="$(docker inspect -f '{{.HostConfig.Privileged}}' "$c")"
  echo "$(docker inspect -f '{{.Name}}' "$c"): runtime=$rt privileged=$priv"
  if [ "$rt" != "$RUNTIME" ] || [ "$priv" != false ]; then echo "FAIL"; exit 1; fi
done

log "persistent home survives container recreation"
alice="$(docker ps -q --filter label=termiplus.user=alice-it --filter label=termiplus.storage=persistent)"
docker rm -f "$alice" >/dev/null
sleep 11   # gateway re-probes instances idle for more than 10s
docker exec -e RUNTIME="$RUNTIME" tp-it-client python3 /t/checks.py persistence

log "socket proxy refuses escape attempts"
docker run --rm --network "${PROJECT}_ot-control" -v "$WORK/deploy/secrets:/s:ro" \
  "$TERMINAL_IMAGE" bash -c '
    set -u; T="X-Proxy-Token: $(cat /s/proxy_token)"; P=http://sockproxy:2375; fail=0
    expect() { want=$1; label=$2; shift 2; got=$(curl -s -o /dev/null -w "%{http_code}" "$@")
      if [ "$got" = "$want" ]; then echo "ok   $want $label"; else echo "FAIL got $got want $want: $label"; fail=1; fi; }
    expect 401 "no proxy token" $P/_ping
    expect 403 "privileged container" -H "$T" -X POST -H "Content-Type: application/json" -d "{\"Image\":\"alpine\",\"HostConfig\":{\"Privileged\":true}}" "$P/containers/create?name=ot-u-0000000000000000-default"
    expect 403 "bind mount via volume driver opts" -H "$T" -X POST -H "Content-Type: application/json" -d "{\"Name\":\"ot-home-0000000000000000-x\",\"DriverOpts\":{\"o\":\"bind\",\"device\":\"/\"}}" $P/volumes/create
    expect 403 "image pull" -H "$T" -X POST "$P/images/create?fromImage=alpine"
    expect 403 "image build" -H "$T" -X POST $P/build
    expect 403 "inspect unmanaged container" -H "$T" $P/containers/termiplus-it-gateway-1/json
    expect 403 "path traversal" -H "$T" --path-as-is $P/containers/../images/json
    exit $fail'

log "all integration checks passed (runtime $RUNTIME)"
