---
type: object
cluster: workspaces
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/gateway/homes.go
---

# Archive helper

A container `ot-a-<hash>-<home>` that is created but **never started**. It mounts one home volume, so the gateway can read or write that home as a tar through Docker's archive API.

## Why this shape

The gateway has no volume mounts and the terminal may be stopped or busy. A stopped helper gives a stable mount for `GET/PUT /containers/<id>/archive`, without running any user code and without touching the live terminal. Every *uploaded* tar (import, restore) is rewritten by the sockproxy's `filterHomeTar`: only regular files, directories and links with clean relative names pass, and xattrs and setuid/setgid bits are dropped.

## Shape

- Label `termiplus.storage=archive`. One helper per home at a time: a second export waits. A leftover helper from an interrupted run is replaced.
- Created and removed around each operation by `withHelper`.

Citations: `internal/naming/naming.go:58-61` (Archive, ArchiveFor), `internal/gateway/homes.go:25` (storageArchive), `:67` (withHelper), `internal/sockproxy/archive.go:23` (filterHomeTar), `internal/sockproxy/proxy.go:273`

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
