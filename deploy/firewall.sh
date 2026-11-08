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
# Internal DNS forwarder (netpolicy overlay): if the dns-forwarder service owns the
# forwarder address (172.30.0.3) on the bridge, terminals may send DNS (udp/tcp
# 53) to it and nothing else, the forwarder may send DNS to its upstream
# (DNS_UPSTREAM from the environment or deploy/.env) and nothing else, and its
# address is pinned to its MAC like the gateway's. Its address always has a
# final DROP while that verified service is present.
#
# Offline network (offline overlay, "egress": "offline" policies): if the
# ot-offline bridge exists, everything entering or leaving it is dropped, both
# routed and to the host, IPv4 and IPv6. The only exception is the gateway
# (172.30.1.2, MAC- and port-pinned like on the sandbox bridge) opening connections to
# offline terminals, plus the replies on those connections. The bridge is a
# Docker internal network with enable_icc:false, so it has no route out even
# without these rules; they are the second layer.
#
# Locked bridge ports: every other port on both bridges (the terminals) runs
# with learning off and "locked on", and gets a static FDB entry for the MAC
# Docker assigned to that container. A terminal that forges another terminal's
# MAC can then neither move that MAC to its own port nor send with it, so it
# never receives the gateway's replies (which carry the other terminal's API
# key). Source guards also bind every port to Docker's IPv4 and MAC, so a
# terminal using its own MAC cannot announce another terminal's IP via ARP.
# New terminals are locked by --watch-ports (termiplus-portlock.service)
# within a second; a port whose MAC is not known yet stays locked without an
# entry, i.e. without traffic. Needs Linux and iproute2 >= 6.2.
set -eu

SANDBOX=172.30.0.0/24
GATEWAY=172.30.0.2
FORWARDER=${FORWARDER_IP:-172.30.0.3}
BRIDGE=ot-sandbox
OFFLINE_BRIDGE=ot-offline
OFFLINE_SUBNET=172.30.1.0/24
OFFLINE_GATEWAY=172.30.1.2

SCRIPT_DIR=$(cd -- "$(dirname -- "$0")" && pwd)
SCRIPT_FILE=$SCRIPT_DIR/$(basename -- "$0")
EGRESS_RULES=${EGRESS_RULES_FILE:-"$SCRIPT_DIR/netpolicy/egress.rules"}
if [ -z "${DNS_UPSTREAM:-}" ] && [ -f "$SCRIPT_DIR/.env" ]; then
  DNS_UPSTREAM=$(sed -n 's/^DNS_UPSTREAM=//p' "$SCRIPT_DIR/.env" | tail -n 1 | tr -d "\"' ")
fi
DNS_UPSTREAM=${DNS_UPSTREAM:-}
# Policy inputs use the same canonical plain IPv4 and overlap checks as the
# Go renderer. Reject host bits, out-of-range octets/prefixes and supernets of
# a never-allowed range before passing any token to iptables-restore.
policy_destination_ok() {
  case "$1" in '' | *[!0-9./]*) return 1 ;; esac
  awk -v value="$1" -v host="${2:-0}" 'BEGIN {
    n=split(value,p,"/"); if (n != (host ? 1 : 2)) exit 1
    if (!host && (p[2] !~ /^[0-9]+$/ || p[2]+0 > 32 || p[2] != sprintf("%d",p[2]+0))) exit 1
    if (split(p[1],a,".") != 4) exit 1
    ip=0
    for (i=1;i<=4;i++) {
      if (a[i] !~ /^[0-9]+$/ || a[i]+0>255 || a[i] != sprintf("%d",a[i]+0)) exit 1
      ip=ip*256+a[i]
    }
    size=2^(host ? 0 : 32-p[2]); if (ip % size) exit 1
    last=ip+size-1
    split("2130706432:16777216 2851995648:65536 1681915904:4194304 2887647232:512",deny," ")
    for (i in deny) {
      split(deny[i],d,":"); if (ip <= d[1]+d[2]-1 && last >= d[1]) exit 1
    }
    exit 0
  }'
}

# Render only validated tokens; never interpolate a policy line as shell code.
filter_rule() { printf '%s\n' "$*"; }

# The admin exception file must be a regular file, never a symlink: a link
# could point at any readable file and turn the diagnostics below into a leak,
# or let a writer of the link's directory swap the policy. Anything else is
# treated as "no admin exceptions" (default-deny stays in force). Diagnostics
# never print file content, only line numbers.
egress_file_usable() { [ -f "$1" ] && [ ! -L "$1" ]; }

apply_egress_allow() {
  ea_file=${APPLY_RULES:-$EGRESS_RULES}
  if [ -e "$ea_file" ] || [ -L "$ea_file" ]; then
    egress_file_usable "$ea_file" || {
      echo "firewall: egress rules file is a symlink or not a regular file - ignoring it (no admin exceptions)" >&2
      return 0
    }
  else
    return 0
  fi
  ea_line=0
  while read -r ea_cidr ea_port ea_proto ea_extra || [ -n "${ea_cidr:-}" ]; do
    ea_line=$((ea_line + 1))
    case "$ea_cidr" in '' | '#'*) continue ;; esac
    if [ -n "$ea_extra" ] || ! policy_destination_ok "$ea_cidr"; then
      echo "firewall: skipping malformed or never-allowed destination on egress rules line $ea_line" >&2
      continue
    fi
    case "$ea_proto" in tcp | udp) ;; *) continue ;; esac
    if [ "$ea_port" = any ]; then
      filter_rule -A TERMIPLUS -i "$BRIDGE" -s "$SANDBOX" -d "$ea_cidr" -p "$ea_proto" -j RETURN
    elif echo "$ea_port" | grep -Eq '^[0-9]{1,5}$' && [ "$ea_port" -ge 1 ] && [ "$ea_port" -le 65535 ]; then
      filter_rule -A TERMIPLUS -i "$BRIDGE" -s "$SANDBOX" -d "$ea_cidr" -p "$ea_proto" --dport "$ea_port" -j RETURN
    else
      echo "firewall: skipping bad port on egress rules line $ea_line" >&2
    fi
  done <"$ea_file"
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
# not help either). Needs ebtables and trusted host-side port discovery;
# fails closed (keeps the previous pins, adds nothing) if any is missing, since
# the ACCEPT would be unsafe (#13).
# mac_of prints the unique MAC in this run's resolved port snapshot. MAC and
# port must come from the same observation, including when a service is absent.
mac_of() {
  port_macs "${2:-$BRIDGE}" | awk -v ip="$1" '$3 == ip {mac=$2; n++} END {if (n == 1) print mac}'
}

