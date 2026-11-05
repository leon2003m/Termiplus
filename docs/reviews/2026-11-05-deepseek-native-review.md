# DeepSeek native integration review — 2026-11-05

OpenCode 2.2.5 ran this review with **DeepSeek V4.1 Flash** through the
`opencode/deepseek-v4.1-flash-free` endpoint. The catalog did not offer the
requested exact V4 Flash name; the Go endpoint's weekly quota was exhausted.
The reviewer received a source snapshot and working-tree patch. It ran first,
without another external reviewer, and could not access Docker or production.

The original pre-fix review is retained below; its line numbers refer to that
snapshot. These are the dispositions in the final working tree:

| Finding | Resolution | Verification |
| --- | --- | --- |
| Removed `/p/desk` policy in setup docs | Use `/p/default` and `/p/scratch` | Instructions checked against shipped policies |
| Firefox/uBlock image claim | Document Chromium only | Image smoke asserts Chromium present and Firefox absent |
| Missing saved shortcut entries inherit logout/lock/mail | Add explicit empty overrides in both default and custom groups | Partial saved profile, retained unrelated shortcut, repeat migration |
| Malformed saved settings abort startup | Preserve original bytes in unique `.invalid-*` files; seed valid defaults | Invalid INI and both XML files, valid replacements, repeat migration and single backup |
| Synthetic-only coordinate guidance coverage | Assert operation and nested field descriptions in actual published native tool specs | Live callable suite: 40 cases pass, all 23 tools covered |

The final image smoke test and `make check` pass. The [callable validation
report](../native-tool-validation.md) explains what those tests establish.
A separate fresh DeepSeek session was then given only source/protocol references
and a normal-user disposable test chat, without the earlier test harness,
results or review. Its [separately authored proof](../../test/native-independent/INDEPENDENT_PROOF.md)
passes 42 checks with no failures; one partial case leaves file-viewer rendering
unproved. That harness tests native HTTP and the PTY prerequisite; it does not
claim to execute Open WebUI's generated model callables.
The subsequent [frontend acceptance](2026-11-05-frontend-acceptance.md) separately
verified viewer rendering and real model-to-viewer delivery through the live UI;
the original independent harness's partial result remains unchanged.

## Original pre-fix report

## Independent review: native desktop/browser integration

Reviewed the working-tree patch and the new untracked files: native Desktop/Browser
integration, `image/desktop` defaults and startup wrappers, the OpenAPI schema
rewrite, and the deterministic native-tool scripts. This was a bounded, local
review. No deployment or live service was used.

### Verdict

No security-boundary regression was identified in the changed code. The changes do
not alter `internal/sockproxy/`, `deploy/firewall.sh`, gateway authentication or
container hardening, and `go test ./...` passes.

Five actionable findings follow. Two are documentation defects, two are defects in
the XFCE defaults migration, and one is a test-coverage gap in the schema rewrite.

### Findings

#### 1. Medium: Native setup instructions point at the removed `/p/desk` policy

**Location:** `docs/deploy-alpha.md:109`, `docs/mcp.md:40`

**Trigger:** An operator follows the migration or "Working together in the native
Desktop" instructions and configures an Open WebUI connection to
`http://gateway:8080/p/desk`.

**Consequence:** The connection returns HTTP 404 `unknown policy`, so Open WebUI's
connection check and OpenAPI discovery fail; the native Desktop/Browser tabs and
tools are unavailable.

**Evidence:** `deploy/policies.json:1-22` defines only `default` and `scratch`.
`internal/gateway/gateway.go:299-302` returns 404 for any policy name not in
`cfg.Policies`. The same document at `docs/deploy-alpha.md:53-55` correctly says
the shipped choices are `default` and `scratch`, making line 109 internally
inconsistent. `README.md:155-156` already gives the working URLs: the root URL for
`default` and `/p/scratch` for the temporary policy.

**Suggested fix:** Use `http://gateway:8080` for `default` (Desktop) and
`http://gateway:8080/p/scratch` for the disposable policy in both documents.

#### 2. Low: Desktop image documentation still advertises Firefox ESR and uBlock Origin

**Location:** `docs/deploy-alpha.md:57-58`

**Trigger:** An operator reads the current desktop-image description or uses it to
decide what browser/privacy controls the image provides.

**Consequence:** The documentation promises software the image does not contain.
Users will look for Firefox or uBlock Origin and support/debugging will start from
a false assumption.

**Evidence:** `image/desktop/Dockerfile:11-12` installs Chromium and
`chromium-sandbox`, not Firefox. `image/desktop/files/etc/xdg/xfce4/helpers.rc:3`
sets `WebBrowser=chromium`, and the smoke test explicitly asserts that Firefox is
absent at `.github/scripts/native-smoke.py:55`. The same paragraph already names
Chromium later, so the Firefox sentence is a leftover from the previous image.

