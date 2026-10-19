---
type: object
cluster: workspaces
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/naming/naming.go
---

# Terminal container

The Docker container that runs Open Terminal (and, for desktop policies, KasmVNC) for one user. MCP calls it a **session**. Persistent: `ot-u-<hash>-<policy>`, one per user and policy. Ephemeral: `ot-e-<hash>-<chat>-<policy>`, one per chat.

## Why this shape

The name *is* the lookup key. The gateway finds a user's container by hashing the user id, so it needs no database. Labels (`termiplus.managed/user/policy/config/storage`) let it adopt containers after a restart and refuse a container whose user label does not match: a 64-bit hash collision fails closed instead of mixing users.

## Shape

- Suffix: 16 hex characters of sha256(user id), then `-` and the policy. Ephemeral puts 12 hex characters of sha256(chat id) between them (`X-Session-Id`, default `nochat`).
- Labels: `termiplus.managed`, `.user`, `.policy`, `.config` (the config hash), `.storage`.
- Env: `OPEN_TERMINAL_API_KEY` = HMAC(`KEY_SECRET`, name). DNS is pinned to the [DNS forwarder](../sandbox/dns-forwarder.md).
- Lifecycle: `ensure` adopts, creates or recreates it on a config-hash mismatch. The reaper stops persistent containers and removes ephemeral ones once idle. Admission enforces `MAX_RUNNING` and `max_per_user`.

Citations: `internal/naming/naming.go:31-44` (regexes), `:49-82` (Suffix, Container, Ephemeral, IsManagedContainer, UserHash), `internal/gateway/lifecycle.go:140` (containerKey), `:165` (ensure), `:201-203` (owner check), `:291` (admit), `:424` (create), `:542` (adopt), `:576` (reap)

## Connected to

- **owns:** its Open Terminal port (8000) and desktop port. Both are reachable only from the gateway on `ot-sandbox`.
- **owned-by:** the gateway lifecycle manager, created through the sockproxy
- **joins:** [Policy](policy.md), [Home volume](home-volume.md) (persistent only), [Principal](../identity/principal.md) (user hash), [Firewall chain](../sandbox/firewall-chain.md)
- **looks-like-but-is-not:** an Open WebUI chat session, and the [archive helper](archive-helper.md) (`ot-a-`, never started)

## If you change this

- **Hits:** the naming regexes (sockproxy `ownedName` re-checks names: `internal/sockproxy/proxy.go:281`), adopt after restart, every `/control/v1/sessions` client, `test/integration`
- **Does not hit:** home contents (the volume is separate)

## Surfaces

| Surface | Role |
|---|---|
| gateway | creates, adopts, stops, removes |
| sockproxy | validates every create |
| MCP (`request_session`, `list_sessions`, `destroy_session`), console | via the control API |

## See

- Source: `internal/naming/naming.go`, `internal/gateway/lifecycle.go`
- Process: [request-to-terminal](../../processes/request-to-terminal.md), [sockproxy-create](../../processes/sockproxy-create.md)
