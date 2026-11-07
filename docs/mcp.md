# Workspace control plane (alpha)

Termiplus manages isolated containers, resource limits, egress, persistent homes,
backups and workspace access. Open WebUI **0.13+** and Open Terminal **0.16.0**
provide the interactive workspace: terminal, files, Git, live events, Desktop,
Browser automation and local app previews. See [native integration](native-integration.md).

The MCP server has no Docker socket. Its `GatewayBackend` calls the gateway's
`/control/v1/` API with a separate control key and the authenticated user's Open
WebUI id. The gateway alone provisions containers through the restricted socket
proxy. Neither model arguments nor share tokens choose another user's container.

## Model tools

| Tool | Behavior |
| --- | --- |
| `list_workspaces`, `list_sessions` | List entitled workspaces and the caller's sessions |
| `request_session` | Provision a workspace; persistent provisioning needs the persist role and confirmation |
| `destroy_session` | Remove the caller's container with confirmation; keep a persistent home |
| `reset_workspace` | Recreate a persistent container while keeping `/home/user`, or remove a temporary sandbox; confirmation required |
| `manage_snapshots` | **List/create only.** At capacity creation fails; it cannot delete/prune/restore an older backup |
| `share_desktop` | List, create or revoke the caller's expiring view-only screen links; creation needs confirmation |
| `open_workspaces` | Read-only overview that opens the workspace panel in MCP Apps hosts (see below) |

The Open WebUI plugin also offers `open_console`, which returns a human console
link. It does not run an administrative operation.
The [complete supported tool inventory](tool-support.md) distinguishes these
seven plugin MCP tools, the `open_workspaces` panel entry and one plugin-only link from the 23 native Open Terminal tools.

**Network policy, entitlements, users, groups, audit queries and departed-user
management are not model tools.** The MCP transport rejects direct calls even
from an admin account and even with a forged `_confirmed` argument. Human
console routes call an internal dispatcher after OIDC, role and CSRF checks.
Snapshot deletion/restoration and pruning are available only through that human
path. Identity-provider administration remains available in the provider console.

The plugin's confirmation modal protects against accidental destructive calls
in normal chat use. It is not cryptographic proof of human consent for a client
holding an MCP credential; server-side allowlists independently enforce the
administration and snapshot restrictions above.

## Workspace panel (MCP App)

Hosts that support [MCP Apps](https://modelcontextprotocol.io/extensions/apps)
(`io.modelcontextprotocol/ui`, Open WebUI 0.13+) get one model-visible entry
tool, `open_workspaces`. It is read-only and returns the caller's entitled
workspaces, sessions, home backups (list) and active screen links, rendered as
an inline panel from the `ui://termiplus/workspaces` resource. The panel is a
single embedded HTML file with no remote scripts, styles, frames or network
access (empty CSP domain lists); it renders data as text only and opens backup
downloads only as `http(s)` links through the host.

The panel's buttons call the existing tools (`reset_workspace`,
`destroy_session`, `manage_snapshots` create, `share_desktop`) through the host.
Those tools are listed with `_meta.ui.visibility: ["app"]`, so an MCP Apps host
hides them from the model. The panel never calls `request_session`; opening it
starts nothing. Destructive buttons need a second click in the panel, and the
host asks the user before every app tool call.

Visibility is a host convention, not authorization. The server enforces the same
allowlist, RBAC, entitlement and `_confirmed` rules for every caller, so the
Open WebUI plugin keeps working unchanged and a forged call gains nothing.
Snapshot restore/delete, pruning and administration stay in the human console.

