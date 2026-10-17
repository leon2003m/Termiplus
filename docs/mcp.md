# termiplus MCP control plane (v1.0 — `alpha` track)

> Status: **design contract, partly implemented** (see §12). This is the agreed
> shape of the v1.0 "Kasm rebuild as an MCP" work that lives on the `alpha`
> branch. The hardened terminal core ships separately as v0.1.0 from `dev`.

## 1. What this is

termiplus today gives Open WebUI (OWUI) per-user sandboxed terminals. v1.0 turns
it into a **Kasm-style workspace control plane exposed over MCP**: an agent (or a
human, through OWUI) can provision, manage and hand off isolated desktop/terminal
sessions, with persistence and network reach governed per user.

Design principles:

- **Control plane, not a file proxy.** The MCP exposes *provisioning and
  lifecycle*, not `read_file`/`write_file`/`exec`. The agent already has native
  filesystem and terminal access *inside* its session, so re-proxying those as
  tools is redundant.
- **Verified identity, not a trusted header.** Every call is tied to a
  cryptographically verified OAuth/OIDC identity. This retires termiplus's
  current "trusted `X-User-Id`" limitation.
- **Agent proposes, human disposes.** Every destructive or security-sensitive
  action requires an explicit human confirmation in OWUI, on top of RBAC.

## 2. Architecture

```
  Keycloak (OIDC)                   (any OIDC IdP; Keycloak first)
       │ SSO
       ▼
  Open WebUI ── native "termiplus" Tool plugin
       │            │  - raises __event_call__ confirmation modals
       │            │  - forwards user's __oauth_token__ (Bearer)
       │            │  - names the Open WebUI user (plugin key)
       │            │  - renders DE preview embeds + management-UI link
       ▼            ▼
  oauth2-proxy  ── validates the JWT (JWKS, iss, aud, exp); forwards sub + groups
       │  (internal network ot-mcp-auth: the only way to reach the MCP server)
       ▼
  termiplus MCP server  ── proxy headers -> roles; Open WebUI user id -> user_id
       │  (sole setter of the gateway X-User-Id, over the API-key link)
       ▼
  gateway ── sockproxy ── Docker ── session containers (runsc / sysbox)
       │                                   │
       │                                   └── KasmVNC / Guacamole stream
       └── management UI (web console, OAuth-protected)
```

Mapping to Kasm's components: gateway ≈ API/connection-proxy, the gateway
manager ≈ Kasm Manager + Agent, sockproxy ≈ the agent's confined Docker access.

## 3. Identity & authentication

- **Protocol:** OAuth2 / OIDC, written **generically** — standard OIDC discovery
  (`/.well-known/openid-configuration`) + JWKS signature verification, with
  configurable issuer, audience and claim names. **Keycloak is the first
  configured issuer; any OIDC IdP works.**