# port_of prints the host-side bridge port (veth) of the container holding IP
# on the network named by the second argument (network and bridge share a
# name), or nothing. Use the same host-only mapping as the workspace pins.
port_of() {
  port_macs "$2" | awk -v ip="$1" '$3 == ip {port=$1; n++} END {if (n == 1) print port}'
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

# Locked bridge ports. The pins above protect the gateway's and forwarder's
# addresses, but a sandboxed root could still forge another terminal's MAC:
# the bridge would learn it on the attacker's port and deliver that terminal's
# traffic (gateway requests carrying its API key) to the attacker instead. So
# every port of both bridges runs with "locked on, learning off" and one static
# FDB entry for the MAC Docker assigned to the container behind it. A locked
# port drops every frame whose source MAC has no entry pointing at that port,
# so a terminal can only send as itself, and nothing it sends moves another
# port's entry. A port whose container cannot be resolved stays locked with no
# entry (no traffic): fail closed. Needs Linux >= 6.2 and iproute2 >= 6.2.
PORTLOCK_DIR=/run/termiplus-portlock

# port_flags_ok PORT: locked, not learning, and no unknown unicast/multicast flooding.
port_flags_ok() {
  link=$(bridge -d link show dev "$1" 2>/dev/null) || return 1
  case "$link" in *" locked on "*) ;; *) return 1 ;; esac
  case "$link" in *" learning off "*) ;; *) return 1 ;; esac
  case "$link" in *" flood off "*) ;; *) return 1 ;; esac
  case "$link" in *" mcast_flood off "*) ;; *) return 1 ;; esac
}

# port_locked PORT [MAC]: locked, not learning, and holding a static entry
# (for MAC when supplied; a stale entry must not prevent a replacement).
port_locked() {
  port_flags_ok "$1" || return 1
  bridge fdb show dev "$1" 2>/dev/null | awk -v mac="${2:-}" '
    / static/ && !/permanent/ && (mac == "" || tolower($1) == tolower(mac)) {found=1}
    END {exit !found}' || return 1
}

# lock_port PORT sets the flags, only if they are not set yet (setting them
# raises a link event, which the watcher would otherwise answer forever).
lock_port() {
  port_flags_ok "$1" || bridge link set dev "$1" locked on learning off flood off mcast_flood off 2>/dev/null
}

# port_macs BRIDGE prints "PORT MAC IPv4" for every container on the network of the
# same name. Match host-side link-netnsid to the kernel namespace ID of the
# Docker PID. Never inspect links inside the workspace: NET_ADMIN can create
# links with chosen indices/MACs there. Ambiguous namespaces or duplicate port
# claims get no entry. Failed discovery revokes the old bindings, fail closed.
port_macs() {
  if [ "${PORT_SNAPSHOT_READY:-0}" = 1 ]; then
    case "$1" in
      "$BRIDGE") printf '%s\n' "$SANDBOX_PORTS" ;;
      "$OFFLINE_BRIDGE") printf '%s\n' "$OFFLINE_PORTS" ;;
      *) return 1 ;;
    esac
    return 0
  fi
  pm_br=$1
  pm_links=$(ip -o link show master "$pm_br" 2>/dev/null) || return 0
  pm_rows=$(docker network inspect "$pm_br" \
    --format '{{range .Containers}}{{.Name}} {{.MacAddress}} {{.IPv4Address}}{{"\n"}}{{end}}' 2>/dev/null) || return 0
  echo "$pm_rows" | while read -r pm_name pm_mac pm_addr; do
      if [ -z "$pm_mac" ] || [ -z "$pm_addr" ]; then continue; fi
      pm_ip=${pm_addr%/*}
      pm_pid=$(docker inspect -f '{{.State.Pid}}' "$pm_name" 2>/dev/null) || continue
      [ "${pm_pid:-0}" -gt 0 ] 2>/dev/null || continue
      pm_nsid=$(lsns --type net --task "$pm_pid" --noheadings --raw --output NETNSID 2>/dev/null) || continue
      case "$pm_nsid" in '' | *[!0-9]*) continue ;; esac
      pm_dev=$(echo "$pm_links" | awk -v id="$pm_nsid" '
        {for (i=1; i<NF; i++) if ($i == "link-netnsid" && $(i+1) == id) {
          port=$2; sub(/[@:].*/, "", port); n++
        }} END {if (n == 1) print port}')
      [ -n "$pm_dev" ] || continue
      echo "$pm_dev $pm_mac $pm_ip"
    done | awk '{n[$1]++; row[$1]=$0} END {for (p in n) if (n[p] == 1) print row[p]}'
}

# ebtables -C changes counters; unlike iptables it does NOT check a rule.
# Compare the canonical listing for our unconditional (or bridge-scoped) jumps.
eb_jump_exists() {
  ej_rule="-j $3"
  [ "$#" -lt 4 ] || ej_rule="--logical-in $4 -j $3"
  ebtables -t "$1" -L "$2" --Lmac2 2>/dev/null | grep -qxF -- "$ej_rule"
}

