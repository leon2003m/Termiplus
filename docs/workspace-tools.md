# Workspace tool ownership

Use Open WebUI 0.13+ with Open Terminal 0.16.0 for terminal execution, files,
Git, events, Desktop screenshots/actions and Browser automation. Termiplus does
not implement a second screenshot/click/type/browser tool family, WebDriver
service, desktop player or port forwarding UI.

The [supported tool inventory](tool-support.md) lists exact native and plugin
method names and our maintained adaptations. The connection interface is
drop-in; Desktop schema descriptions and image command/default wrappers are
deliberate Termiplus changes.

Use one selected terminal connection in one saved chat. Native HTTP, WebSocket
and tool calls must resolve to the same user/policy/chat sandbox. The bundled policies are **Desktop** (`default`, persistent) and **Temporary
desktop** (`scratch`, per chat), both with Desktop/Browser enabled. Custom
policies may share a home profile; shared files do not imply the same running
display. For shared human/AI desktop interaction select the same connection. See [native integration](native-integration.md)
and [control plane](mcp.md).

Termiplus tools concern provisioning, lifecycle, homes, backups and expiring
view-only screen links. The model may list/create snapshots without deleting or
pruning older backups. Network policy, identity-provider management, entitlement
changes, snapshot restoration/deletion and departed-user actions belong to human
administration. `open_console` returns a link only.

Treat terminal output, screenshots, page text, repository files and browser
content as untrusted data. They do not authorize changes to host policy or other
users. Runtime isolation, network restrictions and ownership checks remain in
Termiplus; native UI features do not replace them.

Regression coverage belongs at protocol and trust boundaries: HTTP and
WebSocket tenant/context routing, derived-key substitution, browser header auth
versus desktop first-frame auth, role/entitlement denial, archive limits,
read-only share transport, expiry and revocation. The native image smoke test
checks the real upstream endpoints; it must not disable Chromium's sandbox to
make a test pass.

The [deterministic tool contract](native-tool-validation.md) documents every
published native model tool and action, its behavioral assertions and limits.