- **Flow:**
  1. The user signs in to OWUI via Keycloak SSO.
  2. The native OWUI plugin receives the user's token via the injected
     **`__oauth_token__`** parameter (`access_token`, `id_token`; auto-refreshed,
     encrypted at rest by OWUI) and sends it as `Authorization: Bearer …` to the
     MCP server.
  3. **oauth2-proxy validates the JWT** in front of the MCP server (signature via
     the issuer's JWKS, `iss`/`aud`/`exp`; no login redirects, no cookie
     sessions) and forwards `X-Forwarded-User` (`sub` → `user_id`) and
     `X-Forwarded-Groups`, stripping any client-supplied copies. **termiplus
     contains no token-verification code of its own**: authentication is
     delegated to that established, independently maintained proxy, and the MCP
     server only reads its headers. The listener also shares an internal network
     with the gateway so it can call the control API; therefore a plugin key is
     mandatory in proxy mode, preventing a gateway-network peer from authenticating
     merely by forging proxy identity headers.
  4. **Sessions belong to the Open WebUI user**, so they share homes with that
     user's Open WebUI terminals. `MCP_OWUI_PLUGIN_KEY` is mandatory in proxy
     mode; the plugin sends `X-Termiplus-Owui-Key` (a shared secret, the
     plugin's `plugin_key` valve) and `X-Termiplus-Owui-User` (`__user__["id"]`).
     The MCP server then uses that id as `user_id` and still takes the roles
     from the verified token; without both a verified token and the key the call
     is unauthenticated. The first valid plugin call creates a persistent
     subject-to-Open-WebUI-user binding. That binding is one-to-one and
     immutable through normal requests: a later request trying to rebind either
     side is rejected before any tool runs (JSON-RPC error `-32003`). If the
     binding cannot be stored, the call fails with `-32603` and no tool runs.
     `MCP_OWUI_PLUGIN_KEY` without `MCP_IDENTITY_FILE` refuses to start, since
     there would be nothing to bind against. Proxy mode also requires an explicit
     `MCP_ENTITLEMENTS_FILE`; a missing file now stops startup rather than opening
     every workspace. Re-linking requires stopping the
     MCP server, editing `MCP_IDENTITY_FILE` and starting it again: the file is
     read only at startup and rewritten from memory on the next enrollment, so
     an edit made while it runs is lost. This prevents a stolen/reused plugin
     key from silently transferring an already-linked user's home.
     The plugin key remains a bootstrap trust anchor: keep it only in the
     Open WebUI admin-controlled valve and Docker secret. Keycloak remains the
     source of authentication and groups.
  5. The MCP server is the **only** component that sets the gateway's
     `X-User-Id`, over the existing API-key-authenticated, private MCP↔gateway
     link. The gateway therefore receives a trustworthy identity.
- **Persistent homes** are templated per `user_id` (Kasm-style), never shared by
  name alone (see also audit fix #16 on the core).

**Deployment requirements / gotchas:**

- OWUI **≥ 0.8.6** (CVE-2026-88005: earlier versions could invoke server tools
  with the server's own token).
- Set `OAUTH_SESSION_TOKEN_ENCRYPTION_KEY` (shared across OWUI replicas).
- Keycloak places roles in the **access token** by default while OWUI reads the
  **ID token** — add a client role mapper emitting the `roles` claim where OWUI
  expects it. Enable `ENABLE_OAUTH_ROLE_MANAGEMENT` / `ENABLE_OAUTH_GROUP_MANAGEMENT`
  with the matching `OAUTH_ROLES_CLAIM` / `OAUTH_GROUPS_CLAIM`.

## 4. Authorization — the two-layer gate

Both layers are required for a consequential action:

1. **RBAC (entitlement), from token claims.** Roles/groups decide what a user is
   *allowed* to do at all:
   - `termiplus-persist` → may create **persistent** sessions (else ephemeral only).
   - `termiplus-admin` → may use the network-policy and user/group admin tools.
   - a resource-tier role/group → memory/CPU/session-count caps.
2. **Human-in-the-loop (consent), in OWUI.** **Every** destructive or
   security-sensitive action pauses on an OWUI `__event_call__` **confirmation
   modal** and only proceeds on an explicit human *yes*. The agent can *propose*;
   it cannot self-approve.

Actions requiring the gate (entitled **and** confirmed): creating a persistent
session, destroying a session, changing network policy, and any admin
user/group change. Listing and creating a throwaway *ephemeral* session within
one's entitlement are autonomous.

## 5. MCP tool surface (control plane)

| Tool | Purpose | Annotations | Gate |
|------|---------|-------------|------|
| `list_workspaces` | Workspaces/profiles the caller's groups may launch | readOnly | none |
| `request_session(workspace, persistent?=false, label?)` | Provision (or attach to) a session; returns an **access handle** | — | **persistent → entitlement + confirm** |
| `list_sessions()` | The caller's active sessions | readOnly | none |
| `destroy_session(session_id)` | Tear a session down | destructive | **confirm** |
| `get_network_policy()` *(admin)* | Show custom DNS entries + internal egress whitelist | readOnly | **admin entitlement** |
| `set_network_policy(policy)` *(admin)* | Replace custom DNS + egress whitelist (wraps the core admin API; the deny-list still wins) | destructive | **admin entitlement + confirm** |
| `manage_group(action, group?)` *(admin)* | List the `termiplus-*` groups or one group's members | readOnly | **admin entitlement** |
| `manage_user(action, username, group?)` *(admin)* | Show a user's `termiplus-*` groups, add or remove them (see *User management*) | destructive | **admin entitlement + confirm** for changes |
| `get_audit_log(actor?, tool?, since?, limit?)` *(admin)* | Recorded state-changing calls, newest first (see *Audit log*) | readOnly | **admin entitlement** |
| `reset_workspace(session_id)` | Start a session over from a fresh container (persistent: home kept) | destructive | **confirm** |
| `manage_snapshots(action, workspace, snapshot_id?)` | Snapshots of a persistent workspace's home: list (with download links), create, restore, delete (see *Backups*) | destructive | workspace entitlement; **confirm** for restore and delete |
| `share_desktop(action, session_id?, minutes?, for_user?, share_id?)` | View-only links to one of your desktops: create, list, revoke (see *Shared desktops*) | destructive | **confirm** for create |
| `manage_departed(action, username?)` *(admin)* | Departed users and their archived homes: list, archive now, restore (see *Departed users*) | destructive | **admin entitlement + confirm** for archive and restore |

Deliberately **absent**: `read_file`, `write_file`, `list_dir`, `exec` — done
natively inside the session.

**Access handle** returned by `request_session`:

```json
{
  "session_id": "…",
  "workspace": "…",
  "persistent": false,
  "terminal_url": "https://…/p/<policy>/…",   // text/WS session (first spike)
  "stream_url": null                            // KasmVNC/Guacamole (next spike)
}
```

## 6. Session lifecycle (Kasm model)

- **Ephemeral by default.** A session is a throwaway container unless persistence
  is requested *and* entitled *and* confirmed.
- **Persistent** sessions mount the user's `{user_id}`-templated home; the user
  may use it, start throwaway, or reset it — admins may enforce persistence per
  workspace.
- Idle reaping and capacity limits are inherited from the gateway manager.

## 7. OWUI integration — the native plugin

A native OWUI **Tool/Function plugin** (admin-installed; only native tools get
`__oauth_token__`). Responsibilities:

- Call the MCP server for each tool, forwarding the user's bearer token.
- Raise the **confirmation modal** (`__event_call__`, type `confirmation`) before
  any gated action; abort on cancel.
- Render **DE previews** and the **management-UI link** (below).
- Surface progress/errors via `__event_emitter__` status/notification.

Plugin UI reach (normal plugin, no frontend fork): inline iframe
(`HTMLResponse`), `embeds` events (HTML/URL + `replace`), the artifacts side
panel, modals, toasts, toolbar buttons, and `execute` JS. New top-level
screens/routes/sidebar items would require forking the OWUI frontend and are out
of scope.

## 8. Management UI

A standalone **web console** served by termiplus (OAuth/OIDC-protected, same IdP;
RBAC-aware) for sessions, workspaces, entitlements, network policy and the audit log — the v1.0
successor to the #12 admin API's planned small web UI. Surfaced in OWUI two ways:

- **Direct link:** an Action/toolbar button that opens the full console in a new
  tab for rich admin work (robust, unconstrained by the iframe sandbox).
- **Inline embed:** focused views (e.g. a single session's management panel)
  embedded as an iframe where that is more useful than a context switch.

**Implemented (user part, 2026-10-13).** The MCP server serves the console on
its own listener (`UI_LISTEN_ADDR`, `/ui/`), reached only through `desk-auth`,
the browser sign-in proxy of `deploy/docker-compose.desktop.yml`
(`${DESKTOP_PUBLIC_URL}/ui/`). It lists the workspaces the user is entitled to
and their sessions, starts and removes sessions (asking first) and links their
desktops. All actions go through the same
tools, RBAC and entitlements as MCP calls; the person clicking is the human in
the loop.

- **Identity.** The browser sign-in yields only the Keycloak subject, while
  sessions belong to the Open WebUI user. The MCP server therefore remembers
  the pair whenever the Open WebUI plugin calls it (verified token + plugin
  key + Open WebUI user id) in `MCP_IDENTITY_FILE` (ids only, no secrets;
  decided 2026-10-13 over querying Open WebUI with an admin key). Until a user
  has used the plugin once, the UI says so and shows nothing.
- **CSRF.** The session cookie is SameSite=Lax; every write needs the
  `X-Termiplus-UI: 1` header and a same-origin `Origin`. Strict CSP
  (`default-src 'self'`, no framing), `no-store`, `nosniff`.
- **Exposure.** The UI listener trusts the proxy's identity headers. Only
  `desk-auth` forwards browser requests to it; `mcp-auth` and the gateway,
  which share other networks with the MCP server, never do.

## 9. Desktop-environment previews

Where useful (on `request_session`, and in `list_sessions`), the plugin shows a
**live DE preview**: an `embeds` panel with an `<iframe src="<stream_url>">` to
the session's KasmVNC/Guacamole stream, updated in place via `replace`.

- **iOS is explicitly out of scope** for previews/streaming (sandboxed iframes
  can't stream/download well there) — we target desktop browsers.
- The stream endpoint must set `frame-ancestors` to the OWUI origin and OWUI's
  `IFRAME_CSP` must allow it. termiplus owns the streamer, so this is configurable.

## 10. Build order (spikes on `alpha`)

1. **Control plane + identity + gated lifecycle** (this contract): MCP tools,
   OAuth/OIDC+Keycloak, two-layer gate, ephemeral/persistent sessions, access
   handle returning the existing terminal session.
2. **Graphical streaming**: KasmVNC/Guacamole stream as the access handle + the
   OWUI DE-preview panel.
3. **Management UI** console + its OWUI link/embeds.
4. **RBAC depth / SSO breadth**: richer entitlement model, more IdPs, group sync.

## 11. Security notes & open items

- `alpha` keeps all core boundaries (sockproxy, firewall, netpolicy) intact and
  must keep `make check`/CI green; it does **not** gate on the live adversarial
  validation that a human runs for the core (`test/integration/VALIDATION.md`).
- Streaming/embedding runs in a sandboxed iframe — validate media/WebSocket
  behavior on target desktop browsers.
- The MCP↔gateway link must stay on a private network with the control key;
  oauth2-proxy's token validation (and, for sessions, the Open WebUI plugin key)
  is the trust anchor for all identity. termiplus verifies no tokens itself.

## 12. Implementation status

| Piece | State |
|-------|-------|
| MCP server (`cmd/mcp`, `internal/mcp`): JSON-RPC/HTTP, control-plane tools, RBAC + confirmation-required gate | **done** |
| Gateway control API (`/control/v1`, `X-Control-Key`) + `GatewayBackend` | **done** |
| Opt-in deployment (`deploy/docker-compose.mcp.yml`, `mcp` image target) | **done** |
| OIDC authentication via oauth2-proxy v7.15.5 (Keycloak first, any OIDC IdP) + `MCP_AUTH=proxy` | **done** — verified against a real Keycloak by `test/integration/oidc/run.sh` |
| Sessions keyed by the Open WebUI user id (`MCP_OWUI_PLUGIN_KEY`, plugin `plugin_key` valve) | **done** — same homes as Open WebUI terminals; covered by the same test |
| Native OWUI plugin: confirmation dialog, `__oauth_token__` forwarding (`integrations/openwebui/`) | **done** — tested against the real `mcp` binary in CI |
| OWUI plugin: desktop and console links (open in a new tab) | **done** |
| OWUI plugin: inline desktop preview (iframe in the chat) | **not planned for now** — the browser sign-in cookie is SameSite=Lax, so it is not sent inside a cross-site iframe; allowing that (SameSite=None + `frame-ancestors` for Open WebUI) would weaken the CSRF protection of the console |
| Desktop workspace image (`image/desktop/`, XFCE + KasmVNC 1.5.0, runs Open Terminal too) | **experimental** — renders and streams under gVisor and Sysbox (see below) |
| Desktop streams: policy `"desktop": true`, owner-only `/stream/<container>/` behind the browser sign-in (one login, the gateway fills in KasmVNC's per-container password), `stream_url` handle (`deploy/docker-compose.desktop.yml`) | **done (experimental)** — gateway tests incl. a real KasmVNC; `test/integration/oidc/desktop.sh` against a real Keycloak |
| OWUI plugin gives desktop, port and console links as Markdown links (no secrets in them) | **done** |
| Management UI, user part (`/ui/`: workspaces, own sessions, start/remove, desktop links) behind the desk-auth browser sign-in | **done (experimental)** — Go tests; `test/integration/oidc/desktop.sh` against a real Keycloak |
| Management UI, admin part: edit workspace entitlements and the network policy (`termiplus-admin`); MCP tools `get_network_policy`, `get_entitlements`, `set_entitlements` | **done (experimental)** |
| User management from chat: `manage_user` / `manage_group` (MCP and plugin) over Keycloak's admin API, `deploy/docker-compose.mcp-users.yml` | **done (experimental)** — Go tests with a fake Keycloak; `test/integration/oidc/run.sh` against a real one |
| Network policy from chat: plugin tools `get_network_policy` / `set_network_policy` (admin role, set asks for confirmation); the MCP server gets the gateway's admin key only with `deploy/docker-compose.mcp-netpolicy.yml` | **done (experimental)** |
| OWUI plugin `open_console` tool (link to the management UI, `ui_url` valve) | **done** |
| Port forwarding (`/<session>/<port>/` on its own origin behind `fwd-auth`, owner only; console "Open port", plugin `open_port`) | **done (experimental)** |
| Desktop image built and smoke-tested in CI (`desktop image` job, `.github/scripts/desktop-smoke.sh`) | **done** |
| Workspace entitlements per group (`MCP_ENTITLEMENTS_FILE`, editable by admins) | **done** |
| Audit log of state-changing calls (`MCP_AUDIT_FILE`), `get_audit_log` (MCP, plugin) and a table in the console's admin part | **done (experimental)** — Go and plugin tests |
| Workspace reset, home downloads and snapshots (`reset_workspace`, `manage_snapshots`, console *Backups*; gateway `BACKUP_DIR`) | **done (experimental)** — end-to-end tests through the real gateway and socket proxy; archive engine verified against a real Docker |
| View-only shared desktops (`share_desktop`, `/share/<token>/`, KasmVNC user `viewer`) | **done (experimental)** — end-to-end tests with a fake KasmVNC |
| Departed users: archive homes of users disabled or deleted in Keycloak (`MCP_DEPARTED_AFTER`, `manage_departed`), restore on return | **done (experimental)** — end-to-end tests through the real gateway |

### Gateway control API

Enabled only when the gateway has `CONTROL_API_KEY` (≥32 chars, distinct from
the connection and admin keys); otherwise every `/control/v1` path is 404. Every
request needs `X-Control-Key`; the session endpoints also need `X-User-Id`, which
only the MCP server sets after authenticating the caller.

| Method & path | Does |
|---|---|
| `GET /control/v1/workspaces` | policies as workspaces (`name`, `runtime`, `persistent`) |
| `GET /control/v1/sessions` | the user's sessions, running or stopped |
| `POST /control/v1/sessions` `{"workspace": …}` | create/start a session; ephemeral ones get a fresh `mcp-…` session id |
| `DELETE /control/v1/sessions/{id}` | remove one of the user's containers (home volume kept); another user's id is reported as 404 |

Persistence is a property of the workspace (policy). `request_session` must
state `persistent: true` for a persistent workspace and is refused otherwise, so
a persistent session can never be obtained without passing the RBAC +
confirmation gate.

### Desktop streaming spike (KasmVNC)

`image/desktop/` builds on the terminal image and adds XFCE plus KasmVNC (the
open-source Kasm streaming server), serving the desktop over HTTP/WebSocket on
port 6901 with KasmVNC's own basic auth (user `user`, password `VNC_PW`, at
least 32 characters, write permission only, no admin API). Checked by hand on
this host (2026-10-12):

| | gVisor (runsc) | Sysbox |
|---|---|---|
| XFCE renders (screenshot via the KasmVNC API) | yes | yes |
| HTTP without / with credentials | 401 / 200 | 401 / 200 |
| WebSocket `/websockify` (needs an `Origin` header) | 101 | 101 |
| Idle memory | ~190 MiB | ~90 MiB |

No firewall change is needed (the gateway may already reach any sandbox
port).

### Desktop streams

Decided 2026-10-13: per-container password derived like the terminal key,
browser sign-in through oauth2-proxy; changed the same day to one login (the
password stays inside the gateway) after the second basic-auth prompt proved
unusable. How it fits together:

1. **Image.** The socket proxy allows exactly one image, so the desktop image
   is used as `TERMINAL_IMAGE` (no socket-proxy change). Every container runs
   Open Terminal as before; only policies with `"desktop": true` also start
   XFCE + KasmVNC, because only they get `OPEN_TERMINAL_DESKTOP_PASSWORD`.
2. **Password.** The gateway derives it as HMAC-SHA256(`KEY_SECRET`,
   `"desktop\0" + container`), separate from the terminal key; nothing is
   stored, and rotating `KEY_SECRET` recreates the containers. KasmVNC checks
   it (basic auth, user `user`, write permission only). The start script hands
   it only to KasmVNC, not to Open Terminal or the user's shells (it remains
   visible in PID 1's environment to the user it protects). KasmVNC's per-IP
   brute-force blacklist is off: every browser comes through the gateway's
   single IP, so it would let anyone lock the owner out, and the password has
   256 random bits.
3. **One login.** Browsers reach a desktop at
   `${DESKTOP_PUBLIC_URL}/stream/<container>/` behind `desk-auth`
   (oauth2-proxy, authorization code flow with PKCE, HttpOnly SameSite=Lax
   cookie). Its upstream is the MCP server's UI listener, which maps the
   signed-in OIDC subject to the Open WebUI user (IdentityStore, as for the
   console) and calls the gateway's control API
   `/control/v1/stream/<container>/<path>` with the control key and that
   user's `X-User-Id`. The gateway passes the request on only to a running
   desktop container of exactly that user (anything else is 404), fills in
   KasmVNC's basic auth with the per-container password and drops the
   browser's `Authorization`, cookies and identity headers. The password never
   leaves the gateway, so there is no second prompt.
4. **WebSockets.** KasmVNC wants an `Origin` but does not check it, so the MCP
   server refuses WebSocket upgrades whose `Origin` is not the desktop's own
   origin (cross-site WebSocket hijacking with the Lax cookie).
5. **Handle.** The control API returns `stream_path` to the session's owner;
   the MCP server turns it into `stream_url` (`STREAM_PUBLIC_URL`). The link
   is no secret: the OWUI plugin hands it to the model, which gives it to the
   user as a Markdown link; the console shows it too. The gateway itself no
   longer serves a public `/stream/` path.
6. **Page path and stopped desktops.** KasmVNC's client opens its WebSocket
   at `/<path>` of the host (default `/websockify`), so the MCP server
   redirects the desktop page to `?path=stream/<container>/websockify`. When
   the desktop does not answer (idle-stopped, removed or still starting), the
   page is replaced by a small one (`ui/desktop.js`) that looks the desktop up
   (`/ui/api/desktops/<container>`: the user's session, or a persistent
   desktop workspace of theirs whose container is gone) and offers to start
   it through the console API, then reloads once it answers. Other users'
   desktops look like unknown ones.

`test/integration/oidc/desktop.sh` verifies the sign-in against a real
Keycloak with the overlay's exact flags: no request reaches a desktop without
the session cookie (neither basic auth nor a bearer token replaces it), the
rest of the gateway is unreachable, the gateway is called as the linked Open
WebUI user with the control key and without the browser's credentials or any
token, WebSockets pass only when signed in and from the desktop's origin, and
an unlinked user is refused. The gateway's `TestDesktopStream` and
`TestDesktopStreamThroughMCP` check the owner rule and the full chain; the
opt-in `TestDesktopStreamRealKasmVNC` (`TERMIPLUS_KASM_ADDR` /
`TERMIPLUS_KASM_PASSWORD`, run by `.github/scripts/desktop-smoke.sh`) checks
that the gateway's basic auth and the WebSocket upgrade get through to a real
KasmVNC.

The desktop image is built and smoke-tested in CI (runc only; gVisor and
Sysbox were checked by hand).

### Port forwarding

Like code-server's `/proxy/<port>/`: `FORWARD_PUBLIC_URL/<session>/<port>/<path>`
reaches `localhost:<port>` inside one of the signed-in user's sessions (a dev
server, a notebook, a preview). Decided 2026-10-13: own origin, owner only.

Browser -> `fwd-auth` (oauth2-proxy, own cookie `_termiplus_fwd`) -> MCP
forwarder (`FWD_LISTEN_ADDR`; maps the OIDC subject to the Open WebUI user via
the identity store) -> gateway `/control/v1/forward/<container>/<port>/`
(control key + `X-User-Id`) -> the container.

- The gateway forwards only to a **running** container whose recorded owner is
  **exactly** that user (another user's or an unknown session is 404), never to
  Open Terminal's API port (8000) or the desktop port (6901), and never starts
  a container. It counts the connection for the idle reaper.
- It strips the control key, `X-User-Id`, the proxies' identity headers and
  termiplus' own sign-in cookies; the app's own cookies pass. The app is asked
  for `localhost:<port>` (dev servers refuse unknown hosts), and redirects to
  itself or to host-relative paths are kept under `/<session>/<port>/`.
  WebSockets pass (hot reload).
- Forwards live on their own origin, so a forwarded app can never use the
  console's or the desktops' cookies or pages. The user's forwarded apps share
  that origin with each other; another user's forward is never served to them.
- The base needs its trailing slash; apps with absolute asset paths (`/assets/…`)
  need a base-path setting (Vite `base`, webpack `publicPath`), as with
  code-server's path proxy.

### Workspace entitlements

Which group may see and launch which workspace is configured in the MCP server,
not in the gateway (the gateway trusts the MCP server's checks).
They live in `MCP_ENTITLEMENTS_FILE` (`deploy/mcp-data/entitlements.json` in
the overlay) and admins edit them in the management UI or with the
`set_entitlements` tool (admin role + confirmation), which validates, writes
the file atomically and applies it at once. Example
(`deploy/entitlements.example.json`):

```json
{
  "default": "deny",
  "workspaces": {
    "default": ["termiplus-users"],
    "sysbox":  ["termiplus-dev", "termiplus-admin"]
  }
}
```

A listed workspace needs at least one of its groups (from the token's groups
claim, like the roles). An unlisted one follows `default` (`allow` or `deny`).
`termiplus-admin` is not an implicit entitlement. `list_workspaces` only shows
permitted workspaces and `request_session` refuses others before any
confirmation is asked for. The file is parsed strictly: an unknown field, a
missing `default`, an unknown workspace name, or an empty group list stops the
server (or is refused by `set_entitlements`) instead of opening a workspace by
accident. The example defaults to deny. With `MCP_AUTH=proxy`, a missing file is
a startup error; allow-all behavior without a file is reserved for explicit
development-mode configurations. Entitlement changes also hide existing sessions
and deny their reset, desktop-stream and port-forward access.

### User management

`manage_user` and `manage_group` let `termiplus-admin` users decide who is in
which termiplus group (`termiplus-users`, `-persist`, `-admin`, and any other
top-level `termiplus-*` group that workspace entitlements use), from chat
(plugin tools of the same names) without the Keycloak console. Opt-in with
`deploy/docker-compose.mcp-users.yml`.

- **How.** The MCP server calls Keycloak's admin API as the service account of
  the confidential client `termiplus-mcp-admin` (client credentials grant;
  roles `view-users`, `query-groups`, `manage-users` of `realm-management`).
  `setup-alpha.sh` adds the client to new realms; existing ones need
  `deploy/keycloak/add-mcp-admin-client.sh` once. The overlay puts the MCP
  server and Keycloak on their own internal network `ot-mcp-kc`.
- **Limits enforced by the server.** Only membership of top-level groups named
  `MCP_KC_GROUP_PREFIX*` (default `termiplus-`) changes; other groups are
  refused even by name. Users are never created, deleted or edited and
  credentials are never read; the last enabled member of `termiplus-admin`
  cannot be removed. Every change needs the admin role and a human
  confirmation and is logged with the caller.
- **Effect.** Group claims live in the users' tokens, so a change applies when
  the affected user's token is next refreshed (at the latest on their next
  sign-in).
- **Security impact.** `manage-users` is broader than these tools: a leaked
  `mcp_kc_admin_client_secret` lets its holder edit users of the termiplus
  realm (not other realms). It exists only in `deploy/secrets/` and the MCP
  container, and Keycloak is the only other member of `ot-mcp-kc`. Keycloak
  26's fine-grained admin permissions can narrow the service account to
  group membership; not set up by default.
- Tested against a real Keycloak in `test/integration/oidc/run.sh`, including
  that the new group reaches the user's next token and that an issuer
  hostname different from the internal admin URL works.

### Audit log

With `MCP_AUDIT_FILE` set (`/data/audit.log` in `deploy/docker-compose.mcp.yml`)
the MCP server records every tool call that can change something:
`request_session`, `destroy_session`, `set_entitlements`, `set_network_policy`
and the changes of `manage_user`. That covers chat and the management console,
which call the same tools. Each entry has the time (UTC), the user id the call
acted as, the OIDC subject, the tool, its arguments (control arguments such as
`_confirmed` dropped, at most 4 KiB) and the outcome: `ok`, `error` (with the
message) or `denied` (refused by the role or entitlement check).

- Not recorded: read-only calls, and calls still waiting for the human
  confirmation (the confirmed repeat is recorded).
- The file is JSON lines, mode 0600, synced after each entry. Past
  `MCP_AUDIT_MAX_MB` (default 5) it moves to `audit.log.1`, replacing the
  previous one, so at most twice that is kept.
- `termiplus-admin` users read it with `get_audit_log` (filters `actor`,
  `tool`, `since`, `limit` up to 500) from chat, or in the console's
  *Audit log* table (newest 50). Nobody can change it through termiplus.
- Direct calls to the gateway's admin API (`X-Admin-Key`) bypass the MCP server
  and appear only in the gateway log. An entry is written after the call, so
  if writing fails (full disk) the action has still happened; the failure is
  logged.

### Backups

Users can reset a workspace and keep snapshots of their homes, from chat
(`reset_workspace`, `manage_snapshots`) or in the console (*Reset* on a
session, *Backups* section):

- **Reset** removes the session's container; a persistent workspace starts
  again from a fresh container with the same home (programs installed outside
  `/home/user` are gone), a temporary sandbox is just removed.
- **Download home**: the current home as a tar, straight from the console
  (`/ui/api/workspaces/<workspace>/home/archive`, behind the browser sign-in).
- **Snapshots** are gzip-compressed tars of a home in the gateway's
  `BACKUP_DIR` (`deploy/backups` in `docker-compose.mcp.yml`), at most
  `SNAPSHOT_KEEP` (default 5) per home; the oldest go first. Taking one does
  not stop the workspace (files written meanwhile may be caught
  half-written). Restoring stops the workspace's containers and writes the
  snapshot's files back, overwriting newer versions; files created since then
  stay. Each snapshot can be downloaded.
- Homes are shared between workspaces with the same `home`; their snapshots
  are the same.
- File access goes through the gateway's archive helpers and the socket
  proxy's archive rules (README, *What the socket proxy allows*). Snapshots
  hold users' files: size and protect `deploy/backups` accordingly.

### Shared desktops

`share_desktop` (or *Share (view only)* on a desktop session in the console)
creates a link `${DESKTOP_PUBLIC_URL}/share/<token>/` that lets other people
watch the desktop until it expires (5 minutes to 24 hours, default 60) or is
revoked.

- **View only.** The gateway signs in to KasmVNC as the user `viewer`, which
  has read permission only (`kasmvncpasswd -r`, its own password derived from
  `KEY_SECRET`), so keyboard and mouse input from viewers is ignored. Viewers
  do receive what KasmVNC sends to every client, including clipboard
  contents of the desktop.
- **Who.** Viewers must sign in (desk-auth). Without `for_user`, anyone who can
  sign in and has the link may watch; with `for_user` (needs user management),
  only that user. They need no Open WebUI link and never get the owner's
  session or console.
- **The link** carries 256 random bits; the MCP server stores only its hash
  (`MCP_SHARES_FILE`). At most 10 active links per user. Opening a link is
  logged with the viewer's subject; creating and revoking are in the audit log.
- **Rollout.** The viewer user exists in desktop containers created after the
  updated desktop image and gateway. Older containers are recreated when they
  are stopped and the image changed, or with a reset.

### Departed users

With user management configured and `MCP_DEPARTED_AFTER` set (e.g. `720h`),
the MCP server checks Keycloak every `MCP_DEPARTED_INTERVAL` (default 6h):

1. A user who is **disabled**, or who was known here (identity store) and is
   **deleted** in Keycloak, is departed from that check on
   (`MCP_DEPARTED_FILE`). A failed or empty user listing changes nothing.
2. After `MCP_DEPARTED_AFTER`, the gateway stores each of their homes as an
   archive in `BACKUP_DIR/archived/` and deletes their containers. With
   `ALLOW_HOME_REMOVE=1` on the socket proxy it also deletes the homes;
   otherwise they stay (and keep their disk space).
3. A disabled user who is **enabled again** gets the newest archive of each
   home restored automatically.

Admins see the list and can archive a disabled user right away or restore
someone with `manage_departed` (confirmation required). All of it is in the
audit log with the actor `termiplus`. Archives are never deleted
automatically.

### Authentication (oauth2-proxy)

`deploy/docker-compose.mcp.yml` runs oauth2-proxy (`mcp-auth`, pinned by
digest) with `--skip-jwt-bearer-tokens --api-route=^/`: a request with a valid
bearer JWT for the configured client (audience) passes, everything else gets
401. Keycloak setup: a client `termiplus-mcp` (standard flow off), an audience
mapper on the client Open WebUI signs in with (so access tokens carry
`aud=termiplus-mcp`), and a Group Membership mapper (claim `groups`, full path
off). Membership in the groups `termiplus-persist` / `termiplus-admin` grants
the MCP roles. `MCP_AUTH=dev` (static `MCP_DEV_TOKEN`) remains for local
development only and cannot be combined with `proxy`.

`test/integration/oidc/run.sh` starts a throwaway Keycloak, the proxy with the
overlay's exact image and flags, and the real `mcp` image, and checks: no
token, forged identity headers without a token, wrong audience, tampered
signature, `alg=none` and expired tokens are rejected; forged
`X-Forwarded-User`/`X-Forwarded-Groups` next to a valid token neither
impersonate nor grant roles; group membership grants the persist role; the MCP
server sits only on the internal auth network.

Known limits: the `terminal_session` handle of an ephemeral session created via
the control API is kept in gateway memory only (lost on a gateway restart, which
is acceptable for throwaway sandboxes). Open WebUI chat ids are never returned.
The plugin key is a bootstrap trust anchor: whoever holds it and has a valid
token can attempt to claim an as-yet-unlinked Open WebUI user id on that
subject's first call. After a mapping exists, the server enforces an immutable
one-to-one binding and rejects reassignments before running tools. Keep the key
only in Open WebUI's admin-controlled valve and the Docker secret; do not expose
it to ordinary users or model-visible tool arguments. A caller outside Open
WebUI (another MCP client with a Keycloak token) cannot use the MCP server while
the key is set. If you need stronger first-enrollment assurance than this shared
bootstrap secret, pre-provision the subject-to-user mapping through a trusted
admin workflow before enabling user access.

Upgrading from a version where the newest pair won: an existing
`MCP_IDENTITY_FILE` that maps two subjects to the same Open WebUI user id now
makes the MCP server refuse to start. Decide which subject owns that user,
remove the other entry, then start it.

Verified end-to-end against a real Docker daemon with gVisor (`runsc`) using an
isolated throwaway stack: ephemeral and persistent creation, the mismatch and
confirmation refusals, listing, confirmed destroy keeping the home volume.
