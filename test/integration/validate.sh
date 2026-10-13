#!/usr/bin/env bash
# Live security validation for v0.1.0 (test/integration/VALIDATION.md, V1-V15).
#
#   sudo RUNTIME=sysbox-runc test/integration/validate.sh
#   sudo RUNTIME=runsc       test/integration/validate.sh
#   BUILD=0 ...   # reuse images tagged :it (e.g. after run.sh)
#
# Needs root on a throwaway host or VM: it applies deploy/firewall.sh, flushes
# its chains for V1 and leaves the host firewall rules of the last run in place.
# Starts the base stack plus the netpolicy overlay, an "internal service" at
# 10.99.0.5 (ports 5432 and 5433) in a network namespace routed like a LAN
# host (not a Docker network, which Docker itself isolates), and drives the
# terminals through the gateway like Open WebUI does. Prints PASS/FAIL per check
# and a summary per scenario. Refuses to run next to an existing deployment.
# Helpers are called through expect "$@" and the EXIT trap.
# shellcheck disable=SC2317
set -euo pipefail

RUNTIME="${RUNTIME:-runsc}"
TAG="it"
REG="ghcr.io/leon2003m"
PROJECT="termiplus-val"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d)"
INT_NS="tp-val-int"
INT_IP="10.99.0.5"
CREATED_OWUI=0

[ "$(id -u)" = 0 ] || { echo "run as root (sudo): the firewall is part of the test" >&2; exit 1; }

log() { printf '\n==> %s\n' "$*"; }
declare -A RESULT=()
SCEN=""
scenario() { SCEN="$1"; RESULT[$SCEN]="${RESULT[$SCEN]:-PASS}"; log "$1 - $2"; }
pass() { echo "PASS  [$SCEN] $*"; }
fail() { echo "FAIL  [$SCEN] $*"; RESULT[$SCEN]=FAIL; }
expect() { # description, expected exit (0 = must succeed, 1 = must fail), command...
  local d="$1" want="$2"; shift 2
  if "$@" >/dev/null 2>&1; then got=0; else got=1; fi
  if [ "$got" = "$want" ]; then pass "$d"; else fail "$d (command $([ "$got" = 0 ] && echo succeeded || echo failed))"; fi
}

compose() {
  docker compose -p "$PROJECT" -f "$WORK/deploy/docker-compose.yml" -f "$WORK/deploy/docker-compose.netpolicy.yml" \
    -f "$WORK/deploy/val.override.yml" "$@"
}

