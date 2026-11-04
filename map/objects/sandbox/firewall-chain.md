---
type: object
cluster: sandbox
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: deploy/firewall.sh
---

# Firewall chain

Host iptables and ebtables rules that decide what a terminal on `ot-sandbox` (172.30.0.0/24) can reach: chain `TERMIPLUS` in `DOCKER-USER`, chain `TERMIPLUS-IN` in `INPUT`, and the ebtables `TERMIPLUS-AS` anti-spoof chain.

## Why this shape

A terminal has root inside its sandbox. So isolation cannot depend on anything inside the container:

- Only the gateway (.2) may open connections to terminals, and that allow is pinned to the gateway's real MAC so a terminal cannot spoof its IP.
- Terminal ↔ terminal traffic is dropped twice: here, and by Docker's `enable_icc:false`.
- A fixed deny-list is dropped before any admin allow can match. Private ranges are dropped unless an admin rule returned first. Public egress falls through.
- There is deliberately **no global ESTABLISHED,RELATED accept**. That would bypass policy for long-lived connections on every Docker network. Reply rules are scoped to the gateway and forwarder DNS.
- INPUT from the bridge is dropped, so terminals cannot reach host services. The forwarder may ask a host-local upstream.
- Offline bridge (`ot-offline`, 172.30.1.0/24, only when it exists): rules keyed on the interface come first in `TERMIPLUS` and `TERMIPLUS-IN` and drop everything except gateway (.2, MAC-pinned) → terminal and its replies; IPv6 is dropped too. The bridge is also a Docker internal network with `enable_icc:false`.

## Shape

- Variables: `SANDBOX`, `GATEWAY=.2`, `FORWARDER=.3`, `BRIDGE=ot-sandbox`, `OFFLINE_BRIDGE=ot-offline`, `OFFLINE_SUBNET`, `OFFLINE_GATEWAY=172.30.1.2`, `EGRESS_RULES`, `DNS_UPSTREAM` (from the environment or `deploy/.env`).
- `--if-needed` re-applies only when rules are missing or out of date (`rules_current`: chain jumps, the ebtables attachment, pinned MACs and every offline rule).
- Bridge netfilter must be on (`deploy/sysctl.d/90-termiplus-bridge.conf`, `ensure_bridge_netfilter`).
- systemd: `termiplus-firewall.service` (on Docker start), `.path` (on `egress.rules` change), `termiplus-firewall-watch.timer` (every 20 s, `--if-needed`).

Citations: `deploy/firewall.sh:1-26` (intent), `:35-39`, `:58` (apply_egress_allow), `:95` (ensure_bridge_netfilter), `:128` (pin_ip), `:140` (rules_current), `:175` (ensure_gateway_antispoof), `:219-280` (TERMIPLUS), `:281-299` (TERMIPLUS-IN), `deploy/docker-compose.yml:81`, `:87-101`

## Connected to

- **owns:** the host's TERMIPLUS chains
- **owned-by:** the host (systemd units run the script from the checkout)
- **joins:** [Network policy](network-policy.md) (`egress.rules`), [DNS forwarder](dns-forwarder.md) (.3), the gateway's fixed IP (.2, `deploy/docker-compose.yml:81`; 172.30.1.2 in `deploy/docker-compose.offline.yml`)
- **looks-like-but-is-not:** the [sockproxy rules](sockproxy-rules.md)

## If you change this

- **Hits:** the security boundary (see [AGENTS.md](../../../AGENTS.md)); `test/integration/validate.sh` (V1–V15); the gateway and forwarder IPs in compose; the installed units on live hosts (see [effects](../../effects/CONTEXT.md))
- **Does not hit:** Go code, except the `egress.rules` format

## Surfaces

| Surface | Role |
|---|---|
| host systemd | runs it |
| operator | never runs it by hand on a live host without reason |

## See

- Source: `deploy/firewall.sh`, `deploy/termiplus-firewall*.{path,service,timer}`
- Checks: `test/integration/VALIDATION.md`
