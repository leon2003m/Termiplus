---
type: object
cluster: workspaces
universe: live
status: verified
verified: 2026-11-04 (native integration)
entity: internal/naming/naming.go
---

# Terminal container

The Docker container that runs Open Terminal (including native Desktop/Browser for desktop policies) for one user. MCP calls it a **session**. Persistent: `ot-u-<hash>-<policy>`, one per user and policy. Ephemeral: `ot-e-<hash>-<chat>-<policy>`, one per chat.

## Why this shape

The name *is* the lookup key. The gateway finds a user's container by hashing the user id, so it needs no database. Labels (`termiplus.managed/user/policy/config/storage`) let it adopt containers after a restart and refuse a container whose user label does not match: a 64-bit hash collision fails closed instead of mixing users.

## Shape

- Suffix: 16 hex characters of sha256(user id), then `-` and the policy. Ephemeral puts 12 hex characters of sha256(chat id) between them (`X-Session-Id`, default `nochat`).
- Labels: `termiplus.managed`, `.user`, `.policy`, `.config` (the config hash), `.storage`.
- Env: `OPEN_TERMINAL_API_KEY` = HMAC(`KEY_SECRET`, name). DNS is pinned to the [DNS forwarder](../sandbox/dns-forwarder.md).
- Lifecycle: `ensure` adopts, creates or recreates it on a config-hash mismatch or when it is on any network but its policy's one. `adopt` (at startup) and `checkNetworks` (every minute) list running managed containers from Docker, remove any that has gained an extra network and register the rest, so a removal or inspection that failed once is retried. The reaper stops persistent containers and removes ephemeral ones once idle. Admission enforces `MAX_RUNNING` and `max_per_user`.

Citations: `internal/naming/naming.go:31-44` (regexes), `:49-82` (Suffix, Container, Ephemeral, IsManagedContainer, UserHash), `internal/gateway/lifecycle.go:167` (containerKey), `:186` (networkOK), `:218` (ensure), `:254-257` (owner check), `:348` (admit), `:481` (create), `:611` (adopt), `:620` (checkNetworks), `:681` (reap)

## Connected to

- **owns:** its Open Terminal port (8000). It is reachable only from the gateway on `ot-sandbox`.
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

`internal/gateway/control.go:withStream` exposes in-memory observed start/activity
and idle deadline to the owner console. `internal/gateway/lifecycle.go:ensure`
records starts observed here; already-running adopted containers have no known
start time. Active connections suppress idle expiry. These are display metadata,
not process telemetry or a new Docker endpoint.

The terminal image includes small agent utilities (fd-find, rsync, bc, pipx) in
`image/Dockerfile`. Inventory, measured growth and the unimplemented hybrid
desktop-control/egress design are in `docs/workspace-tools.md`.

The former custom desktop/WebDriver/accessibility experiments are removed;
use native Open Terminal Desktop and Browser tools. Native image validation
lives in `.github/scripts/desktop-smoke.sh` and `native-smoke.py`.

## Native integration update (2026-11-04)

Native Desktop and Browser live in Open Terminal on port 8000, with TigerVNC reachable only through a local Unix socket. Session metadata carries desktop: true, not stream/password/forward URLs. Source: internal/gateway/control.go:sessionInfo, lifecycle.go, image/desktop/Dockerfile.
