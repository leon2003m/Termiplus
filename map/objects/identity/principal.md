---
type: object
cluster: identity
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/mcp/mcp.go
---

# Principal

The caller of an MCP tool, the console or a desktop link: a user id plus roles (Keycloak groups). In code: `mcp.Principal`.

## Why this shape

termiplus never verifies tokens itself. oauth2-proxy does that and forwards the subject and groups as headers, and the MCP server trusts them only because it can be reached through that proxy alone. With the plugin key set, `UserID` is the **Open WebUI user id**, not the OIDC subject, so MCP sessions share homes with that user's Open WebUI terminals.

## Shape

- `UserID`: the key every container, volume and archive name is hashed from. It is passed to the gateway as `X-User-Id`.
- `Roles`: group names. `termiplus-persist` (RolePersist) is needed to *request* a persistent session. `termiplus-admin` (RoleAdmin) is needed for the admin tools.
- `Subject`: the OIDC subject when `UserID` came from the plugin. It is recorded in the [IdentityStore](identity-store.md).
- Headers: `X-Forwarded-User` / `X-Forwarded-Groups` from the proxy, and `X-Termiplus-Owui-Key` / `X-Termiplus-Owui-User` from the plugin.

Citations: `internal/mcp/mcp.go:22` (Principal), `internal/mcp/mcp.go:104-105` (RolePersist, RoleAdmin), `internal/mcp/auth.go:13-23` (headers), `internal/mcp/auth.go:40` (ProxyHeaderAuthenticator), `internal/mcp/server.go:431-439` (persist check), `internal/mcp/server.go:575-578` (adminOnly)

## Connected to

- **owns:** nothing stored. It exists per request.
- **owned-by:** the authenticator chosen by `MCP_AUTH` (`proxy`, or `dev` = leftover StaticAuthenticator) in `cmd/mcp/main.go:49`
- **joins:** [Entitlements](entitlements.md) (which workspaces), [IdentityStore](identity-store.md) (subject ↔ Open WebUI id), [Terminal container](../workspaces/terminal-container.md) (names hash the user id)
- **looks-like-but-is-not:** the gateway's `X-User-Id`. The gateway trusts whoever holds the gateway or control key to name the user, and has no notion of roles.

## If you change this

- **Hits:** every name in `internal/naming/naming.go` (the user hash), [Entitlements](entitlements.md), the plugin headers in `integrations/openwebui/termiplus_workspaces.py:350-355`, the Keycloak group names in `deploy/keycloak/realm-termiplus.json.tmpl`
- **Does not hit:** sockproxy, firewall, netpolicy

## Surfaces

| Surface | Role |
|---|---|
| oauth2-proxy | writes the identity headers |
| Open WebUI plugin | names the Open WebUI user, with the plugin key |
| MCP tools, console, stream, forward listeners | read |

## See

- Source: `internal/mcp/auth.go`, `internal/mcp/mcp.go`
- Rule: [../../../AGENTS.md](../../../AGENTS.md). Auth code is never hand-rolled; use oauth2-proxy.
