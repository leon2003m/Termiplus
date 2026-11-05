# Supported tools and integration changes

Verified baseline: **Open WebUI 0.13.0**, **Open Terminal 0.16.0**, Termiplus
Workspaces plugin **0.6.0**, on 2026-11-05. This is the supported and tested
inventory, not a promise about every later upstream version.

## What “drop-in” means here

Termiplus is a **replacement terminal-server connection behind the normal
Open WebUI interface**. Configure the gateway URL and connection key in Open
WebUI, then select that connection in the chat. Open WebUI discovers the native
Open Terminal tools and supplies its own Desktop, Browser, Files, Git and
terminal UI. The repository does not build a fork of Open WebUI or patch its
core Python/frontend code. The image installs the released `open-terminal`
package; it does not patch that package's Python source.

There are nevertheless deliberate adaptations. The gateway rewrites routing
and authentication, **edits descriptions in `/openapi.json`**, and the custom
workspace image changes desktop defaults and selected command behavior. Thus
“drop-in terminal-server integration” describes the connection interface;
“an entirely transparent proxy” would be inaccurate.

The optional Workspaces plugin is our Python code installed through Open
WebUI's regular Workspace → Tools extension/API. It runs inside Open WebUI
through that supported extension mechanism. It adds lifecycle/storage tools,
not another Desktop/Browser implementation. Native workspace use does not
require selecting that plugin on a model.

## Native model tools: 23

These names belong to Open Terminal. Open WebUI generates model callables from
the connection's published OpenAPI specification, with its own `display_file`
adapter. Paths below are native paths behind the gateway policy prefix, not
public unauthenticated endpoints. For example, `/p/scratch/execute` routes to
`/execute` in the authenticated user's saved-chat workspace.

| Tool name | Native operation | Purpose / prerequisite |
| --- | --- | --- |
| `get_info` | `GET /info` | Workspace capabilities and environment |
| `run_command` | `POST /execute` | Command, cwd, environment, wait/output options |
| `list_processes` | `GET /execute` | Native command process list |
| `get_process_status` | `GET /execute/{process_id}/status` | State, exit code and output cursor/tail |
| `send_process_input` | `POST /execute/{process_id}/input` | Standard input of a running native command |
| `kill_process` | `DELETE /execute/{process_id}` | Stop a native command, optionally force |
| `write_file` | `POST /files/write` | Create/overwrite text and parent directories |
| `read_file` | `GET /files/read` | File contents, line ranges and supported image results |
| `list_files` | `GET /files/list` | Directory entries |
| `replace_file_content` | `POST /files/replace` | Targeted replacements, optional line bounds/multiple matches |
| `glob_search` | `GET /files/glob` | Filename globs, exclusions and type/limit filters |
| `grep_search` | `GET /files/grep` | Content search, regex/literal/case options and include filters |
| `match_files` | `GET /files/matches` | Ranked file matching and pagination |
| `search_files` | `GET /files/search` | Ranked filename search and file/directory filters |
| `display_file` | `GET /files/display` | File-viewer metadata; Open WebUI also adapts `page`/`inline` |
| `send_user_terminal_input` | `POST /api/terminals/user/input` | Input to an attached native UI shell, distinct from a command process |
| `read_user_terminal` | `GET /api/terminals/user/output` | Read that attached shell's output |
| `desktop_screenshot` | `GET /desktop/screenshot` | Shared XFCE desktop PNG; Desktop capability enabled |
| `desktop_action` | `POST /desktop/action` | Shared desktop mouse/keyboard actions |
| `browser_tabs` | `GET /browser/tabs` | Native Browser tabs in the selected chat |
| `browser_snapshot` | `GET /browser/snapshot` | Accessibility tree and element refs; Chrome-mode Browser tab required |
| `browser_screenshot` | `GET /browser/screenshot` | Browser PNG; Chrome-mode Browser tab required |
| `browser_action` | `POST /browser/action` | Browser automation; Chrome-mode Browser tab required |

`desktop_action` supports **7** actions: `click`, `double_click`, `move`, `drag`,
`scroll`, `type`, `key`.

`browser_action` supports **11** actions: those seven plus `navigate`, `back`,
`forward`, `reload`.

Desktop coordinates are raw **1280×800 pixels** for this Linux baseline.
Click/double-click/move require `x` and `y`; drag additionally requires `to_x`
and `to_y`. Type uses `text`, key uses `key`. Browser may use native element
refs where advertised; its viewport coordinates are not a second coordinate
system for the shared XFCE desktop.

Keep one connection selected in one saved chat. Browser automation requires a
created native Browser tab in Chrome mode. User-shell tools require an attached
PTY WebSocket; creating a terminal record alone is insufficient. Their 404
without an attached shell, and Browser's prerequisite errors without a suitable
tab, are expected. Both positive and prerequisite/error paths are tested.

Git, notebooks, events, app previews, Docker, LibreOffice and other installed
programs are available through native UI/API features or `run_command`; they
are **not additional tool names** in this 23-tool inventory. See the
[behavioral contract and parameter coverage](native-tool-validation.md).

## Optional Termiplus Workspaces plugin: 8

These are our own model-visible extension methods. Seven call the same-named
MCP tools; `open_console` only returns a link and has no MCP counterpart.
They supplement the selected native connection and do not attach or switch it.

