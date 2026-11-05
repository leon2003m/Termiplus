# Native Open WebUI integration

Current architecture, 2026-11-05: Open WebUI 0.13 and Open Terminal 0.16 own
workspace interaction. The alpha checkpoint `808cc87` is the pre-migration
baseline. Termiplus supplies isolation, routing, lifecycle, homes and access;
it does not add another desktop or browser control implementation.

Termiplus uses the standard terminal-server connection and optional Workspace
Tools extension without a core Open WebUI fork. It is not a byte-for-byte
transparent proxy: the gateway annotates Desktop OpenAPI descriptions and the
image supplies narrow command/startup wrappers. See the [supported tool inventory
and maintained adaptations](tool-support.md) for all 23 native tools, eight
optional plugin methods and the precise compatibility boundary.

## Ownership

| Feature | Current owner |
| --- | --- |
| Desktop display, screenshots, mouse and keyboard | Open WebUI 0.13.0 + Open Terminal 0.16.0; KasmVNC player and relay removed |
| Browser automation and local app previews | Native Browser tab; separate port-link tool and forwarding UI removed |
| Files, terminals, Git, installed skills, live events | Native Open Terminal connection; pass the authenticated user's policy and chat consistently |
| Container provisioning, runtimes, limits, egress and persistent homes | Keep in termiplus |
| Snapshots and home archives | Keep; AI may list/create snapshots, never delete or prune them |
| Network policy, entitlements, identity groups and departed users | Human administration only; remove these from the model-facing tool surface |
| Console link | Keep as a link to human administration |
| View-only expiring desktop shares | Termiplus image-only viewer, refreshed every five seconds; ordinary chat sharing is not equivalent |

Only released features or an explicit upstream implementation/commit establish
a replacement. A guessed roadmap is not a basis for deleting working behavior.

## Compatibility and migration requirements

- Keep per-user containers, existing home volumes and the Docker allowlist.
- Select the same policy in the Open WebUI terminal picker for shell, desktop,
  browser and files. A separate MCP session is not an automatic attachment.
- Verify native HTTP and first-message-authenticated WebSockets, including
  chat scoping, wrong keys, cross-user isolation and long-lived connection use.
- Keep desktop/browser credentials inside the existing terminal connection.
- Do not expose workspace-controlled HTML on the administration origin.
- Remove obsolete implementation, tests for removed contracts, deploy settings
  and documentation together; preserve security coverage for retained paths.
- Run `make check` before each commit. Image and real Open WebUI validation
  must be reported separately from in-process tests.

## Upstream evidence