A native Open WebUI MCP connection (Streamable HTTP) goes through the same
`oauth2-proxy` as the plugin: authentication **OAuth** (the user's SSO token)
and Headers `{"X-Termiplus-Owui-Key": "<shared key>", "X-Termiplus-Owui-User":
"{{USER_ID}}"}`, so `ProxyHeaderAuthenticator` links the same Open WebUI id.
Verify the panel in your Open WebUI version before retiring the plugin.

## Working together in the native Desktop

1. Configure an Open WebUI terminal connection to `http://gateway:8080/p/default`
   for Desktop, or `http://gateway:8080/p/scratch` for Temporary desktop,
   with the gateway API key. The policy must enable `desktop` and select the
   native desktop image. Apply Open WebUI connection access restrictions.
2. Select that connection in the chat's terminal picker. For an ephemeral policy,
   save the chat first and keep the same chat for every tab/tool.
3. Open **Desktop** in the native workspace. Ask the assistant to use that
   connection's native desktop screenshot/action tools. Human and assistant
   use the same display. Browser uses the native Browser tab and automation.

A session created through MCP/console does **not** switch the chat's connection.
Persistent policies resolve to the same user/policy container. Ephemeral MCP
provisioning creates an independent session; to work with the assistant in an
ephemeral Desktop, use the selected connection in a saved chat and let native
requests provision it. No VNC password, separate owner Desktop URL or `open_port`
tool is involved. Local apps appear through native `/ports`, `/proxy` and Browser.
Native app-preview WebSocket support depends on the upstream protocol; this
migration does not promise generic WebSocket forwarding to arbitrary local ports.

## Browser and Desktop transport

HTTP requests carry the gateway bearer, `X-User-Id` and `X-Session-Id`. Open WebUI
sets these on its authenticated relay. The gateway replaces the bearer with a
derived container key and strips cookies. Desktop/audio, PTY, process and events
WebSockets use first-message authentication. Optional `chat_id` in that message
selects the same ephemeral container as HTTP; conflicting contexts fail closed.

Only native `/browser/view/<session>/<capability>/{ws,stream,vnc}` WebSockets use
header bearer authentication, matching Open WebUI's browser relay. The gateway
validates its key before provisioning and forwards application frames unchanged,
without injecting the Desktop auth message. A browser capability alone never
replaces gateway authentication or user routing. URL capabilities must not be
logged. Public clients must not receive the shared gateway key or choose arbitrary
identity headers. Keep the gateway behind the trusted Open WebUI relay.

## Console, homes and access

`deploy/docker-compose.desktop.yml` adds browser OIDC for the human console at
`CONSOLE_PUBLIC_URL/ui/`. Users must use the plugin once to link their OIDC subject
to their Open WebUI id. Binding conflicts fail closed. The default entitlement
file denies workspaces until an admin grants access. Entitlements apply to MCP
and console operations; configure the corresponding Open WebUI connection access
as well, since the terminal gateway trusts its authenticated relay.

Persistent homes are volumes derived from user id and home profile. Policies
can share a home profile, including terminal and desktop. Ephemeral policies
have no persistent home. Stopping a container preserves a persistent home;
reset removes it only after confirmation. Snapshots and departed-user archives
live under gateway `BACKUP_DIR`, not in MCP data. `make check` covers lifecycle,
archives, ownership, role checks and the human/model boundary.

The human console supports session lifecycle, home downloads, snapshots and
screen-share management. Admins manage entitlements and network policy there.
User/group membership is managed in the identity provider's console; internal
directory support remains for the departure watcher and role checks.

## Expiring view-only screen shares

Open WebUI has no verified replacement for Termiplus' owner-issued, expiring,
revocable screen links. Termiplus retains those links using authenticated native
`/desktop/screenshot` images, **refreshed every five seconds**. This replaces the
old interactive VNC share transport: no input, clipboard, audio, arbitrary
container HTML or WebSocket is exposed to recipients. This is periodic viewing,
not full-motion screen streaming.

Recipients sign in on the separate share origin; links can be limited to a named
OIDC subject. Tokens are random and stored hashed. Expiry/revocation cancels
in-flight reads. The gateway checks the owner, desktop policy and running state
without starting a stopped container. Images have byte/dimension limits and are
re-encoded; the viewer serves its own restrictive HTML/CSP only.

Console thumbnails use the same native display, normalized to at most 320 pixels,
with the existing 30-second cache. They do not capture a parallel legacy display.

## Deployment settings

Use the overlays and secret files in [deployment](deploy-alpha.md). The MCP
process accepts `GATEWAY_URL`, `CONTROL_API_KEY[_FILE]`, `MCP_AUTH=proxy`,
`MCP_OWUI_PLUGIN_KEY[_FILE]`, `MCP_IDENTITY_FILE`, `MCP_ENTITLEMENTS_FILE`,
`MCP_AUDIT_FILE`, `UI_LISTEN_ADDR`, `CONSOLE_PUBLIC_URL`, `STREAM_LISTEN_ADDR`
and `STREAM_PUBLIC_URL`. The last two names are retained for configuration
compatibility but now serve **only `/share/`**. The console and share origin
must differ. The optional `docker-compose.mcp-netpolicy.yml` grants the human
console its network-policy backend; it does not expose a model tool.
`docker-compose.mcp-users.yml` supplies the directory service account.

`MCP_AUTH=dev` and the stub backend are for local tests only. Production proxy
mode requires the plugin key, durable identity bindings and an entitlement file.
Keep MCP listeners private to their oauth2-proxies. Never expose trusted identity
headers directly to browsers. The append-only audit log records changes through
the dispatcher; direct gateway admin calls appear in the gateway log instead.

## Migration from the alpha checkpoint

Checkpoint: `808cc87`. Rebuild terminal and desktop images together. Upgrade the
Open WebUI tool to version 0.6.0 and use the native terminal connection. Recreate
workspace containers through the normal lifecycle so their new feature flags
apply; keep existing home volumes and backups. Do not run the old Kasm entrypoint
beside the native desktop.

Removed: KasmVNC, its passwords/themes/player and owner stream routes; custom
port proxy, `fwd-auth`, port-link UI and AI admin tools. Remove old
`FWD_LISTEN_ADDR`, `FORWARD_PUBLIC_URL` and `FORWARD_OIDC_CLIENT_ID` settings.
Existing forward OIDC clients/secrets can be retired manually; setup never
deletes deployed credentials. Old `/stream/`, `/control/v1/stream/` and
`/control/v1/forward/` links return 404. Existing screen-share links keep their
stored token/expiry semantics and use the new image-only viewer.

Native Chromium uses its sandbox. The migration adds no Docker capability,
mount, host port, runtime exception or firewall allowance. If a runtime cannot
start Chromium securely, report that failure; do not silently add `--no-sandbox`.
