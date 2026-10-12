#!/bin/sh
# Egress rules for the terminal sandbox network. Run on the VM after
# `docker compose up` (and after every Docker restart, e.g. via a systemd unit).
#
# Terminals may reach the internet, nothing internal:
#   - no other terminals on the same bridge
#   - no private ranges (your LAN, Proxmox, customer networks)
#   - no services on the VM itself
# The gateway (172.30.0.2) may reach every terminal. That allow is pinned to the
# gateway's real MAC at L2 (ebtables) so a sandboxed root cannot spoof the
# gateway's source IP to reach another terminal (#13), and the sandbox bridge
# runs with enable_icc:false so terminal<->terminal isolation does not fail open
# if these rules are ever missing (#14).
#
# Admin-set exceptions: the gateway renders deploy/netpolicy/egress.rules from
# the validated network policy (admin API, #12). Each line "CIDR PORT PROTO"
# opens exactly one internal destination. They are applied AFTER the fixed
# deny-list below and re-checked here, so no entry can ever open a never-allowed
# range (loopback, link-local/metadata, CGNAT, the sandbox subnet itself).
set -eu

SANDBOX=172.30.0.0/24
GATEWAY=172.30.0.2
BRIDGE=ot-sandbox

SCRIPT_DIR=$(cd -- "$(dirname -- "$0")" && pwd)
EGRESS_RULES=${EGRESS_RULES_FILE:-"$SCRIPT_DIR/netpolicy/egress.rules"}

# apply_egress_allow turns each validated line of the rendered rules file into a
# RETURN rule (sandbox -> CIDR [dport PORT] -p PROTO). It re-validates the shape
# and refuses any clearly never-allowed destination even though the gateway
# already validated it, because this file lives on the host and could be
# tampered with. The deny DROP rules placed before this in the chain are the
# real guarantee; these checks only skip malformed or hostile lines.
apply_egress_allow() {
  [ -f "$EGRESS_RULES" ] || return 0
  while read -r cidr port proto _; do
    case "$cidr" in '' | '#'*) continue ;; esac
    if ! echo "$cidr" | grep -Eq '^[0-9]{1,3}(\.[0-9]{1,3}){3}/[0-9]{1,2}$'; then
      echo "firewall: skipping malformed cidr '$cidr'" >&2
      continue
    fi
    case "$proto" in
      tcp | udp) ;;
      *)
        echo "firewall: skipping bad proto '$proto' for $cidr" >&2
        continue
        ;;
    esac
    case "$cidr" in
      127.* | 169.254.* | 172.30.0.* | 100.6[4-9].* | 100.[7-9][0-9].* | 100.1[0-1][0-9].* | 100.12[0-7].*)
        echo "firewall: refusing never-allowed destination '$cidr'" >&2
        continue
        ;;
    esac
    if [ "$port" = any ]; then
      iptables -A TERMIPLUS -s "$SANDBOX" -d "$cidr" -p "$proto" -j RETURN
    elif echo "$port" | grep -Eq '^[0-9]{1,5}$' && [ "$port" -ge 1 ] && [ "$port" -le 65535 ]; then
      iptables -A TERMIPLUS -s "$SANDBOX" -d "$cidr" -p "$proto" --dport "$port" -j RETURN
    else
      echo "firewall: skipping bad port '$port' for $cidr" >&2
    fi
  done <"$EGRESS_RULES"
}

# Bridge-netfilter must be on BEFORE any DROP rule is installed. Same-bridge
# container<->container traffic is bridged, not routed, so the terminal<->terminal
# DROP only takes effect once bridge-nf-call-ip(6)tables=1. If the module can't
# load or the sysctl won't take, fail closed and install no rules at all, rather
# than leave DROP rules that look present while bridged traffic bypasses them.
# (Persisted across reboot by deploy/sysctl.d/90-termiplus-bridge.conf.)
ensure_bridge_netfilter() {
  modprobe br_netfilter 2>/dev/null || true
  for key in net.bridge.bridge-nf-call-iptables net.bridge.bridge-nf-call-ip6tables; do
    if ! sysctl -qw "$key=1" 2>/dev/null; then
      echo "firewall: cannot set $key (br_netfilter unavailable) - refusing to install rules" >&2
      exit 1
    fi
    val=$(sysctl -n "$key" 2>/dev/null || echo "")
    if [ "$val" != 1 ]; then
      echo "firewall: $key is '$val', not 1 - refusing to install rules (terminal<->terminal would fail open)" >&2
      exit 1
    fi
  done
}
ensure_bridge_netfilter

