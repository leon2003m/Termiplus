# Open WebUI workspace tool

Requires Open WebUI **0.13+**, OIDC sign-in and the Termiplus MCP overlay.
Open Terminal **0.16.0** supplies desktop/browser interaction through the native
terminal connection. This tool manages only Termiplus workspace lifecycle,
list/create snapshots and expiring screen-share links. It exposes no network,
user/group, departed-user, entitlement or backup-deletion administration.

Install `termiplus_workspaces.py` (version 0.6.0) under Workspace → Tools. Set
`mcp_url` (default `http://mcp-auth:4180/`), `plugin_key` from
`deploy/secrets/mcp_owui_plugin_key`, and optional `ui_url` pointing to the human
console. Open WebUI must forward the signed-in OIDC token and supply the user id;
set `OAUTH_SESSION_TOKEN_ENCRYPTION_KEY` for its stored OAuth session.

Select the desired native terminal connection **in the chat** and open Desktop
or Browser there. The assistant uses that connection's native tools. Calling
`request_session` does not select or attach a connection; ephemeral sessions
created by it are separate from a chat's automatic native session.

Destructive lifecycle/share operations use the human confirmation dialog;
anything other than an explicit affirmative answer cancels. Server-side roles,
entitlements and ownership still apply. Snapshot creation cannot implicitly
prune old snapshots, and direct MCP calls cannot bypass the admin-tool exclusion.
Legacy desktop passwords/stream URLs are removed from tool results.

`make test-plugin` runs the plugin against the real MCP binary with a stub
backend: authentication, identity headers, dialogs, cancellation, and admin/model
separation. See [control plane](../../docs/mcp.md) for the complete contract.
