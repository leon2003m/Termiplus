---
type: process
status: verified
verified: 2026-11-04 (native integration)
consumes: [../objects/sandbox/network-policy.md, ../objects/identity/service-keys.md]
produces: [../objects/sandbox/dns-forwarder.md, ../objects/sandbox/firewall-chain.md]
---

# netpolicy-apply

An admin's network policy (DNS entries and internal egress allows) becomes live in the DNS forwarder and the host firewall.

## Input → Movement → Output

An admin sends a policy, either with `PUT /admin/netpolicy` and the admin key or through the human console. The gateway validates it against the deny list. It then writes three files atomically: `policy.json`, the dnsmasq hosts file and `egress.rules`. dnsmasq picks up the hosts file through inotify. A systemd `.path` unit sees `egress.rules` change and reruns `deploy/firewall.sh`.

## Why this shape

The gateway runs unprivileged and never touches iptables. It only renders files. The host applies them, so the privileged step stays in one reviewed script. Validation happens twice: in Go before writing, and again in `firewall.sh` per line. The rules file lives on the host and could be tampered with. The fixed deny DROP rules come before the allows, so no entry can open loopback, link-local or metadata, CGNAT, or the sandbox subnet. Writing all three files atomically keeps DNS and egress from disagreeing.

## Steps

1. The human console invokes the internal `set_network_policy` handler after admin-role and CSRF checks. Model-facing MCP discovery and calls reject this tool, including for administrators: `internal/mcp/server.go`, `internal/mcp/ui.go`. The backend forwards the authorized console action to the gateway: `internal/mcp/gateway_backend.go`.
2. `serveAdmin` returns 404 when no admin key or policy store is configured. It routes `/admin/netpolicy`: `internal/gateway/admin.go:19-24`, `:32`, `:49`. The key is checked at `checkAdminKey`, `admin.go:97`.
3. `putNetPolicy` takes a size-limited body, decodes it strictly and rejects trailing data: `admin.go:65`.
4. `Store.Put` → `write` → `Validate`, which checks the `DenyList` plus any extra deny: `internal/netpolicy/store.go:85`, `:93`, `netpolicy.go:72`, `:147`.
5. Render, then write the three files atomically: `render.go:18` (`DnsmasqHosts`), `:41` (`EgressRules`), `store.go:142` (`writeFilesAtomic`).
6. The forwarder reloads the hosts dir via inotify; no signal is needed: `deploy/dns-forwarder/entrypoint.sh:7-9`, `:20`.
7. `termiplus-firewall.path` (`PathChanged=…/egress.rules`) starts `termiplus-firewall.service` → `deploy/firewall.sh`. `apply_egress_allow` rechecks each line after the deny list: `deploy/firewall.sh:16-20`, `:56`, `:68-97`, `:540`.
8. Separately, the watch timer (every 20 s) reruns `firewall.sh --if-needed`. It acts only when the chains or pinned MACs are missing: `deploy/termiplus-firewall-watch.timer`, `firewall.sh:363-410`.

## If you change this

- **Hits:** the netpolicy file paths (`NETPOLICY_*` in `cmd/gateway/main.go:51-53` and `deploy/docker-compose.netpolicy.yml:55-57`), the file format shared by `render.go` and `firewall.sh`, `internal/netpolicy/*_test.go`. `firewall.sh` and the deny list are security boundaries ([AGENTS.md](../../AGENTS.md)).
- **Does not hit:** the terminal API path, sockproxy, the container create.

## Surfaces

| Surface | Role |
|---|---|
| admin (curl or human console) | sends the policy |
| gateway `/admin` | validates, renders, writes |
| dns-forwarder | serves DNS entries |
| host systemd `.path` + `firewall.sh` | applies egress |

## See

- Objects: [Network policy](../objects/sandbox/network-policy.md), [Firewall chain](../objects/sandbox/firewall-chain.md), [DNS forwarder](../objects/sandbox/dns-forwarder.md)
- Source: `internal/gateway/admin.go`, `internal/netpolicy/`, `deploy/firewall.sh`

## Native integration update (2026-11-04)

Network policy is a human-only console/admin action; modelTool rejects get/set_network_policy even for admins. Source: internal/mcp/server.go and ui.go.
