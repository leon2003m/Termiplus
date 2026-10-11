#!/bin/sh
# Egress rules for the terminal sandbox network. Run on the VM after
# `docker compose up` (and after every Docker restart, e.g. via a systemd unit).
#
# Terminals may reach the internet, nothing internal:
#   - no other terminals on the same bridge
#   - no private ranges (your LAN, Proxmox, customer networks)
#   - no services on the VM itself
# The gateway (172.30.0.2) may reach every terminal.
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

# Forwarded traffic (container -> elsewhere). DOCKER-USER runs before Docker's own rules.
iptables -N TERMIPLUS 2>/dev/null || iptables -F TERMIPLUS
iptables -A TERMIPLUS -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
iptables -A TERMIPLUS -s "$GATEWAY" -d "$SANDBOX" -j RETURN
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

# Same-bridge traffic between containers is bridged, not routed; make the
# bridge hand it to ip(6)tables so the rules here apply to container<->container
# traffic too (both v4 and v6; without the v6 sysctl two terminals could talk
# over link-local IPv6 unfiltered).
modprobe br_netfilter 2>/dev/null || true
sysctl -qw net.bridge.bridge-nf-call-iptables=1
sysctl -qw net.bridge.bridge-nf-call-ip6tables=1

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
