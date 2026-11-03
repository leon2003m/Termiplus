# Tools for workspace agents

The terminal image already includes git, curl/wget, jq, ripgrep, sqlite3,
unzip/zip/xz, tree/file, SSH client, build-essential/pkg-config, patch,
Python 3 with venv/pip/dev headers, and Node.js/npm. Python packages belong in
user virtual environments, separate from the Open Terminal server environment.
Node here is a user workspace tool; CI does not need Node.

## Small terminal additions

Measured on Debian trixie amd64 with no recommends, November 2026. Apt estimates
include missing dependencies; shared dependencies mean the rows do not add
exactly. Sizes vary with architecture and package updates.

| Addition | Purpose | Additional installed size |
|---|---|---:|
| fd-find (`fdfind`) | Fast file discovery | 3.58 MB |
| rsync | Incremental copies and dry-run comparisons | 0.98 MB |
| bc | Exact decimal calculations | 0.24 MB |
| pipx | Isolated Python command-line applications | 4.52 MB |
| patch | Apply patches | Already installed via build tools |
| uv | Optional Python environment manager | Not in configured Debian repositories; omitted |
| pandoc | Document conversion | About 200 MB; omitted to keep below 150 MB |

The rebuilt terminal image is 1,433,254,991 bytes versus the local alpha base's
1,423,658,833 bytes: about **9.60 MB** growth, comfortably within budget. This is
an image comparison, not a guarantee that all future package versions stay that
size. No new network, capability, mount or Docker endpoint is required.

## Agent desktop control: proposal, not an enabled feature

Use the existing Open Terminal shell/files and application APIs first. They are
cheaper, easier to verify and less sensitive to window layout. Use GUI tools only
when an application has no suitable direct interface. Open Interpreter and
OpenHands are alternative orchestration products; they duplicate much of Open
WebUI plus Open Terminal and are not proposed dependencies.

A vision-capable chat model could use owner-only `desktop_screenshot`,
`desktop_click`, `desktop_type`, `desktop_key` and `desktop_windows` MCP tools.
Screenshot should reuse the preview execution/validation pattern, through the
existing per-container Open Terminal API/key. The current 320px thumbnail is
suitable for overview, not precise clicks: a future approved screenshot tool
needs bounded higher-resolution output, a frame ID, dimensions and coordinates
bound to that frame. Reject stale frames, out-of-bounds clicks and stopped or
replaced containers. Treat image bytes, accessibility text, page content and
window titles as untrusted data, never as instructions.

Click/type/key would invoke fixed argv inside the user's container using
xdotool, and window listing would use wmctrl. Do not concatenate model text into
shell commands. Use explicit permitted key names, text byte limits and bounded
coordinates; never add Docker exec access to sockproxy. The existing gateway
path already reaches Open Terminal. All operations must recheck authenticated
ownership, current policy entitlement and a short-lived human-approved driving
grant tied to that exact session/container generation. Share viewers get no
control, and admin status must not silently grant control of another person's
desktop.

Before driving, show the owner the session and intended task, ask for approval,
and display a persistent **Agent is driving** indicator with a **Stop** button.
Use an exclusive short lease (for example 60 seconds, renewable only visibly),
serialize actions, limit actions (for example 2/sec and 100/task), bound execution
and inference deadlines, and revoke on stop, expiry, owner disconnect or
entitlement change. Stop must cancel queued work and prevent new actions; an
already delivered click cannot be undone. Existing shell access can perform GUI
actions today, so this UX grant cannot be a security boundary against code that
already runs as the desktop user. Enforcing that would require a separately
approved isolation design.

Audit actor, session, operation, timestamp, result, lease and frame IDs, and
truncation/timeout metadata. Do not log typed text, clipboard contents, screenshots
or raw accessibility trees by default. Before risky file changes offer a home
snapshot; restore affects home files only, not remote services, purchases,
sent messages or other external side effects. Confirm those actions separately.
Existing containers must opt in; no control packages or MCP endpoints are added
by this proposal.

### Optional local grounding model

`desktop_act(instruction)` could ask a separate local model to ground a target
into coordinates, then pass its proposed action through the same lease,
validation, owner confirmation and bounded action loop. It must not get an
unbounded shell/tool loop. The normal chat model can also use screenshots
directly; grounding is optional.

