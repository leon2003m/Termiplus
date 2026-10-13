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
     server only reads its headers. It is reachable solely through the proxy
     (internal network `ot-mcp-auth`).
  4. **Sessions belong to the Open WebUI user**, so they share homes with that
     user's Open WebUI terminals. With `MCP_OWUI_PLUGIN_KEY` set (the overlay
     does), the plugin also sends `X-Termiplus-Owui-Key` (a shared secret, the
     plugin's `plugin_key` valve) and `X-Termiplus-Owui-User` (`__user__["id"]`).
     The MCP server then uses that id as `user_id` and still takes the roles
     from the verified token; without both a verified token and the key the call
     is unauthenticated. Open WebUI fetched the token for that same user, and
     the trust is the same as towards the gateway, where Open WebUI names the
     user in `X-User-Id` with its API key. Keycloak stays the login and the
     source of the groups.
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
| `set_network_policy(custom_dns[], egress[])` *(admin)* | Custom DNS + egress whitelist (wraps the core admin API) | destructive | **admin entitlement + confirm** |
| `manage_user` / `manage_group` *(admin)* | User/group/entitlement administration | destructive | **admin entitlement + confirm** |

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
RBAC-aware) for sessions, workspaces, entitlements and network policy — the v1.0
successor to the #12 admin API's planned small web UI. Surfaced in OWUI two ways:

- **Direct link:** an Action/toolbar button that opens the full console in a new
  tab for rich admin work (robust, unconstrained by the iframe sandbox).
- **Inline embed:** focused views (e.g. a single session's management panel)
  embedded as an iframe where that is more useful than a context switch.

**Implemented (user part, 2026-10-13).** The MCP server serves the console on
its own listener (`UI_LISTEN_ADDR`, `/ui/`), reached only through `desk-auth`,
the browser sign-in proxy of `deploy/docker-compose.desktop.yml`
(`${DESKTOP_PUBLIC_URL}/ui/`). It lists the workspaces the user is entitled to
and their sessions, starts and removes sessions (asking first) and shows the
desktop link and password for their desktops. All actions go through the same
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
| Desktop streams: policy `"desktop": true`, gateway `/stream/<container>/`, per-container password, `stream_url` handle, browser sign-in (`deploy/docker-compose.desktop.yml`) | **done (experimental)** — gateway tests incl. a real KasmVNC; `test/integration/oidc/desktop.sh` against a real Keycloak |
| OWUI plugin shows the desktop link and password to the user (inline embed), never to the model | **done** |
| Management UI, user part (`/ui/`: workspaces, own sessions, start/remove, desktop links) behind the desk-auth browser sign-in | **done (experimental)** — Go tests; `test/integration/oidc/desktop.sh` against a real Keycloak |
| Management UI, admin part: edit workspace entitlements and the network policy (`termiplus-admin`); MCP tools `get_network_policy`, `get_entitlements`, `set_entitlements` | **done (experimental)** |
| OWUI plugin `open_console` tool (link to the management UI, `ui_url` valve) | **done** |
| Desktop image built and smoke-tested in CI (`desktop image` job, `.github/scripts/desktop-smoke.sh`) | **done** |
| Workspace entitlements per group (`MCP_ENTITLEMENTS_FILE`, editable by admins) | **done** |

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
browser sign-in through oauth2-proxy. How it fits together:

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
3. **Routing.** The gateway serves `/stream/<container>/` for running desktop
   containers only (anything else is 404) and never starts a container for a
   stream request. It forwards the browser's basic auth, drops cookies and
   `X-User-Id`, counts open streams so the idle reaper keeps the desktop, and
   refuses **cross-origin WebSockets** (KasmVNC does not check `Origin`, and
   browsers send cached basic-auth credentials cross-site).
4. **Handle.** The control API returns `stream_path` + `stream_password` to the
   session's owner only; the MCP server turns it into `stream_url`
   (`STREAM_PUBLIC_URL`). The OWUI plugin removes the password from the tool
   result and shows link and password to the user in an inline embed, which
   Open WebUI does not send to the model.
5. **Browser sign-in.** `deploy/docker-compose.desktop.yml` adds `desk-auth`
   (oauth2-proxy, authorization code flow with PKCE, HttpOnly SameSite=Lax
   cookie) whose only upstream is the gateway's `/stream/`. It passes no token
   of its own and keeps the browser's basic auth and Host.

`test/integration/oidc/desktop.sh` verifies the sign-in against a real
Keycloak with the overlay's exact flags: no request reaches the stream without
the session cookie (neither basic auth nor a bearer token replaces it), the
rest of the gateway is unreachable, basic auth and Host arrive unchanged, no
token is forwarded, WebSockets pass only when signed in. The gateway's
`TestDesktopStreamRealKasmVNC` (opt-in, `TERMIPLUS_KASM_ADDR` /
`TERMIPLUS_KASM_PASSWORD`) checks the route against a real KasmVNC container:
401 without and 200 with the password, 101 for a same-origin WebSocket, 403
for cross-origin or missing `Origin`.

Known limits: authorization to a desktop is knowing its password; the browser
sign-in only proves a Keycloak user, not that it is the owner (the owner's
identity is the Open WebUI user id). The desktop image is built and smoke-tested in CI (runc only; gVisor and Sysbox were checked by hand).

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
missing `default` or an empty group list stops the server (or is refused by
`set_entitlements`) instead of opening a workspace by accident. While the file
does not exist, every workspace is open to every signed-in user.

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
Whoever holds the plugin key can name any Open WebUI user (with any valid
token), exactly like the holder of the gateway API key; keep it in Open WebUI's
valve and the Docker secret only. A caller outside Open WebUI (another MCP
client with a Keycloak token) cannot use the MCP server while the key is set.

Verified end-to-end against a real Docker daemon with gVisor (`runsc`) using an
isolated throwaway stack: ephemeral and persistent creation, the mismatch and
confirmation refusals, listing, confirmed destroy keeping the home volume.
