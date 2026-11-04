#!/bin/sh
# Egress rules for the terminal sandbox network. Run on the VM after
# `docker compose up` (and after every Docker restart, e.g. via a systemd unit).
#
# Terminals may reach the internet, nothing internal:
#   - no other terminals on the same bridge
#   - no private ranges (your LAN, Proxmox, customer networks)
#   - no services on the VM itself
# The gateway (172.30.0.2) may reach every terminal. That allow is pinned at L2
# (ebtables) to the gateway's real MAC and to its own bridge port, so a
# sandboxed root cannot spoof the gateway's source IP to reach another terminal
# (#13), not even by forging the gateway's MAC as well, and the sandbox bridge
# runs with enable_icc:false so terminal<->terminal isolation does not fail open
# if these rules are ever missing (#14).
#
# Admin-set exceptions: the gateway renders deploy/netpolicy/egress.rules from
# the validated network policy (admin API, #12). Each line "CIDR PORT PROTO"
# opens exactly one internal destination. They are applied AFTER the fixed
# deny-list below and re-checked here, so no entry can ever open a never-allowed
# range (loopback, link-local/metadata, CGNAT, the sandbox subnet itself).
#
# Internal DNS forwarder (netpolicy overlay): if a container holds the
# forwarder address (172.30.0.3) on the bridge, terminals may send DNS (udp/tcp
# 53) to it and nothing else, the forwarder may send DNS to its upstream
# (DNS_UPSTREAM from the environment or deploy/.env) and nothing else, and its
# address is pinned to its MAC like the gateway's. Without the forwarder none
# of these rules exist.
#
# Offline network (offline overlay, "egress": "offline" policies): if the
# ot-offline bridge exists, everything entering or leaving it is dropped, both
# routed and to the host, IPv4 and IPv6. The only exception is the gateway
# (172.30.1.2, MAC- and port-pinned like on the sandbox bridge) opening connections to
# offline terminals, plus the replies on those connections. The bridge is a
# Docker internal network with enable_icc:false, so it has no route out even
# without these rules; they are the second layer.
set -eu

SANDBOX=172.30.0.0/24
GATEWAY=172.30.0.2
FORWARDER=${FORWARDER_IP:-172.30.0.3}
BRIDGE=ot-sandbox
OFFLINE_BRIDGE=ot-offline
OFFLINE_SUBNET=172.30.1.0/24
OFFLINE_GATEWAY=172.30.1.2

SCRIPT_DIR=$(cd -- "$(dirname -- "$0")" && pwd)
EGRESS_RULES=${EGRESS_RULES_FILE:-"$SCRIPT_DIR/netpolicy/egress.rules"}
if [ -z "${DNS_UPSTREAM:-}" ] && [ -f "$SCRIPT_DIR/.env" ]; then
  DNS_UPSTREAM=$(sed -n 's/^DNS_UPSTREAM=//p' "$SCRIPT_DIR/.env" | tail -n 1 | tr -d "\"' ")
fi
DNS_UPSTREAM=${DNS_UPSTREAM:-}

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
      127.* | 169.254.* | 172.30.0.* | 172.30.1.* | 100.6[4-9].* | 100.[7-9][0-9].* | 100.1[0-1][0-9].* | 100.12[0-7].*)
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
# sees it, and any frame on the bridge claiming the gateway's IP or MAC that
# does not enter through the gateway's own bridge port (so forging both does
# not help either). Needs ebtables, nsenter and the gateway container running;
# fails closed (keeps the previous pins, adds nothing) if any is missing, since
# the ACCEPT would be unsafe (#13).
# mac_of prints the MAC of the container holding IP on the bridge (or on the
# network named by the second argument), or nothing.
mac_of() {
  docker network inspect "${2:-$BRIDGE}" \
    --format '{{range .Containers}}{{.IPv4Address}} {{.MacAddress}}{{"\n"}}{{end}}' 2>/dev/null \
    | awk -v ip="$1" '{split($1, a, "/"); if (a[1] == ip) print $2}'
}