**Suggested fix:** Replace "Firefox ESR with uBlock Origin" with "Chromium" (and
keep the existing statement that Chromium's sandbox remains enabled).

#### 3. Medium: Shortcut migration does not suppress inherited logout/lock shortcuts on many saved homes

**Location:** `image/desktop/defaults.py:81-86`; test at
`.github/scripts/native-smoke.py:137-149`

**Trigger:** An existing persistent home has an
`xfce4-keyboard-shortcuts.xml` that contains at least one saved shortcut but does
not contain explicit entries for `<Primary><Alt>Delete`, `<Primary><Alt>l`, or
`XF86Mail`. This is the normal result when a user has customized any other shortcut
and XFCE has stored only that change.

**Consequence:** XFCE continues to inherit the system defaults for the missing
entries, so `Ctrl+Alt+Delete` still runs `xfce4-session-logout`, `Ctrl+Alt+L` still
runs `xflock4`, and the mail key still launches the mail helper. This contradicts
`docs/native-integration.md:111-113` ("hides logout/lock controls on saved homes as
well as new desktops") and allows the desktop UI to end or lock the session even
though workspace lifecycle is supposed to belong to Open WebUI.

**Evidence:** The migration only iterates properties already present in the saved
file (`for prop in group`, `if prop.get("name") in (...)`). It never creates a
missing property. The installed XFCE system defaults contain the active bindings:
`/etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfce4-keyboard-shortcuts.xml:13-14,21`
sets `xfce4-session-logout`, `xflock4`, and `exo-open --launch MailReader`. A local
simulation with a saved file containing only an unrelated `<Primary><Alt>t`
shortcut produced a migrated file with no Delete/lock entries at all, so the
inherited defaults remain. The smoke test does not catch this because it builds a
file containing an explicit `<Primary><Alt>Delete` `type="empty"` placeholder
(`native-smoke.py:137-140`), then only asserts that `type="empty"` disappeared
(`native-smoke.py:148-149`).

**Suggested fix:** Ensure each target shortcut exists before clearing it: create
the property under the same `commands/default` group with `type="string"` and
value `""` when absent, then clear any command based on the value as today. Add a
test case with a saved file that has an unrelated shortcut and no target entries.

#### 4. Medium: A malformed saved XFCE file aborts desktop startup instead of being repaired

**Location:** `image/desktop/defaults.py:32-35,49,78`;
`image/desktop/files/usr/local/bin/startxfce4:3-5`

**Trigger:** A persistent home contains a truncated/corrupt `mimeapps.list`,
`xfce4-panel.xml`, or `xfce4-keyboard-shortcuts.xml` (for example after a crash,
disk-full event, or an older XFCE version). `configparser.read()` or `ET.parse()`
raises on that file.

**Consequence:** `desktop-defaults.py` exits nonzero. `startxfce4` is run with
`set -eu` and invokes the migration on line 5 before `exec /usr/bin/startxfce4`, so
the wrapper exits and XFCE never starts. Because the bad file is on the persistent
home, every subsequent container start fails the same way; there is no fallback to
the shipped defaults.

**Evidence:** Neither parser is inside an exception handler. The failure chain is
deterministic from the code; I could not run the image to observe the resulting
container state because Docker was unavailable in this review environment. This is
a code-level finding, not a live observation.

**Suggested fix:** Catch parse errors per file, move the damaged file aside and
seed the shipped default (or skip that migration step), and make the wrapper
best-effort rather than fatal. Add tests with invalid XML and invalid INI content.

#### 5. Low: The OpenAPI rewrite has only synthetic unit coverage

**Location:** `internal/gateway/native_schema_test.go:10-29`;
wiring at `internal/gateway/gateway.go:407-414`

**Trigger:** A future Open Terminal release renames the schema component or changes
the path key, or the live response differs from the synthetic fixture.

**Consequence:** `describeDesktopActions` silently stops adding the coordinate
guidance while the unit tests still pass. Models fall back to the old normalized
coordinates and continue to receive 400/422 responses.

**Evidence:** The tests construct hand-written JSON with
`components.schemas.Action`/`open_terminal__desktop__Action` and paths
`/desktop/action`; they do not pass a real upstream response through the gateway.
`native-tool-contract.py` exercises the callables but does not assert the rewritten
descriptions. This is a coverage gap rather than an observed failure.

**Suggested fix:** Add a gateway test that serves a representative OpenAPI document
through a fake terminal and asserts the field and operation descriptions, including
the failure/pass-through cases.

### Checks performed and review limits

- `go test ./...` passed.
- `go vet`/`gofmt` checks reported no issues; `shellcheck` passed for `image/start`,
  `image/start-docker`, `startxfce4`, and `thunar`.
- Python files compiled and the desktop XML parsed successfully.
- `map/CLAUDE.md` matches the checked generated map files.
- No Docker daemon was available (`/var/run/docker.sock` permission denied), so
  image builds, nested Docker, XFCE startup, and live Desktop/Browser behavior
  were not independently verified. `hadolint` was unavailable. No Git metadata
  was present in the snapshot, so commit-level and mode checks were not possible.
- Live Open WebUI 0.13 / Open Terminal 0.16 testing was not reproduced; the
  review does not confirm the documentation's live-validation claims. Findings 3
  and 4 are code-level defects whose runtime consequences are inferred, not
  observed. Findings 1 and 2 are directly observable from the repository files.
