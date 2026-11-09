#!/usr/bin/env bash
# Cold-start timing of the anonymous spec sandbox (user "termiplus-spec"): the
# first user-less /openapi.json request through the gateway after the stack is
# up and no sandbox exists. Phases come from Docker events, the gateway's
# health probe and process start times inside the sandbox.
#
#   sudo test/integration/owui-browser/timing.sh [policy]    (stack must be up, spec sandbox absent)
set -euo pipefail
POLICY="${1:-default}"
STATE="${OWUI_E2E_STATE:-/var/tmp/owui-e2e}"
OUT="${TIMING_OUT:-$STATE/artifacts/timing}"
PROJECT=termiplus-owui
mkdir -p "$OUT"
KEY="$(cat "$STATE/work/deploy/secrets/gateway_api_key")"
now() { date +%s.%N; }
RUN="${TIMING_LABEL:-run}"

# A cold sandbox: no spec container and no spec instance in the gateway, as right
# after a stack start. COLD_CACHES=1 also drops the VM's page cache (the state
# after a reboot), COLD_LOAD=<n> runs n CPU burners during the request (a stack
# starting up with Keycloak and Open WebUI at the same time).
mapfile -t old < <(docker ps -aq --filter label=termiplus.user=termiplus-spec)
if [ "${#old[@]}" -gt 0 ]; then docker rm -f "${old[@]}" >/dev/null; fi
docker restart "$PROJECT-gateway-1" >/dev/null
for _ in $(seq 1 60); do
  [ "$(docker inspect -f '{{.State.Health.Status}}' "$PROJECT-gateway-1")" = healthy ] && break
  sleep 1
done
if [ "${COLD_CACHES:-0}" = 1 ]; then sync; echo 3 >/proc/sys/vm/drop_caches; fi
LOADPIDS=()
for _ in $(seq 1 "${COLD_LOAD:-0}"); do
  timeout 60 sh -c 'while :; do :; done' &
  LOADPIDS+=($!)
done
sleep 1

T0="$(now)"
docker events --since "$T0" --format '{{.TimeNano}} {{.Type}} {{.Action}} {{.Actor.Attributes.name}}' \
  --filter type=container >"$OUT/$RUN.events" 2>&1 &
EV=$!
# The request exactly as Open WebUI's spec fetch sends it: no X-User-Id.
docker exec tp-owui-client curl -s -o /dev/null -m 120 \
  -w 'http=%{http_code} connect=%{time_connect} starttransfer=%{time_starttransfer} total=%{time_total}\n' \
  -H "Authorization: Bearer $KEY" "http://gateway:8080/p/$POLICY/openapi.json" >"$OUT/$RUN.curl" &
CURL=$!
# Poll the sandbox's /health from the host once it exists.
name=""; ip=""; first_conn=""; first_200=""
while kill -0 "$CURL" 2>/dev/null; do
  if [ -z "$name" ]; then
    name="$(docker ps -a --format '{{.Names}}' --filter label=termiplus.user=termiplus-spec | head -1)"
  fi
  if [ -n "$name" ] && [ -z "$ip" ]; then
    ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$name" 2>/dev/null || true)"
  fi
  if [ -n "$ip" ]; then
    code="$(curl -s -o /dev/null -m 1 -w '%{http_code}' "http://$ip:8000/health" || true)"
    [ "$code" = 000 ] || [ -n "$first_conn" ] || first_conn="$(now)"
    if [ "$code" = 200 ]; then first_200="$(now)"; break; fi
  fi
  sleep 0.1
done
wait "$CURL" || true
for p in "${LOADPIDS[@]}"; do kill "$p" 2>/dev/null || true; done
T1="$(now)"
sleep 1
kill "$EV" 2>/dev/null || true
echo "container=$name ip=$ip" >"$OUT/$RUN.summary"
python3 - "$T0" "$T1" "${first_conn:-0}" "${first_200:-0}" "$OUT/$RUN.events" "$OUT/$RUN.curl" >>"$OUT/$RUN.summary" <<'PY'
import sys
t0, t1, fc, f200 = map(float, sys.argv[1:5])
print("T0 request sent: 0.00 s")
for line in open(sys.argv[5]):
    p = line.split()
    if len(p) >= 3 and p[0].isdigit():
        print("%+7.2f s  docker %s %s" % (int(p[0]) / 1e9 - t0, p[2], p[3] if len(p) > 3 else ""))
if fc: print("%+7.2f s  /health first answers (any HTTP code)" % (fc - t0))
if f200: print("%+7.2f s  /health 200 (gateway waitReady would return)" % (f200 - t0))
print("%+7.2f s  client got its answer: %s" % (t1 - t0, open(sys.argv[6]).read().strip()))
PY
# Process start times inside the sandbox, relative to the container's own start.
if [ -n "$name" ]; then
  docker exec "$name" sh -c 'ps -eo lstart=,etimes=,args= | sort -k5,5nr | cut -c1-150' >"$OUT/$RUN.ps" 2>&1 || true
  docker inspect -f 'created={{.Created}} started={{.State.StartedAt}}' "$name" >>"$OUT/$RUN.summary" || true
  docker logs -t "$name" >"$OUT/$RUN.container.log" 2>&1 || true
  docker logs --since "$(date -u -d @"${T0%.*}" +%Y-%m-%dT%H:%M:%S)" "$PROJECT-gateway-1" >"$OUT/$RUN.gateway.log" 2>&1 || true
fi
cat "$OUT/$RUN.summary"
