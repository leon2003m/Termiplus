---
type: process
status: verified
verified: 2026-10-19 @ a35e601
consumes: [../objects/identity/principal.md, ../objects/identity/identity-store.md]
produces: []
---

# port-forward

A signed-in browser reaches a web app that the user runs on some port inside their own running session, like code-server's `/proxy/<port>/`.

## Input → Movement → Output

The browser opens `<forward origin>/<session>/<port>/<path>`, signed in by `fwd-auth`. The MCP forwarder maps the subject to the Open WebUI id, checks the session is entitled, and calls `/control/v1/forward/<session>/<port>/<path>` with the control key and that user's id. The gateway forwards to that port of the container if it is a running container of exactly that user. It never forwards to the terminal API port or the desktop port.

## Why this shape

User apps are untrusted content, so they get their own origin, shared only between forwarded apps, never with the console or desktops. termiplus' own `_termiplus*` cookies are removed before the app sees the request. The API and desktop ports are refused because they have their own routes and credentials, which a forward would bypass. Redirects are rewritten to stay under the `/<session>/<port>` prefix.

Open WebUI's Ports panel links to Open Terminal's own proxy (`/proxy/<port>/`), which Open WebUI serves on its own origin, where an app would run with the user's Open WebUI sign-in. The gateway therefore never lets `/proxy/…` reach a terminal. A GET for a running session of exactly that user is redirected to this forward. Without `FORWARD_PUBLIC_URL` the link is refused. `/ports` (the list) still passes.

## Steps

1. Forwarder: authenticate, look up the id, check entitlement, strip the identity headers, set `X-User-Id`: `internal/mcp/forward.go:42-110`.
2. Gateway `serveForward`: parse the port (canonical, 1–65535), refuse the API and desktop ports: `internal/gateway/forward.go:29-41`.
3. `forwardTarget`: managed name, running instance of that user, else 404. Never starts one: `forward.go:98-110`.
4. Drop auth cookies, set `X-Forwarded-Prefix`, rewrite `Location`: `forward.go:113`, `:133`.
5. Open WebUI Ports panel: the terminal path `/proxy/<port>/<path>` is intercepted after the identity checks (`internal/gateway/gateway.go:336-343`). `serveOTProxy` applies the same port rules and `forwardTarget`, then answers 307 to `FORWARD_PUBLIC_URL/<container>/<port>/<path>`. Anything else is 404: `internal/gateway/otproxy.go:27-55`.

## If you change this

- **Hits:** `deploy/docker-compose.desktop.yml` (`fwd-auth`, `FORWARD_PUBLIC_URL`), the console links, `internal/gateway/forward_test.go`, `internal/gateway/otproxy_test.go`, the gateway's `FORWARD_PUBLIC_URL`. Origin separation is a security boundary.
- **Does not hit:** the terminal API path, desktops

## Surfaces

| Surface | Role |
|---|---|
| browser | uses the app |
| Open WebUI Ports panel | links via `/proxy/<port>/`, redirected here |
| fwd-auth | signs in |
| MCP forward listener (`FWD_LISTEN_ADDR`) | maps and checks |
| gateway | forwards |

## See

- Objects: [Terminal container](../objects/workspaces/terminal-container.md), [Principal](../objects/identity/principal.md)
- Source: `internal/mcp/forward.go`, `internal/gateway/forward.go`, `internal/gateway/otproxy.go`