cleanup() {
  log "cleanup"
  docker rm -f tp-val-client >/dev/null 2>&1 || true
  if [ -n "$INT_PID" ]; then kill "$INT_PID" >/dev/null 2>&1 || true; fi
  ip netns del "$INT_NS" >/dev/null 2>&1 || true
  ip link del tp-val-h >/dev/null 2>&1 || true
  compose down -v >/dev/null 2>&1 || true
  mapfile -t ctrs < <(docker ps -aq --filter label=termiplus.managed=true)
  if [ "${#ctrs[@]}" -gt 0 ]; then docker rm -f "${ctrs[@]}" >/dev/null 2>&1 || true; fi
  mapfile -t vols < <(docker volume ls -q --filter label=termiplus.managed=true)
  if [ "${#vols[@]}" -gt 0 ]; then docker volume rm "${vols[@]}" >/dev/null 2>&1 || true; fi
  docker volume rm -f "$FOREIGN_VOL" >/dev/null 2>&1 || true
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    docker network inspect ot-sandbox >/dev/null 2>&1 || break
    docker network rm ot-sandbox >/dev/null 2>&1 || sleep 3
  done
  if [ "$CREATED_OWUI" = 1 ]; then docker network rm owui >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
FOREIGN_VOL=""
INT_PID=""
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
  docker build -q --target gateway -t "$REG/termiplus-gateway:$TAG" "$ROOT" >/dev/null
  docker build -q --target sockproxy -t "$REG/termiplus-sockproxy:$TAG" "$ROOT" >/dev/null
  docker build -q -t "$REG/termiplus-terminal:$TAG" "$ROOT/image" >/dev/null
fi
docker build -q -t "$REG/termiplus-dns-forwarder:$TAG" "$ROOT/deploy/dns-forwarder" >/dev/null
TERM_IMG="$REG/termiplus-terminal:$TAG"

log "prepare deployment in $WORK (runtime $RUNTIME)"
cp -r "$ROOT/deploy" "$WORK/deploy"
mkdir -p "$WORK/deploy/secrets" "$WORK/deploy/netpolicy/hosts"
for s in proxy_token gateway_api_key key_secret admin_api_key; do
  openssl rand -hex 32 | tr -d '\n' >"$WORK/deploy/secrets/$s"
done
chmod 0644 "$WORK"/deploy/secrets/*
chown -R 65534:65534 "$WORK/deploy/netpolicy"
cat >"$WORK/deploy/policies.json" <<JSON
{
  "default": { "runtime": "$RUNTIME", "memory_mb": 1024, "cpus": 1, "pids": 512 },
  "scratch": { "runtime": "$RUNTIME", "memory_mb": 1024, "cpus": 1, "pids": 512,
               "storage": "ephemeral", "idle_timeout": "5m", "max_per_user": 2 }
}
JSON
cat >"$WORK/deploy/val.override.yml" <<YAML
services:
  sockproxy:
    environment:
      ALLOWED_RUNTIMES: "$RUNTIME"
YAML
# The operator's own resolver, as the forwarder's upstream (never a public one).
UPSTREAM="$(awk '/^nameserver/ && $2 !~ /^127\./ {print $2; exit}' /etc/resolv.conf)"
if [ -z "$UPSTREAM" ] && command -v resolvectl >/dev/null; then
  UPSTREAM="$(resolvectl dns 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i !~ /^127\./ && $i ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/) {print $i; exit}}' | head -1)"
fi
[ -n "$UPSTREAM" ] || { echo "no non-loopback resolver found for DNS_UPSTREAM" >&2; exit 1; }
cat >"$WORK/deploy/.env" <<ENV
TERMINAL_IMAGE=$TERM_IMG
TERMIPLUS_VERSION=$TAG
DOCKER_GID=$(stat -c %g /var/run/docker.sock)
DNS_UPSTREAM=$UPSTREAM
ENV

if ! docker network inspect owui >/dev/null 2>&1; then
  docker network create owui >/dev/null
  CREATED_OWUI=1
fi

log "start stack (base + netpolicy overlay)"
compose --env-file "$WORK/deploy/.env" up -d --no-build --pull never --wait --wait-timeout 180
"$WORK/deploy/firewall.sh" >/dev/null

log "internal service at $INT_IP (5432, 5433)"
ip netns add "$INT_NS"
ip link add tp-val-h type veth peer name tp-val-n
ip link set tp-val-n netns "$INT_NS"
ip addr add 10.99.0.1/24 dev tp-val-h
ip link set tp-val-h up
ip -n "$INT_NS" addr add "$INT_IP/24" dev tp-val-n
ip -n "$INT_NS" link set tp-val-n up
ip -n "$INT_NS" link set lo up
ip -n "$INT_NS" route add default via 10.99.0.1
sysctl -qw net.ipv4.ip_forward=1
ip netns exec "$INT_NS" python3 -c '
import socket, threading
def serve(port):
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("0.0.0.0", port)); s.listen(16)
    while True:
        c, _ = s.accept(); c.sendall(b"hello\n"); c.close()
for p in (5432, 5433):
    threading.Thread(target=serve, args=(p,), daemon=True).start()
threading.Event().wait()' >/dev/null 2>&1 &
INT_PID=$!

KEY="$(cat "$WORK/deploy/secrets/gateway_api_key")"
ADMIN="$(cat "$WORK/deploy/secrets/admin_api_key")"
docker run -d --name tp-val-client --network owui "$TERM_IMG" sleep infinity >/dev/null

# gw METHOD PATH [USER] [BODY] [extra curl args]: call the gateway as Open WebUI.
gw() {
  local m="$1" p="$2" u="${3:-}" b="${4:-}"
  shift 4 2>/dev/null || shift $#
  local args=(-s -m 120 -X "$m" -H "Authorization: Bearer $KEY")
  [ -n "$u" ] && args+=(-H "X-User-Id: $u")
  [ -n "$b" ] && args+=(-H 'Content-Type: application/json' -d "$b")
  docker exec tp-val-client curl "${args[@]}" "$@" "http://gateway:8080$p"
}
admin() { # METHOD [BODY] [KEY] -> "<status>"
  local args=(-s -o /dev/null -w '%{http_code}' -m 20 -X "$1")
  [ -n "${3-$ADMIN}" ] && args+=(-H "X-Admin-Key: ${3-$ADMIN}")
  [ -n "${2:-}" ] && args+=(-H 'Content-Type: application/json' -d "$2")
  docker exec tp-val-client curl "${args[@]}" http://gateway:8080/admin/netpolicy
}
ctr() { docker ps -q --filter "label=termiplus.user=$1" --filter "label=termiplus.policy=${2:-default}" | head -1; }
ip_of() { docker inspect -f '{{(index .NetworkSettings.Networks "ot-sandbox").IPAddress}}' "$1"; }
asroot() { local c="$1"; shift; docker exec -u 0 "$c" bash -c "$*"; }
asuser() { local c="$1"; shift; docker exec -u 1000 "$c" bash -c "$*"; }
tcp() { # container host port -> succeeds if a TCP connection is accepted within 3s
  asroot "$1" "timeout 4 bash -c 'exec 3<>/dev/tcp/$2/$3' "
}
reapply() { "$WORK/deploy/firewall.sh" >/dev/null; }
refuses() { # message, docker run args...: the container must exit with message in its log
  local msg="$1" out
  shift
  out="$(timeout 20 docker run --rm "$@" 2>&1 || true)"
  grep -qF "$msg" <<<"$out"
}

log "start terminals for val-a and val-b"
gw GET /files/cwd val-a >/dev/null
gw GET /files/cwd val-b >/dev/null
A="$(ctr val-a)"
B="$(ctr val-b)"
if [ -z "$A" ] || [ -z "$B" ]; then echo "terminals did not start" >&2; exit 1; fi
AIP="$(ip_of "$A")"
BIP="$(ip_of "$B")"
echo "A=$AIP B=$BIP"

# ---------------------------------------------------------------- Group A
scenario V1 "terminal -> terminal is blocked, with and without the host firewall"
for phase in "with firewall" "without firewall"; do
  expect "$phase: A pings B" 1 asroot "$A" "ping -c1 -W2 $BIP"
  expect "$phase: A reaches B:8000" 1 tcp "$A" "$BIP" 8000
  expect "$phase: A reaches B:22" 1 tcp "$A" "$BIP" 22
  if [ "$phase" = "with firewall" ]; then
    iptables -D DOCKER-USER -j TERMIPLUS 2>/dev/null || true
    iptables -D INPUT -j TERMIPLUS-IN 2>/dev/null || true
    ebtables -t filter -D FORWARD -j TERMIPLUS-AS 2>/dev/null || true
  fi
done
reapply
expect "gateway still reaches A" 0 test "$(gw GET /files/cwd val-a | grep -c cwd)" -ge 1

scenario V2 "gateway source-IP / ARP spoofing is blocked"
expect "ebtables chain TERMIPLUS-AS exists" 0 ebtables -t filter -L TERMIPLUS-AS
asroot "$B" "rm -f /tmp/got; (timeout 25 python3 -c '
import socket; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.bind((\"0.0.0.0\", 9999))
d, a = s.recvfrom(100); open(\"/tmp/got\",\"w\").write(a[0])' &) " || true
if asroot "$A" "ip addr add 172.30.0.2/24 dev eth0" 2>/dev/null; then
  asroot "$A" "python3 -c '
import socket; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.bind((\"172.30.0.2\", 0))
[s.sendto(b\"x\", (\"$BIP\", 9999)) for _ in range(5)]'" >/dev/null 2>&1 || true
  expect "A cannot reach B:8000 from the spoofed gateway IP" 1 asroot "$A" "curl -s -m3 --interface 172.30.0.2 http://$BIP:8000/health"
  asroot "$A" "ip addr del 172.30.0.2/24 dev eth0" 2>/dev/null || true
else
  pass "A cannot even add the gateway IP to its interface"
fi
sleep 2
expect "B received no packet with the spoofed source" 1 asroot "$B" "test -s /tmp/got"
expect "gateway -> terminal still works" 0 test "$(gw GET /files/cwd val-b | grep -c cwd)" -ge 1

scenario V3 "persistent volume owned by someone else is refused"
gw GET /files/cwd val-c >/dev/null
C="$(ctr val-c)"
FOREIGN_VOL="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/home/user"}}{{.Name}}{{end}}{{end}}' "$C")"
docker rm -f "$C" >/dev/null
docker volume rm "$FOREIGN_VOL" >/dev/null
docker volume create --label termiplus.managed=true --label termiplus.user=mallory "$FOREIGN_VOL" >/dev/null
sleep 11 # the gateway re-checks instances idle for more than 10s
code="$(gw GET /files/cwd val-c "" -o /dev/null -w '%{http_code}')"
expect "gateway refuses val-c with a foreign home volume (got $code)" 1 test "$code" = 200
expect "foreign volume keeps its owner label" 0 test "$(docker volume inspect -f '{{index .Labels "termiplus.user"}}' "$FOREIGN_VOL")" = mallory
expect "no container was started on it" 1 test -n "$(ctr val-c)"

# ---------------------------------------------------------------- Group B
scenario V4 "never-openable destinations"
expect "host via the bridge (172.30.0.1:22)" 1 tcp "$A" 172.30.0.1 22
expect "metadata 169.254.169.254:80" 1 tcp "$A" 169.254.169.254 80
expect "CGNAT 100.64.0.1:80" 1 tcp "$A" 100.64.0.1 80
expect "forwarder on a non-DNS port (172.30.0.3:80)" 1 tcp "$A" 172.30.0.3 80
expect "another sandbox host (172.30.0.200:80)" 1 tcp "$A" 172.30.0.200 80
expect "IPv6 has no route" 1 asroot "$A" "curl -6 -s -m3 http://[2606:4700:4700::1111]/"
expect "IPv4-mapped IPv6 to the host" 1 asroot "$A" "curl -g -s -m3 http://[::ffff:172.30.0.1]:22/"

scenario V5 "br_netfilter ordering and persistence"
expect "bridge-nf-call-iptables = 1" 0 test "$(sysctl -n net.bridge.bridge-nf-call-iptables)" = 1
expect "bridge-nf-call-ip6tables = 1" 0 test "$(sysctl -n net.bridge.bridge-nf-call-ip6tables)" = 1
expect "sysctl.d file shipped" 0 test -f "$ROOT/deploy/sysctl.d/90-termiplus-bridge.conf"
SHIM="$(mktemp -d)"
printf '#!/bin/sh\ncase "$*" in *-w*|*-qw*) exit 1;; esac\nexec /usr/sbin/sysctl "$@"\n' >"$SHIM/sysctl"
chmod +x "$SHIM/sysctl"
iptables -D DOCKER-USER -j TERMIPLUS 2>/dev/null || true
iptables -F TERMIPLUS 2>/dev/null || true
expect "firewall.sh fails when the sysctl cannot be set" 1 env PATH="$SHIM:$PATH" "$WORK/deploy/firewall.sh"
expect "and installed no rules" 1 iptables -C DOCKER-USER -j TERMIPLUS
rm -rf "$SHIM"
reapply

scenario V6 "default-deny internal + admin whitelist"
expect "internal 10.99.0.5:5432 blocked by default" 1 tcp "$A" "$INT_IP" 5432
expect "PUT egress allow 10.99.0.5/32 5432 tcp" 0 test "$(admin PUT "{\"dns_entries\":[],\"egress_allow\":[{\"cidr\":\"$INT_IP/32\",\"port\":5432,\"proto\":\"tcp\"}]}")" = 200
reapply
expect "5432 now reachable" 0 tcp "$A" "$INT_IP" 5432
expect "5433 still blocked" 1 tcp "$A" "$INT_IP" 5433
expect "PUT empty policy" 0 test "$(admin PUT '{"dns_entries":[],"egress_allow":[]}')" = 200
reapply
expect "5432 blocked again" 1 tcp "$A" "$INT_IP" 5432
code="$(admin PUT '{"dns_entries":[],"egress_allow":[{"cidr":"172.30.0.0/24","port":80,"proto":"tcp"}]}')"
expect "deny-list CIDR is rejected (got $code)" 0 test "${code:0:1}" = 4

scenario V7 "public egress works"
expect "https://1.1.1.1 connects" 0 asroot "$A" "curl -s -o /dev/null -m8 https://1.1.1.1"

# ---------------------------------------------------------------- Group C
scenario V8 "DNS via the forwarder"
expect "forwarder address pinned to its MAC" 0 bash -c "ebtables -t filter -L TERMIPLUS-AS | grep -q 172.30.0.3"
if [ "$RUNTIME" = runsc ]; then
  # gVisor (#3): the terminal talks to the forwarder directly.
  expect "resolv.conf points at 172.30.0.3" 0 asroot "$A" "grep -q '^nameserver 172.30.0.3' /etc/resolv.conf"
  expect "Docker's 127.0.0.11 is not usable" 1 asroot "$A" "dig +time=2 +tries=1 @127.0.0.11 example.com | grep -q 'status: NOERROR'"
else
  # runc/Sysbox: Docker's embedded resolver (Sysbox exposes it at the bridge
  # gateway), which forwards only to the pinned forwarder; V9 proves the path
  # with a record nobody else has.
  expect "resolv.conf names the forwarder or Docker's resolver" 0 asroot "$A" \
    "grep -qE '^nameserver (172\.30\.0\.3|127\.0\.0\.11|172\.30\.0\.1)\$' /etc/resolv.conf"
fi
expect "the forwarder answers directly" 0 asroot "$A" "dig +time=3 +tries=1 @172.30.0.3 example.com | grep -q 'status: NOERROR'"
expect "getent hosts example.com" 0 asroot "$A" "getent hosts example.com"
expect "curl -I https://example.com" 0 asroot "$A" "curl -sI -m10 https://example.com"

scenario V9 "custom DNS entry live reload"
expect "PUT db.internal -> 10.99.0.5" 0 test "$(admin PUT "{\"dns_entries\":[{\"name\":\"db.internal\",\"ip\":\"$INT_IP\"}],\"egress_allow\":[]}")" = 200
ok=1
for _ in $(seq 1 15); do
  if [ "$(asroot "$A" "getent hosts db.internal | cut -d' ' -f1" 2>/dev/null)" = "$INT_IP" ]; then ok=0; break; fi
  sleep 1
done
expect "db.internal resolves to $INT_IP within 15s" 0 test "$ok" = 0
admin PUT '{"dns_entries":[],"egress_allow":[]}' >/dev/null
ok=1
for _ in $(seq 1 15); do
  if ! asroot "$A" "getent hosts db.internal" >/dev/null 2>&1; then ok=0; break; fi
  sleep 1
done
expect "removed entry stops resolving within 15s" 0 test "$ok" = 0

scenario V10 "DNS cannot launder egress"
code="$(admin PUT '{"dns_entries":[{"name":"meta.internal","ip":"169.254.169.254"}],"egress_allow":[]}')"
if [ "${code:0:1}" = 4 ]; then
  pass "a DNS entry for a deny-listed IP is rejected ($code)"
else
  sleep 3
  expect "connection to the deny-listed IP behind the name is dropped" 1 asroot "$A" "curl -s -m3 http://meta.internal/"
fi
admin PUT "{\"dns_entries\":[{\"name\":\"db.internal\",\"ip\":\"$INT_IP\"}],\"egress_allow\":[]}" >/dev/null
sleep 3
expect "an allowed-looking name for a not-whitelisted internal IP is still blocked" 1 tcp "$A" db.internal 5432
admin PUT '{"dns_entries":[],"egress_allow":[]}' >/dev/null

# ---------------------------------------------------------------- Group E
scenario V12 "admin API key"
expect "no key -> 401" 0 test "$(admin GET "" "")" = 401
expect "wrong key -> 401" 0 test "$(admin GET "" wrong-key-0123456789abcdef0123456789)" = 401
expect "connection key as admin key -> 401" 0 test "$(admin GET "" "$KEY")" = 401
expect "admin key -> 200" 0 test "$(admin GET)" = 200
GW_IMG="$REG/termiplus-gateway:$TAG"
base_env=(--tmpfs "/tmp:rw,mode=1777" -e GATEWAY_API_KEY="$KEY" -e KEY_SECRET="$(cat "$WORK/deploy/secrets/key_secret")" -e TERMINAL_IMAGE=x
  -e DOCKER_API=http://127.0.0.1:1 -e PROXY_TOKEN=x -e NETPOLICY_FILE=/tmp/p.json
  -e NETPOLICY_EGRESS_FILE=/tmp/e -e NETPOLICY_DNSMASQ_FILE=/tmp/d)
expect "gateway refuses a short admin key" 0 refuses "admin API key must be at least 32 characters" "${base_env[@]}" -e ADMIN_API_KEY=short "$GW_IMG"
expect "gateway refuses admin key == connection key" 0 refuses "admin API key must differ from the connection API key" "${base_env[@]}" -e ADMIN_API_KEY="$KEY" "$GW_IMG"

scenario V13 "mandatory PROXY_TOKEN"
SP_IMG="$REG/termiplus-sockproxy:$TAG"
sp_env=(-e TERMINAL_IMAGE=x -e ALLOWED_RUNTIMES="$RUNTIME" -e SANDBOX_NETWORK=ot-sandbox)
expect "sockproxy refuses to start without PROXY_TOKEN" 0 refuses "PROXY_TOKEN is required" "${sp_env[@]}" "$SP_IMG"
expect "sockproxy refuses a short PROXY_TOKEN" 0 refuses "PROXY_TOKEN is required" "${sp_env[@]}" -e PROXY_TOKEN=short "$SP_IMG"
docker rm -f tp-val-sp >/dev/null 2>&1 || true
docker run -d --name tp-val-sp "${sp_env[@]}" -e PROXY_TOKEN="$(openssl rand -hex 32)" "$SP_IMG" >/dev/null
sleep 3
expect "sockproxy starts with a valid token" 0 test "$(docker inspect -f '{{.State.Running}}' tp-val-sp)" = true
docker rm -f tp-val-sp >/dev/null 2>&1 || true

# ---------------------------------------------------------------- Group F
scenario V14 "root in the container is not root on the host"
expect "sudo works inside" 0 asuser "$A" "sudo -n true"
expect "PID 1 is the sandbox's init, not the host's systemd" 1 asuser "$A" "sudo -n cat /proc/1/cmdline | tr '\0' ' ' | grep -q systemd"
expect "host disk device not visible" 1 asuser "$A" "ls /dev | grep -qE '^(sd|vd|nvme)'"
expect "mknod + read of the host disk fails" 1 asuser "$A" "sudo -n mknod /tmp/hd b 254 0 && sudo -n head -c1 /tmp/hd"
expect "loading a kernel module fails" 1 asuser "$A" "sudo -n modprobe dummy && test -d /sys/module/dummy"
expect "mounting the host root fails" 1 asuser "$A" "sudo -n mkdir -p /mnt/h && sudo -n mount /dev/vda1 /mnt/h"
expect "host hostname not visible" 1 asuser "$A" "test \"\$(hostname)\" = \"$(hostname)\""
expect "sysrq / kernel writes fail" 1 asuser "$A" "echo h | sudo -n tee /proc/sysrq-trigger"

scenario V15 "ephemeral profile and provisioning"
gw POST /p/scratch/execute val-a '{"command":"echo marker > ~/eph"}' -H 'X-Session-Id: chat-one' >/dev/null
sleep 2
E1="$(ctr val-a scratch)"
expect "ephemeral container has no home volume" 1 test -n "$(docker inspect -f '{{range .Mounts}}{{.Name}}{{end}}' "$E1")"
expect "file written in chat-one" 0 asuser "$E1" "test -f ~/eph"
gw GET /p/scratch/files/cwd val-a "" -H 'X-Session-Id: chat-two' >/dev/null
E2="$(docker ps -q --filter label=termiplus.user=val-a --filter label=termiplus.policy=scratch | grep -v "$E1" | head -1)"
expect "a new chat gets a fresh container" 0 test -n "$E2"
expect "the file is not there" 1 asuser "$E2" "test -f ~/eph"
expect "profile tools present (git, python3, node)" 0 asuser "$E2" "command -v git && command -v python3 && command -v node"

log "summary (runtime $RUNTIME)"
rc=0
for s in V1 V2 V3 V4 V5 V6 V7 V8 V9 V10 V12 V13 V14 V15; do
  printf '%-4s %s\n' "$s" "${RESULT[$s]:-not run}"
  [ "${RESULT[$s]:-FAIL}" = PASS ] || rc=1
done
echo "V11 is covered by run.sh (socket proxy escape attempts)."
exit "$rc"
