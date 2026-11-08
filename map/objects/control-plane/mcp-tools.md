---
type: object
cluster: control-plane
universe: live
status: verified
verified: 2026-11-04 (native integration)
entity: internal/mcp/server.go
---

# MCP tools and human dispatcher

The model-facing JSON-RPC surface is an explicit allowlist in
`internal/mcp/server.go:modelTool`. It permits workspace/session listing,
request/destroy/reset lifecycle, snapshot list/create, and expiring desktop
shares. `listTools` filters discovery; `callTool` independently rejects direct
calls to every excluded tool, including admin callers with `_confirmed=true`.

Snapshot delete/restore are refused at this transport. Model confirmation is
removed for snapshot creation so it can never request implicit pruning. A full
backup store returns a capacity error; the human console handles pruning.

Network policy, entitlements, user/group, audit and departed management stay in
the internal dispatcher for human administration/background services. They are
not model tools. `internal/mcp/ui.go` checks OIDC, roles and CSRF before `invoke`.
The provider console handles user/group membership. `invoke` retains ownership,
RBAC, confirmation and audit behavior for all authorized paths.

`GatewayBackend` calls `/control/v1/` using the control key and the linked
Open WebUI id. Plugin confirmation is normal UX, not cryptographic authorization
against a client that already holds credentials. Native desktop/browser/files/
execution are supplied by the selected Open WebUI connection, not these tools.
MCP provisioning does not automatically attach a chat to a connection.

Source: `internal/mcp/server.go:callTool`, `internal/mcp/server.go:invoke`,
`internal/mcp/homes.go`, `internal/mcp/gateway_backend.go`,
`integrations/openwebui/termiplus_workspaces.py`,
`internal/mcp/server_test.go:TestModelCannotInvokeConsoleToolsEvenAsAdmin`.
See [tool call](../../processes/mcp-tool-call.md) and [Share](share.md).

Console modal scroll locking and consistent heading/preview/action card layout
are in `internal/mcp/ui/app.js:ask` and `loadSessions`; failed desktop previews
collapse until an image loads, styled by `ui/app.css`.