# Source guards run in bridge nat PREROUTING before neighbour processing or
# MAC learning. Every port, including newly attached/unresolved ports, must
# match a Docker-assigned (port, Ethernet MAC, IPv4) tuple. ARP also checks its
# sender hardware address. Everything else, including IPv6/VLAN frames, drops.
#
# Two chains per bridge allow updates without flushing an attached chain. The
# replacement is built detached with an explicit final DROP, attached BEFORE
# the old guard, then the old jump is removed. While both are attached their
# allowed sets intersect: a failed update can deny traffic, never open a gap.
# Do not rely on user-chain policies: ebtables-nft reports them as RETURN.
source_chains() {
  case "$1" in
    "$BRIDGE") echo "TERMIPLUS-IP-SA TERMIPLUS-IP-SB" ;;
    "$OFFLINE_BRIDGE") echo "TERMIPLUS-IP-OA TERMIPLUS-IP-OB" ;;
    *) return 1 ;;
  esac
}

# source_guard BRIDGE prints the attached guard's name. On first installation
# attach a DROP-only chain before looking up containers. Never flush a chain
# until its jump is removed and another guard is known to be attached.
source_guard() {
  sg_br=$1
  sg_names=$(source_chains "$sg_br") || return 1
  sg_a=${sg_names% *}
  sg_b=${sg_names#* }
  sg_listing=$(ebtables -t nat -L PREROUTING 2>/dev/null) || return 1
  sg_active=$(echo "$sg_listing" | awk -v br="$sg_br" -v a="$sg_a" -v b="$sg_b" '
    $1 == "--logical-in" && $2 == br && $3 == "-j" && ($4 == a || $4 == b) {print $4; exit}')
  if [ -z "$sg_active" ]; then
    ebtables -t nat -N "$sg_a" 2>/dev/null || ebtables -t nat -F "$sg_a" || return 1
    ebtables -t nat -A "$sg_a" -j DROP || return 1
    ebtables -t nat -I PREROUTING 1 --logical-in "$sg_br" -j "$sg_a" || return 1
    sg_active=$sg_a
  fi
  sg_other=$sg_a
  [ "$sg_other" != "$sg_active" ] || sg_other=$sg_b
  # Recover a previous interrupted swap while retaining the active guard.
  # Use the successful snapshot, not a failed listing mistaken for absence.
  sg_count=$(echo "$sg_listing" | awk -v br="$sg_br" -v c="$sg_other" '
    $1 == "--logical-in" && $2 == br && $3 == "-j" && $4 == c {n++} END {print n+0}')
  while [ "$sg_count" -gt 0 ]; do
    ebtables -t nat -D PREROUTING --logical-in "$sg_br" -j "$sg_other" || return 1
    sg_count=$((sg_count - 1))
  done
  echo "$sg_active"
}

# source_pins_current BRIDGE BINDINGS checks exact rule count as well as every
# tuple, so stale allows are revoked on disconnect, address or port reuse.
source_pins_current() {
  sc_names=$(source_chains "$1") || return 1
  sc_chain=""
  for sc_c in $sc_names; do
    if eb_jump_exists nat PREROUTING "$sc_c" "$1"; then
      [ -z "$sc_chain" ] || return 1
      sc_chain=$sc_c
    fi
  done
  [ -n "$sc_chain" ] || return 1
  sc_listing=$(ebtables -t nat -L "$sc_chain" --Lmac2 2>/dev/null) || return 1
  sc_count=1 # explicit final DROP
  while read -r sc_dev sc_mac sc_ip; do
    [ -n "$sc_dev" ] || continue
    echo "$sc_listing" | grep -qxF -- "-p IPv4 -s $sc_mac -i $sc_dev --ip-src $sc_ip -j RETURN" || return 1
    echo "$sc_listing" | grep -qxF -- "-p ARP -s $sc_mac -i $sc_dev --arp-ip-src $sc_ip --arp-mac-src $sc_mac -j RETURN" || return 1
    sc_count=$((sc_count + 2))
  done <<EOF
$2
EOF
  echo "$sc_listing" | grep -qF "entries: $sc_count," || return 1
  echo "$sc_listing" | tail -n 1 | grep -qxF -- "-j DROP" || return 1
}

sync_source_pins() {
  ss_br=$1
  ss_rows=$2
  ss_active=$(source_guard "$ss_br") || return 1
  source_pins_current "$ss_br" "$ss_rows" && return 0
  ss_names=$(source_chains "$ss_br") || return 1
  ss_next=${ss_names% *}
  [ "$ss_next" != "$ss_active" ] || ss_next=${ss_names#* }
  ebtables -t nat -N "$ss_next" 2>/dev/null || ebtables -t nat -F "$ss_next" || return 1
  while read -r ss_dev ss_mac ss_ip; do
    [ -n "$ss_dev" ] || continue
    ebtables -t nat -A "$ss_next" -i "$ss_dev" -s "$ss_mac" -p IPv4 --ip-src "$ss_ip" -j RETURN || return 1
    ebtables -t nat -A "$ss_next" -i "$ss_dev" -s "$ss_mac" -p ARP --arp-ip-src "$ss_ip" --arp-mac-src "$ss_mac" -j RETURN || return 1
  done <<EOF
$ss_rows
EOF
  ebtables -t nat -A "$ss_next" -j DROP || return 1
  ebtables -t nat -I PREROUTING 1 --logical-in "$ss_br" -j "$ss_next" || return 1
  ebtables -t nat -D PREROUTING --logical-in "$ss_br" -j "$ss_active" || return 1
}

# watcher fast path: a locked port still needs its source guard; the FDB alone
# says nothing about IP ownership. Full tuple/policy checks run in sync_all.
source_port_pinned() {
  spp_names=$(source_chains "$1") || return 1
  for spp_c in $spp_names; do
    eb_jump_exists nat PREROUTING "$spp_c" "$1" || continue
    ebtables -t nat -L "$spp_c" 2>/dev/null | grep -qF -- "-i $2 " && return 0
  done
  return 1
}

# sync_ports BRIDGE installs source guards before admitting any MAC into the
# FDB, then locks all ports and supplies known static entries. Existing locked
# ports are included in the source refresh. Unknown ports stay denied even if
# an old FDB entry survives. 1 = error, 2 = unresolved container (retry).
sync_ports() {
  sp_br=$1
  # Guard bridge names even before they exist: a later-created offline bridge
  # must not admit an unresolved first port before the watcher sees its event.
  source_guard "$sp_br" >/dev/null || return 1
  if [ ! -d "/sys/class/net/$sp_br/brif" ]; then
    sync_source_pins "$sp_br" ""
    return $?
  fi
  sp_macs=$(port_macs "$sp_br")
  if ! sync_source_pins "$sp_br" "$sp_macs"; then
    echo "firewall: cannot synchronize IP/MAC source pins on $sp_br" >&2
    return 1
  fi
  sp_rc=0
  for sp_p in /sys/class/net/"$sp_br"/brif/*; do
    [ -e "$sp_p" ] || continue
    sp_dev=${sp_p##*/}
    if ! lock_port "$sp_dev"; then
      [ -e "$sp_p" ] || continue
      echo "firewall: cannot lock bridge port $sp_dev on $sp_br (needs Linux and iproute2 >= 6.2)" >&2
      sp_rc=1
      continue
    fi
    sp_mac=$(echo "$sp_macs" | awk -v d="$sp_dev" '$1 == d {print $2; exit}')
    if [ -z "$sp_mac" ]; then
      [ "$sp_rc" -eq 1 ] || sp_rc=2
      continue
    fi
    port_locked "$sp_dev" "$sp_mac" && continue
    if ! bridge fdb replace "$sp_mac" dev "$sp_dev" master static 2>/dev/null; then
      [ "$sp_rc" -eq 1 ] || sp_rc=2
    fi
  done
  return "$sp_rc"
}

# sync_all runs sync_ports for both bridges, one run at a time host-wide, and
# returns the worse result.
sync_all() {
  mkdir -p "$PORTLOCK_DIR"
  sa_rc=0
  for sa_br in "$BRIDGE" "$OFFLINE_BRIDGE"; do
    sa_one=0
    (
      flock 9 || exit 1
      sync_ports "$sa_br"
    ) 9>"$PORTLOCK_DIR/lock" || sa_one=$?
    if [ "$sa_one" -eq 1 ] || { [ "$sa_one" -ne 0 ] && [ "$sa_rc" -eq 0 ]; }; then
      sa_rc=$sa_one
    fi
  done
  return "$sa_rc"
}

# port_worker keeps syncing while containers are still starting (their port
# exists before Docker lists them) or the watcher saw new ports meanwhile
# ($PORTLOCK_DIR/dirty), for up to a minute per run.
port_worker() {
  pw_i=0
  while [ "$pw_i" -lt 120 ]; do
    rm -f "$PORTLOCK_DIR/dirty"
    pw_rc=0
    sync_all || pw_rc=$?
    # A recreated gateway/forwarder has a new MAC and port. Refresh its L3
    # allowances and legacy pins as soon as source discovery succeeds, rather
    # than waiting for the timer. Source guards remain attached on failure.
    if [ "$pw_rc" -ne 1 ]; then
      "$SCRIPT_FILE" --if-needed || return 1
    fi
    if [ "$pw_rc" -ne 2 ] && [ ! -e "$PORTLOCK_DIR/dirty" ]; then
      return "$pw_rc"
    fi
    pw_i=$((pw_i + 1))
    sleep 0.5
  done
  echo "firewall: some bridge ports could not be matched to a container - they stay locked with no traffic" >&2
  return 2
}

# ip monitor prefixes removals with "Deleted", shifting the interface field.
link_event_port() {
  echo "$1" | awk '{i=($1 == "Deleted" ? 3 : 2); sub(/:$/, "", $i); sub(/@.*/, "", $i); print $i}'
}

# --lock-ports: one sync, then exit. 0 = all ports locked and resolved,
# 1 = locking unsupported, 2 = some port unresolved (locked, no traffic).
if [ "${1:-}" = --lock-ports ]; then
  rc=0
  sync_all || rc=$?
  exit "$rc"
fi

# --watch-ports (termiplus-portlock.service): lock each new port the moment it
# joins a bridge, then give it its entry as soon as Docker lists the container.
# The termiplus-firewall-watch timer re-syncs every 20 s as a backstop.
if [ "${1:-}" = --watch-ports ]; then
  if ! command -v lsns >/dev/null 2>&1 || ! command -v flock >/dev/null 2>&1; then
    echo "firewall: lsns (NETNSID) and flock are required to lock bridge ports" >&2
    exit 1
  fi
  mkdir -p "$PORTLOCK_DIR"
  watch_rc=0
  sync_all || watch_rc=$?
  if [ "$watch_rc" -eq 1 ]; then
    echo "firewall: cannot establish source guards/port locks; refusing watcher startup" >&2
    exit 1 # systemd retries; do not report an unprotected watcher as healthy
  fi
  (
    sleep 1 # let the monitor below start first, so no port slips between
    port_worker || true
  ) &
  worker=""
  ip -o monitor link | while read -r line; do
    for br in "$BRIDGE" "$OFFLINE_BRIDGE"; do
      case "$line" in *" master $br "*) ;; *) continue ;; esac
      # "17: vethabc@if2: <...> ... master ot-sandbox ..." -> vethabc
      dev=$(link_event_port "$line")
      [ -n "$dev" ] || continue
      case "$line" in
        Deleted*) ;; # revoke the old tuple even if no replacement appears
        *)
          if port_locked "$dev" && source_port_pinned "$br" "$dev"; then continue; fi
          lock_port "$dev" || echo "firewall: cannot lock bridge port $dev on $br" >&2
          ;;
      esac
      touch "$PORTLOCK_DIR/dirty"
      if [ -z "$worker" ] || ! kill -0 "$worker" 2>/dev/null; then
        port_worker &
        worker=$!
      fi
    done
  done
  exit 1 # the monitor ended; systemd restarts the service
