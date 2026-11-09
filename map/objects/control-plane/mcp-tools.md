---
type: object
cluster: control-plane
universe: live
status: verified
verified: 2026-11-08 (MCP App panel)
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

MCP App: `internal/mcp/app.go` adds the read-only `open_workspaces` entry
tool (model + app visible, `structuredContent`) and serves the embedded panel
`internal/mcp/app/workspaces.html` via `resources/list|read` (authenticated).
`toolMeta` marks the eight management tools `visibility:["app"]` in
`tools/list` only; `callTool`/`invoke` gates are unchanged, so app-only is UX,
not authorization. The overview carries `console_url` (`uiBase + "/ui/"`, absent without a console base) so the panel can open the console for restores. The panel never calls `request_session`. JSON-RPC
notifications get HTTP 202 with no body.

The four panel action tools (`destroy_session`, `reset_workspace`,
`share_desktop` create/revoke, `manage_snapshots` create) return the freshly
rebuilt panel state plus a human `note` via `app.go:panelResult` (built on
`app.go:overview`), so the panel re-renders from the action's own response in
one `tools/call` — no second `open_workspaces` refresh, which halves a host's
per-call approval prompts. The merge keeps each tool's original result fields
(e.g. share `url`) for model-only hosts, so the change is additive and the
gates/`_confirmed` rule are untouched. The panel shows a just-created share
`url` once (`showNewShare` in `app/workspaces.html`: read-only input set as
text, Copy and Dismiss buttons, cleared on the next action), because the share
store keeps only the token hash and the URL cannot be recovered later. `cmd/mcp` under `MCP_STUB_BACKEND=1`
uses `NewStubDevBackend`+`ConfigureStubServer` (in-memory shares/snapshots) so
every panel control works in the dev preview; the gateway path is unchanged.
Browser-level validation: `test/mcp-app/` drives the panel in headless chromium
through a host harness mirroring Open WebUI's AppBridge.

Factory reset: `wipe_home` (`internal/mcp/homes.go`, tool; `GatewayBackend.WipeHome`
-> `POST /control/v1/sessions/<id>/wipe-home`, `internal/gateway/control.go`)
is a destructive, app-visible tool that always requires `_confirmed=true`
(same gate as `destroy_session`). It is distinct from `reset_workspace`, which
keeps the home. `gateway.wipeHome` (`internal/gateway/userhomes.go`) is
fail-closed and ordered: (0) known prerequisites are checked before anything
is stopped (`Config.AllowHomeRemove`, the gateway's copy of `ALLOW_HOME_REMOVE`, else `errHomeRemoveOff` -> 403 "factory reset is disabled"; backups configured, home exists, snapshot limit not reached via
`backupStore.wouldPrune`); (1) mandatory `g.snapshot(..., prune=false)` of the
home; any error (backups not configured, snapshot limit reached, no home)
aborts and nothing is removed; (2) `manager.removeHome` (the already-allowed
home-remove, `ALLOW_HOME_REMOVE=1`; sockproxy is not widened; a proxy 403 despite the gateway flag becomes `errHomeRemoveDenied`, the snapshot is kept; the MCP server's `WithHomeRemove` makes the panel hide the action via `home_remove` in the panel state); (3) `ensure`
starts a fresh container on a new empty volume. Only persistent workspaces
qualify (temporary ones: `errNotPersistent`). `lockHomeTracked`
(`internal/gateway/homes.go`) stops the home's containers and reports which were
running; if the snapshot or `removeHome` then fails, `restartPolicies` starts
them again, so a refused wipe does not leave workspaces stopped. The result carries the snapshot
id in the panel note and `snapshot` field. Tests:
`internal/gateway/mcp_homes_e2e_test.go:TestMCPWipeHome*`,
`internal/mcp/app_test.go:TestWipeHomeIsAppOnlyAndNeedsConfirmation`.

Source: `internal/mcp/server.go:callTool`, `internal/mcp/server.go:invoke`,
`internal/mcp/homes.go`, `internal/mcp/gateway_backend.go`,
`integrations/openwebui/termiplus_workspaces.py`,
`internal/mcp/server_test.go:TestModelCannotInvokeConsoleToolsEvenAsAdmin`,
`internal/mcp/app_test.go`.

With `MCP_SHARES=off` (or `-tags noshare`) `share_desktop` is not registered at
all (`Server.assembleTools`); `modelTool`/`appOnly` keep it only as a static
upper bound. See [Share](share.md#kill-switch-mcp_sharesoff--tags-noshare).
See [tool call](../../processes/mcp-tool-call.md) and [Share](share.md).

Console modal scroll locking and consistent heading/preview/action card layout
are in `internal/mcp/ui/app.js:ask` and `loadSessions`; failed desktop previews
collapse until an image loads, styled by `ui/app.css`.
