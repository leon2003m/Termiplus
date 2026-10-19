---
type: object
cluster: workspaces
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/naming/naming.go
---

# Home volume

A Docker volume `ot-home-<hash>-<home>` mounted at `/home/user`: one per user and `Policy.home` group. This is the user's persistent data.

## Why this shape

The container is disposable (it is recreated on any config change); the volume is not. Splitting them lets the gateway recreate, stop and update containers freely. Before reusing an existing volume, the owner label is checked, so a hash collision cannot hand one user another's home.

## Shape

- Name from `naming.Volume`. Labels include `termiplus.user`.
- Optional disk quota `HOME_VOLUME_SIZE`, applied by the sockproxy (needs XFS with prjquota, otherwise the create fails).
- Ephemeral policies have no volume.
- Operations: reset, snapshot, restore, export, import, remove. All go through the [archive helper](archive-helper.md).

Citations: `internal/naming/naming.go:55`, `internal/gateway/lifecycle.go:436-446` (ownership check), `internal/gateway/homes.go:31` (homeOf), `:121` (exportHome), `:197` (importHome), `:224` (removeHome), `internal/gateway/userhomes.go:61` (reset), `:90` (snapshot), `internal/sockproxy/validate.go:262` (ValidateVolume)

## Connected to

- **owns:** the user's files
- **owned-by:** one user (label), created by the gateway via the sockproxy
- **joins:** [Terminal container](terminal-container.md), [Backup](../storage/backup.md), [Archive helper](archive-helper.md), [Departed](../control-plane/departed.md)
- **looks-like-but-is-not:** a departed user's *archive* (a tar under `BACKUP_DIR/archived/`)

## If you change this

- **Hits:** user data. Renaming the scheme orphans every existing home. Also sockproxy volume validation and `AllowHomeRemove`.
- **Does not hit:** container naming for ephemeral policies

## Surfaces

| Surface | Role |
|---|---|
| user (terminal, console download/upload) | reads and writes |
| gateway | creates, tars, removes |
| admin `/admin/homes/` | manages |

## See

- Source: `internal/gateway/homes.go`, `internal/gateway/userhomes.go`
