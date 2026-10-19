---
type: object
cluster: sandbox
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/netpolicy/netpolicy.go
---

# Network policy

The admin-set DNS entries and internal egress allowances for terminals. In code: `netpolicy.Policy` (`dns_entries`, `egress_allow`). It is **not** a terminal [Policy](../workspaces/policy.md).

## Why this shape

The default is fixed in code: public egress allowed, internal destinations denied. Admins need narrow exceptions ("this one internal Git server on 443") without editing the firewall. The rule is enforced in Go, not in the shell script: a fixed deny-list (loopback, link-local including the metadata address, CGNAT, the sandbox subnet) can never be opened, whatever the admin enters. The firewall re-checks it anyway.

## Shape

- `DNSEntry` (hostname → address) and `EgressRule` (CIDR, port, proto).
- `Validate` → `Validated`, which renders `DnsmasqHosts()` for the forwarder and `EgressRules()` (lines of `CIDR PORT PROTO`) for the firewall.
- `Store.Put` validates, then writes policy, hosts and egress files atomically. A rejected policy changes nothing on disk.
- Files (netpolicy overlay): `NETPOLICY_FILE`, `NETPOLICY_DNSMASQ_FILE`, `NETPOLICY_EGRESS_FILE` under `deploy/netpolicy/`.

Citations: `internal/netpolicy/netpolicy.go:1-11` (rule), `:43` (Policy), `:50-61` (denyList), `:72` (Validate), `internal/netpolicy/render.go:18`, `:41`, `internal/netpolicy/store.go:85` (Put), `:142` (writeFilesAtomic), `internal/gateway/admin.go:64` (putNetPolicy), `deploy/docker-compose.netpolicy.yml:55-59`

## Connected to

- **owns:** `deploy/netpolicy/{policy.json,egress.rules,hosts/managed}`
- **owned-by:** the gateway (`/admin/netpolicy`, admin key)
- **joins:** [Firewall chain](firewall-chain.md) (reads `egress.rules`), [DNS forwarder](dns-forwarder.md) (reads the hosts dir), [MCP tools](../control-plane/mcp-tools.md) (`get_network_policy`, `set_network_policy`)
- **looks-like-but-is-not:** a terminal Policy, and [Entitlements](../identity/entitlements.md)

## If you change this

- **Hits:** the deny-list is a security boundary (see [AGENTS.md](../../../AGENTS.md)); the `egress.rules` format is parsed again by `deploy/firewall.sh:47`; `test/integration/validate.sh`
- **Does not hit:** sockproxy, container naming

## Surfaces

| Surface | Role |
|---|---|
| admin via MCP or console | writes |
| gateway | validates and renders |
| firewall.sh, dnsmasq | read the rendered files |

## See

- Source: `internal/netpolicy/`
- Process: [netpolicy-apply](../../processes/netpolicy-apply.md)
