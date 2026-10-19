---
type: process
status: verified
verified: 2026-10-19 @ a35e601
consumes: [../objects/identity/principal.md, ../objects/identity/identity-store.md, ../objects/control-plane/share.md]
produces: []
---

# desktop-stream

A signed-in browser gets the KasmVNC desktop of its own desktop container, or a view-only one through a share link. The browser never sees a VNC password.

## Input → Movement → Output

The browser opens `<desktop origin>/stream/<session>/...` (or `/share/<token>/...`). It is signed in by `desk-stream-auth` (oauth2-proxy). The MCP desktop handler maps the OIDC subject to the Open WebUI user, checks the session is entitled, and calls the gateway's `/control/v1/stream/<session>/...` with the control key and the owner's `X-User-Id`. The gateway finds that user's running desktop, fills in KasmVNC basic auth (owner or `viewer`) and proxies.

## Why this shape

KasmVNC knows only basic auth, and the desktop serves arbitrary user-controlled content. So the desktops live on their own origin (never the console's). The gateway injects the per-container password, so it never reaches a browser. Cookies, `Set-Cookie`, service workers and `Clear-Site-Data` are dropped, so one desktop cannot plant state for the origin. Cross-origin WebSocket upgrades are refused. Unknown, stopped and foreign sessions all return the same 404. The gateway never *starts* a container for a stream request.

## Steps

1. `DesktopHandler`: refuse `Service-Worker`, route `/stream/` and `/share/`: `internal/mcp/stream.go:64-80`.
2. Authenticate the proxy headers, look up the Open WebUI id (unlinked → 403), check entitlement: `stream.go:86-108`.
3. Share path: look up the token hash, check expiry and the optional named viewer, set `view`: `stream.go:114`, `internal/mcp/shares.go:58`.
4. `proxy`: same-origin check on upgrades, strip the identity headers, set `X-User-Id` = owner, `view_only` if view, drop `untrustedResponseHeaders`: `stream.go:84`, `:142-190`.
5. Gateway `serveStream`: `streamTarget` (running desktop of that user, else 404), then basic auth as `user` or `viewer`: `internal/gateway/stream.go:41-58`, `:97`.

## If you change this

- **Hits:** `deploy/docker-compose.desktop.yml` (origins, `desk-stream-auth`), `image/desktop/start-desktop.sh` (the two passwords), `internal/gateway/stream_test.go`, `test/integration/oidc/desktop.sh`. Browser isolation is a security boundary.
- **Does not hit:** non-desktop policies

## Surfaces

| Surface | Role |
|---|---|
| browser | views |
| desk-stream-auth | signs in |
| MCP desktop listener (`STREAM_LISTEN_ADDR`) | maps and checks |
| gateway control API | proxies with VNC credentials |

## See

- Objects: [Share](../objects/control-plane/share.md), [Terminal container](../objects/workspaces/terminal-container.md), [Service keys](../objects/identity/service-keys.md)
- Source: `internal/mcp/stream.go`, `internal/gateway/stream.go`
