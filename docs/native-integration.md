# Native Open WebUI integration

Decision, 2026-11-04: the existing alpha implementation is the checkpoint before
migrating to Open WebUI's native workspace. Do not add another desktop or browser
control implementation to termiplus.

## Ownership

| Feature | Owner and migration decision |
| --- | --- |
| Desktop display, screenshots, mouse and keyboard | Open WebUI 0.13.0 + Open Terminal 0.16.0; retire the KasmVNC player and relay |
| Browser automation and local app previews | Native Browser tab; retire the separate port-link tool and forwarding UI |
| Files, terminals, Git, installed skills, live events | Native Open Terminal connection; pass the authenticated user's policy and chat consistently |
| Container provisioning, runtimes, limits, egress and persistent homes | Keep in termiplus |
| Snapshots and home archives | Keep; AI may list/create snapshots, never delete or prune them |
| Network policy, entitlements, identity groups and departed users | Human administration only; remove these from the model-facing tool surface |
| Console link | Keep as a link to human administration |
| View-only expiring desktop shares | No verified native replacement; do not describe ordinary chat sharing as equivalent |

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

## Validation status (2026-11-04)

- Alpha checkpoint: `808cc87`; integration work is on `feat/native-workspace-integration`.
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
  smoke test with `sysbox-runc`; the hosted-runner installation has not run here.
  No test or image disables Chromium's sandbox to accommodate the runner.
- Updated OIDC/deployment integration scripts have syntax/lint coverage here;
  the real provider and host-firewall scenarios were not rerun.

No push or release was performed. Existing home volumes, backup data and deployed
credentials are retained. Running workspaces are adopted; their image updates
when their normal lifecycle recreates them.

## Testing in Open WebUI

1. Reload Open WebUI and open a new chat.
2. In the connection picker beside the message input, choose **Desktop**
   (persistent) or **Temporary desktop** (per chat).
3. In the workspace tab bar, choose **+**, then **Desktop**. The same menu
   also offers **Browser**, terminals and files.
4. Keep that connection selected when asking the model to use the workspace;
   the model and the visible native tabs then use the same user/policy/chat.

Requesting a session with the MCP plugin provisions it but does not select a
native connection in the frontend. Desktop entries depend on the selected
connection's capabilities; choosing a plain terminal hides them.