fi

# Serialize full applies and their current-state checks. Link events, policy
# updates and the timer may race; none may flush/rebuild another apply's chains.
mkdir -p "$PORTLOCK_DIR"
exec 8>"$PORTLOCK_DIR/apply.lock"
flock 8 || exit 1

# Each forwarder upstream (DNS_UPSTREAM, comma-separated) must be a plain IPv4
# address outside the never-allowed ranges; anything else gets no rule.
upstream_ok() { policy_destination_ok "$1" 1; }
upstreams() { echo "$DNS_UPSTREAM" | tr -s ', ' '\n' | grep -v '^$' || true; }

# --if-needed (for the termiplus-firewall-watch timer): only (re)apply when the
# rules are missing or a pinned MAC no longer matches the running gateway or
# forwarder, e.g. after a reboot or a recreated gateway. Full applies replace
# each IP family atomically; unchanged policy skips unnecessary transactions.
# Policy snapshot helpers. A completion marker binds every owned L3 chain to
# the exact script, policy contents, upstreams and discovered service identities.
# A partial apply or a removed/changed rule cannot pass the timer fast path.
policy_fingerprint() {
  pf_script=$(sha256sum <"$SCRIPT_FILE") || return 1
  {
    printf '%s\n' "$pf_script"
    if egress_file_usable "$EGRESS_RULES"; then sha256sum <"$EGRESS_RULES"; else echo no-egress-file; fi
    printf '%s\n' "$DNS_UPSTREAM" "$GATEWAY" "$FORWARDER"
    printf 'ipv6-filter=%s\n' "${IPV6_FILTER:-1}"
    host_upstreams
    printf '%s %s\n' "$mac" "$port" "$forwarder_mac" "$forwarder_port" \
      "$offline_gateway_mac" "$offline_gateway_port"
  } | sha256sum | cut -d ' ' -f 1
}

