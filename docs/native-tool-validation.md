# Deterministic native tool validation

Validated live on 2026-11-05 with Open WebUI 0.13.0, Open Terminal 0.16.0,
the deployed gateway and the desktop image with bounded pointer synchronization
and unknown-X11-key failure handling.
All **23 discovered native model tools**, all their published top-level
parameters and all **7 Desktop / 11 Browser actions** have positive coverage.
Two consecutive full runs passed **39 cases** each. Following the external review,
the final run passes **40 cases**, adding a check of the live published Desktop
coordinate guidance; all three runs include 51 intentionally rejected calls.

The inventory comes from Open WebUI's actual `get_terminal_tools`, after its
OpenAPI conversion and `display_file` adapter. Tests invoke those returned
callables directly with fixed inputs, without model inference. Requests use the
same configured gateway connection and authenticated user/chat headers as chat
tool execution. HTTP success alone is insufficient: assertions check file bytes,
process output/status, decoded PNGs, accessibility snapshots and GUI events.

## Extracted tools and checks

| Native tool | Deterministic checks |
| --- | --- |
| `get_info` | Nonempty operator environment information |
| `write_file` | Parent creation, exact UTF-8 byte count, overwrite |
| `read_file` | Exact UTF-8 contents, inclusive line range, image decoding; missing file, invalid range and unsupported binary |
| `list_files` | Expected file/directory entries; missing directory |
| `replace_file_content` | Sequential and line-scoped replacements; ambiguous match leaves the file unchanged; missing file |
| `grep_search` | Literal/regex, case folding, include filter, file-only results and result limit; malformed regex and missing path |
| `match_files` | Ranked content match, hidden-file option and pagination parameters; blank query and missing directory |
| `search_files` | Filename, hidden/default behavior, file/directory filters and limit; invalid type |
| `glob_search` | Glob, exclusion, type and limit; missing directory |
| `display_file` | Existing/missing viewer metadata, including WebUI's `page`/`inline` adapter arguments |
| `run_command` | CWD/environment, stdout/stderr, async start, wait/tail and nonzero exit; invalid wait |
| `list_processes` | Started process appears in this chat's list |
| `get_process_status` | Completion, output, cursor and tail; missing process |
| `send_process_input` | Real stdin round trip; exited and missing process |
| `kill_process` | SIGTERM and SIGKILL followed by observed killed status; missing process |
| `read_user_terminal` | Actual attached UI PTY output; no connected shell |
| `send_user_terminal_input` | Input produces observed shell output; no connected shell |
| `desktop_screenshot` | Decodable nonuniform 1280×800 PNG from the shared desktop |
| `desktop_action` | Every action; left/middle/right click, all scroll directions, Unicode paste, keys, repeated unchanged-position clicks and batches; invalid fields, bounds, whole-batch preflight, unknown X11 keys and partial-batch progress |
| `browser_tabs` | Empty initial state, selected tab and two-tab state |
| `browser_snapshot` | Expected accessibility text and real refs, default/explicit tab; missing UI tab and unknown session |
| `browser_screenshot` | Decodable nonuniform PNG; missing UI tab and unknown session |
| `browser_action` | Every action, navigation history/reload, coordinates, refs and ref-based replacement, explicit tab, Unicode, buttons, scroll directions and repeated clicks; invalid URLs/keys/refs/bounds/fields, preflight and partial-batch progress |

The suite also checks operation and nested coordinate-field descriptions in the
actual Open WebUI tool specs. Removing the gateway annotation or upstream schema
drift that loses its guidance must fail this check.

Desktop actions: `click`, `double_click`, `move`, `drag`, `scroll`, `type`, `key`.
Browser actions add `navigate`, `back`, `forward`, `reload`.

A local HTTP fixture records DOM input, mouse and scroll events independently
of the action response. The shared Desktop opens that page in a dedicated
sandboxed Chromium profile in kiosk mode; Browser opens it in native Chrome mode.
One browser is closed before the next starts, preserving the existing PID limit.
Scroll assertions observe actual scroll-position changes. Drag assertions observe
the mouse release at its requested destination. An unknown Desktop key or a deliberately stale Browser ref
after a successful click verifies `completed_actions=1`, `failed_action_index=1`
and that the first action actually happened.

## Reproduce

The runner is `.github/scripts/native-tool-contract.py`; its local page/server is
`.github/scripts/native-tool-fixture.py`. Both run inside the existing Open WebUI
backend environment. Use an existing authorized admin bearer token from a secret
source; no database user, permanent key or authentication setting is created.
The script loads the backend's existing secret file only when needed to import
Open WebUI's normal configuration. It does not mint tokens.

