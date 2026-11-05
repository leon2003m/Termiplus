# Open Terminal standard-image comparison

Reviewed 2026-11-04 against the official **v0.16.0** image, matching the server
version pinned in `image/Dockerfile`. The upstream checkout's current branch
already differs; the release tag is the comparison baseline.

Sources: [official Dockerfile](https://github.com/open-webui/open-terminal/blob/v0.16.0/Dockerfile),
[Python requirements](https://github.com/open-webui/open-terminal/blob/v0.16.0/pyproject.toml),
[entrypoint](https://github.com/open-webui/open-terminal/blob/v0.16.0/entrypoint.sh).
Read for comparison only; no upstream implementation was copied.

The comparison uses command lookup and Python module discovery in the rebuilt
Termiplus desktop image, separately for the normal user interpreter and the
isolated server interpreter. Package presence is not a full functional test.

## Restored in this change

The native transport migration had removed the Termiplus desktop applications
and branding. The complete image has Dolphin as folder handler, Chromium as its
only browser, Kate, LibreOffice, the app dock, matching dark Qt/GTK themes and
the Termiplus wallpaper. Native TigerVNC and audio remain.
Both shipped workspace policies now enable Desktop and Browser under Sysbox;
only persistent versus temporary storage is a user-facing choice.

## Standard-image tools included

| Area | Standard-image feature | Termiplus implementation |
| --- | --- | --- |
| Python | Scientific analysis and charts | NumPy, pandas, SciPy, scikit-learn, Matplotlib, Seaborn and Plotly in `/opt/workspace`, on the user PATH. |
| Python | Web, data and document helpers | Requests, BeautifulSoup, lxml, SQLAlchemy, psycopg2, PyYAML, TOML, jsonlines, tqdm, Rich, openpyxl, WeasyPrint, python-docx, python-pptx, pypdf and csvkit. |
| Notebooks | Interactive Python | Jupyter, IPython, nbclient and ipykernel. The native server's `python3` kernel explicitly launches `/opt/workspace/bin/python`, sharing the user's libraries without modifying the API server venv. |
| Documents | Office, PDF, media and typesetting | LibreOffice with GTK desktop integration, Pandoc, Poppler, FFmpeg, base LaTeX, Caladea and Carlito fonts. |
| Docker | CLI, Compose, Buildx and daemon | Preinstalled Debian packages; the existing Sysbox startup launches the inner daemon. The host Docker socket stays inaccessible. |
| Development | Build and scripting | GCC/build-essential, Make, CMake, Python, Node, npm, Perl, Ruby and Lua. Debian supplies Python/Node rather than upstream's Python 3.12 / Node 22 bootstrap. |
| Utilities | Session, system and network tools | tmux, screen, btop (instead of htop), strace, sysstat, net-tools, netcat, socat, telnet, man, xmlstarlet, zstd, 7-Zip, full Vim and gawk, alongside the existing Git, SSH, rsync, jq and archive tools. |
| Lifecycle | Process reaping | Tini is PID 1. Startup releases only abandoned native-managed Chromium singleton links while holding the native profile lease; active and unmanaged profiles are preserved. |

## Desktop choices (2026-11-05)

Chromium is the sole browser and default HTTP/HTTPS handler. Ristretto and
ImageMagick are intentionally omitted; Pillow and the browser still handle
images. btop replaces htop. LibreOffice, Dolphin, Kate and XFCE Terminal remain.
The startup migration repairs stale app launchers on saved homes and hides
mail, TeX documentation GUI and logout entries. XFCE logout/lock shortcuts and
panel session actions are removed so workspace controls own lifecycle. This
is a UI guard; users retain sudo inside their workspace.
Partial saved shortcut files receive explicit overrides for inherited logout,
lock and mail bindings. Invalid MIME/panel/shortcut files are kept as uniquely
named `.invalid-*` backups and replaced with valid defaults. Smoke tests exercise
both recovery and repeat startup while retaining unrelated personal settings.

The model must also have Desktop-aware system instructions. A saved model
prompt describing only terminal tools can contradict the available native
Desktop tools even when its vision capability is enabled.

## Deliberate architecture differences

- Termiplus runs one isolated container per user/workspace (or per user/chat),
  rather than enabling upstream's shared-container multi-user mode. The latter's
  `cap_setgid` server setup is therefore not required for this deployment.
- Network filtering is enforced by the host firewall, DNS forwarder and Docker
  API allowlist. Upstream's in-container iptables/ipset/dnsmasq entrypoint is not
  an additional required feature; it must not replace those host boundaries.
- Extra packages are built into the image or installed explicitly with apt,
  pip and npm. Upstream's environment-driven bootstrap variables are not used;
  no new container environment fields are admitted by the socket proxy.
- The official v0.16.0 image defaults Chromium to `--no-sandbox`. Termiplus keeps
  Chromium's sandbox enabled and validates Browser under Sysbox. There is no
  reason to adopt the upstream default here.
- The Open Terminal API version is the same. Missing convenience packages do
  not by themselves indicate missing Files, Git, events, skills, notebook,
  desktop or browser endpoints. The gateway forwards the native protocols;
  real protocol tests and package inventory answer different questions.

## Validation

The Sysbox smoke test checks native Desktop and authenticated streams, selected
wallpaper, Dolphin associations, Python imports, a pandas notebook execution,
DOCX-to-PDF conversion, a nested Docker build/run without network, Compose and
Buildx, browser lock recovery and sandboxed Chromium automation. It also checks
authentication failures and that a policy explicitly disabling Desktop denies it.

`OPEN_TERMINAL_INFO`, an upstream setting, adds Desktop action examples to the
native `/system` prompt. Click/move use `x/y`; only drag destinations use
`to_x/to_y`. The gateway also annotates native OpenAPI field descriptions with
these requirements and the fixed Linux 1280×800 raw pixel scale, leaving the
API and validation unchanged. A narrow xdotool wrapper replaces unbounded
pointer synchronization with a bounded actual-position check: repeated clicks
at the current position otherwise hang under TigerVNC and return HTTP 502.
Unknown X11 key warnings become failed actions with partial-batch progress.
The smoke test covers these failures, subsequent valid input, repeated native
clicks and screenshot dimensions.
Open WebUI must mark a vision-capable model
as supporting vision so screenshots reach the model as images.

## Local model verification (2026-11-05)

Tests used saved Open WebUI chats and its real streaming native-tool loop with
Gemma 4 12B, including image results. Each run had a time and tool-call budget;
no replacement model or custom desktop protocol was used. A temporary local
page supplied independently observable codes, button events and navigation.
The model prompt was corrected to describe Chromium and native type/key actions.

The last five runs after that correction were:

| Task | Reasoning | Result |
| --- | --- | --- |
| Open visible desktop Chromium and read the page code | Off | Passed |
| Follow a link through a prepared native Browser tab | On | Passed |
| Open desktop Chromium and click a page button by screenshot | On | Failed: incorrect pixel targeting, no completed answer |
| Repeat visible desktop navigation and page-code reading | On | Passed |
| Click a button and follow a link in the native Browser tab | Off | Passed: both changed codes and server events verified |

Earlier runs exposed the repeated-pointer input stall, normalized coordinates,
and key/text confusion. The Browser fixture also required an opened chat tab.
Accumulated test windows hit the existing 512-process/thread limit and stalled
Chrome startup; closing those test windows and restarting the test browser
restored it. Resource limits and browser sandbox settings were retained.
A manually controlled pixel click independently produced the expected button
event through the real Open WebUI Desktop API. Test tabs and the temporary
page server were removed after verification.

These results demonstrate working native transport and successful browser
interaction; they do not establish reliable screenshot-based mouse targeting
for this local model or guarantee that every tool-calling model will succeed.