# The configured DNS upstreams that are addresses of this host. render_ipv4
# opens INPUT for them, so a host address change must invalidate the marker.
host_has_ip() { ip -4 -o addr show | awk '{print $4}' | cut -d/ -f1 | grep -qx "$1"; }
host_upstreams() {
  for hu_up in $(upstreams); do
    if upstream_ok "$hu_up" && host_has_ip "$hu_up"; then echo "host-upstream $hu_up"; fi
  done
}

# IPv6 is dropped for both bridges by ip6tables. Without a usable
# ip6tables/ip6tables-restore that is impossible, so IPv4 policy is still
# enforced and IPv6 is closed another way: disable_ipv6 on the bridges.
detect_ipv6_filter() {
  if command -v ip6tables >/dev/null 2>&1 && command -v ip6tables-restore >/dev/null 2>&1 &&
    ip6tables -w 10 -S FORWARD >/dev/null 2>&1; then
    IPV6_FILTER=1
  else
    IPV6_FILTER=0
    echo "firewall: ip6tables unavailable - enforcing IPv4 policy and dropping all IPv6 frames on the termiplus bridges with ebtables" >&2
  fi
}

# Without ip6tables the real gate is an ebtables drop of every IPv6 frame
# entering either bridge (bridged terminal<->terminal link-local traffic never
# reaches the host stack, and bridge-nf-call-ip6tables filters nothing). It
# lives in TERMIPLUS-AS, which every run flushes and rebuilds, so it vanishes
# again once ip6tables works. It is part of the verified state.
ipv6_frame_drop_current() {
  fd_listing=$(ebtables -t filter -L TERMIPLUS-AS --Lmac2 2>/dev/null) || return 1
  for fd_br in "$BRIDGE" "$OFFLINE_BRIDGE"; do
    printf '%s\n' "$fd_listing" | awk -v br="$fd_br" '{
      p = i = d = 0
      for (n = 1; n < NF; n++) {
        if ($n == "-p" && $(n + 1) == "IPv6") p = 1
        if ($n == "--logical-in" && $(n + 1) == br) i = 1
        if ($n == "-j" && $(n + 1) == "DROP") d = 1
      }
      if (p && i && d) f = 1
    } END { exit !f }' || return 1
  done
}

ensure_ipv6_frame_drop() {
  ! ipv6_frame_drop_current || return 0
  for ef_br in "$BRIDGE" "$OFFLINE_BRIDGE"; do
    ebtables -t filter -I TERMIPLUS-AS 1 -p IPv6 --logical-in "$ef_br" -j DROP || return 1
  done
  ipv6_frame_drop_current
}

# Additional layer: the host's own IPv6 stack on the bridges. Best effort; a
# bridge that does not exist yet is covered by the ebtables drop.
ipv6_bridges_closed() {
  sysctl -n net.ipv6.conf.all.disable_ipv6 >/dev/null 2>&1 || return 0
  for ic_br in "$BRIDGE" "$OFFLINE_BRIDGE"; do
    ic_key=net.ipv6.conf.$ic_br.disable_ipv6
    ic_val=$(sysctl -n "$ic_key" 2>/dev/null) || continue
    if [ "$ic_val" != 1 ] && [ "${1:-}" = set ]; then
      sysctl -qw "$ic_key=1" 2>/dev/null || true
      ic_val=$(sysctl -n "$ic_key" 2>/dev/null) || ic_val=
    fi
    [ "$ic_val" = 1 ] || return 1
  done
}

policy_rules() {
  iptables -w 10 -S TERMIPLUS || return 1
  iptables -w 10 -S TERMIPLUS-IN || return 1
  if [ "${IPV6_FILTER:-1}" = 1 ]; then
    ip6tables -w 10 -S TERMIPLUS6 || return 1
    ip6tables -w 10 -S TERMIPLUS6-IN || return 1
  fi
  ebtables -t filter -L TERMIPLUS-AS --Lmac2 || return 1
  ebtables -t nat -L TERMIPLUS-PORT --Lmac2 || return 1
}

