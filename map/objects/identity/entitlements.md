---
type: object
cluster: identity
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/mcp/entitlements.go
---

# Entitlements

Which workspaces ([Policies](../workspaces/policy.md)) a caller may see and launch, decided by group. In code: `mcp.Entitlements` (`MCP_ENTITLEMENTS_FILE`).

## Why this shape

The gateway has no roles, so the check lives in the MCP server and the gateway trusts the control key. Parsing is strict on purpose: unknown fields, a missing or unknown `default`, or an empty group list are rejected, so a typo cannot open a workspace to everyone.

## Shape

- `workspaces`: workspace → groups (any one is enough); `default`: `allow` | `deny` for unlisted workspaces.
- A nil `*Entitlements` allows everything, but only without a file under `MCP_AUTH=dev`. With `MCP_AUTH=proxy` a missing file is fatal at startup (`cmd/mcp/main.go:80-86`).
- `setup-alpha.sh` writes a deny-all file. `deploy/entitlements.example.json` is the example.
- Changed at runtime with the admin tool `set_entitlements` (unknown workspace names are refused, and a failed lookup refuses too: `server.go:78`).

Citations: `internal/mcp/entitlements.go:1-26` (doc), `internal/mcp/entitlements.go:62` (Allowed), `internal/mcp/server.go:50-147` (load, validate, filter, set)

## Connected to

- **owns:** the workspace → group table
- **owned-by:** the MCP server
- **joins:** [Principal](principal.md) roles, [Policy](../workspaces/policy.md) names, [MCP tools](../control-plane/mcp-tools.md) (`list_workspaces`, `request_session`, `list_sessions`, `get_entitlements`, `set_entitlements`)
- **looks-like-but-is-not:** `termiplus-persist`. That role gates *persistence*, not which workspace.

## If you change this

- **Hits:** what every MCP user sees, the console, `test/integration/alpha-e2e.sh`
- **Does not hit:** Open WebUI terminal connections to `/p/<policy>`. Those go straight to the gateway and Open WebUI decides who sees them.

## Surfaces

| Surface | Role |
|---|---|
| admin (`set_entitlements`, file) | writes |
| MCP tools and console | read |

## See

- Source: `internal/mcp/entitlements.go`, `deploy/entitlements.example.json`
