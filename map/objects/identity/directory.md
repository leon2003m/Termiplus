---
type: object
cluster: identity
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/mcp/directory.go
---

# Directory

The user directory behind the OIDC provider, as far as the admin tools need it. In code: the `mcp.Directory` interface, implemented by `KeycloakDirectory`.

## Why this shape

Admins manage access from chat (`manage_user`, `manage_group`) without a Keycloak console. The directory only reads users and groups and changes group membership. It **never creates users or touches credentials**, so a compromised MCP server cannot mint accounts.

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