hook_current() {
  hc_listing=$("$1" -w 10 -S "$2") || return 1
  hc_first=$(printf '%s\n' "$hc_listing" | awk '$1 == "-A" {print; exit}')
  [ "$hc_first" = "-A $2 -j $3" ]
}

rules_current() {
  [ -f "$PORTLOCK_DIR/policy.applied" ] || return 1
  read -r rc_policy rc_rules <"$PORTLOCK_DIR/policy.applied" || return 1
  [ "$rc_policy" = "$(policy_fingerprint)" ] || return 1
  rc_actual=$(policy_rules) || return 1
  [ "$rc_rules" = "$(printf '%s\n' "$rc_actual" | sha256sum | cut -d ' ' -f 1)" ] || return 1
  hook_current iptables DOCKER-USER TERMIPLUS || return 1
  hook_current iptables INPUT TERMIPLUS-IN || return 1
  if [ "${IPV6_FILTER:-1}" = 1 ]; then
    hook_current ip6tables FORWARD TERMIPLUS6 || return 1
    hook_current ip6tables INPUT TERMIPLUS6-IN || return 1
  else
    ipv6_frame_drop_current || return 1
  fi
  for rc_br in "$BRIDGE" "$OFFLINE_BRIDGE"; do
    source_pins_current "$rc_br" "$(port_macs "$rc_br")" || return 1
  done
  eb_jump_exists filter FORWARD TERMIPLUS-AS || return 1
  eb_jump_exists nat PREROUTING TERMIPLUS-PORT || return 1
}
# An address is not a service identity. Only an administrator-created Compose
# service with the matching IP/MAC may receive special allowances. Workspaces
# (termiplus.managed=true) never qualify, even if a service label is also present.
service_owned() {
  so_rows=$(docker network inspect "$2" --format '{{range $id, $c := .Containers}}{{$id}} {{$c.MacAddress}} {{$c.IPv4Address}}{{"\n"}}{{end}}' 2>/dev/null) || return 1
  so_id=$(printf '%s\n' "$so_rows" | awk -v ip="$1" -v mac="$3" '
    {split($3,a,"/"); if (a[1] == ip && $2 == mac) {id=$1; n++}} END {if (n == 1) print id}')
  [ -n "$so_id" ] || return 1
  so_labels=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.service"}} {{index .Config.Labels "termiplus.managed"}}' "$so_id" 2>/dev/null) || return 1
  case "$so_labels" in "$4 " | "$4 <no value>") return 0 ;; *) return 1 ;; esac
}

# One discovery per bridge for a full apply/current-state check. Install guards
# before discovery even on a first boot. The long-running watcher deliberately
# takes fresh observations on each iteration instead of retaining this cache.
snapshot_discovery() {
  for sd_br in "$BRIDGE" "$OFFLINE_BRIDGE"; do
    (flock 9 && source_guard "$sd_br" >/dev/null) 9>"$PORTLOCK_DIR/lock" || return 1
  done
  SANDBOX_PORTS=$(port_macs "$BRIDGE") || return 1
  OFFLINE_PORTS=$(port_macs "$OFFLINE_BRIDGE") || return 1
  PORT_SNAPSHOT_READY=1
}

snapshot_services() {
  mac=$(mac_of "$GATEWAY")
  port=$(port_of "$GATEWAY" "$BRIDGE")
  forwarder_mac=$(mac_of "$FORWARDER")
  forwarder_port=$(port_of "$FORWARDER" "$BRIDGE")
  if [ -n "$forwarder_mac" ] && ! service_owned "$FORWARDER" "$BRIDGE" "$forwarder_mac" dns-forwarder; then
    forwarder_mac=; forwarder_port=
  fi
  offline_gateway_mac=$(mac_of "$OFFLINE_GATEWAY" "$OFFLINE_BRIDGE")
  offline_gateway_port=$(port_of "$OFFLINE_GATEWAY" "$OFFLINE_BRIDGE")

}

prepare_policy_snapshot() {
  snapshot_discovery || return 1
  snapshot_services || return 1
}

if [ "${1:-}" = --if-needed ]; then
  prepare_policy_snapshot || exit 1
  detect_ipv6_filter
  lock_rc=0
  sync_all || lock_rc=$?
  if [ "$lock_rc" -eq 1 ]; then
    echo "firewall: source guards/bridge ports could not be synchronized" >&2
    exit 1
  fi
  if rules_current; then exit 0; fi
  echo "firewall: rules missing or outdated - applying"
fi

ensure_gateway_antispoof() {
  if ! command -v ebtables >/dev/null 2>&1; then
    echo "firewall: ebtables not found - refusing, gateway source IP would be spoofable (#13)" >&2
    exit 1
  fi
  if ! command -v lsns >/dev/null 2>&1; then
    echo "firewall: lsns not found - refusing, cannot pin the gateway to its bridge port (#13)" >&2
    exit 1
  fi
  # All identities were resolved once, before current-state checks. Do not
  # rediscover here: the completion marker must describe exactly these pins.
  if [ -z "$mac" ]; then
    echo "firewall: gateway absent; installing static deny policy without gateway allowances" >&2
  fi
  ebtables -t filter -N TERMIPLUS-AS -P RETURN 2>/dev/null || ebtables -t filter -F TERMIPLUS-AS
  ebtables -t filter -P TERMIPLUS-AS RETURN
  # User chains in ebtables default to ACCEPT, which would end PREROUTING.
  ebtables -t nat -N TERMIPLUS-PORT -P RETURN 2>/dev/null || ebtables -t nat -F TERMIPLUS-PORT
  ebtables -t nat -P TERMIPLUS-PORT RETURN
  if [ -n "$mac" ]; then pin_ip "$GATEWAY" "$mac" "$BRIDGE" "$port"; fi
  if [ -n "$forwarder_mac" ]; then
    pin_ip "$FORWARDER" "$forwarder_mac" "$BRIDGE" "$forwarder_port"
  fi
  if [ -n "$offline_gateway_mac" ]; then
    pin_ip "$OFFLINE_GATEWAY" "$offline_gateway_mac" "$OFFLINE_BRIDGE" "$offline_gateway_port"
  fi
  eb_jump_exists filter FORWARD TERMIPLUS-AS || ebtables -t filter -I FORWARD 1 -j TERMIPLUS-AS
  eb_jump_exists nat PREROUTING TERMIPLUS-PORT || ebtables -t nat -I PREROUTING 1 -j TERMIPLUS-PORT
}
# Source guards are independent of the legacy service pins: full applies never
# flush an attached source guard, including during gateway/forwarder recreation.
[ "${PORT_SNAPSHOT_READY:-0}" = 1 ] || prepare_policy_snapshot || exit 1
initial_lock_rc=0
sync_all || initial_lock_rc=$?
if [ "$initial_lock_rc" -eq 1 ]; then
  echo "firewall: source pins/port locks failed; refusing full apply" >&2
  exit 1