- [Open WebUI 0.13.0 release](https://github.com/open-webui/open-webui/releases/tag/v0.13.0)
- [Open Terminal 0.15.0 release](https://github.com/open-webui/open-terminal/releases/tag/v0.15.0)
- [Open Terminal 0.16.0](https://github.com/open-webui/open-terminal/tree/v0.16.0)

Upstream source may be read to understand the protocol. Implement termiplus
changes independently; do not copy Open WebUI code.

## Validation status (2026-11-05)

- Alpha checkpoint: `808cc87`; the integrated native workspace changes are
  included in release candidate `1.0.0-rc.3`.
- `make check` passes, including race detection and all 30 plugin tests.
- Native gateway tests cover first-frame and Browser header authentication,
  chat scoping, per-user routing, capability discovery and denied legacy routes.
- Both Open Terminal 0.16.0 images build. Under Sysbox, the image smoke test
  passes authenticated screenshots, keyboard actions, the first-frame-authenticated
  VNC stream, wrong-key rejection, native app previews and Chromium automation
  snapshots with Chromium's sandbox enabled. Plain policies deny Desktop.
- The running Open WebUI 0.13.0 installation now uses the new gateway, MCP and
  desktop image, with explicit Desktop and Temporary desktop connections and
  plugin 0.6.0. Capability discovery, screenshots, keyboard actions and the
  authenticated desktop WebSocket pass through the actual Open WebUI proxy.
  Rollback images, deployment settings, the Open WebUI database and existing
  ephemeral home data were backed up before the rollout.
- A browser check confirms both connections appear in the picker and selecting
  Desktop adds Browser and Desktop to the native tab menu. Opening Desktop
  renders the live 1280 × 800 canvas. Native Browser creation, sandboxed Chromium,
  automation snapshots and its capability-authenticated VNC WebSocket also pass
  through the actual Open WebUI proxy and gateway.
- Chromium's sandbox fails with the ordinary Docker runtime on this host.
  The desktop CI job now installs checksum-pinned Sysbox 0.7.1 and runs the same
  smoke test with `sysbox-runc`; the hosted-runner build and smoke test pass.
  No test or image disables Chromium's sandbox to accommodate the runner.
- The Docker-backed OIDC/proxy integration suite passes all 33 checks. GitHub
  CI, Security, CodeQL and gVisor integration checks pass on the integrated code.
  The production host-firewall scenarios were not rerun for this release.
- The [tool inventory](tool-support.md), [deterministic validation](native-tool-validation.md),
  [independent review](reviews/2026-11-05-deepseek-native-review.md) and
  [frontend acceptance](reviews/2026-11-05-frontend-acceptance.md) distinguish
  native callable, HTTP/PTY and real viewer coverage.

Existing home volumes, backup data and deployed credentials are retained.
Running workspaces are adopted; their image updates when their normal lifecycle
recreates them. Release candidate `1.0.0-rc.3` packages this native integration;
see [the changelog](../CHANGELOG.md#100-rc3---2026-11-05) for upgrade notes.

## Testing in Open WebUI

1. Reload Open WebUI and open a new chat.
2. In the connection picker beside the message input, choose **Desktop**
   (persistent) or **Temporary desktop** (per chat).
3. In the workspace tab bar, choose **+**, then **Desktop**. The same menu
   also offers **Browser**, terminals and files.
4. Keep that connection selected when asking the model to use the workspace;
   the model and the visible native tabs then use the same user/policy/chat.

Requesting a session with the MCP plugin provisions it but does not select a
native connection in the frontend. Both shipped connections now advertise Desktop and Browser; terminal-only
policies are no longer offered by the default configuration.

## Desktop defaults restored (2026-11-04)

The transport migration initially dropped the existing desktop applications and
branding. Restore Dolphin, Chromium, Kate, the app dock,
Qt/GTK theme and wallpaper while retaining native TigerVNC and audio.
The native startxfce4 launcher seeds missing user preferences and initializes
the wallpaper for TigerVNC's monitor name, preserving existing valid choices.

The shipped policies are now only `default` (Desktop) and `scratch` (Temporary
desktop), both Sysbox with desktop/browser enabled. The persistent home name is
unchanged. The complete image also includes inner Docker with Compose/Buildx,
scientific Python and Jupyter, Office/PDF/media tools and the standard CLI set.
See [image comparison](open-terminal-image-review.md) for coverage and deliberate
architecture differences. Native model context explains Desktop coordinates,
and vision-capable models must have vision enabled in Open WebUI. The saved
model system prompt must describe the shared graphical XFCE desktop and its
native screenshot/action tools; remove old terminal-only instructions and
references to removed tools. Browser automation is separate from shared
desktop applications. Chromium is the only installed browser, btop replaces
htop, and Ristretto/ImageMagick are intentionally omitted. Startup repairs
obsolete app defaults and hides logout/lock controls on saved homes as well
as new desktops, including inherited shortcuts absent from partial saved files.
Malformed MIME/panel/shortcut settings are preserved as `.invalid-*` backups
before restoring valid defaults. Pointer synchronization is bounded so repeated clicks at the
current position no longer hang or time out with HTTP 502. Unknown X11 key
names fail with HTTP 502 and report partial-batch progress. Linux screenshots use
1280×800 raw pixel coordinates; the schema and image context describe this
explicitly rather than assuming a normalized vision coordinate scale.

Validated on 2026-11-05: `make check`, the complete Sysbox native smoke test
(including notebook analysis, Office-to-PDF and nested Docker build/run), real
Open WebUI Desktop HTTP/VNC and Browser automation, visible Chromium rendering,
and repeated native clicks at the same pointer position returning HTTP 200.
A local model selecting tools is a separate check from completing a browser
task: full chat tests must verify page reads, button clicks and navigation
against observed browser state, with tool-call and time budgets. The live test model's supported vision
capability is enabled. Reload Open WebUI and start a new chat to discard old
connection selections and failed tool-call history.

The [deterministic native tool suite](native-tool-validation.md) extracts the
actual Open WebUI callables and verifies all 23 model tools without inference,
including independent DOM observations for Desktop and Browser input.
