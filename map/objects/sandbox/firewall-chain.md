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

## Shape

- Variables: `SANDBOX`, `GATEWAY=.2`, `FORWARDER=.3`, `BRIDGE=ot-sandbox`, `EGRESS_RULES`, `DNS_UPSTREAM` (from the environment or `deploy/.env`).
- `--if-needed` re-applies only when rules are missing or out of date (`rules_current`).
- Bridge netfilter must be on (`deploy/sysctl.d/90-termiplus-bridge.conf`, `ensure_bridge_netfilter`).
- systemd: `termiplus-firewall.service` (on Docker start), `.path` (on `egress.rules` change), `termiplus-firewall-watch.timer` (every 20 s, `--if-needed`).

Citations: `deploy/firewall.sh:1-26` (intent), `:35-39`, `:47` (apply_egress_allow), `:84` (ensure_bridge_netfilter), `:116` (pin_ip), `:125` (rules_current), `:145` (ensure_gateway_antispoof), `:178-225` (TERMIPLUS), `:230-243` (TERMIPLUS-IN), `deploy/docker-compose.yml:81`, `:87-101`

## Connected to

- **owns:** the host's TERMIPLUS chains
- **owned-by:** the host (systemd units run the script from the checkout)
- **joins:** [Network policy](network-policy.md) (`egress.rules`), [DNS forwarder](dns-forwarder.md) (.3), the gateway's fixed IP (.2, `deploy/docker-compose.yml:81`)
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
