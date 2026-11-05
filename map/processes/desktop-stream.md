---
type: process
status: verified
verified: 2026-11-04 (native integration)
consumes: [../objects/identity/principal.md, ../objects/identity/identity-store.md, ../objects/control-plane/share.md]
produces: []
---

# Native Desktop and screen shares

The owner uses Open WebUI's native Desktop tab and tools on the selected
terminal connection. HTTP `/desktop/*` uses the gateway bearer and user/chat
headers; Desktop stream/audio use first-frame authentication. The gateway
substitutes the derived container key and relays frames. Browser capability
WebSockets instead authenticate with the gateway bearer in the upgrade headers,
on the three narrowly matched native view endpoints. Other sockets retain
first-frame auth. Source: `internal/gateway/ws.go:serveWebSocket`,
`internal/gateway/gateway.go:ServeHTTP`, `internal/gateway/native_test.go`.

The image uses Open Terminal 0.16, XFCE and TigerVNC's Unix socket. No second
Kasm server, VNC password, owner stream listener, player or port 6901 exists.
Source: `image/Dockerfile`, `image/desktop/Dockerfile`.

Signed-in recipients may open `/share/<token>/` on the separate share origin.
`internal/mcp/stream.go:DesktopHandler` authenticates the OIDC subject, checks
expiry and optional named recipient, and serves its own HTML plus normalized
PNG only. The image comes from `/control/v1/screen/<session>` as the owner;
`internal/gateway/preview.go:serveScreen` checks ownership and running state.
Images refresh every five seconds. Input, clipboard, audio and all WebSocket
upgrades are refused. Revoke/expiry cancels active reads and interrupts blocked
socket writes. Reads have time and byte/dimension bounds; encoding stops at its
output limit. Request deadlines are 15 seconds for screens and five for previews. Workspace
headers/body fetches have an independent three-second deadline. Normalized
image writes get two to four seconds, capped by the request/share deadline and
covering the final HTTP flush. This is periodic viewing, not full-motion
streaming.

Console previews use the same native `/desktop/screenshot` with `X-User-Id`,
320-pixel normalization and the existing 30-second cache. They do not start a
stopped container. `internal/preview/image.go` rejects invalid/oversized images
and re-encodes pixels. See [Share](../objects/control-plane/share.md).
`internal/preview/response.go` provides socket cancellation and per-service
admission: four MCP responses (one per viewer, at most two per owner), eight
gateway responses (four
per user), and one decoder/encoder per service. Workspace fetches, cache hits
and preview mutex waits never hold that image slot. Admitted decoder waiters
are bounded by the response limit and context. Excess responses return 429.
Fetch/decoder timeouts select 504; write-start failures select 503, never an
implicit empty 200. Cancelled sockets may prevent delivery of that status.

Changes affect the desktop overlay, image smoke tests, MCP/gateway share tests
and OIDC fixtures. They must preserve tenant routing and read-only recipient
access. Native image tests and real Open WebUI testing are separate gates.

The image also packages the Termiplus applications and theme: Dolphin, Chromium,
Kate and wallpaper. `image/desktop/files/usr/local/bin/startxfce4` is invoked
by Open Terminal inside its display/D-Bus session; it seeds missing preferences
and sets the wallpaper for the connected monitor only if its current image is
absent. `image/desktop/defaults.py` seeds the dock before XFCE creates stock
panels, migrates removed Firefox/Ristretto/htop and mail launchers on saved homes,
sets Chromium and Dolphin as defaults, removes panel actions and disables
logout/lock/mail keyboard shortcuts, including inherited bindings absent from
partial saved profiles. Malformed MIME/panel/shortcut files are preserved as
`.invalid-*` backups and replaced with valid defaults so startup can recover.
Hidden desktop entries omit unsupported
mail, TeX documentation GUI and logout; XFCE kiosk settings disable shutdown
and saved sessions. These are UI guards, not a restriction against the user
with sudo. Other personal settings remain on the existing home volume. The native smoke
test checks the running XFCE wallpaper selection and Dolphin folder association.

`image/desktop/prepare.py` acquires native Chromium profile leases before
removing abandoned singleton symlinks after container recreation. Managed homes
survive but PIDs are reused; active leases and unmanaged profiles stay intact.
The desktop Dockerfile's `OPEN_TERMINAL_INFO` adds coordinate examples through
the upstream `/system` prompt. Click/move require `x/y`; `to_x/to_y` are drag
destinations. Open WebUI model vision capability must be enabled for image tools.
`internal/gateway/native_schema.go` adds these requirements to native OpenAPI
operation and field descriptions as well; runtime action validation is unchanged.
The Linux screenshot is 1280×800; native schema descriptions explain raw pixels
and conversion from normalized vision coordinates. The smoke test checks its size.
`image/desktop/files/usr/local/bin/xdotool` intercepts only the two native
synchronous pointer-move command shapes, then checks actual pointer position
with a deadline. This avoids TigerVNC waits on repeated clicks at the current
position, including drag starts. It also turns unknown-key warnings from the
native `key --clearmodifiers` command into a nonzero exit, preserving native
partial-batch failure reporting. Other xdotool commands pass through unchanged.
The smoke test repeats native clicks at the same point to catch input timeouts.
User Python and the explicitly installed notebook kernel use `/opt/workspace`;
the API retains `/opt/open-terminal`. Inner Docker starts through `image/start`
under Sysbox, using its own socket, and Tini is PID 1.

Live model-tool contract verification uses `.github/scripts/native-tool-contract.py`
and `.github/scripts/native-tool-fixture.py` to observe actual DOM input from
shared Desktop Chromium and native Browser. It covers all published actions,
repeated clicks, batch preflight and Desktop/Browser partial failures through the actual
Open WebUI callables. It also asserts the live published coordinate descriptions;
this is separate from model inference and recipient shares.
