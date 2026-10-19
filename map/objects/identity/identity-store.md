---
type: object
cluster: identity
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/mcp/identities.go
---

# IdentityStore

A file that maps each OIDC subject to one Open WebUI user id, and back. In code: `mcp.IdentityStore` (`MCP_IDENTITY_FILE`).

## Why this shape

A home belongs to an Open WebUI user id, but the console and desktop links only see the OIDC subject. The first mapping is learned only from the trusted plugin call (verified identity plus the plugin key). After that, a mapping is **immutable** through the API, so a later plugin call cannot quietly move someone's persistent home to another identity. Changing one is a manual admin edit of the file.

## Shape

- `subjects`: OIDC subject → Open WebUI user id; `owners`: the reverse. Both are 1:1.
- It is scoped to one OIDC issuer. Supporting more issuers needs issuer+subject keys first.
- A missing file starts empty.

Citations: `internal/mcp/identities.go:1-38` (doc and fields), `cmd/mcp/main.go:126-132` (wiring)

## Connected to

- **owns:** the subject ↔ user-id pairs
- **owned-by:** the MCP server process
- **joins:** [Principal](principal.md), [Departed](../control-plane/departed.md) (directory users are matched through it), the console, stream and forward listeners (each needs it; `cmd/mcp/main.go:155-197`)
- **looks-like-but-is-not:** Keycloak's user list ([Directory](directory.md))

## If you change this

- **Hits:** who owns which home, departed-user cleanup, console sign-in, desktop and forward access checks
- **Does not hit:** the gateway (it only ever sees the Open WebUI user id)

## Surfaces

| Surface | Role |
|---|---|
| MCP server | reads and writes (first mapping only) |
| admin | edits the file by hand to move an identity |

## See

- Source: `internal/mcp/identities.go`
