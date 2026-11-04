---
type: process
status: verified
verified: 2026-11-04 (native integration)
consumes: [../objects/identity/principal.md, ../objects/identity/identity-store.md, ../objects/control-plane/share.md]
produces: []
---

# Native Desktop and screen shares

The owner uses Open WebUI's native Desktop tab and tools on the selected
terminal connection. HTTP `/desktop/*` uses the gateway bearer and user/chat
headers; Desktop stream/audio use first-frame authentication. The gateway
substitutes the derived container key and relays frames. Browser capability
WebSockets instead authenticate with the gateway bearer in the upgrade headers,
on the three narrowly matched native view endpoints. Other sockets retain
first-frame auth. Source: `internal/gateway/ws.go:serveWebSocket`,
`internal/gateway/gateway.go:ServeHTTP`, `internal/gateway/native_test.go`.

The image uses Open Terminal 0.16, XFCE and TigerVNC's Unix socket. No second
Kasm server, VNC password, owner stream listener, player or port 6901 exists.
Source: `image/Dockerfile`, `image/desktop/Dockerfile`.

Signed-in recipients may open `/share/<token>/` on the separate share origin.
`internal/mcp/stream.go:DesktopHandler` authenticates the OIDC subject, checks
expiry and optional named recipient, and serves its own HTML plus normalized
PNG only. The image comes from `/control/v1/screen/<session>` as the owner;
`internal/gateway/preview.go:serveScreen` checks ownership and running state.
Images refresh every five seconds. Input, clipboard, audio and all WebSocket
upgrades are refused. Revoke/expiry cancels active reads; reads have time and
byte/dimension bounds. This is periodic viewing, not full-motion streaming.

Console previews use the same native `/desktop/screenshot` with `X-User-Id`,
320-pixel normalization and the existing 30-second cache. They do not start a
stopped container. `internal/preview/image.go` rejects invalid/oversized images
and re-encodes pixels. See [Share](../objects/control-plane/share.md).

Changes affect the desktop overlay, image smoke tests, MCP/gateway share tests
and OIDC fixtures. They must preserve tenant routing and read-only recipient
access. Native image tests and real Open WebUI testing are separate gates.