# port_of prints the host-side bridge port (veth) of the container holding IP
# on the network named by the second argument (network and bridge share a
# name), or nothing. It asks the container's own network namespace which
# interface carries that MAC and which host interface is its peer, so it does
# not trust the bridge's learned MAC table, which a forger could poison.
port_of() {
  name=$(docker network inspect "$2" \
    --format '{{range .Containers}}{{.IPv4Address}} {{.Name}}{{"\n"}}{{end}}' 2>/dev/null \
    | awk -v ip="$1" '{split($1, a, "/"); if (a[1] == ip) print $2}')
  [ -n "$name" ] || return 0
  m=$(mac_of "$1" "$2")
  [ -n "$m" ] || return 0
  pid=$(docker inspect -f '{{.State.Pid}}' "$name" 2>/dev/null) || return 0
  [ "${pid:-0}" -gt 0 ] 2>/dev/null || return 0
  # "2: eth0@if17: <...> ... link/ether MAC ..." -> 17, the peer's ifindex
  idx=$(nsenter -t "$pid" -n ip -o link 2>/dev/null \
    | awk -v m="$m" 'index(tolower($0), tolower(m)) {split($2, a, "@if"); sub(/:$/, "", a[2]); print a[2]; exit}')
  [ -n "$idx" ] || return 0
  for dev in /sys/class/net/*; do
    [ "$(cat "$dev/ifindex" 2>/dev/null)" = "$idx" ] || continue
    if [ "$(basename "$(readlink "$dev/master" 2>/dev/null)")" = "$2" ]; then
      basename "$dev"
    fi
    return 0
  done
}

# pin_ip IP MAC BRIDGE PORT drops every frame (IPv4 or ARP) that claims IP
# from any MAC but MAC (filter FORWARD), and every frame on BRIDGE that claims
# IP or MAC from any port but PORT (nat PREROUTING). The port pin is what stops
# a sandboxed root that forges both the IP and the MAC; it runs before the
# bridge learns MACs, so forged frames cannot redirect the real one's traffic.
pin_ip() {
  ebtables -t filter -A TERMIPLUS-AS -p IPv4 --ip-source "$1" -s ! "$2" -j DROP
  ebtables -t filter -A TERMIPLUS-AS -p ARP --arp-ip-src "$1" -s ! "$2" -j DROP
  ebtables -t nat -A TERMIPLUS-PORT -p IPv4 --ip-source "$1" --logical-in "$3" ! -i "$4" -j DROP
  ebtables -t nat -A TERMIPLUS-PORT -p ARP --arp-ip-src "$1" --logical-in "$3" ! -i "$4" -j DROP
  ebtables -t nat -A TERMIPLUS-PORT -s "$2" --logical-in "$3" ! -i "$4" -j DROP
}

# port_pinned LISTING IP NETWORK: the port pin for IP names the container's
# current bridge port.
port_pinned() {
  pp=$(port_of "$2" "$3")
  [ -n "$pp" ] || return 1
  echo "$1" | grep -F -- "--ip-src $2 " | grep -qF -- "! -i $pp " || return 1
}

# offline_present: the offline overlay's bridge exists on this host.
offline_present() { ip link show "$OFFLINE_BRIDGE" >/dev/null 2>&1; }

# --if-needed (for the termiplus-firewall-watch timer): only (re)apply when the
# rules are missing or a pinned MAC no longer matches the running gateway or
# forwarder, e.g. after a reboot or a recreated gateway. A full run briefly
# rebuilds the chains, so it is not repeated while everything is current.
rules_current() {
  iptables -C DOCKER-USER -j TERMIPLUS 2>/dev/null || return 1
  iptables -C INPUT -j TERMIPLUS-IN 2>/dev/null || return 1
  pinned=$(ebtables -t filter -L TERMIPLUS-AS 2>/dev/null) || return 1
  ebtables -t filter -C FORWARD -j TERMIPLUS-AS 2>/dev/null || return 1
  ports=$(ebtables -t nat -L TERMIPLUS-PORT 2>/dev/null) || return 1
  ebtables -t nat -C PREROUTING -j TERMIPLUS-PORT 2>/dev/null || return 1
  for ip in "$GATEWAY" "$FORWARDER"; do
    m=$(mac_of "$ip")
    [ -z "$m" ] && continue
    echo "$pinned" | grep -F -- "--ip-src $ip " | grep -qiF -- "$m" || return 1
    port_pinned "$ports" "$ip" "$BRIDGE" || return 1
  done
  if offline_present; then
    iptables -C TERMIPLUS -i "$OFFLINE_BRIDGE" -j DROP 2>/dev/null || return 1
    iptables -C TERMIPLUS-IN -i "$OFFLINE_BRIDGE" -j DROP 2>/dev/null || return 1
    iptables -C TERMIPLUS -o "$OFFLINE_BRIDGE" -j DROP 2>/dev/null || return 1
    ip6tables -C FORWARD -i "$OFFLINE_BRIDGE" -j DROP 2>/dev/null || return 1
    ip6tables -C FORWARD -o "$OFFLINE_BRIDGE" -j DROP 2>/dev/null || return 1
    ip6tables -C INPUT -i "$OFFLINE_BRIDGE" -j DROP 2>/dev/null || return 1
    m=$(mac_of "$OFFLINE_GATEWAY" "$OFFLINE_BRIDGE")
    if [ -n "$m" ]; then
      echo "$pinned" | grep -F -- "--ip-src $OFFLINE_GATEWAY " | grep -qiF -- "$m" || return 1
      port_pinned "$ports" "$OFFLINE_GATEWAY" "$OFFLINE_BRIDGE" || return 1
      iptables -C TERMIPLUS -i "$OFFLINE_BRIDGE" -o "$OFFLINE_BRIDGE" -s "$OFFLINE_GATEWAY" -d "$OFFLINE_SUBNET" -j ACCEPT 2>/dev/null || return 1
      iptables -C TERMIPLUS -i "$OFFLINE_BRIDGE" -o "$OFFLINE_BRIDGE" -s "$OFFLINE_SUBNET" -d "$OFFLINE_GATEWAY" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || return 1
    fi
  fi
}
if [ "${1:-}" = --if-needed ]; then
  if [ -z "$(mac_of "$GATEWAY")" ]; then
    exit 0 # gateway not (yet) running: nothing to pin, try again later
  fi
  if rules_current; then
    exit 0
  fi
  echo "firewall: rules missing or outdated - applying"
fi

ensure_gateway_antispoof() {
  if ! command -v ebtables >/dev/null 2>&1; then
    echo "firewall: ebtables not found - refusing, gateway source IP would be spoofable (#13)" >&2
    exit 1
  fi
  if ! command -v nsenter >/dev/null 2>&1; then
    echo "firewall: nsenter not found - refusing, cannot pin the gateway to its bridge port (#13)" >&2
    exit 1
  fi
  # Look everything up before touching a rule, so a refusal leaves the
  # previous pins in place.
  mac=$(mac_of "$GATEWAY")
  if [ -z "$mac" ]; then
    echo "firewall: no MAC for gateway $GATEWAY on $BRIDGE - is the gateway running? refusing (#13)" >&2
    exit 1
  fi
  port=$(port_of "$GATEWAY" "$BRIDGE")
  if [ -z "$port" ]; then
    echo "firewall: no bridge port for gateway $GATEWAY on $BRIDGE - refusing (#13)" >&2
    exit 1
  fi
  # The forwarder's address gets the same pins, so its DNS allowances below
  # cannot be ridden by a terminal claiming to be the forwarder.
  forwarder_mac=$(mac_of "$FORWARDER")
  forwarder_port=""
  if [ -n "$forwarder_mac" ]; then
    forwarder_port=$(port_of "$FORWARDER" "$BRIDGE")
    if [ -z "$forwarder_port" ]; then
      echo "firewall: no bridge port for forwarder $FORWARDER on $BRIDGE - refusing" >&2
      exit 1
    fi
  fi
  # The gateway's address on the offline bridge, same reason. Without it the
  # gateway gets no allow there and offline terminals are unreachable (closed).
  offline_gateway_mac=""
  offline_gateway_port=""
  if offline_present; then
    offline_gateway_mac=$(mac_of "$OFFLINE_GATEWAY" "$OFFLINE_BRIDGE")
    if [ -n "$offline_gateway_mac" ]; then
      offline_gateway_port=$(port_of "$OFFLINE_GATEWAY" "$OFFLINE_BRIDGE")
      if [ -z "$offline_gateway_port" ]; then
        echo "firewall: no bridge port for gateway $OFFLINE_GATEWAY on $OFFLINE_BRIDGE - refusing" >&2
        exit 1
      fi
    else
      echo "firewall: no gateway on $OFFLINE_BRIDGE - offline terminals stay unreachable" >&2
    fi
  fi

  ebtables -t filter -N TERMIPLUS-AS 2>/dev/null || ebtables -t filter -F TERMIPLUS-AS
  # User chains in ebtables default to ACCEPT, which would end PREROUTING.
  ebtables -t nat -N TERMIPLUS-PORT -P RETURN 2>/dev/null || ebtables -t nat -F TERMIPLUS-PORT
  ebtables -t nat -P TERMIPLUS-PORT RETURN
  pin_ip "$GATEWAY" "$mac" "$BRIDGE" "$port"
  if [ -n "$forwarder_mac" ]; then
    pin_ip "$FORWARDER" "$forwarder_mac" "$BRIDGE" "$forwarder_port"
  fi
  if [ -n "$offline_gateway_mac" ]; then
    pin_ip "$OFFLINE_GATEWAY" "$offline_gateway_mac" "$OFFLINE_BRIDGE" "$offline_gateway_port"
  fi
  ebtables -t filter -C FORWARD -j TERMIPLUS-AS 2>/dev/null || ebtables -t filter -I FORWARD 1 -j TERMIPLUS-AS
  ebtables -t nat -C PREROUTING -j TERMIPLUS-PORT 2>/dev/null || ebtables -t nat -I PREROUTING 1 -j TERMIPLUS-PORT
}
ensure_gateway_antispoof

# Each forwarder upstream (DNS_UPSTREAM, comma-separated) must be a plain IPv4
# address outside the never-allowed ranges; anything else gets no rule.
upstream_ok() {
  echo "$1" | grep -Eq '^[0-9]{1,3}(\.[0-9]{1,3}){3}$' || return 1
  case "$1" in
    127.* | 169.254.* | 172.30.0.* | 172.30.1.* | 100.6[4-9].* | 100.[7-9][0-9].* | 100.1[0-1][0-9].* | 100.12[0-7].*) return 1 ;;
  esac
}
upstreams() { echo "$DNS_UPSTREAM" | tr -s ', ' '\n' | grep -v '^$' || true; }

# Forwarded traffic (container -> elsewhere). DOCKER-USER runs before Docker's own rules.
iptables -N TERMIPLUS 2>/dev/null || iptables -F TERMIPLUS
# Offline bridge first, by interface, so no rule below (all keyed on the
# sandbox subnet or on admin allowances) can ever let its traffic through.
if offline_present; then
  if [ -n "${offline_gateway_mac:-}" ]; then
    iptables -A TERMIPLUS -i "$OFFLINE_BRIDGE" -o "$OFFLINE_BRIDGE" -s "$OFFLINE_GATEWAY" -d "$OFFLINE_SUBNET" -j ACCEPT
    iptables -A TERMIPLUS -i "$OFFLINE_BRIDGE" -o "$OFFLINE_BRIDGE" -s "$OFFLINE_SUBNET" -d "$OFFLINE_GATEWAY" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
  fi
  iptables -A TERMIPLUS -i "$OFFLINE_BRIDGE" -j DROP
  iptables -A TERMIPLUS -o "$OFFLINE_BRIDGE" -j DROP
fi
# The gateway->terminal allow is ACCEPT, not RETURN: ACCEPT terminates
# traversal so Docker's enable_icc:false inter-container DROP (which runs in
# FORWARD after DOCKER-USER) does not drop this required flow. Do not install a
# broad ESTABLISHED,RELATED ACCEPT here: DOCKER-USER is global to Docker
# forwarding, and that rule would bypass other networks' policy and current
# egress restrictions for long-lived connections. Docker handles reply traffic;
# outbound packets must continue through the current egress policy below.
iptables -A TERMIPLUS -s "$GATEWAY" -d "$SANDBOX" -j ACCEPT
# Replies on those connections. They are sandbox -> sandbox too, so without this
# the terminal <-> terminal drop below would cut every gateway request. Scoped to
# the gateway as destination and to connections conntrack has already seen
# (opened by the gateway; a terminal's own new packets to it are never accepted),
# unlike a global ESTABLISHED,RELATED rule.
iptables -A TERMIPLUS -s "$SANDBOX" -d "$GATEWAY" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
# terminal -> forwarder DNS (only with the forwarder present). ACCEPT, as for the
# gateway, to beat Docker's icc:false drop; the forwarder's address is MAC-pinned.
if [ -n "${forwarder_mac:-}" ]; then
  for p in udp tcp; do
    iptables -A TERMIPLUS -s "$SANDBOX" -d "$FORWARDER" -p "$p" --dport 53 -j ACCEPT
    # and its answers back to the terminal, only on those DNS connections
    iptables -A TERMIPLUS -s "$FORWARDER" -d "$SANDBOX" -p "$p" --sport 53 -m conntrack --ctstate ESTABLISHED -j ACCEPT
  done
  for up in $(upstreams); do
    if upstream_ok "$up"; then
      for p in udp tcp; do
        iptables -A TERMIPLUS -s "$FORWARDER" -d "$up" -p "$p" --dport 53 -j RETURN
      done
    else
      echo "firewall: DNS upstream '$up' unusable - the forwarder gets no rule for it" >&2
    fi
  done
fi
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
# Nothing on the offline bridge may talk to the host, not even on connections
# that look established (checked before the ESTABLISHED,RELATED return).
if offline_present; then
  iptables -A TERMIPLUS-IN -i "$OFFLINE_BRIDGE" -j DROP
fi
iptables -A TERMIPLUS-IN -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
# An upstream resolver on this host itself: only the forwarder may ask it.
if [ -n "${forwarder_mac:-}" ]; then
  for up in $(upstreams); do
    if upstream_ok "$up" && ip -4 -o addr show | awk '{print $4}' | cut -d/ -f1 | grep -qx "$up"; then
      for p in udp tcp; do
        iptables -A TERMIPLUS-IN -i "$BRIDGE" -s "$FORWARDER" -d "$up" -p "$p" --dport 53 -j RETURN
      done
    fi
  done
fi
iptables -A TERMIPLUS-IN -i "$BRIDGE" -j DROP
iptables -C INPUT -j TERMIPLUS-IN 2>/dev/null || iptables -I INPUT 1 -j TERMIPLUS-IN

# No IPv6 for the sandbox (the network is IPv4 only). Block all three paths,
# mirroring the IPv4 rules: routed egress (FORWARD), terminal->host (INPUT),
# and same-bridge terminal<->terminal (needs the v6 bridge sysctl set above).
# A blanket drop is correct here because terminals have no legitimate IPv6.
ip6tables -C FORWARD -i "$BRIDGE" -j DROP 2>/dev/null || ip6tables -I FORWARD 1 -i "$BRIDGE" -j DROP
ip6tables -C INPUT -i "$BRIDGE" -j DROP 2>/dev/null || ip6tables -I INPUT 1 -i "$BRIDGE" -j DROP
if offline_present; then
  ip6tables -C FORWARD -i "$OFFLINE_BRIDGE" -j DROP 2>/dev/null || ip6tables -I FORWARD 1 -i "$OFFLINE_BRIDGE" -j DROP
  ip6tables -C FORWARD -o "$OFFLINE_BRIDGE" -j DROP 2>/dev/null || ip6tables -I FORWARD 1 -o "$OFFLINE_BRIDGE" -j DROP
  ip6tables -C INPUT -i "$OFFLINE_BRIDGE" -j DROP 2>/dev/null || ip6tables -I INPUT 1 -i "$OFFLINE_BRIDGE" -j DROP
fi

echo "termiplus firewall rules applied"
