---
type: object
cluster: workspaces
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/gateway/lifecycle.go
---

# Policy (workspace)

One kind of terminal environment, reached at `/p/<name>/`. The MCP tools, the plugin and the console call it a **workspace**. In code: `gateway.Policy`, loaded from `policies.json`.

## Why this shape

Open WebUI only knows "a terminal connection with a URL". Putting runtime, limits, storage mode and image behind a URL prefix lets an admin offer several terminals (gVisor, sysbox, desktop, ephemeral scratch) with no change in Open WebUI beyond adding a connection. Every field ends up in the container's config hash, so editing a policy recreates its containers on their next request.

## Shape

- `runtime` (`runsc` | `sysbox-runc`, must be in the sockproxy's `ALLOWED_RUNTIMES`), `memory_mb`, `cpus`, `pids`
- `storage`: `persistent` (default: one container per user plus a home volume) | `ephemeral` (one container per chat, no volume, deleted when idle)
- `home`: groups policies that share one home volume (default: the policy name; ignored for ephemeral)
- `idle_timeout`, `max_per_user` (ephemeral cap, default 3; the least recently used idle sandbox is evicted)
- `desktop`: KasmVNC XFCE next to Open Terminal (needs the desktop image)
- `image`: another image built on the terminal image. It must be in the sockproxy's `EXTRA_IMAGES`.
- Name: `[a-z0-9]{1,24}` (`naming.go:31-44`)

Citations: `internal/gateway/lifecycle.go:20-56` (Policy), `internal/gateway/lifecycle.go:154-158` (configHash), `internal/gateway/gateway.go:207` (specPolicies), `cmd/gateway/main.go:32-35` (POLICIES_FILE), `deploy/policies.json`

## Connected to

- **owns:** nothing at runtime. It is a template for [Terminal containers](terminal-container.md).
- **owned-by:** the operator's `policies.json` (mounted read-only into the gateway, `deploy/docker-compose.yml:67,76`)
- **joins:** [Entitlements](../identity/entitlements.md) (by name), [Home volume](home-volume.md) (via `home`), [sockproxy rules](../sandbox/sockproxy-rules.md) (runtimes, images, limits must fit)
- **looks-like-but-is-not:** `netpolicy.Policy` ([Network policy](../sandbox/network-policy.md))

## If you change this

- **Hits:** the config hash, so every container of that policy is recreated; sockproxy limits (a policy above `MAX_MEMORY` and the like is refused at create); Open WebUI connection URLs (outside the repo); entitlements keyed by the name
- **Does not hit:** home contents (renaming `home` points at a *different* volume, the old one stays)

## Surfaces

| Surface | Role |
|---|---|
| operator | writes `policies.json` |
| gateway | reads at startup |
| MCP `list_workspaces`, console | read via `/control/v1/workspaces` |

## See

- Source: `internal/gateway/lifecycle.go`, `deploy/policies.json`
- The live host keeps a local uncommitted edit of `deploy/policies.json`; see [effects](../../effects/CONTEXT.md).