# ensure_gateway_antispoof pins the gateway IP (172.30.0.2) to the gateway
# container's real MAC at L2. With enable_icc:false the gateway->terminal rule
# below must be ACCEPT (to beat Docker's inter-container DROP), and ACCEPT on a
# bare source IP is spoofable on the shared bridge - a sandboxed root could
# forge src=172.30.0.2 and ride it to another terminal. This drops any frame
# (IPv4 or ARP) claiming the gateway's IP from any other MAC, before iptables
# sees it. Needs ebtables and the gateway container running; fails closed
# (installs nothing) if either is missing, since the ACCEPT would be unsafe (#13).
ensure_gateway_antispoof() {
  if ! command -v ebtables >/dev/null 2>&1; then
    echo "firewall: ebtables not found - refusing, gateway source IP would be spoofable (#13)" >&2
    exit 1
  fi
  mac=$(docker network inspect "$BRIDGE" \
    --format '{{range .Containers}}{{.IPv4Address}} {{.MacAddress}}{{"\n"}}{{end}}' 2>/dev/null \
    | awk -v ip="$GATEWAY" '{split($1, a, "/"); if (a[1] == ip) print $2}')
  if [ -z "$mac" ]; then
    echo "firewall: no MAC for gateway $GATEWAY on $BRIDGE - is the gateway running? refusing (#13)" >&2
    exit 1
  fi
  ebtables -t filter -N TERMIPLUS-AS 2>/dev/null || ebtables -t filter -F TERMIPLUS-AS
  ebtables -t filter -A TERMIPLUS-AS -p IPv4 --ip-source "$GATEWAY" -s ! "$mac" -j DROP
  ebtables -t filter -A TERMIPLUS-AS -p ARP --arp-ip-src "$GATEWAY" -s ! "$mac" -j DROP
  ebtables -t filter -C FORWARD -j TERMIPLUS-AS 2>/dev/null || ebtables -t filter -I FORWARD 1 -j TERMIPLUS-AS
}
ensure_gateway_antispoof

# Forwarded traffic (container -> elsewhere). DOCKER-USER runs before Docker's own rules.
iptables -N TERMIPLUS 2>/dev/null || iptables -F TERMIPLUS
# ESTABLISHED and the gateway->terminal allow are ACCEPT, not RETURN: ACCEPT
# terminates traversal so Docker's enable_icc:false inter-container DROP (which
# runs in FORWARD after DOCKER-USER) does not then drop these wanted flows. The
# gateway source IP is made unspoofable by ensure_gateway_antispoof above.
iptables -A TERMIPLUS -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A TERMIPLUS -s "$GATEWAY" -d "$SANDBOX" -j ACCEPT
# terminal <-> terminal: dropped here (belt) and again by Docker's icc:false drop.
iptables -A TERMIPLUS -s "$SANDBOX" -d "$SANDBOX" -j DROP
# Fixed deny-list: dropped before any admin allow rule can match it, so these
# ranges can never be opened by the network policy.
for net in 100.64.0.0/10 169.254.0.0/16; do
  iptables -A TERMIPLUS -s "$SANDBOX" -d "$net" -j DROP
done
# Admin-set allowances for specific internal destinations (default-deny for
# private ranges; public egress stays allowed by the implicit RETURN below).
apply_egress_allow
# Everything else private stays denied unless an allow rule above returned first.
for net in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16; do
  iptables -A TERMIPLUS -s "$SANDBOX" -d "$net" -j DROP
done
iptables -C DOCKER-USER -j TERMIPLUS 2>/dev/null || iptables -I DOCKER-USER 1 -j TERMIPLUS

# Traffic from terminals to the VM itself (INPUT): drop. DNS still works,
# Docker's embedded resolver (127.0.0.11) lives inside each container's
# network namespace and is not affected by this rule.
iptables -N TERMIPLUS-IN 2>/dev/null || iptables -F TERMIPLUS-IN
iptables -A TERMIPLUS-IN -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
iptables -A TERMIPLUS-IN -i "$BRIDGE" -j DROP
iptables -C INPUT -j TERMIPLUS-IN 2>/dev/null || iptables -I INPUT 1 -j TERMIPLUS-IN

# No IPv6 for the sandbox (the network is IPv4 only). Block all three paths,
# mirroring the IPv4 rules: routed egress (FORWARD), terminal->host (INPUT),
# and same-bridge terminal<->terminal (needs the v6 bridge sysctl set above).
# A blanket drop is correct here because terminals have no legitimate IPv6.
ip6tables -C FORWARD -i "$BRIDGE" -j DROP 2>/dev/null || ip6tables -I FORWARD 1 -i "$BRIDGE" -j DROP
ip6tables -C INPUT -i "$BRIDGE" -j DROP 2>/dev/null || ip6tables -I INPUT 1 -i "$BRIDGE" -j DROP

echo "termiplus firewall rules applied"
