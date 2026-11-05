# termiplus system map

A walkable map of this repo for agents who will change it. The source tree is
the truth; every card cites `path:line` and stops there. Edit this file only:
`AGENTS.md` and `routing.md` next to it are generated twins (`make map`), and
`make lint` fails if they drift.

Baseline verified against commit `a35e601` on 2026-10-19; native integration
updates reflect the working tree on 2026-11-05. Individual cards retain their
verification dates. Lines may move; cited symbols are the stable anchor.

## Where to go

| Question | Open |
|---|---|
| How do I walk this map, what are live / leftover / ghost? | [CONTEXT.md](CONTEXT.md) |
| What is X (a noun)? | [objects/_index.md](objects/_index.md), then one card |
| How does X move (request, create, tool call, stream, departure, release)? | [processes/](processes/) |
| I am changing X, what else moves? | [effects/CONTEXT.md](effects/CONTEXT.md) |
| Which node types and fields are allowed? | [_meta/schema.md](_meta/schema.md) |
| Adding a card | copy [_templates/object.md](_templates/object.md) or [_templates/process.md](_templates/process.md) |
| Rules for commits, security boundaries, releases | [../AGENTS.md](../AGENTS.md) (not this map) |

## The system in five lines

1. **gateway** (`cmd/gateway`, `internal/gateway`) is what Open WebUI talks to as a terminal: `/p/<policy>/...` with the gateway API key. It starts one container per user and policy (or per chat) and proxies to it.
2. **sockproxy** (`cmd/sockproxy`, `internal/sockproxy`) is the only holder of the Docker socket. It allows a handful of endpoints and rebuilds every create request. The gateway never reaches Docker directly.
3. **mcp** (`cmd/mcp`, `internal/mcp`) is the alpha control plane: limited MCP lifecycle/storage tools for chat, a human management console and expiring image-only screen shares. It calls the gateway's `/control/v1/` API; it never touches Docker.
4. **netpolicy** (`internal/netpolicy`) plus `deploy/firewall.sh` and `deploy/dns-forwarder` decide what a terminal may reach. Public egress is allowed, internal is denied except admin-set entries.
5. **deploy/** is compose files (base plus overlays), host firewall and systemd units. **image/** is the terminal and desktop images.

## Names that collide (read before any card)

| Word you see | What it is in code | Card |
|---|---|---|
| **workspace** (MCP tools, plugin, console) | a gateway **Policy** name from `policies.json` | [Policy](objects/workspaces/policy.md) |
| **session** (MCP, `session_id`) | a terminal **container** name, `ot-u-…` (persistent) or `ot-e-…` (ephemeral) | [Terminal container](objects/workspaces/terminal-container.md) |
| **session** (`X-Session-Id` from Open WebUI) | the **chat** id; picks the ephemeral container, ignored for persistent | [Terminal container](objects/workspaces/terminal-container.md) |
| **home** | a Docker volume `ot-home-…` mounted at `/home/user`; one per user and `Policy.home` group | [Home volume](objects/workspaces/home-volume.md) |
| **archive helper** | a never-started container `ot-a-…` that mounts one home so the gateway can tar it | [Archive helper](objects/workspaces/archive-helper.md) |
| **archive** (departed user) | a stored tar of a home under `BACKUP_DIR/archived/`, not the helper | [Backup](objects/storage/backup.md) |
| **user id** | the **Open WebUI user id** (`X-User-Id`), the key for every name hash | [Principal](objects/identity/principal.md) |
| **subject** | the **OIDC subject** from Keycloak, mapped once to an Open WebUI id | [IdentityStore](objects/identity/identity-store.md) |
| **persist**, `termiplus-persist` | an MCP role (Keycloak group) needed to *request* a persistent session; which workspaces a user sees is Entitlements | [Principal](objects/identity/principal.md) |
| **policy** (sandbox network) | `netpolicy.Policy`: DNS entries and egress allows, not a terminal Policy | [Network policy](objects/sandbox/network-policy.md) |
| **key** | six different secrets (gateway, control, admin, proxy token, key secret, plugin key); never interchangeable | [Service keys](objects/identity/service-keys.md) |
| **share** | an expiring view-only link to someone's desktop, not a file share | [Share](objects/control-plane/share.md) |

## Native workspace integration (2026-11-04)

Open WebUI 0.13 + Open Terminal 0.16 own interactive Desktop, Browser, Git,
files, terminals, events and app previews. Termiplus owns container isolation,
user/policy/chat routing, homes, backups, entitlements and human administration.
See [native Desktop](processes/desktop-stream.md),
[native app preview](processes/port-forward.md),
[model tool boundary](objects/control-plane/mcp-tools.md) and
[migration](../docs/native-integration.md). No AI administration or snapshot
pruning is exposed. Shared viewing is image-only at five-second intervals.
The native image smoke test runs under Sysbox, including Chromium with its
sandbox enabled. The desktop CI job installs a checksum-pinned Sysbox package;
ordinary Docker is insufficient for that browser sandbox test on the live host.

The shipped workspace choices are Desktop (`default`) and Temporary desktop
(`scratch`), both Sysbox with native Desktop/Browser enabled. The complete
`image/desktop` includes Dolphin, Chromium, Kate, dark theme and wallpaper;
`image/` remains its build dependency. See the [image comparison](../docs/open-terminal-image-review.md).
It also supplies standard workspace CLI/document tools, scientific Python and
Jupyter, plus a private Sysbox Docker daemon with Compose/Buildx. The API venv
stays separate from user Python; native model context announces the shared graphical
desktop and explains desktop input. `image/desktop/defaults.py` migrates obsolete
app launchers and removes session-ending UI controls without resetting the layout.
It explicitly suppresses inherited logout/lock/mail shortcuts in partial saved
profiles, and preserves malformed INI/XML files as `.invalid-*` backups before
seeding usable defaults.

`image/desktop/files/usr/local/bin/xdotool` bounds native pointer synchronization
so repeated clicks at the current position do not hang, and turns libxdo's
unknown-key warnings into failed native actions with partial-batch progress.
Native schema guidance
uses the Linux display's 1280×800 pixel size; smoke tests verify that contract.

`.github/scripts/native-tool-contract.py` resolves Open WebUI's actual terminal
callables and deterministically tests every published model tool, parameter and
Desktop/Browser action against a disposable scratch chat. Its independent DOM
oracle is `.github/scripts/native-tool-fixture.py`; results and scope are in
[tool validation](../docs/native-tool-validation.md). Inventory drift fails coverage;
live callable descriptions must retain the gateway's pixel-coordinate guidance.
`test/native-independent/` preserves a fresh DeepSeek session's separately
authored and executed native HTTP/PTY proof, with its scope and remaining visual
limits distinguished from the actual Open WebUI callable suite.

[Supported tools and adaptations](../docs/tool-support.md) is the explicit
23-native/8-plugin inventory and integration boundary: no core Open WebUI fork,
but intentional OpenAPI description rewriting and image command/default wrappers.
The [frontend acceptance](../docs/reviews/2026-11-05-frontend-acceptance.md)
records real viewer rendering, model-to-viewer delivery, download and native
Desktop/Chrome UI checks in a disposable normal-user chat.