| Plugin method | Supported operation / human boundary |
| --- | --- |
| `list_workspaces` | List workspace profiles available to the caller |
| `list_sessions` | List the caller's entitled sessions |
| `request_session` | Provision a workspace; persistent provisioning requires the persist role and confirmation |
| `destroy_session` | Remove the caller's container with confirmation; keep a persistent home |
| `reset_workspace` | Recreate a persistent container while retaining `/home/user`; remove a temporary sandbox; confirmation required |
| `manage_snapshots` | `list`, `create` only; no implicit pruning, restore or delete |
| `share_desktop` | `create`, `list`, `revoke`; expiring image-only view links, creation requires confirmation |
| `open_console` | Return the human console link; execute no administration |

MCP discovery and direct calls enforce the seven-tool allowlist even for admin
accounts. Network policy, entitlements, user/group administration, audit queries,
departed-user actions and snapshot restore/delete/pruning are outside the model
surface. Human console/provider administration has its own authentication and
permission checks. See [MCP contract](mcp.md) and
[plugin setup](../integrations/openwebui/README.md).

## Adaptations we maintain

| Area | Our change | Source / scope |
| --- | --- | --- |
| Connection routing and isolation | Select user/policy/chat container, create/start on demand; substitute a derived container key, strip cookies and adapt native WebSocket authentication. Discovery is shared only across equivalent runtime/image/egress/feature policies. | [gateway](../internal/gateway/gateway.go), [WebSocket relay](../internal/gateway/ws.go), lifecycle and socket-proxy boundaries |
| OpenAPI response descriptions | Add raw-pixel dimensions, required `x/y`, drag destinations and examples to Desktop schema fields and operations. The gateway requests an uncompressed discovery response; body length/cache validators are adjusted after rewriting. Endpoints, field types, required lists and native validation remain upstream. | [native_schema.go](../internal/gateway/native_schema.go), invoked by the gateway for `GET /openapi.json` |
| Native model context | Add desktop/app availability and coordinate instructions through upstream `OPEN_TERMINAL_INFO` and execution-description settings. Model vision/settings are configured through ordinary Open WebUI settings. | [base image](../image/Dockerfile), [desktop image](../image/desktop/Dockerfile), [startup](../image/start) |
| Desktop input command behavior | Bound selected `xdotool mousemove --sync` waits by checking the actual pointer position. Convert unknown-key warnings into nonzero exit status, producing upstream HTTP 502/partial-action reporting. Other command forms pass through. | [xdotool wrapper](../image/desktop/files/usr/local/bin/xdotool); changes a command used by upstream, not its Python handler |
| Desktop defaults and applications | Chromium only, Dolphin, Kate, LibreOffice, XFCE Terminal, btop; wallpaper, Qt/GTK theme and dock; hide logout/switch-user/lock and unsupported menu entries. Retain valid personal settings, override inherited session/mail shortcuts, preserve malformed settings as `.invalid-*` before recovery. | [defaults.py](../image/desktop/defaults.py), [theme.py](../image/desktop/theme.py), [desktop startup](../image/desktop/files/usr/local/bin/startxfce4), [launcher compatibility](../image/desktop/files/usr/local/bin/thunar) |
| Chromium startup recovery | Release stale singleton links only for native-managed profiles whose lease can be acquired. | [prepare.py](../image/desktop/prepare.py) |
| Workspace runtime and packages | Sysbox desktop policies, private inner Docker with Compose/Buildx, developer/document tools, scientific Python and notebooks in a user venv separate from the API venv; Tini process reaping, PTY controlling-terminal and resolver startup helpers. | [image comparison](open-terminal-image-review.md), [base image](../image/Dockerfile), [desktop image](../image/desktop/Dockerfile), [bash helper](../image/bash-ctty) |
| Lifecycle, storage and sharing | Homes, snapshots/archives, entitlements and human administration; optional Workspaces/MCP extension and expiring image-only shares. | [MCP](mcp.md), [plugin](../integrations/openwebui/termiplus_workspaces.py) |

KasmVNC, our separate interactive desktop player, custom port-link tool
`open_port` and its forwarding service were removed. Human/AI interaction uses
the same native Desktop/Browser connection. View-only Termiplus shares remain
an additional service, not a replacement for that native UI.

## Verification and upgrade boundary

The [actual Open WebUI callable suite](native-tool-validation.md) passed all
23 tools, every published top-level parameter and all 7/11 action variants:
40 cases, including 51 expected rejection calls. It also checks the rewritten
coordinate descriptions. A fresh DeepSeek session's
[independent HTTP/PTY proof](../test/native-independent/INDEPENDENT_PROOF.md)
passed 42 checks with one partial visual-viewer case and no failures.

Neither result proves arbitrary model task completion. The second harness does
not independently establish Desktop pointer effects or Open WebUI callable
conversion; the actual-callable suite covers those separate obligations.
Visual file-viewer rendering remains unproved by these suites.
The separate [frontend acceptance](reviews/2026-11-05-frontend-acceptance.md)
verified rendered text/image/PDF views, real model-to-viewer delivery, PDF
download, the shared Desktop and the native Chrome browser through the live UI.

On an Open WebUI/Open Terminal upgrade, rerun the contract and Sysbox image
checks before claiming support for that version. New/renamed tools or actions
must fail inventory coverage until reviewed. The schema annotation and narrow
command wrappers are explicit compatibility maintenance points even though no
upstream core source fork is required.
