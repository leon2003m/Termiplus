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
       │            │  - renders DE preview embeds + management-UI link
       ▼            ▼
  oauth2-proxy  ── validates the JWT (JWKS, iss, aud, exp); forwards sub + groups
       │  (internal network ot-mcp-auth: the only way to reach the MCP server)
       ▼
  termiplus MCP server  ── reads the proxy's identity headers -> (user_id, roles)
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
  4. The MCP server is the **only** component that sets the gateway's
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
- The MCP↔gateway link must stay on a private network with the API key; the MCP
  server's JWT validation is the trust anchor for all identity.

## 12. Implementation status

| Piece | State |
|-------|-------|
| MCP server (`cmd/mcp`, `internal/mcp`): JSON-RPC/HTTP, control-plane tools, RBAC + confirmation-required gate | **done** |
| Gateway control API (`/control/v1`, `X-Control-Key`) + `GatewayBackend` | **done** |
| Opt-in deployment (`deploy/docker-compose.mcp.yml`, `mcp` image target) | **done** |
| OIDC authentication via oauth2-proxy v7.15.5 (Keycloak first, any OIDC IdP) + `MCP_AUTH=proxy` | **done** — verified against a real Keycloak by `test/integration/oidc/run.sh` |
| Native OWUI plugin: confirmation dialog, `__oauth_token__` forwarding (`integrations/openwebui/`) | **done** — tested against the real `mcp` binary in CI |
| OWUI plugin: DE previews and management-UI link | todo (needs streaming / management UI) |
| Desktop workspace image (`image/desktop/`, XFCE + KasmVNC 1.5.0) | **experimental** — renders and streams under gVisor and Sysbox (see below) |
| Stream routing through the gateway + `stream_url` | todo — **open decision**: how the gateway authenticates and forwards the stream |
| Management UI | todo |
| Workspace entitlements per group (`deploy/entitlements.json`, `MCP_ENTITLEMENTS_FILE`) | **done** |

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
port). Not done yet, and an **open decision** because it is
authentication-relevant: how the gateway routes `/stream/...` to port 6901 and
supplies the per-container stream password (for example derived like the
terminal key from `KEY_SECRET`), and how browser access is authenticated
(oauth2-proxy cookie flow). `VNC_PW` is visible inside the container to the
user it protects, so it must be unique per container.

### Workspace entitlements

Which group may see and launch which workspace is configured in the MCP server,
not in the gateway (the gateway trusts the MCP server's checks).
`deploy/entitlements.json` is mounted as `MCP_ENTITLEMENTS_FILE`:

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
missing `default` or an empty group list stops the server instead of opening a
workspace by accident. The shipped file is `{"default": "allow"}`; without
`MCP_ENTITLEMENTS_FILE` every workspace is open to every signed-in user. Changes
take effect on restart.

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
The MCP user id is the OIDC `sub`, while Open WebUI's own terminal connection
keys homes by Open WebUI's user id, so a persistent home created through MCP is
not yet the same volume as the user's Open WebUI terminal home (open decision).

Verified end-to-end against a real Docker daemon with gVisor (`runsc`) using an
isolated throwaway stack: ephemeral and persistent creation, the mismatch and
confirmation refusals, listing, confirmed destroy keeping the home volume.
