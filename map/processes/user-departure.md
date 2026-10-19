---
type: process
status: verified
verified: 2026-10-19 @ a35e601
consumes: [../objects/identity/directory.md, ../objects/identity/identity-store.md, ../objects/workspaces/home-volume.md]
produces: [../objects/control-plane/departed.md, ../objects/storage/backup.md, ../objects/control-plane/audit-log.md]
---

# user-departure

A user who leaves Keycloak (disabled or deleted) has their homes archived after a grace period, and gets them back if re-enabled.

## Input → Movement → Output

On a timer, the MCP server lists all Keycloak users. Newly disabled users, and subjects known to the IdentityStore that are missing from Keycloak, become departed entries with a start time. Entries older than `MCP_DEPARTED_AFTER` are archived. For each of the subject's gateway user ids, the gateway tars every home into `BACKUP_DIR/archived/`. It then removes the home (only if the sockproxy allows it) and all of the user's containers. A re-enabled user is restored from the newest archive of each home.

## Why this shape

Deletion is irreversible and Keycloak can be briefly wrong, so departure only starts a timer. A failed or empty user listing aborts the round instead of marking everyone departed. Archiving happens before removal. A home whose backup fails is never removed, and the run stops with an error. Home removal also needs `ALLOW_HOME_REMOVE=1` in the sockproxy, so a deployment can keep homes even after archiving. Every automatic action is audited with actor `termiplus`.

## Steps

1. `RunDeparted` runs every `MCP_DEPARTED_INTERVAL`, one run at a time: `internal/mcp/departed.go:104`, `cmd/mcp/main.go:140-149`.
2. `CheckDeparted`: list users, and on error stop. Record `disabled`. Delete the entries of users who came back. Record `deleted` only for a non-empty listing. Collect due entries: `departed.go:134-207`.
3. Back: restore via `userHomes(…, "restore")` with an audit entry: `departed.go:195-199`, `:216`.
4. Due: `archiveDeparted` → `userHomes(…, "archive")` for each id from `gatewayUserIDs` (Open WebUI id and subject): `departed.go:120`, `:234`.
5. Gateway control API `POST user/archive` / `user/restore`: `internal/gateway/control.go:109`, `:118`.
6. `archiveUser`: for each persistent home, back it up as `archived`, then `removeHome`; then remove every container of the user: `internal/gateway/userhomes.go:131-168`.
7. `restoreUser` restores the newest archived backup per home: `userhomes.go:177`.
8. Admins can force, list or restore with `manage_departed`: `departed.go:261`, `:310`.

## If you change this

- **Hits:** data retention for real users, the backup layout (`archived/`), `AllowHomeRemove` in the sockproxy (`internal/sockproxy/validate.go:48`), `internal/gateway/mcp_homes_e2e_test.go`, the departed file format.
- **Does not hit:** Keycloak (read-only), a user's own snapshots, the terminal request path.

## Surfaces

| Surface | Role |
|---|---|
| MCP timer | detects and schedules |
| Keycloak admin API | source of truth for users (read) |
| gateway control API | archives, removes, restores |
| sockproxy | allows or refuses home removal |
| admin console / chat | `manage_departed` |

## See

- Objects: [Departed](../objects/control-plane/departed.md), [Backup](../objects/storage/backup.md), [Directory](../objects/identity/directory.md)
- Source: `internal/mcp/departed.go`, `internal/gateway/userhomes.go`
