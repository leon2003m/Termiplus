---
type: object
cluster: storage
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/gateway/backups.go
---

# Backup

A gzip-compressed tar of one [home volume](../workspaces/home-volume.md), stored by the gateway under `BACKUP_DIR`. It is either a user's **snapshot** or the **archive** of a departed user's home. In code: `gateway.Backup`, `backupStore`.

## Why this shape

Homes are Docker volumes, so a reset or a departure would otherwise destroy data for good. The gateway already streams homes as tars (`exportHome`). A backup is that same stream, written to a file. Snapshots and archives use separate directories, because snapshots are pruned to the last N and archives must never be pruned. The directory path contains the volume name (which carries the user hash) and is validated against `naming.VolumeName`, so one user's request can never name another user's backup.

## Shape

- Layout: `<BACKUP_DIR>/snapshots/<volume>/<id>.tar.gz` (kept: `SNAPSHOT_KEEP`, default 5) and `<BACKUP_DIR>/archived/<volume>/<id>.tar.gz`.
- Id: UTC creation time, `20261016T221500Z`, with a suffix `-001`… inside one second. Ids sort in creation order and are never reused.
- Tar entries are `user/...`, the `exportHome` format. Restores go back through the [archive helper](../workspaces/archive-helper.md), which filters uploaded tars.
- Without `BACKUP_DIR`, snapshots, user archives and departed handling answer "not configured". The MCP overlay sets it to `/data/backups`.

Citations: `internal/gateway/backups.go:20-27` (doc), `:36` (id regexp), `:41` (Backup), `:47` (backupStore), `:54` (newBackupStore), `:66` (homeDir), `:106` (create), `internal/gateway/gateway.go:57-59`, `:201-202`, `cmd/gateway/main.go:76-77`, `internal/gateway/userhomes.go:107` (restoreBackup), `:121` (ArchivedHome), `:131` (archiveUser), `:177` (restoreUser), `deploy/docker-compose.mcp.yml:118`

## Connected to

- **owns:** the files under `BACKUP_DIR`
- **owned-by:** the gateway
- **joins:** [Home volume](../workspaces/home-volume.md), [Archive helper](../workspaces/archive-helper.md), [Departed](../control-plane/departed.md), [MCP tools](../control-plane/mcp-tools.md) (`manage_snapshots`, `reset_workspace`)
- **looks-like-but-is-not:** the archive helper container (`ot-a-…`), which only mounts a home; and an off-host backup of the host, which termiplus does not make

## If you change this

- **Hits:** the tar format of `exportHome`/`importHome`, `manage_snapshots`, departed archiving, the console's snapshot list, `internal/gateway/backups_test.go`
- **Does not hit:** the sockproxy rules (backups are files in the gateway container, not Docker objects)

## Surfaces

| Surface | Role |
|---|---|
| gateway control API `workspaces/<ws>/home/...`, `user/archive`, `user/restore`, `user/archives` | creates, lists, restores |
| host operator | backs up the `BACKUP_DIR` volume |

## See

- Source: `internal/gateway/backups.go`, `internal/gateway/userhomes.go`
- Process: [user-departure](../../processes/user-departure.md)