fi
ensure_gateway_antispoof

# Remove only our unconditional hooks and insert one at the front. These
# operations and both owned chains commit together under the xtables lock.
restore_hook() {
  rh_listing=$("$1" -w 10 -S "$2") || return 1
  rh_count=$(printf '%s\n' "$rh_listing" | awk -v rule="-A $2 -j $3" '$0 == rule {n++} END {print n+0}')
  while [ "$rh_count" -gt 0 ]; do
    filter_rule -D "$2" -j "$3"
    rh_count=$((rh_count - 1))
  done
  filter_rule -I "$2" 1 -j "$3"
}

render_ipv4() {
  # Forwarded traffic (container -> elsewhere). DOCKER-USER runs before Docker's own rules.
  printf '%s\n' '*filter' ':TERMIPLUS - [0:0]' ':TERMIPLUS-IN - [0:0]'
  if [ -n "${offline_gateway_mac:-}" ]; then
    filter_rule -A TERMIPLUS -i "$OFFLINE_BRIDGE" -o "$OFFLINE_BRIDGE" -s "$OFFLINE_GATEWAY" -d "$OFFLINE_SUBNET" -j ACCEPT
    filter_rule -A TERMIPLUS -i "$OFFLINE_BRIDGE" -o "$OFFLINE_BRIDGE" -s "$OFFLINE_SUBNET" -d "$OFFLINE_GATEWAY" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
  fi
  filter_rule -A TERMIPLUS -i "$OFFLINE_BRIDGE" -j DROP
  filter_rule -A TERMIPLUS -o "$OFFLINE_BRIDGE" -j DROP
  # The gateway->terminal allow is ACCEPT, not RETURN: ACCEPT terminates
  # traversal so Docker's enable_icc:false inter-container DROP (which runs in
  # FORWARD after DOCKER-USER) does not drop this required flow. Do not install a
  # broad ESTABLISHED,RELATED ACCEPT here: DOCKER-USER is global to Docker
  # forwarding, and that rule would bypass other networks' policy and current
  # egress restrictions for long-lived connections. Docker handles reply traffic;
  # outbound packets must continue through the current egress policy below.
  if [ -n "${mac:-}" ]; then
    filter_rule -A TERMIPLUS -i "$BRIDGE" -o "$BRIDGE" -s "$GATEWAY" -d "$SANDBOX" -j ACCEPT
  fi
  # Replies on those connections. They are sandbox -> sandbox too, so without this
  # the terminal <-> terminal drop below would cut every gateway request. Scoped to
  # the gateway as destination and to connections conntrack has already seen
  # (opened by the gateway; a terminal's own new packets to it are never accepted),
  # unlike a global ESTABLISHED,RELATED rule.
  if [ -n "${mac:-}" ]; then
    filter_rule -A TERMIPLUS -i "$BRIDGE" -o "$BRIDGE" -s "$SANDBOX" -d "$GATEWAY" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
  fi
  # terminal -> forwarder DNS (only with the forwarder present). ACCEPT, as for the
  # gateway, to beat Docker's icc:false drop; the forwarder's address is MAC-pinned.
  if [ -n "${forwarder_mac:-}" ]; then
    for p in udp tcp; do
      filter_rule -A TERMIPLUS -i "$BRIDGE" -s "$SANDBOX" -d "$FORWARDER" -p "$p" --dport 53 -j ACCEPT
      # and its answers back to the terminal, only on those DNS connections
      filter_rule -A TERMIPLUS -i "$BRIDGE" -s "$FORWARDER" -d "$SANDBOX" -p "$p" --sport 53 -m conntrack --ctstate ESTABLISHED -j ACCEPT
    done
    for up in $(upstreams); do
      if upstream_ok "$up"; then
        for p in udp tcp; do
          filter_rule -A TERMIPLUS -i "$BRIDGE" -s "$FORWARDER" -d "$up" -p "$p" --dport 53 -j RETURN
        done
      else
        echo "firewall: DNS upstream '$up' unusable - the forwarder gets no rule for it" >&2
      fi
    done
  fi
  # The forwarder can reach only its validated DNS upstreams. This terminal
  # DROP is before all admin exceptions and the public-egress fallthrough.
  if [ -n "${forwarder_mac:-}" ]; then
    filter_rule -A TERMIPLUS -i "$BRIDGE" -s "$FORWARDER" -j DROP
  fi
  # terminal <-> terminal: dropped here (belt) and again by Docker's icc:false drop.
  filter_rule -A TERMIPLUS -i "$BRIDGE" -s "$SANDBOX" -d "$SANDBOX" -j DROP
  # Fixed deny-list: dropped before any admin allow rule can match it, so these
  # ranges can never be opened by the network policy.
  for net in 127.0.0.0/8 100.64.0.0/10 169.254.0.0/16 172.30.1.0/24; do
    filter_rule -A TERMIPLUS -i "$BRIDGE" -s "$SANDBOX" -d "$net" -j DROP
  done
  # Admin-set allowances for specific internal destinations (default-deny for
  # private ranges; public egress stays allowed by the implicit RETURN below).
  apply_egress_allow || return 1
  # Everything else private stays denied unless an allow rule above returned first.
  for net in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16; do
    filter_rule -A TERMIPLUS -i "$BRIDGE" -s "$SANDBOX" -d "$net" -j DROP
  done


  # Traffic from terminals to the VM itself (INPUT): drop. DNS still works,
  # Docker's embedded resolver (127.0.0.11) lives inside each container's
  # network namespace and is not affected by this rule.

  # Nothing on the offline bridge may talk to the host, not even on connections
  # that look established (checked before the ESTABLISHED,RELATED return).
  filter_rule -A TERMIPLUS-IN -i "$OFFLINE_BRIDGE" -j DROP
  # An upstream resolver on this host itself: only the forwarder may ask it.
  if [ -n "${forwarder_mac:-}" ]; then
    for up in $(upstreams); do
      if upstream_ok "$up" && host_has_ip "$up"; then
        for p in udp tcp; do
          filter_rule -A TERMIPLUS-IN -i "$BRIDGE" -s "$FORWARDER" -d "$up" -p "$p" --dport 53 -j RETURN
        done
      fi
    done
  fi
  if [ -n "${forwarder_mac:-}" ]; then
    filter_rule -A TERMIPLUS-IN -i "$BRIDGE" -s "$FORWARDER" -j DROP
  fi
  filter_rule -A TERMIPLUS-IN -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  filter_rule -A TERMIPLUS-IN -i "$BRIDGE" -j DROP
  restore_hook iptables DOCKER-USER TERMIPLUS || return 1
  restore_hook iptables INPUT TERMIPLUS-IN || return 1
  printf '%s\n' COMMIT

}

