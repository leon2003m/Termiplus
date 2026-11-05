---
type: object
cluster: sandbox
universe: live
status: verified
verified: 2026-11-05 @ working tree
entity: deploy/firewall.sh
---

# Firewall chain

Host iptables and ebtables rules that decide what a terminal on `ot-sandbox` (172.30.0.0/24) can reach: chain `TERMIPLUS` in `DOCKER-USER`, chain `TERMIPLUS-IN` in `INPUT`, and the ebtables anti-spoof chains `TERMIPLUS-AS` (filter `FORWARD`, MAC pin) and `TERMIPLUS-PORT` (nat `PREROUTING`, bridge-port pin). It also locks every terminal's bridge port to the MAC Docker gave that container. Bridge-scoped `TERMIPLUS-IP-S{A,B}` / `TERMIPLUS-IP-O{A,B}` nat PREROUTING source guards bind all containers to their Docker port/MAC/IPv4 tuples; ARP checks sender IP and MAC. Unknown ports and other EtherTypes drop before bridge learning. Empty guards are installed for both bridge names even when a bridge does not yet exist.

## Why this shape

A terminal has root inside its sandbox. So isolation cannot depend on anything inside the container:

- Only the gateway (.2) may open connections to terminals, and that allow is pinned to the gateway's real MAC and to its own bridge port (veth), so a terminal cannot spoof its IP, not even by forging the gateway's MAC as well. The port pin runs in nat `PREROUTING`, before the bridge learns MACs, so forged frames cannot redirect the gateway's traffic either. The forwarder (.3) and the offline gateway (172.30.1.2) get the same pins. Discovery matches host-side `link-netnsid` to the kernel `NETNSID` reported by `lsns` for Docker's PID; it does not read links inside a workspace or trust the bridge's learned MAC table.
- Terminal ports on both bridges run with MAC learning, unknown unicast and multicast flooding off and `locked on`, plus a static FDB entry for the container's Docker-assigned MAC. A terminal that forges another terminal's MAC can neither move it to its own port nor send with it, so it never gets the gateway's replies (with the other terminal's API key). The MAC comes from `docker network inspect`, the port from host namespace matching; ambiguous mappings get no entry. Needs Linux and iproute2 ≥ 6.2, plus `lsns` with `NETNSID` output.
- MAC port locks alone do not prevent own-MAC/victim-IP ARP poisoning. Source guards enforce both identities, including on existing locked ports. New generations are built detached and attached before old jumps are removed; overlapping generations intersect, so interrupted swaps remain closed. Docker discovery failure removes allowances rather than trusting a stale FDB entry.
- Terminal ↔ terminal traffic is dropped twice: here, and by Docker's `enable_icc:false`.
- A fixed deny-list is dropped before any admin allow can match. Private ranges are dropped unless an admin rule returned first. Public egress falls through.
- There is deliberately **no global ESTABLISHED,RELATED accept**. That would bypass policy for long-lived connections on every Docker network. Reply rules are scoped to the gateway and forwarder DNS.
- INPUT from the bridge is dropped, so terminals cannot reach host services. The forwarder may ask a validated host-local DNS upstream; all its other routed/host traffic drops before admin exceptions or established-connection allowances.
- Offline bridge (`ot-offline`, 172.30.1.0/24, guarded even before it exists): rules keyed on the interface come first in `TERMIPLUS` and `TERMIPLUS-IN` and drop everything except gateway (.2, MAC-pinned) → terminal and its replies; IPv6 is dropped too. The bridge is also a Docker internal network with `enable_icc:false`.

## Shape

- Compose `ip_range` reserves the low half of each sandbox subnet for static
  services. A workspace at the forwarder address cannot gain DNS privileges:
  `service_owned` requires the matching IP/MAC and trusted `dns-forwarder` Compose
  label, rejects managed workspaces, and source guards require the resolved port.
