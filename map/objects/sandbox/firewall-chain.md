---
type: object
cluster: sandbox
universe: live
status: verified
verified: 2026-11-04 @ ec97348
entity: deploy/firewall.sh
---

# Firewall chain

Host iptables and ebtables rules that decide what a terminal on `ot-sandbox` (172.30.0.0/24) can reach: chain `TERMIPLUS` in `DOCKER-USER`, chain `TERMIPLUS-IN` in `INPUT`, and the ebtables anti-spoof chains `TERMIPLUS-AS` (filter `FORWARD`, MAC pin) and `TERMIPLUS-PORT` (nat `PREROUTING`, bridge-port pin). It also locks every terminal's bridge port to the MAC Docker gave that container.

## Why this shape

A terminal has root inside its sandbox. So isolation cannot depend on anything inside the container:

- Only the gateway (.2) may open connections to terminals, and that allow is pinned to the gateway's real MAC and to its own bridge port (veth), so a terminal cannot spoof its IP, not even by forging the gateway's MAC as well. The port pin runs in nat `PREROUTING`, before the bridge learns MACs, so forged frames cannot redirect the gateway's traffic either. The forwarder (.3) and the offline gateway (172.30.1.2) get the same pins. The port is found from the container's own network namespace (`nsenter`), not from the bridge's MAC table.
- Terminal ports on both bridges run with MAC learning off and `locked on`, plus a static FDB entry for the container's Docker-assigned MAC. A terminal that forges another terminal's MAC can neither move it to its own port nor send with it, so it never gets the gateway's replies (with the other terminal's API key). The MAC comes from `docker network inspect`, the port from the container's namespace (`nsenter`); a port with no known MAC stays locked with no entry, i.e. fails closed. Needs Linux and iproute2 ≥ 6.2.
- Terminal ↔ terminal traffic is dropped twice: here, and by Docker's `enable_icc:false`.
- A fixed deny-list is dropped before any admin allow can match. Private ranges are dropped unless an admin rule returned first. Public egress falls through.
- There is deliberately **no global ESTABLISHED,RELATED accept**. That would bypass policy for long-lived connections on every Docker network. Reply rules are scoped to the gateway and forwarder DNS.
- INPUT from the bridge is dropped, so terminals cannot reach host services. The forwarder may ask a host-local upstream.
- Offline bridge (`ot-offline`, 172.30.1.0/24, only when it exists): rules keyed on the interface come first in `TERMIPLUS` and `TERMIPLUS-IN` and drop everything except gateway (.2, MAC-pinned) → terminal and its replies; IPv6 is dropped too. The bridge is also a Docker internal network with `enable_icc:false`.

## Shape

- Variables: `SANDBOX`, `GATEWAY=.2`, `FORWARDER=.3`, `BRIDGE=ot-sandbox`, `OFFLINE_BRIDGE=ot-offline`, `OFFLINE_SUBNET`, `OFFLINE_GATEWAY=172.30.1.2`, `EGRESS_RULES`, `DNS_UPSTREAM` (from the environment or `deploy/.env`).
- `--if-needed` re-applies only when rules are missing or out of date (`rules_current`: chain jumps, both ebtables attachments, pinned MACs, pinned bridge ports (so a recreated gateway is re-pinned) and every offline rule).
- Port lock: `sync_all` (both bridges, under `flock`) runs at the end of every full apply and inside `--if-needed`; `--lock-ports` runs one pass (exit 0 all done, 1 cannot lock, 2 some MAC unknown yet); `--watch-ports` follows `ip monitor link` and locks each new port as it joins a bridge, then a worker fills its FDB entry.
- Bridge netfilter must be on (`deploy/sysctl.d/90-termiplus-bridge.conf`, `ensure_bridge_netfilter`).
- systemd: `termiplus-firewall.service` (on Docker start), `.path` (on `egress.rules` change), `termiplus-firewall-watch.timer` (every 20 s, `--if-needed`), `termiplus-portlock.service` (long-running, `--watch-ports`).

Citations: `deploy/firewall.sh:1-44` (intent), `:47-56`, `:68` (apply_egress_allow), `:105` (ensure_bridge_netfilter), `:145` (port_of), `:172` (pin_ip), `:191-320` (port lock: port_locked, port_macs, sync_ports, sync_all, port_worker), `:324` (--lock-ports), `:333` (--watch-ports), `:367` (rules_current), `:396` (--if-needed), `:412` (ensure_gateway_antispoof), `:488-546` (TERMIPLUS), `:550-568` (TERMIPLUS-IN), `:580-595` (port lock on full apply), `deploy/termiplus-portlock.service`, `deploy/docker-compose.yml:81`, `:87-101`

## Connected to

- **owns:** the host's TERMIPLUS chains
- **owned-by:** the host (systemd units run the script from the checkout)
- **joins:** [Network policy](network-policy.md) (`egress.rules`), [DNS forwarder](dns-forwarder.md) (.3), the gateway's fixed IP (.2, `deploy/docker-compose.yml:81`; 172.30.1.2 in `deploy/docker-compose.offline.yml`)
- **looks-like-but-is-not:** the [sockproxy rules](sockproxy-rules.md)

## If you change this

- **Hits:** the security boundary (see [AGENTS.md](../../../AGENTS.md)); `test/integration/validate.sh` (V1–V16); the gateway and forwarder IPs in compose; the installed units on live hosts (see [effects](../../effects/CONTEXT.md))
- **Does not hit:** Go code, except the `egress.rules` format

## Surfaces

| Surface | Role |
|---|---|
| host systemd | runs it |
| operator | never runs it by hand on a live host without reason |

## See

- Source: `deploy/firewall.sh`, `deploy/termiplus-firewall*.{path,service,timer}`, `deploy/termiplus-portlock.service`
- Checks: `test/integration/VALIDATION.md`
