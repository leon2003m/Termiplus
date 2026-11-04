---
type: object
cluster: control-plane
universe: live
status: verified
verified: 2026-11-04 (native integration)
entity: internal/mcp/shares.go
---

# Share

An owner-issued, expiring and revocable **view-only** screen link. It is not a
file share or an interactive desktop connection. `mcp.Share` and `ShareStore`
keep hashed random tokens, default TTL one hour, maximum 24 hours, and at most
10 active shares per owner. Optional `ForSubject` limits a link to one signed-in
OIDC subject. `MCP_SHARES_FILE` persists the store.

`share_desktop` and the console create/list/revoke links. The separate listener
(`STREAM_LISTEN_ADDR`, legacy name) serves only `/share/`. Recipients get a
normalized native desktop PNG every five seconds, never arbitrary container
HTML, VNC bytes, audio, input or clipboard. They still sign in through
`desk-stream-auth`. Expiry/revoke cancels in-flight reads and wins over late
responses. Stopped/foreign desktop containers are not started for a share.

The source path is `internal/mcp/shares.go`, `internal/mcp/stream.go`,
`internal/gateway/preview.go:serveScreen`, `internal/preview/image.go`.
The console's named-recipient picker requires ownership and entitlement and
only returns exact enabled usernames; it never enumerates the directory.
Protocol/ownership tests live in `internal/gateway/mcp_homes_e2e_test.go`.
See [native desktop](../../processes/desktop-stream.md).
