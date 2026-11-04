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
- The actual Open Terminal 0.16.0 terminal image builds. Its authenticated file
  API, capability discovery and disabled desktop policy pass a container test.
- The desktop image build stopped during package installation because the host
  filesystem ran out of space. Only this test's containers/images were removed.
  The graphical image smoke test, Chromium sandbox compatibility and real
  Open WebUI Desktop/Browser interaction remain unverified. The new smoke test
  requires Chromium to start with its sandbox; it does not permit `--no-sandbox`.
- Updated OIDC/deployment integration scripts have syntax/lint coverage here;
  the real provider and host-firewall scenarios were not rerun.

No deployment, push or release is part of this migration commit. Existing
workspace containers, homes, backup data and deployed credentials are retained.
