---
type: process
status: verified
verified: 2026-11-04 (native integration)
consumes: [../objects/workspaces/policy.md, ../objects/identity/service-keys.md]
produces: [../objects/workspaces/terminal-container.md, ../objects/workspaces/home-volume.md]
---

# request-to-terminal

An Open WebUI terminal request becomes a proxied call into that user's own container. The container is started or created on the way if needed.

## Input → Movement → Output

Open WebUI sends `/p/<policy>/<rest>` with the gateway API key, `X-User-Id` and (for chats) `X-Session-Id`. The gateway resolves the policy and the container name, makes sure the container exists, runs the current config and answers, then reverse-proxies the request. The output is the terminal's answer. The request reaches the container carrying that container's own key, and the caller's cookies and proxy token are stripped.

## Why this shape

Open WebUI thinks it talks to *one* Open Terminal. The gateway must therefore be a plain terminal from the outside (`/api/v1/` is 404 here) while fanning out per user inside. Every name is derived from the user id by hashing. If the container's owner label does not match, the gateway refuses to route rather than mix users. Paths are canonicalised *before* the policy split, so `..` cannot slip past the `/api/v1` deny.

## Steps

1. Reject encoded slashes and non-canonical paths: `internal/gateway/gateway.go:250-263`.
2. Admin and control APIs are tried first, each with its own key: `gateway.go:267-273`.
3. Split `/p/<policy>`, defaulting to `default`. An unknown policy is 404: `gateway.go:275-284`, `:403`.
4. Bearer check, except for WebSockets, which authenticate in their first message (`ws.go:78`): `gateway.go:286-292`, `:363`.
5. Validate `X-User-Id` and `X-Session-Id`. Spec paths use a shared spec user: `gateway.go:300-334`.
6. `ensure`: name = `naming.Container` or `naming.Ephemeral`, lock per name, fast path for a ready instance: `internal/gateway/lifecycle.go:218-242`.
7. Inspect. Refuse on an owner label mismatch. Recreate when the config hash differs, the container is on any network besides its policy's one (`networkOK`), an ephemeral sandbox is stopped, or a stopped persistent container's image moved: `lifecycle.go:244-279`, `:186`.
8. `admit` (capacity, making room for ephemerals), then `create` via [sockproxy-create](sockproxy-create.md), then start: `lifecycle.go:281-298`, `:348`, `:481`.
9. Wait until ready, then record the instance: `lifecycle.go:300-327`, `:573`.
10. Proxy with `Authorization: Bearer <container key>`, removing `Cookie` and `X-Proxy-Token`: `gateway.go:376-399`.
11. Later the reaper stops idle persistent containers and removes idle ephemeral ones: `lifecycle.go:681`. Every minute, before reaping, `checkNetworks` lists running managed containers from Docker and removes one that has gained a network its policy does not allow (`docker network connect`): `lifecycle.go:620`, `:721`.

## If you change this

- **Hits:** Open WebUI terminal connections (path layout, headers), `naming` (every existing container and volume name), the config hash (a change recreates every container on next use), `internal/gateway/e2e_test.go`, `test/integration/run.sh`
- **Does not hit:** the MCP server, except that `request_session` reaches `ensure` through the control API

## Surfaces

| Surface | Role |
|---|---|
| Open WebUI | caller |
| gateway | routes, creates, proxies |
| sockproxy | executes the Docker calls |
| Open Terminal in the container | answers |

## See

- Objects: [Policy](../objects/workspaces/policy.md), [Terminal container](../objects/workspaces/terminal-container.md), [Home volume](../objects/workspaces/home-volume.md), [Service keys](../objects/identity/service-keys.md)
- Source: `internal/gateway/gateway.go`, `internal/gateway/lifecycle.go`, `internal/gateway/ws.go`

## Native integration update (2026-11-04)

Native HTTP and Desktop/PTY/events/process sockets preserve user and chat. Optional first-frame chat_id must agree with an existing header. Browser view ws/stream/vnc instead use header bearer authentication, checked before provisioning. Discovery is shared only across equal runtime, image, egress and Desktop feature flags. Source: internal/gateway/gateway.go:specPolicies, internal/gateway/ws.go, internal/gateway/native_test.go.

`proxyHTTP` annotates successful GET `/openapi.json` responses with the native
Desktop action coordinate requirements (`native_schema.go`). This adds model
descriptions only, preserving endpoints, schemas' constraints, auth and request
validation. Non-schema, compressed and oversized bodies pass through unchanged.
