---
type: process
status: verified
verified: 2026-11-04 (native integration)
consumes: [../objects/identity/principal.md, ../objects/identity/identity-store.md]
produces: []
---

# Native app preview (legacy port forward removed)

Open WebUI's native Browser/app preview uses Open Terminal `/ports` and
`/proxy/<port>/...` through the authenticated terminal connection. The gateway
routes to the caller's policy/chat container and replaces the bearer; native
Open Terminal discovers allowed local process ports inside that container.

The custom MCP forward listener, gateway control forward routes, `fwd-auth`,
console port links and `open_port` tool are removed. They return no equivalent
legacy URL. There is no host-side arbitrary-port proxy. Native browser
capability WebSockets are handled by `internal/gateway/ws.go` with header bearer
authentication; generic app-port WebSocket parity is not claimed.

Source: `internal/gateway/gateway.go:proxyHTTP`,
`internal/gateway/ws.go:nativeBrowserSocket`, `internal/gateway/native_test.go`,
`internal/gateway/forward_test.go`, `deploy/docker-compose.desktop.yml`.
See [native migration](../../docs/native-integration.md).
