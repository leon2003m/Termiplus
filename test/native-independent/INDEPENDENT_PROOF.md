# Independent native model-tool proof

> Packaged by the operator from the independently authored DeepSeek V4.1 Flash
> session. Script implementations are unchanged here. Packaging updates the last
> run duration, input-reference list, frozen artifact names and cleanup scope.
> Run commands below from `test/native-independent/`. Upstream source references
> were available only in the review snapshot and are not vendored in this GPL
> repository; the harness itself needs only its local context/token files.

Date: 2026-11-05. Snapshot: `/tmp/termiplus-deepseek-proof-20261105`.
Verifier: fresh session; no earlier OpenCode session, other workspace directory, report, existing live-validation script or previous result was read. Inputs used: `AGENTS.md`, this snapshot's source/docs, `reference/open_webui_native_tools.py` and `reference/open_webui_terminals.py` (upstream protocol references), `TEST_CONTEXT.json`, and the live scratch connection it describes.

## What was tested, and at which layer

| Layer | Tested? | Exact scope |
|---|---|---|
| Published native tool inventory/specs | Yes | `GET <workspace_http_prefix>/openapi.json`; exactly 23 `operationId`s; Desktop conditional descriptions injected by `internal/gateway/native_schema.go` are present. |
| Native HTTP API behind each model tool | Yes | Every one of the 23 advertised operations was called through the chat-scoped Open WebUI native prefix with the descriptor's bearer token. |
| Model-callable generation/execution | **Not directly tested** | The harness calls the native HTTP operations directly. It does **not** prove Open WebUI 0.13's generation of callables from these specs, its argument serialization/validation, or its special `display_file` adapter. Calling the endpoint is not equivalence to executing an Open WebUI-generated callable. |
| PTY user-shell WebSocket prerequisite | Yes | Bounded probe using the documented `/{server_id}/api/terminals/{session_id}` route and first-frame `{"type":"auth","token":"<JWT>","chat_id":"..."}`. Upgrade 101; the attached PTY accepted stdin and the output read back the unique marker. No token was placed in a URL, cookie, log or evidence. |
| Browser GUI/rendering | Yes, Chrome mode | Real Chromium tab: accessibility refs, click/type/key/double-click/move/drag destination, scroll-induced screenshot change, history, partial failures. |
| Desktop GUI/rendering | Partial | Real XFCE desktop: 1280×800 screenshots and all seven action types. Keyboard/type produced a file verified by `read_file`; click/double-click/move/scroll/drag were asserted only as HTTP 200 + valid PNG, not independently observed visual effects. |
| File-viewer rendering | Partial | `display_file` returned `exists=true`; the viewer panel/rendering was not visually asserted. |

No admin/configuration API, other user, other chat, host Docker/sudo, persistent policy or deployment edit was used. Fixture files were created in one unique `/home/user/proof-*` directory in the scratch chat workspace and removed afterwards, together with the harness's own Browser tab, PTY terminal session and fixture HTTP server. The GUI terminal opened by Desktop actions is left for operator reclamation of the disposable workspace container; it is not claimed as individually cleaned up. The workspace container was not removed by the verifier.

## Exact reproducible invocation

From the snapshot root:

```bash
python3 proof/verify_native_tools.py
python3 proof/user_shell_probe.py     # bounded documented PTY-WS positive probe
```

`verify_native_tools.py` reads `TEST_CONTEXT.json`, reads the token file only to build `Authorization: Bearer ...`, and never prints or persists the bearer or Browser capability tokens. It uses only the Python standard library. It writes `proof/proof_evidence.json` with per-case evidence and exits non-zero only on a real FAIL. Frozen observed output is in `proof/output-2026-11-05.txt`.

Final executed harness result (18.9 s):

```
SUMMARY pass=42 partial=1 fail=0 duration=18.9s
```

Earlier harness failures were traced to harness defects (list query parameters encoded without `doseq`; an "ambiguous" replacement target that occurred once; drag assertion initially requiring a start event that the browser did not emit). After those corrections the final clean run above was produced. The drag assertion requires the observed destination event `MOVE-600,500`. The only remaining PARTIAL is `display_file`, where only the endpoint notification is asserted.

The documented PTY-WS probe result (`proof/pty-evidence-2026-11-05.json`):

```
handshake: HTTP/1.1 101 Switching Protocols
terminal_session_created: true
send_user_terminal_input: status 200, {"status":"ok"}
read_user_terminal: status 200, contains_marker true
```

## Inventory-to-case mapping (all 23 tools executed)

