---
type: object
cluster: control-plane
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/mcp/departed.go
---

# Departed

A user who is disabled in Keycloak, or who was known here and has since been deleted there. After `MCP_DEPARTED_AFTER`, their homes are archived and their containers (and, where allowed, homes) are removed. A re-enabled user gets their archives back. In code: `DepartedEntry`, `DepartedStore` (`MCP_DEPARTED_FILE`).

## Why this shape

Homes of people who left must not live forever, and they must not be deleted the moment someone is briefly disabled. So departure starts a timer instead of a deletion. Archiving runs before removal. The check **never concludes anything from a partial or empty user listing**: a Keycloak outage must not look like everyone leaving.

## Shape

- Entry: subject, reason (`disabled` | `deleted` | `admin` when forced by `manage_departed`), first seen, gateway result per user id, last error.
- `RunDeparted` runs `CheckDeparted` every `MCP_DEPARTED_INTERVAL` (default 6 h). One run at a time.
- Requires `MCP_KC_ADMIN_URL` ([Directory](../identity/directory.md)) and the [IdentityStore](../identity/identity-store.md).
- Actions go to the gateway's `/control/v1/user/archive` and `user/restore`, and are audited with actor `termiplus`. Admins drive them with `manage_departed`.

Citations: `internal/mcp/departed.go:16-24` (doc), `:26` (DepartedEntry), `:37` (DepartedStore), `:104` (RunDeparted), `:134` (CheckDeparted), `:234` (archiveDeparted), `:310` (tool), `cmd/mcp/main.go:140-149`, `internal/gateway/userhomes.go:131` (archiveUser), `:177` (restoreUser)

## Connected to

- **owns:** the departed file
- **owned-by:** the MCP server
- **joins:** [Directory](../identity/directory.md), [IdentityStore](../identity/identity-store.md), [Backup](../storage/backup.md) (`archived/`), [Home volume](../workspaces/home-volume.md), [Audit log](audit-log.md)
- **looks-like-but-is-not:** a user's own snapshot ([Backup](../storage/backup.md) `snapshots/`)

## If you change this

- **Hits:** user data retention, the gateway user archive endpoints, `AllowHomeRemove` in the sockproxy, `internal/gateway/mcp_homes_e2e_test.go`
- **Does not hit:** Keycloak itself (read-only here)

## Surfaces

| Surface | Role |
|---|---|
| timer in MCP | runs it |
| admin `manage_departed` | inspects, forces, restores |

## See

- Source: `internal/mcp/departed.go`
- Process: [user-departure](../../processes/user-departure.md)