render_ipv6() {
  printf '%s\n' '*filter' ':TERMIPLUS6 - [0:0]' ':TERMIPLUS6-IN - [0:0]'
  for v6_br in "$BRIDGE" "$OFFLINE_BRIDGE"; do
    filter_rule -A TERMIPLUS6 -i "$v6_br" -j DROP
    filter_rule -A TERMIPLUS6 -o "$v6_br" -j DROP
    filter_rule -A TERMIPLUS6-IN -i "$v6_br" -j DROP
  done
  restore_hook ip6tables FORWARD TERMIPLUS6 || return 1
  restore_hook ip6tables INPUT TERMIPLUS6-IN || return 1
  printf '%s\n' COMMIT
}

# Generate and validate both complete snapshots before changing either family.
# --noflush preserves Docker/unrelated chains; declaring our chains replaces
# their contents inside the restore transaction, never in a live flush window.
# If one family fails, its previous policy remains and no completion is recorded.
apply_ip_policy() {
  umask 077
  [ -n "${IPV6_FILTER:-}" ] || detect_ipv6_filter
  ap_dir=$(mktemp -d "$PORTLOCK_DIR/apply.XXXXXX") || return 1
  trap 'rm -rf "$ap_dir"' EXIT
  ap_fingerprint=$(policy_fingerprint) || return 1
  APPLY_RULES=$ap_dir/egress.rules
  : >"$APPLY_RULES" || return 1
  if egress_file_usable "$EGRESS_RULES"; then
    # cp -P never follows the source: a link swapped in after the check is
    # copied as a link, which apply_egress_allow then refuses.
    rm -f "$APPLY_RULES"
    cp -P "$EGRESS_RULES" "$APPLY_RULES" || return 1
  elif [ -e "$EGRESS_RULES" ] || [ -L "$EGRESS_RULES" ]; then
    echo "firewall: egress rules file is a symlink or not a regular file - ignoring it (no admin exceptions)" >&2
  fi
  rm -f "$PORTLOCK_DIR/policy.applied"
  render_ipv4 >"$ap_dir/ipv4" || return 1
  iptables-restore -w 10 --noflush --test <"$ap_dir/ipv4" || return 1
  if [ "$IPV6_FILTER" = 1 ]; then
    render_ipv6 >"$ap_dir/ipv6" || return 1
    ip6tables-restore -w 10 --noflush --test <"$ap_dir/ipv6" || return 1
  fi
  iptables-restore -w 10 --noflush <"$ap_dir/ipv4" || return 1
  if [ "$IPV6_FILTER" = 1 ]; then
    ip6tables-restore -w 10 --noflush <"$ap_dir/ipv6" || return 1
  else
    if ! ensure_ipv6_frame_drop; then
      echo "firewall: cannot drop IPv6 frames on the termiplus bridges - refusing to certify the policy (IPv4 rules are installed)" >&2
      return 1
    fi
    ipv6_bridges_closed set || echo "firewall: could not disable IPv6 on a termiplus bridge (the ebtables drop still applies)" >&2
  fi
  [ "$ap_fingerprint" = "$(policy_fingerprint)" ] || return 1
  ap_rules=$(policy_rules) || return 1
  printf '%s %s\n' "$ap_fingerprint" "$(printf '%s\n' "$ap_rules" | sha256sum | cut -d ' ' -f 1)" >"$ap_dir/completed"
  mv "$ap_dir/completed" "$PORTLOCK_DIR/policy.applied" || return 1
  rm -rf "$ap_dir"
  trap - EXIT
}
apply_ip_policy

echo "termiplus firewall rules applied"

# Lock every bridge port to its container's MAC (see sync_ports). Last, so a
# refusal here still leaves all the rules above in place.
lock_rc=0
sync_all || lock_rc=$?
case "$lock_rc" in
  0) echo "termiplus bridge ports locked" ;;
  2) echo "firewall: some bridge ports have no container yet - locked with no traffic; termiplus-portlock.service finishes them" >&2 ;;
  *)
    echo "firewall: bridge ports could not be locked - terminal MACs stay spoofable" >&2
    exit 1
    ;;
esac
