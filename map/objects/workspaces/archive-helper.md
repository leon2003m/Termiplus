---
type: object
cluster: workspaces
universe: live
status: verified
verified: 2026-11-07 @ working tree
entity: internal/gateway/homes.go
---

# Archive helper

A container `ot-a-<hash>-<home>` that is created but **never started**. It mounts one home volume, so the gateway can read or write that home as a tar through Docker's archive API.

## Why this shape

The gateway has no volume mounts and the terminal may be stopped or busy. A stopped helper gives a stable mount for `GET/PUT /containers/<id>/archive`, without running any user code and without touching the live terminal. Every *uploaded* tar (import, restore) is rewritten by the sockproxy's `filterHomeTar`: only regular files, directories and links with clean relative names pass, and xattrs and setuid/setgid bits are dropped.

## Shape

- Label `termiplus.storage=archive`. One helper per home at a time. Export admission allows one running or queued export per user across all homes; an overlapping export is refused with 429. Other users wait for the global export slots within their deadline. A leftover helper from an interrupted run is replaced.
- Each export has two time bounds: the absolute `ExportTimeout` (2h) and an idle deadline (`ExportIdleTimeout`, default 1m). A client that stops reading has its blocked write cut off after the idle bound, so a stalled reader releases the helper lock and export slot in a minute rather than holding them for the full `ExportTimeout` (`idleDeadlineWriter`).
- Created and removed around each operation by `withHelper`.
- When `EXPORT_MAX_BYTES` is positive, `copyHomeArchive` bounds both raw tar bytes and cumulative logical member sizes. It rejects oversized members at their headers, before consuming a sparse file's expanded holes. A zero limit keeps exports uncapped.

Citations: `internal/naming/naming.go:58-61` (Archive, ArchiveFor), `internal/gateway/homes.go`: `withHelper`, `exportHome`, `copyHomeArchive`, `exportLimits`, `idleDeadlineWriter`; `internal/gateway/archive_limits_test.go`, `internal/gateway/export_limits_test.go` (`TestExportIdleDeadlineEndsStalledWrite`); `internal/sockproxy/archive.go:23` (filterHomeTar), `internal/sockproxy/proxy.go:273`.

## Connected to

- **owns:** nothing persistent
- **owned-by:** the gateway, for the duration of one export or import
- **joins:** [Home volume](home-volume.md), [Backup](../storage/backup.md)
- **looks-like-but-is-not:** an *archive* (the stored tar of a departed user)

## If you change this

- **Hits:** export, import, snapshot, restore, departed archive, and the sockproxy archive endpoint rules
- **Does not hit:** running terminals

## Surfaces

| Surface | Role |
|---|---|
| gateway | creates and removes it |
| sockproxy | validates the create and filters uploaded tars |

## See

- Source: `internal/gateway/homes.go`, `internal/sockproxy/archive.go`