Copy the two scripts into the backend container, for example:

```sh
docker cp .github/scripts/native-tool-contract.py open-webui:/tmp/native-tool-contract.py
docker cp .github/scripts/native-tool-fixture.py open-webui:/tmp/native-tool-fixture.py
```

Supply the bearer token on stdin, with `TOKEN_FILE` referring to an existing
protected token file, and select the disposable scratch connection:

```sh
docker exec -i -e PYTHONPATH=/app/backend open-webui \
  python /tmp/native-tool-contract.py \
  --terminal-id termiplus-scratch --token-stdin \
  --output /tmp/native-tool-contract-report.json < "$TOKEN_FILE"
docker cp open-webui:/tmp/native-tool-contract-report.json /tmp/native-tool-contract-report.json
```

Alternatively supply `OWUI_TEST_TOKEN` inside the backend process. Do not put the
token in command-line arguments. This test run did not create or retain an
admin-key file.

The runner refuses connection URLs outside `/p/scratch`. It creates a new saved
test chat and therefore a separate scratch workspace, not an existing personal
desktop. Each run owns a random `/tmp/tp-native-contract-*` directory, its
processes, browser tabs/profile and PTY. Cleanup runs even after assertion
failures and closes those resources; the empty saved test chat remains as a
trace. The gateway reclaims the disposable workspace through its normal idle
lifecycle. Do not run multiple instances against the same scratch context.

JSON output contains per-case results, durations, a schema fingerprint and
per-tool positive/negative counts and covered parameter names. Exit status is
nonzero for any failed assertion, uncovered tool/action/parameter or changed
tool inventory. Future upstream tools cannot silently fall out of coverage.
Fixture polling and cleanup also make real tool calls; call totals are not a
count of distinct behavioral assertions.

## Scope and observed upstream behavior

This verifies callable generation, HTTP serialization, live gateway routing and
native execution. It does not prove that a particular model will select the
right tool, infer pixel coordinates, or complete arbitrary tasks. Open WebUI's
rendering of the `display_file` viewer and model-result/image persistence are
outside this callable suite; its viewer metadata and image transport are tested.
Existing gateway tests separately cover authentication and tenant isolation.

Browser and user-shell tools require their corresponding native UI session.
Their initial HTTP 404 responses are intentional; the suite then opens/attaches
a real UI session and verifies success. Notebook, Git, upload and lifecycle UI
endpoints are not among these 23 model tools. Existing native image smoke tests
cover notebooks and other image capabilities separately.

Optional numeric query parameters currently appear as strings after WebUI's
OpenAPI conversion; the tests use that actual published contract. Likewise,
`grep_search.include` and `glob_search.exclude` expose one string pattern rather
than an array. `display_file.page` is removed by the native WebUI adapter before
the terminal request, and its viewer presentation is handled by WebUI.

The desktop image translates xdotool's unknown-key warnings into a failed exit
status. Unknown but syntactically valid X11 key names therefore return HTTP 502
instead of a misleading successful screenshot. The suite checks this failure,
partial-batch progress after a preceding click, and recovery with a valid key.
Browser rejects unknown key names with HTTP 400.

## External review and independent proof

OpenCode ran the first source review alone with the available DeepSeek V4.1
Flash model. Its [five findings and fixes](reviews/2026-11-05-deepseek-native-review.md)
include saved-profile recovery and missing inherited-shortcut overrides.

A fresh session then authored and executed its own harness using source,
upstream protocol references and a normal-user disposable scratch chat. It
received neither this suite nor its results or the first review. Its final
[independent report and scripts](../test/native-independent/INDEPENDENT_PROOF.md)
record **42 passed, 1 partial, 0 failed**. All 23 native HTTP operations were
called, including positive user-shell input/output through an attached PTY.
Browser actions observed DOM effects; Desktop key/type created a file read
back independently. The partial case is file-viewer rendering. Desktop pointer
effects and Open WebUI callable generation were not independently established
by that second harness; this callable suite covers those separate obligations.

The subsequent [frontend acceptance](reviews/2026-11-05-frontend-acceptance.md)
separately verifies rendered text/image/PDF views, model-to-viewer delivery and
download, the shared Desktop and Chrome browser through the real Open WebUI UI.
It does not change the scope or partial result of the independent harness.
