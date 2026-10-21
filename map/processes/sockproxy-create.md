---
type: process
status: verified
verified: 2026-10-19 @ a35e601
consumes: [../objects/workspaces/policy.md, ../objects/sandbox/sockproxy-rules.md]
produces: [../objects/workspaces/terminal-container.md, ../objects/workspaces/home-volume.md]
---

# sockproxy-create

The gateway's wish for a container is checked field by field and *rebuilt* by the sockproxy. Only that rebuilt body reaches the Docker socket.

## Input → Movement → Output

The gateway sends `POST /volumes/create` (persistent only) and `POST /containers/create?name=…` with labels, the image, runtime, limits, one home mount, env and DNS. The sockproxy checks the proxy token and the name pattern, strictly decodes the body (unknown fields are an error), validates each field against `Rules`, and writes a fresh body with hard-coded hardening. Docker creates exactly that container. Nothing the gateway sent passes through unread.

## Why this shape

The gateway is internet-facing (through Open WebUI) and parses user traffic. If it is ever taken over, it must still be unable to create a privileged container, bind-mount the host, pick another network or resolver, or mount another user's home. So the policy lives in a separate small process with no other job. That process allows a body only if it can be expressed in the allow-list, and it writes the security fields itself instead of trusting them. The gateway runs its own checks too (owner label on the volume) as defense in depth.

## Steps

1. Gateway builds the spec. For persistent policies it first checks the volume's owner label, then creates the volume: `internal/gateway/lifecycle.go:437-497`.
2. Sockproxy: token check (`X-Proxy-Token`), path normalization, then the route switch: `internal/sockproxy/proxy.go:91-120`, `:429`.
3. `/volumes/create` → `ValidateVolume` (no driver options; size from `HOME_VOLUME_SIZE`): `proxy.go:153`, `validate.go:262`.
4. `/containers/create` → `ValidateCreate`: `proxy.go:142`, `internal/sockproxy/validate.go:134`.
   - name must be a container, ephemeral or helper name: `validate.go:137-139`
   - strict decode, then image, runtime, network, memory, CPU and pids limits: `validate.go:143-165`, `:290`
   - DNS empty, or exactly `ALLOWED_DNS`: `validate.go:168-175`
   - ephemeral: no mounts. Persistent: one writable volume at `/home/user` whose name carries the same user hash. A helper mounts only its own home: `validate.go:177-199`
   - env ≤ 32 entries matching a pattern; labels sanitized, duplicate keys refused: `validate.go:202-208`, `:313`
5. Rebuild: `Privileged:false`, `MemorySwap=Memory`, restart `no`, `AutoRemove` for ephemerals, capped json-file logs, storage size: `validate.go:219-246`.
6. Later calls (`start`, `stop`, `json`, `archive`, `DELETE`) are allowed only on owned names: `proxy.go:190-280`, `:281`, `:362`.

## If you change this

- **Hits:** every gateway create (a field the gateway adds is refused until `Rules` knows it), `internal/sockproxy/*_test.go`, `test/integration/validate.sh` V-checks. **Security boundary:** see [AGENTS.md](../../AGENTS.md); a change needs a test and a Security impact section.
- **Does not hit:** MCP, Open WebUI

## Surfaces

| Surface | Role |
|---|---|
| gateway | caller with `PROXY_TOKEN` |
| sockproxy | validator, the only socket holder |
| Docker daemon | executes |

## See

- Objects: [Sockproxy rules](../objects/sandbox/sockproxy-rules.md), [Terminal container](../objects/workspaces/terminal-container.md), [Home volume](../objects/workspaces/home-volume.md), [DNS forwarder](../objects/sandbox/dns-forwarder.md)
- Source: `internal/sockproxy/validate.go`, `internal/sockproxy/proxy.go`