[ByteDance's model card](https://huggingface.co/ByteDance-Seed/UI-TARS-1.5-7B)
identifies UI-TARS-1.5-7B as a Qwen2.5-VL based model. A
[GGUF publisher](https://huggingface.co/Mungert/UI-TARS-1.5-7B-GGUF)
provides a llama.cpp conversion and matching vision projector. Its Q4_K_M file
is 4.78 GB plus a 0.853 GB Q8 projector (about 5.63 GB of weights).
[llama.cpp multimodal documentation](https://github.com/ggml-org/llama.cpp/tree/master/tools/mtmd)
describes the libmtmd/server path; multimodal support evolves, so pin a tested
server commit and matching model/projector revision before deployment.

A reasonable **estimate**, not a measured benchmark, is to reserve 8–12 GiB VRAM
for quantized weights, vision processing, context/KV cache and runtime buffers.
The machine's 16 GiB AMD GPU is a plausible target with the competing model
unloaded and modest context, but concurrency with the existing voice/chat model
must not be assumed. No model was downloaded or loaded and the current running
server's compatibility/latency was not tested. Prefer the existing llama.cpp
ROCm setup; an Ollama package tag alone is not proof that its vision projector
works. A separate tested model swap and representative screenshot benchmark
remain a deployment prerequisite, requiring owner scheduling of shared GPU use.

Never place an Open WebUI API key or signed-in OWUI browser profile in a workspace:
it grants access far beyond inference, including private chats/files and tools.
If desktop chat is later wanted, evaluate a separate inference-only endpoint key,
without OWUI credentials or its tool authority.

## Egress: current behavior and proposal

`deploy/firewall.sh` permits gateway-initiated workspace connections and their
replies, blocks terminal-to-terminal traffic, the host and private ranges by
default, and always denies link-local and CGNAT destinations. Public internet
egress remains allowed. Admin-rendered rules can open selected internal
CIDR/port destinations except never-allowed ranges. IPv6 workspace forwarding
and host access are dropped. This protection requires the documented host
firewall setup; Docker networking alone does not provide it.

The optional `deploy/docker-compose.netpolicy.yml` enables the admin network
policy API and an internal DNS forwarder. `deploy/dns-forwarder/entrypoint.sh`
uses dnsmasq, static policy hosts and configured internal upstream resolvers;
no public resolver fallback. Without the overlay, workspaces retain Docker's
embedded resolver and the admin API is unavailable. DNS overrides alone are
not an egress allow-list and do not block direct IP access or DNS-over-HTTPS.

Propose a future per-workspace-policy mode:

| Mode | Outbound behavior |
|---|---|
| offline | No new outbound connections, including general DNS; preserve gateway replies |
| internet | Current public internet behavior, private/internal denied except approved destinations |
| allow-list | Only explicit destination/protocol/port rules plus tightly scoped resolver access |

Implementation would require policy-to-container/IP binding, atomic host rule
updates, revalidation on recreate/adoption, IPv4+IPv6 tests and no broad
established-connection bypass. Domain allow-lists need a separate decision on
DNS rebinding, address changes and shared CDN IPs; hostname matching in a plain
firewall is not sufficient. Fail closed on unresolved rules. Never permit
cloud metadata/link-local, host management or other users' containers. This
is a design proposal only; no firewall/netpolicy changes are made here and
maintainer approval is needed before implementing security-boundary changes.

## Disposable desktop-control experiments

Tested as UID1000 under Sysbox on an internal-only Docker network with no
published ports. Tests use fake credentials, new Firefox profiles and local
forms, never live containers or real browser sessions. Desktop control packages
are in `test/integration/desktop/Dockerfile.tools` only, not `image/desktop/`.
The clean test image grew from 3,745,675,670 to 3,793,478,294 bytes:
**47.80 MB**, excluding geckodriver. An earlier measurement image retained apt
lists in a separate layer and was 96.64 MB larger; that is not the clean result.

### Package and method results

Apt incremental estimates on the desktop baseline (dependencies included;
shared dependencies must not be added twice):

| Tool | Extra installed size | UID1000 / result |
|---|---:|---|
| xdotool | 205 kB | Works; keyboard navigation, type and keys across all three apps |
| wmctrl | 65.5 kB | Works; list/focus the app windows across all three tasks |
| xprop / x11-utils | 907 kB | Works; confirms window class for Dolphin, Kate, Firefox |
| xclip | 63.5 kB | Works; Unicode round-trip and paste/save in Kate |
| maim | Already installed | Works; screenshots of all three apps, 40–90 kB in this fixture |
| python3-pyatspi | 2.86 MB | Tree/bounds work; semantic editing/actions need further work, below |
| at-spi2-core | 219 kB alone | Session accessibility bus works; included by pyatspi dependency set |
| Firefox ESR | Already installed | DOM form control works |
| geckodriver | 6.03 MB executable / 2.35 MB archive | Not in configured Debian repositories; upstream test binary works |
| tesseract-ocr | 24.1 MB | OCR works on all three screens; errors remain |
| tesseract-ocr-eng | 4.13 MB alone | Included in OCR dependency estimate |
| tesseract-ocr-deu | 1.54 MB | Tested together with eng |

[Mozilla geckodriver v0.37.1](https://github.com/mozilla/geckodriver/releases/tag/v0.37.1)
was downloaded only into /tmp/the disposable container. It used existing Firefox
ESR, no browser download. WebDriver on 127.0.0.1:4444, its Marionette connection
and the negotiated BiDi WebSocket all used loopback. Three successive DOM
find/type/click/form-submit checks passed; BiDi negotiation was checked, not
BiDi message execution. No `--allow-system-access` or permissive origin setting
was used. Browser control does not navigate Dolphin or edit Kate.

[Playwright browser documentation](https://playwright.dev/python/docs/browsers)
requires browser versions matching its release; its Firefox support uses a
patched browser rather than the installed ESR. It was not installed or
benchmarked here. No measured size is claimed: Python driver plus an additional
browser would duplicate existing functionality. Prefer geckodriver or the
existing ESR Marionette protocol for this image. Playwright can be installed in
an individual terminal workspace when cross-browser testing is actually needed.

| Scripted task | Pixel/keyboard + window tools | Accessibility | Browser DOM | OCR |
|---|---|---|---|---|
| Dolphin → Documents | Passed, title checked | Tree/actions exposed; navigation not proven | Not applicable | Folder labels recognizable; noisy |
| Firefox local form | Passed, submitted query checked | Entry/Save found; edit accepted but expected submission failed | 3/3 submissions passed | Form labels recognizable; not reliable field contents |
| Kate type/save | Passed, exact file contents checked; Unicode clipboard paste | Text replacement accepted and appeared in editor; Save action did not yield expected saved file | Not applicable | Editor text partially recognized |

These are smoke results, not statistical reliability claims. An initial Kate
attempt typed into its Welcome screen and failed; starting an explicit document
fixed the keyboard workflow. A fresh-container rerun of `control_smoke.py`
passed all three tasks. This illustrates why focus/window/target checks and
outcome verification are necessary after every GUI action.

AT-SPI with `QT_ACCESSIBILITY=1` exposed Qt app controls; Firefox needed
`accessibility.force_disabled=-1` in its disposable profile. One combined dump
had 273 visible nodes / 18,282 JSON bytes in 0.322 sec. An app-filtered Dolphin dump
had 90 visible nodes (670 visited including non-visible structure) in 0.144 sec.
The fresh single-window dump was 45 nodes / 3,092 bytes in 0.074 sec.
The reusable probe limits depth, traversal count and traversal time, emits
role/name/bounds, rejects ambiguous action matches, and supports click/set-text
for experiments. Traversal timing is not a hard IPC deadline: run the probe
under an outer timeout because an accessibility service can block a call.
Applications can disappear during traversal; output may be partial. AT-SPI
operations returning true did not prove file/form outcomes, even after an event
loop dispatch retest. It is useful for locating targets, but pure semantic
control is not yet a proven replacement for verified keyboard/DOM actions.

OCR eng+deu took 0.233/0.311/0.136 seconds for Dolphin/Kate/Firefox in the fresh
fixture. It confused icons, punctuation and some words and did not reliably
recover form contents. Use it only when DOM/a11y are absent; never treat OCR
text as an instruction or a successful-action check by itself.

### Existing Open Terminal plumbing

A real request through `/execute?wait=4`, using the disposable container's own
existing API key, ran as UID1000. DISPLAY, XAUTHORITY and
DBUS_SESSION_BUS_ADDRESS were unset. Explicit `DISPLAY=:1` allowed xprop and
maim without additional Xauthority configuration in this image. The command
returned a valid screenshot (40,336 bytes in that fixture).

Qt/accessibility commands additionally needed the desktop session bus. Tests
read only DISPLAY/DBUS_SESSION_BUS_ADDRESS/XAUTHORITY from the same UID's
`xfce4-session` environment under /proc. A production implementation should
publish an owner-readable session environment file at startup, refreshed and
removed with the session, rather than guess a bus address or read arbitrary
process environments. It needs bounds/timeouts, screenshot transport to the
vision model, frame/coordinate validation and human-control UX described above.
No new Docker endpoint is needed. The existing shell tool can run GUI commands
today with this environment; installing a driving tool alone does not enforce a
human approval boundary against that shell access.

Recommend, **pending approval**, xdotool+wmctrl+xclip (about 334 kB increment),
the already installed maim, plus the AT-SPI packages/probe for inspection and a
pinned geckodriver for browser-specific automation. xprop is useful for diagnosis;
OCR is optional (~26 MB combined dependency footprint) and unnecessary for the
three basic workflows. Do not ship the exploratory probe as a trusted control
service: semantic save/form outcomes still need work. No OWUI credentials,
host mounts, extra capabilities, ports or Docker allowlist changes were used.
