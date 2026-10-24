---
type: object
cluster: control-plane
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: internal/mcp/shares.go
---

# Share

An expiring, **view-only** link to someone's desktop session (KasmVNC's viewer user: no keyboard or mouse). It is not a file share. In code: `mcp.Share`, `ShareStore` (`MCP_SHARES_FILE`).

## Why this shape

Showing a colleague your screen must not hand over control or your session. So the link carries a random token and the store keeps only its hash. Viewers still have to sign in, so a leaked link alone is not enough. A share can also be limited to one named user. TTL and count are capped.

## Shape

- TTL: default 1 h, max 24 h. At most 10 active shares per owner. Path `/share/<token>`.
- Fields: public `share_id` (first 12 hex characters of the token hash), owner (Open WebUI id), session, workspace, created, expires, optional for-subject/for-name.
- Created and revoked with `share_desktop` (confirmed). Served by the desktop listener, which asks the gateway stream for the view-only user.

Citations: `internal/mcp/shares.go:19-24` (doc), `:26-31` (limits), `:34` (Share), `:58` (ShareStore), `:330` (tool), `internal/mcp/stream.go:115` (serveShare), `:142` (proxy, `view`), `internal/gateway/stream.go:13-22`

## Connected to

- **owns:** the share file
- **owned-by:** the MCP server (needs `UI_LISTEN_ADDR` and the stream listener)
- **joins:** [Terminal container](../workspaces/terminal-container.md) (desktop policies only), [IdentityStore](../identity/identity-store.md), [MCP tools](mcp-tools.md)
- **looks-like-but-is-not:** a port forward, or a home download

## If you change this

- **Hits:** the stream proxy view flag, the gateway's view-only header, the console share UI, `internal/mcp/shares_test.go`
- **Does not hit:** terminal (non-desktop) sessions

## Surfaces

| Surface | Role |
|---|---|
| owner | creates and revokes |
| signed-in viewer | watches |

## See

- Source: `internal/mcp/shares.go`, `internal/mcp/stream.go`
- Process: [desktop-stream](../../processes/desktop-stream.md)
