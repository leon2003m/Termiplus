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
`desk-stream-auth`. Expiry/revoke cancels in-flight reads, interrupts blocked
socket writes and wins over late responses. Request deadlines are 15 seconds
for screens and five for console previews. Workspace fetches have an independent
three-second deadline covering headers and body; timeout returns 504. After
normalization, socket writes
get two seconds plus one second per 4 MiB (at most four seconds), capped by the
request deadline and interrupted by share cancellation. Stopped/foreign desktop
containers are not started for a share.

MCP admits four active image responses, one per viewer and at most two per owner,
counted across links, sessions and recipients. Owners remain able to serve two
viewers concurrently. Gateway admits eight, at most four per user, so a share request and
its owner's thumbnails may overlap. Excess responses get 429/`Retry-After: 1`.
Only decoding/encoding holds the single image-processing slot per service.
Bounded HTTP body reads, Docker Inspect, preview-cache locks and cached writes
hold no image slot. Admitted responses waiting for decoding are bounded by the
response limit and their contexts; no extra unbounded queue exists. Preview
cache locks are released before socket writes. PNG output reserves exactly
8 MiB up front and rejects output beyond that limit. Responses release admission
capacity on success/error; cancellation interrupts downstream writes. Decoder
admission timeout returns 504, and a failure starting a normalized-image write
selects 503. A socket already cancelled may prevent delivery of the error status.

The source path is `internal/mcp/shares.go`, `internal/mcp/stream.go`,
`internal/gateway/preview.go:serveScreen`, `internal/preview/image.go` and
`internal/preview/response.go`. Slow-reader cancellation and admission regressions
live in `internal/mcp/stream_test.go`; callback lifetime and image budgets are
tested in `internal/preview/response_test.go`.
The console's named-recipient picker requires ownership and entitlement and
only returns exact enabled usernames; it never enumerates the directory.
Protocol/ownership tests live in `internal/gateway/mcp_homes_e2e_test.go`.
See [native desktop](../../processes/desktop-stream.md).
