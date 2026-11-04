---
type: object
cluster: sandbox
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/sockproxy/validate.go
---

# Sockproxy rules

The complete policy the Docker socket proxy enforces. Anything not expressible here cannot be requested. In code: `sockproxy.Rules`, plus the endpoint switch in `proxy.go`.

## Why this shape

Whoever can create containers on Docker is root on the host. So the gateway gets no socket. It talks to a proxy that allows a handful of endpoints and **rebuilds** every create body from validated fields (it never forwards the client's JSON). A bug or compromise in the gateway is then bounded by these rules: an allowed image, runtime and network, limits under the caps, exactly one home mount that belongs to the same user, no privileges, and no host mounts.

## Shape

- Rules: `Image`, `ExtraImages`, `Runtimes` (`ALLOWED_RUNTIMES`, e.g. runsc and sysbox-runc), `Network`, `MaxMemory`, `MaxNanoCPUs`, `MaxPids`, `AllowedDNS` (exactly the forwarder, or no DNS at all), `HomeVolumeSize`, `RootfsSize`.
- Endpoints: `_ping` and `version`; list (managed containers only); create; volume create, inspect and delete (delete only with `AllowHomeRemove`); image inspect (allowed images, ID only); and per-container ops. Container references are resolved to a managed container with a terminal name first, then forwarded by full ID. Everything else: "endpoint not allowed".
- Create: the name must match a terminal, ephemeral or archive pattern. Ephemeral containers get no mounts. Others get exactly one writable volume at `/home/user` with the same user hash. Env and labels go through an allowlist. Output: `Privileged:false`, `MemorySwap=Memory`, `RestartPolicy:no`, `AutoRemove` for ephemeral, capped json-file logs.
- Volume create: no driver options (`o=bind` would be a host mount); terminal volume names only.
- The proxy runs as uid 65534 with only the docker group (`deploy/docker-compose.yml:26`).

Citations: `internal/sockproxy/validate.go:17` (Rules), `:134` (ValidateCreate), `:219-246` (rebuilt body), `:262` (ValidateVolume), `:290` (strictDecode), `:313` (sanitizeLabels), `internal/sockproxy/proxy.go:98` (token), `:120-193` (endpoint switch), `:200` (containerOp), `:281` (ownedName), `:362` (resolveOwned)

## Connected to

- **owns:** the only Docker socket mount
- **owned-by:** the `sockproxy` service; configured by env in `deploy/docker-compose.yml`
- **joins:** [Terminal container](../workspaces/terminal-container.md), [Home volume](../workspaces/home-volume.md), [Archive helper](../workspaces/archive-helper.md), [Policy](../workspaces/policy.md) (limits must fit), [DNS forwarder](dns-forwarder.md) (`ALLOWED_DNS`)
- **looks-like-but-is-not:** the [firewall](firewall-chain.md). The sockproxy controls *what is created*; the firewall controls *what it can reach*.

## If you change this

- **Hits:** the security boundary. Per [AGENTS.md](../../../AGENTS.md), any loosening needs a test, a Security impact section and maintainer approval. Also `internal/sockproxy/*_test.go` and the gateway's create body (`lifecycle.go:424`).
- **Does not hit:** MCP, the plugin

## Surfaces

| Surface | Role |
|---|---|
| gateway | the only client (`X-Proxy-Token`) |
| Docker daemon | behind it |

## See

- Source: `internal/sockproxy/`, `cmd/sockproxy/main.go`
- Process: [sockproxy-create](../../processes/sockproxy-create.md)