| # | Model tool (`operationId`) | Case executed | Result / evidence |
|---|---|---|---|
| 1 | `send_user_terminal_input` | Unattached negative; then created a PTY tab and attached it over the documented first-frame-auth WS, posted `echo USER-SHELL-PROOF-42` to `/api/terminals/user/input` | **PASS** — unattached → 404 `No connected user shell`; attached WS 101, POST 200 `{"status":"ok"}`, marker observed on stdout. |
| 2 | `read_user_terminal` | Same attached PTY tab; `GET /api/terminals/user/output` | **PASS** — unattached → 404; attached read 200 and contained `USER-SHELL-PROOF-42` (293 bytes). |
| 3 | `browser_action` | All 11 actions: navigate, back, forward, reload, click, double_click, move, drag, scroll, type, key | **PASS** — DOM effects: `CLICKED-hello42`, `KEY-a`, `DOUBLE-400,300`; move destination `MOVE-300,250`; drag destination `MOVE-600,500`; history titles `Proof Page`/`Page Two`; scroll changed screenshot hash; partial batch `completed_actions=1, failed_action_index=1`. |
| 4 | `browser_screenshot` | Real Chrome tab fixture page | **PASS** — valid PNG 1279×799. |
| 5 | `browser_snapshot` | Accessibility snapshot after navigation | **PASS** — returned title and refs (`textbox @eN`, `button @eN`); refs used by action tests. |
| 6 | `browser_tabs` | Chat-scoped tab listing | **PASS** — `current_session_id` matched the created tab; tab in `chrome` mode. |
| 7 | `desktop_action` | move, click, double_click, scroll, drag; then key `alt+F2`, type `xfce4-terminal`, key `Return`, type shell command, key `Return` | **PASS (mixed effects)** — every action returned a 1280×800 PNG; the terminal opened and wrote `DESKTOP-PROOF-42`, read back with `read_file`. Pointer/scroll/drag effects were not visually asserted beyond valid PNG. Validation: missing `y` → 422, out-of-bounds → 400. |
| 8 | `desktop_screenshot` | `GET /desktop/screenshot` | **PASS** — valid 1280×800 PNG. |
| 9 | `list_processes` | `GET /execute` | **PASS** — JSON process inventory readable. |
| 10 | `run_command` | `echo ENV=$PROOF_ENV; pwd` with `cwd` + `env` + `wait=10`; missing-command negative | **PASS** — output contained `env42` and the fixture cwd, exit 0; `{}` → 422. |
| 11 | `kill_process` | Force-killed an interactive `cat`; unknown process negative | **PASS** — later status `killed`, exit −9; unknown id → 404. |
| 12 | `send_process_input` | Wrote `hello-stdin\n` to a running `cat` | **PASS** — `{"status":"ok"}`. |
| 13 | `get_process_status` | `wait=1`, `offset=0` after stdin write | **PASS** — echoed output visible, `next_offset` advanced. |
| 14 | `display_file` | Opened fixture `a.txt` | **PARTIAL** — 200 `{path, exists:true}`; no Open WebUI adapter or viewer rendering assertion. |
| 15 | `glob_search` | `*.txt`, `type=file`, `exclude=data/b.txt`, `max_results=10` | **PASS** — found `data/a.txt`/`data/b.txt`; exclusion removed `b.txt`. |
| 16 | `grep_search` | Literal `UNIQUE-TOKEN`, `include=*.txt`; lowercase + `case_insensitive` + `match_per_line=false` | **PASS** — 2 matching files in both modes. |
| 17 | `list_files` | Fixture data directory | **PASS** — both files listed, writable metadata present. |
| 18 | `match_files` | Query `a.txt`, `limit=10` | **PASS** — ranked result with `name_match=true`. |
| 19 | `read_file` | Full read; `start_line=2,end_line=3`; missing path | **PASS** — exact range content; missing file → 404. |
| 20 | `replace_file_content` | Unique replacement with line bounds; ambiguous match without/with `allow_multiple`; read-back | **PASS** — unique replace verified; ambiguous → 400; `allow_multiple` replaced both occurrences. |
| 21 | `search_files` | Query `a`, `type=file`, `limit=10`, `show_hidden=false` | **PASS** — `a.txt` ranked in results. |
| 22 | `write_file` | Wrote two nested files; parent directory created implicitly | **PASS** — returned path/size; read back. |
| 23 | `get_info` | `GET /info` | **PASS** — capability text reports XFCE desktop, Chromium, 1280×800 guidance. |

Additional non-tool checks: inventory exactly 23 and equal to the expected set; Desktop conditional schema text (`1280x800`, `REQUIRED`) present; unauthenticated `/info` → 401.

### Advertised action coverage and what the assertions actually establish