- Variables: `SANDBOX`, `GATEWAY=.2`, `FORWARDER=.3`, `BRIDGE=ot-sandbox`, `OFFLINE_BRIDGE=ot-offline`, `OFFLINE_SUBNET`, `OFFLINE_GATEWAY=172.30.1.2`, `EGRESS_RULES`, `DNS_UPSTREAM` (from the environment or `deploy/.env`).
- `apply_ip_policy` renders and validates both families, then replaces only owned chains and their hooks using `iptables-restore` / `ip6tables-restore --noflush -w 10`. Each family commits atomically; Docker and unrelated chains survive. A failure leaves that family's previous complete policy and no completion marker, so the next timer retries. Static denies are installed without a running gateway; gateway allowances require resolved pins.
- `--if-needed` checks the completion marker against the complete canonical L3 and legacy service-pin chain hash and a fingerprint of the script, egress file, upstreams and service identities. It also verifies first-position hooks and source tuples on both bridges. Missing/changed rules, an incomplete apply or a changed policy cannot pass the fast path.
- `snapshot_discovery` caches one resolved observation per bridge for a full apply
  or `--if-needed` run. `snapshot_services` feeds the same identities into legacy
  pins, rendering and `policy_fingerprint`; absent-to-present changes invalidate
  the next run rather than marking unrendered identities current. Watchers refresh
  discovery each iteration. The script hash follows the actual invoked filename.
- `policy_destination_ok` accepts canonical IPv4 CIDRs only, rejects host bits and overlaps with fixed never-allowed ranges, and validates plain upstream IPv4 addresses.
- Port lock: `sync_all` (both bridges, under `flock`) runs at the end of every full apply and inside `--if-needed`; `--lock-ports` runs one pass (exit 0 all done, 1 cannot lock, 2 some MAC unknown yet); `--watch-ports` follows `ip monitor link` and locks each new port as it joins a bridge, then a worker fills its FDB entry.
- Bridge netfilter must be on (`deploy/sysctl.d/90-termiplus-bridge.conf`, `ensure_bridge_netfilter`).
- systemd: `termiplus-firewall.service` (on Docker start), `.path` (starts repeatable `termiplus-firewall-reload.service` on `egress.rules` change), `termiplus-firewall-watch.timer` (every 20 s, `--if-needed`), `termiplus-portlock.service` (long-running, `--watch-ports`; worker also refreshes service pins).

Citations: `deploy/firewall.sh`: `port_macs` (Docker IP/MAC and host NETNSID port resolution), `eb_jump_exists` (canonical listing; ebtables `-C` is not a check), `source_guard`, `source_pins_current`, `sync_source_pins` (two-generation guarded replacement), `sync_ports`, `sync_all`, `port_worker`, `--watch-ports`, `rules_current`, `policy_fingerprint`, `policy_rules`, `policy_destination_ok`, `render_ipv4`, `render_ipv6`, `apply_ip_policy`, `ensure_gateway_antispoof`; `deploy/termiplus-portlock.service`; `deploy/termiplus-firewall.path`; `deploy/termiplus-firewall-reload.service`; `test/firewall/test_source_pins.py`; `test/firewall/test_units.py`; `test/firewall/test_policy.py`; `test/integration/source-pins.py`; `test/integration/firewall-policy.py`. Symbols are stable anchors when line numbers move.

## Connected to

- **owns:** the host's TERMIPLUS chains
- **owned-by:** the host (systemd units run the script from the checkout)
- **joins:** [Network policy](network-policy.md) (`egress.rules`), [DNS forwarder](dns-forwarder.md) (.3), the gateway's fixed IP (.2, `deploy/docker-compose.yml:81`; 172.30.1.2 in `deploy/docker-compose.offline.yml`)
- **looks-like-but-is-not:** the [sockproxy rules](sockproxy-rules.md)

## If you change this

- **Hits:** the security boundary (see [AGENTS.md](../../../AGENTS.md)); `test/integration/validate.sh` (V1–V16), the isolated source-pin kernel regression; the gateway and forwarder IPs in compose; the installed units on live hosts (see [effects](../../effects/CONTEXT.md))
- **Does not hit:** Go code, except the `egress.rules` format

## Surfaces

| Surface | Role |
|---|---|
| host systemd | runs it |
| operator | never runs it by hand on a live host without reason |

## See

- Source: `deploy/firewall.sh`, `deploy/termiplus-firewall*.{path,service,timer}`, `deploy/termiplus-portlock.service`
- Checks: `test/integration/VALIDATION.md`
