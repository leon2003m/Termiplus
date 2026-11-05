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

## Install through the Open WebUI API

The following request sequence applies to Open WebUI 0.13.0. Use an admin API
credential supplied by the administrator and an HTTPS origin such as
`https://chat.example.com`; all paths below are relative to that origin. Keep
the credential in memory (for example, prompt with Python `getpass.getpass()`),
and send `Authorization: Bearer <ADMIN_TOKEN>` and `Content-Type: application/json`.
Do not put credentials in command arguments, request files, shell history or
workspace containers, or print response bodies: valve responses contain secrets.
If the credential is unavailable or rejected, stop and ask the administrator;
do not recover credentials from the database or mint a token as an install step.

1. Read `integrations/openwebui/termiplus_workspaces.py` from the checked-out
   release; its header must say `version: 0.6.0`. Send `GET /api/v1/tools/`
   and find the exact tool name `termiplus workspaces` or ID
   `termiplus_workspaces`. Reuse an existing ID; stop if the match is ambiguous.
2. Create with `POST /api/v1/tools/create`, or update with
   `POST /api/v1/tools/id/<TOOL_ID>/update`. The JSON body is:

   ```json
   {
     "id": "<TOOL_ID>",
     "name": "termiplus workspaces",
     "content": "<ENTIRE_CONTENTS_OF_termiplus_workspaces.py>",
     "meta": {"description": "Manage termiplus workspaces through MCP."}
   }
   ```

   Generate the body with a JSON encoder using the actual file contents, not
   the placeholder. For updates, preserve the existing `meta` and
   `access_grants` returned by `GET /api/v1/tools/id/<TOOL_ID>`.
3. Read `GET /api/v1/tools/id/<TOOL_ID>/valves` into memory and merge these
   settings, preserving other valves; send the merged object directly to
   `POST /api/v1/tools/id/<TOOL_ID>/valves/update` (no `valves` wrapper):

   ```json
   {
     "mcp_url": "http://mcp-auth:4180/",
     "plugin_key": "<CONTENTS_OF_mcp_owui_plugin_key>",
     "ui_url": "https://desk.example.com/ui/"
   }
   ```

   The plugin key comes from the deployment's `mcp_owui_plugin_key` secret:
   normally `deploy/secrets/mcp_owui_plugin_key`, or
   `/opt/docker/termiplus/secrets/mcp_owui_plugin_key` when using that external
   secrets directory. Read it directly into memory and strip its trailing
   newline. The MCP URL is server-side on Open WebUI's Docker network; the UI
   URL is the browser console URL, including `/ui/`. Neither is an Open WebUI admin credential.
4. Enable the tool on the target model: `GET
   /api/v1/models/model?id=<MODEL_ID>` (for example, `termiplus-test`). Append
   `<TOOL_ID>` to `meta.toolIds` only if absent, preserving existing tool IDs
   and all other model settings, including access grants. POST that model
   object to `/api/v1/models/model/update`; do not replace `meta` wholesale.
5. Verify with fresh GET requests: `/api/v1/tools/id/<TOOL_ID>` must contain
   source matching the local file and the `version: 0.6.0` header;
   `/api/v1/tools/id/<TOOL_ID>/valves` must have nonempty `mcp_url`,
   `plugin_key`, and `ui_url`; `/api/v1/models/model?id=<MODEL_ID>` must include
   the ID in `meta.toolIds`. Report only the version and presence/enabled
   checks, never valve values or the admin credential. Discard the credential
   after verification. These checks do not require reading or sending chats.
