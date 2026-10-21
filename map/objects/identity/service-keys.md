---
type: object
cluster: identity
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: cmd/gateway/main.go
---

# Service keys

Six separate secrets, each guarding exactly one hop. They are never interchangeable, and holding one never implies another.

## Why this shape

Each hop has its own blast radius. Open WebUI may start terminals but not read homes. The MCP server may manage sessions but not reach Docker. Only the sockproxy holds the socket. One shared key would collapse every boundary into the weakest client.

## Shape

| Key | Env / secret | Held by | Proves | Checked at |
|---|---|---|---|---|
| gateway API key | `GATEWAY_API_KEY` / `gateway_api_key` | Open WebUI | may use `/p/<policy>/` | `internal/gateway/gateway.go:373` (checkBearer), `internal/gateway/ws.go:78` (first WS message) |
| control key | `CONTROL_API_KEY` | MCP server | may use `/control/v1/` and name any user | `internal/gateway/control.go:29`, `:73` |
| admin key | `ADMIN_API_KEY` | MCP server (admin tools) | may set network policy, manage homes | `internal/gateway/admin.go:96` |
| proxy token | `PROXY_TOKEN` / `proxy_token` | gateway | may talk to the sockproxy | `internal/sockproxy/proxy.go:98` (`X-Proxy-Token`) |
| key secret | `KEY_SECRET` / `key_secret` | gateway | derives each container's API key (HMAC over the name) and desktop password | `internal/gateway/lifecycle.go:124`, `:140` |
| plugin key | `MCP_OWUI_PLUGIN_KEY` | Open WebUI tool (valve `plugin_key`) | may name the Open WebUI user to the MCP server | `internal/mcp/auth.go:21-23`, `:45` |

Rotating the key secret changes every container's config hash (`lifecycle.go:155-158`), so running terminals are recreated on their next request.

## Connected to

- **owns:** nothing. Values live in `deploy/secrets/` (gitignored) or `.env`.
- **owned-by:** the deployment
- **joins:** [Principal](principal.md), [Terminal container](../workspaces/terminal-container.md) (the per-container key), [sockproxy rules](../sandbox/sockproxy-rules.md)
- **looks-like-but-is-not:** OIDC tokens. Those are verified by oauth2-proxy, never by termiplus.

## If you change this

- **Hits:** compose secrets (`deploy/docker-compose.yml`), `setup-alpha.sh`, Open WebUI connection settings and tool valves (outside this repo; see [effects](../../effects/CONTEXT.md))
- **Does not hit:** firewall, netpolicy

## Surfaces

| Surface | Role |
|---|---|
| operator | creates and rotates them |
| each service | reads its own key only |

## See

- Source: `cmd/gateway/main.go:46-74`, `cmd/mcp/main.go:33-68`
- Never read or commit `deploy/secrets/`.
