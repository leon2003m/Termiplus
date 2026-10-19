---
type: object
cluster: control-plane
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/mcp/server.go
---

# MCP tools

The JSON-RPC tools the MCP server offers to chat (via the Open WebUI tool) and to the management console. Each tool has a gate (RBAC), an optional confirmation, a handler, and an audit entry. In code: `mcp.tool`, built in `buildTools`.

## Why this shape

An LLM picks the calls, so the server must not trust *it*: every destructive tool needs `"_confirmed": true`, which only the plugin's human modal adds. Admin tools are gated on `termiplus-admin`. Workspace access is re-checked against [Entitlements](../identity/entitlements.md) on every call. The console runs the same `invoke` path, so chat and UI cannot drift apart.

## Shape

| Tool | Gate | Confirm | Source |
|---|---|---|---|
| `list_workspaces`, `list_sessions` | entitled | no | `server.go:416`, `:454` |
| `request_session` | entitled; persistent needs `termiplus-persist` | persistent only | `server.go:430` |
| `destroy_session` | own session | yes | `server.go:468` |
| `reset_workspace`, `manage_snapshots` | own home | yes | `homes.go:260`, `:286` |
| `share_desktop` | own desktop session | yes | `shares.go:330` |
| `get_network_policy`, `get_entitlements`, `get_audit_log`, `manage_group` | admin | no | `server.go:485`, `:495`, `:561`, `:541` |
| `set_network_policy`, `set_entitlements`, `manage_user`, `manage_departed` | admin | yes | `server.go:524`, `:506`, `:551`, `departed.go:310` |

- Transport: `POST` JSON-RPC 2.0 (`initialize`, `tools/list`, `tools/call`), body ≤ 1 MiB.
- `callTool`: authenticate, record the identity binding (fail closed on conflict), then `invoke`.
- `invoke`: gate (denied → audit "denied"), confirm check, handle, audit "ok" or "error".
- Backend: `GatewayBackend` calls `/control/v1/` with `X-Control-Key` and `X-User-Id`.

Citations: `internal/mcp/server.go:154-166` (tool), `:231` (ServeHTTP), `:303` (callTool), `:337` (invoke), `:413` (buildTools), `:575` (adminOnly), `internal/mcp/gateway_backend.go:187` (do)

## Connected to

- **owns:** no state of its own
- **owned-by:** the MCP server (`cmd/mcp`)
- **joins:** [Principal](../identity/principal.md), [Entitlements](../identity/entitlements.md), [Audit log](audit-log.md), [Share](share.md), [Departed](departed.md), [Directory](../identity/directory.md), the gateway control API
- **looks-like-but-is-not:** Open Terminal's own API, which Open WebUI reaches through `/p/<policy>/`

## If you change this

- **Hits:** the plugin `integrations/openwebui/termiplus_workspaces.py` (tool names, the confirmation modal), the console `internal/mcp/ui.go` and `internal/mcp/ui/`, `test/integration/alpha-e2e.sh`, `internal/mcp/server_test.go`
- **Does not hit:** sockproxy, firewall

## Surfaces

| Surface | Role |
|---|---|
| Open WebUI tool | calls (through oauth2-proxy) |
| console (`UI_LISTEN_ADDR`) | calls `invoke` directly |
| gateway | serves the backend calls |

## See

- Source: `internal/mcp/server.go`
- Process: [mcp-tool-call](../../processes/mcp-tool-call.md)
