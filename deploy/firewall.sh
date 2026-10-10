#!/bin/sh
# Egress rules for the terminal sandbox network. Run on the VM after
# `docker compose up` (and after every Docker restart, e.g. via a systemd unit).
#
# Terminals may reach the internet, nothing internal:
#   - no other terminals on the same bridge
#   - no private ranges (your LAN, Proxmox, customer networks)
#   - no services on the VM itself
# The gateway (172.30.0.2) may reach every terminal.
set -eu

SANDBOX=172.30.0.0/24
GATEWAY=172.30.0.2
BRIDGE=ot-sandbox

# Forwarded traffic (container -> elsewhere). DOCKER-USER runs before Docker's own rules.
iptables -N TERMIPLUS 2>/dev/null || iptables -F TERMIPLUS
iptables -A TERMIPLUS -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
iptables -A TERMIPLUS -s "$GATEWAY" -d "$SANDBOX" -j RETURN
iptables -A TERMIPLUS -s "$SANDBOX" -d "$SANDBOX" -j DROP
for net in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 169.254.0.0/16; do
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
