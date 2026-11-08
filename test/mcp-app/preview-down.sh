#!/usr/bin/env bash
# Tear down the preview made by preview-up.sh, on the nested daemon only.
# Scoped to the termiplus-preview project and the tp-preview-* containers by
# name; nothing is removed by label or glob.
#   KEEP_DATA=1 keeps volumes and local secrets (a later preview-up reuses them)
set -euo pipefail

export DOCKER_HOST="${PREVIEW_DOCKER_HOST:-tcp://127.0.0.1:2376}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$here/.preview"
PROJECT=termiplus-preview

case "$(docker info --format '{{json .Runtimes}}' 2>/dev/null)" in
  *'"runc"'*) ;;
  *) echo "preview-down: nested daemon not reachable at $DOCKER_HOST" >&2; exit 1 ;;
esac

docker rm -f tp-preview-edge tp-preview-harness tp-preview-fill >/dev/null 2>&1 || true

if [ -f "$WORK/deploy/.env" ]; then
  for f in "$WORK"/deploy/secrets/*; do
    case "$(basename "$f")" in realm-*.json | keycloak.env) continue ;; esac
    export "SECRET_$(basename "$f")=$(cat "$f")"
  done
  down=(down)
  [ "${KEEP_DATA:-0}" = 1 ] || down=(down -v)
  docker compose -p "$PROJECT" --env-file "$WORK/deploy/.env" \
    -f "$WORK/deploy/docker-compose.yml" -f "$WORK/deploy/docker-compose.mcp.yml" \
    -f "$WORK/deploy/docker-compose.desktop.yml" -f "$WORK/deploy/docker-compose.keycloak.yml" \
    -f "$WORK/deploy/preview.override.yml" "${down[@]}"
fi

# Session containers and home volumes the gateway created for the preview
# users are not compose services; remove the ones of this preview by name.
if [ "${KEEP_DATA:-0}" != 1 ]; then
  for v in policies kc-import mcp-data backups edge-ca edge-data harness-app; do
    docker volume rm "termiplus-preview-$v" >/dev/null 2>&1 || true
  done
  rm -rf "$WORK" "$here/preview-ca.crt"
fi
echo "preview removed (session containers and home volumes created by the gateway remain on the nested daemon until removed with docker ps / docker volume ls)"
