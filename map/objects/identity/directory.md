---
type: object
cluster: identity
universe: live
status: verified
verified: 2026-11-04 (native integration)
entity: internal/mcp/directory.go
---

# Directory

The user directory behind the OIDC provider, as far as the admin tools need it. In code: the `mcp.Directory` interface, implemented by `KeycloakDirectory`.

## Why this shape

User/group membership is administered by humans in the Keycloak console. The directory only reads users and groups and changes group membership. It **never creates users or touches credentials**, so a compromised MCP server cannot mint accounts.

## Shape

- Methods: Groups, Members, FindUser (exact username), UserGroups, AddToGroup, RemoveFromGroup, and Users (for departed-user cleanup).
- Keycloak admin REST via a confidential client's service account. It needs the realm-management roles view-users, query-groups and manage-users.
- Only groups with the prefix `MCP_KC_GROUP_PREFIX` (default `termiplus-`) are managed.

Citations: `internal/mcp/directory.go:14-50`, `cmd/mcp/main.go:91-97`, `deploy/keycloak/add-mcp-admin-client.sh`

## Connected to

- **owns:** nothing. Keycloak owns the data.
- **owned-by:** the MCP server, when `MCP_KC_ADMIN_URL` is set
- **joins:** [Principal](principal.md) roles (= groups), [Departed](../control-plane/departed.md), [MCP tools](../control-plane/mcp-tools.md)
- **looks-like-but-is-not:** the [IdentityStore](identity-store.md)

## If you change this

- **Hits:** `manage_user`, `manage_group`, departed cleanup, the Keycloak client setup script
- **Does not hit:** sign-in itself (oauth2-proxy and Keycloak)

## Surfaces

| Surface | Role |
|---|---|
| admin tools | read and change membership |
| Keycloak | owns users and groups |

## See

- Source: `internal/mcp/directory.go`, `internal/mcp/users.go`

## Native integration update (2026-11-04)

User/group tools are excluded from model discovery and direct calls. Human membership administration uses the identity-provider console. The directory remains for internal role checks, exact-name recipient lookup and departure watching. Source: internal/mcp/server.go:modelTool and cmd/mcp/main.go.