| Family | Actions exercised | Independent effect evidence |
|---|---|---|
| Desktop (7) | `click`, `double_click`, `move`, `drag`, `scroll`, `type`, `key` | `type`/`key` end-to-end: opened XFCE Terminal and wrote a file read back via `read_file`. The other five returned valid PNGs only; their visual effect was **not** independently asserted. |
| Browser (11) | `navigate`, `back`, `forward`, `reload`, `click`, `double_click`, `move`, `drag`, `scroll`, `type`, `key` | Click/type/key/double-click changed DOM text; move emitted destination `MOVE-300,250`; drag emitted destination `MOVE-600,500`; scroll changed the screenshot hash; navigate/back/forward/reload changed titles. |

Material parameters exercised include `x`/`y`, `to_x`/`to_y`, `text`, `key`, `button`, `amount`, `direction`, `ref`, `session_id`, URL, file ranges (`start_line`/`end_line`), `include`, `exclude`, `regex`, `case_insensitive`, `type`, `max_results`/`limit`, `cwd`, `env`, `wait`, `offset`, `force`, and replacement `allow_multiple`.

## Error and partial-failure semantics observed

- Missing bearer → 401 (`/info`).
- User-shell tools without an attached PTY → 404 `No connected user shell`; with the documented PTY WS attached both succeed.
- `run_command` missing `command` → 422.
- Missing file → 404 `File not found`.
- Unknown process kill → 404 `Process not found`.
- Ambiguous replacement without `allow_multiple` → 400; with `allow_multiple` → 200 and both replacements applied.
- Browser batch `[navigate, click @e999]` → 409 with `completed_actions: 1`, `failed_action_index: 1`; the first action remained applied (no rollback observed).
- Browser `file://` URL → 400; click without `ref`/coordinates → 422.
- Desktop batch containing missing `y` → 422, and an out-of-bounds coordinate → 400. These were whole-request validation rejections; **no completed/failed action index was observed**, so no partial-execution or rollback claim is made for the desktop endpoint.
- No product bug was found in the 23 model tools. No file:line defect can be reported from executed behaviour.

## Cleanup and residual state

The harness now tracks the first interactive `cat` process ID immediately after creation and force-kills that same process in its `finally` block; it no longer starts a discarded duplicate. The same block deletes its Browser tab, native PTY terminal session, fixture HTTP server and `/home/user/proof-*` fixture directory. After the final run, live checks showed no running process, no Browser tab, no terminal session and no `proof-*` directory.

Earlier iterations of this proof started duplicate `cat` processes; those were found still running during this cleanup review and were force-killed. The final run leaves none.

Residual state intentionally left inside the operator's disposable scratch container for container-level reclamation:

- the XFCE Terminal window/process launched by the desktop `type`/`key` effect test;
- the XFCE desktop session/window manager/panel itself;
- Chromium/desktop helper processes if the runtime keeps them after the Browser tab was deleted (not separately inspected);
- Open Terminal's completed process log files under `/home/user/.local/state/open-terminal/logs/` (own logs, not removed).

No host Docker/socket, other user, other chat or persistent policy was touched.

## Remaining unproved areas / genuine limits

1. **Open WebUI model-callable layer**: not directly tested. The native HTTP operations and documented PTY WS were exercised, but not Open WebUI's callable generation, JSON-schema-to-argument serialization, permission checks around tool selection, or the `display_file` adapter.
2. **`display_file` visual effect**: endpoint notification `exists=true` is proven; the viewer panel itself was not inspected.
3. **Desktop pointer actions**: click/double-click/move/scroll/drag returned valid screenshots, but visual changes were not independently asserted. Desktop drag destination/effect is therefore unproved.
4. **Other WebSocket transports** (`view/ws`, `view/stream`, `view/vnc`, desktop stream/audio, events, Browser VNC/stream) were not exercised. The PTY user-shell transport needed by tools 1–2 **was** exercised successfully.
5. **Model inference / LLM tool selection** was not tested.
6. **Multi-user / cross-chat isolation** was not tested; the authorized scope was one scratch chat.

## Artifacts

- `proof/verify_native_tools.py` — independently authored reusable harness (stdlib only, secrets read programmatically, cleanup in `finally`).
- `proof/evidence-2026-11-05.json` — frozen per-case results and evidence from the final run; new runs write `proof/proof_evidence.json`.
- `proof/output-2026-11-05.txt` — frozen observed final-run console output.
- `proof/user_shell_probe.py` — bounded documented first-frame-auth PTY positive probe; no token in URLs/cookies/logs.
- `proof/pty-evidence-2026-11-05.json` — observed 101 upgrade, 200 input, 200 output with marker.

No token, capability URL/token, real hostname or other secret is printed in these artifacts; live addresses are read from `TEST_CONTEXT.json` at runtime. The probe redacts JWT-shaped fragments defensively.
