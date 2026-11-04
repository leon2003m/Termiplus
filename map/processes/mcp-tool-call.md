---
type: process
status: verified
verified: 2026-10-19 @ a35e601
consumes: [../objects/identity/principal.md, ../objects/identity/entitlements.md, ../objects/control-plane/mcp-tools.md]
produces: [../objects/control-plane/audit-log.md]
---

# mcp-tool-call

A tool call from the chat model reaches the gateway only after sign-in, an identity binding, a role/entitlement gate and, for destructive tools, a human "yes".

## Input → Movement → Output

The Open WebUI plugin forwards the model's call with the user's OAuth token, plus the plugin key and the Open WebUI user id. oauth2-proxy verifies the token and sets the identity headers. The MCP server binds subject to user id, gates, asks for confirmation if needed, runs the handler against the gateway control API, and audits the outcome. The model gets a JSON text result, or an error that says why.

## Why this shape

The model decides *what* to call, so nothing on the model's side may decide *whether* it runs. Token checks are done by oauth2-proxy, not hand-written code. The Open WebUI id is taken from the plugin only together with its key, and only once per subject (see [IdentityStore](../objects/identity/identity-store.md)). Confirmation is a round-trip: the server refuses with `codeConfirmRequired`. The plugin drops any `_confirmed` the model sent, shows a modal, and re-calls with `_confirmed: true` only after a human clicks yes.

## Steps

1. Plugin: bearer token, plugin key and user headers. A model-supplied `_confirmed` is stripped: `integrations/openwebui/termiplus_workspaces.py:337-356`.
2. oauth2-proxy (`mcp-auth`) checks the token and sets `X-Forwarded-User`/`-Groups`: `deploy/docker-compose.mcp.yml:47`.
3. `ServeHTTP`: POST only, body ≤ 1 MiB, JSON-RPC 2.0, dispatch: `internal/mcp/server.go:231-276`.
4. `callTool`: `Authenticate` builds the [Principal](../objects/identity/principal.md); `ids.Record` binds subject → user id and fails closed on a conflict: `server.go:303-321`, `internal/mcp/auth.go:40`.
5. `invoke`: the tool's gate checks role and entitlement (refusal is audited `denied`): `server.go:337-347`.
6. Confirmation: static (`destructive`) or per-argument (`confirmIf`). Without `_confirmed`, `codeConfirmRequired` is returned and nothing is audited: `server.go:348-360`.
7. Plugin shows the modal and re-calls on yes: `termiplus_workspaces.py:364-375`, `:389`.
8. Handler → `GatewayBackend.do` → `/control/v1/...` with `X-Control-Key` and `X-User-Id`: `internal/mcp/gateway_backend.go:187`.
9. Audit `ok` or `error`, then a text result: `server.go:362-373`, `internal/mcp/audit.go:187`.

## If you change this

- **Hits:** the plugin (error codes, the `_confirmed` contract), the console (same `invoke`), [Audit log](../objects/control-plane/audit-log.md) entries, `internal/mcp/server_test.go`, `integrations/openwebui/test_termiplus_workspaces.py`, `test/integration/alpha-e2e.sh`. Auth headers are a security boundary: see [AGENTS.md](../../AGENTS.md).
- **Does not hit:** the plain terminal path ([request-to-terminal](request-to-terminal.md))

## Surfaces

| Surface | Role |
|---|---|
| chat model | picks the tool |
| user | confirms in the modal |
| plugin | forwards, strips, confirms |
| oauth2-proxy | verifies the token |
| MCP server | binds, gates, audits |
| gateway control API | acts |

## See

- Objects: [MCP tools](../objects/control-plane/mcp-tools.md), [Principal](../objects/identity/principal.md), [Entitlements](../objects/identity/entitlements.md), [Service keys](../objects/identity/service-keys.md)
- Source: `internal/mcp/server.go`, `integrations/openwebui/termiplus_workspaces.py`
