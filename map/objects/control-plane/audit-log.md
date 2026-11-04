---
type: object
cluster: control-plane
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/mcp/audit.go
---

# Audit log

A JSON-lines file of every MCP tool call that can change something, whether it ran, failed or was refused, from chat and the console alike (`MCP_AUDIT_FILE`). In code: `AuditLog`, `AuditEntry`.

## Why this shape

Destructive actions are chosen by an LLM and confirmed by a human, so an admin needs to see who did what afterwards. Read-only calls and calls still waiting for confirmation are left out, so the log stays about changes. Size-capped rotation (`<file>.1`) keeps at most two files' worth of history without a log daemon.

## Shape

- Entry: time, actor (user id, or `termiplus` for automatic jobs), subject, tool, args (capped at 4 KiB of JSON), outcome (`ok` | `error` | `denied`), error.
- `MCP_AUDIT_MAX_MB` (default 5). Read with `get_audit_log` (admin), at most 500 entries.

Citations: `internal/mcp/audit.go:1-30` (doc), `:32` (AuditEntry), `:43` (AuditLog), `:65` (Append), `:84` (rotate), `:187` (Server.audit), `cmd/mcp/main.go:103-104`

## Connected to

- **owns:** the audit file
- **owned-by:** the MCP server
- **joins:** [MCP tools](mcp-tools.md), [Departed](departed.md)
- **looks-like-but-is-not:** the gateway's request logs (stdout)

## If you change this

- **Hits:** `get_audit_log`, the console audit view, `internal/mcp/audit_test.go`
- **Does not hit:** the gateway

## Surfaces

| Surface | Role |
|---|---|
| MCP server | appends |
| admin | reads |

## See

- Source: `internal/mcp/audit.go`
