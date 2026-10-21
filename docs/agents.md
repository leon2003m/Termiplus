# Models in a workspace

The terminal and desktop images come with what a model needs to work in a
workspace on the user's behalf: a manifest that says what the workspace is,
skills, office tools and, on desktops, a browser and GUI tools. None of it is
a security boundary. The model can read and change all of it; the boundaries
stay the container, the socket proxy and the firewall.

## Manifest (`~/AGENTS.md`)

On every start, `image/agent/agent-setup` writes `/etc/termiplus/AGENTS.md`
and links it to `~/AGENTS.md`. Open WebUI reads that file into every turn of a
chat that uses the terminal (up to 32 KB). It states:

- the mode: persistent (only `/home/user` survives) or sandbox (deleted with the chat)
- whether sudo works, and whether there is a desktop
- the rules: cheapest way first (shell, then `tp-browser`, then accessibility,
  then screenshots), verify every action, files for the user go to
  `~/outputs`, ask before anything irreversible or outward-facing, hand over
  logins, captchas and payments to the user
- the tools below

A user's own `~/AGENTS.md` (a regular file) is never replaced. `tp-status`
prints the manifest plus live facts: disk, memory, open windows, top
processes. The start script also adds a line to Open Terminal's tool
description that points the model at `~/AGENTS.md`.

## Skills

Open Terminal lists `~/.agents/skills/<name>/SKILL.md` at `/skills`.
`agent-setup` links the image's skills from `/usr/share/termiplus/skills`
there, again without replacing the user's own:

| Skill | Image | Covers |
|---|---|---|
| `documents` | terminal | convert, read and write Office files and PDFs, OCR, search; never overwrite originals |
| `handoff` | terminal | when to stop and ask, how to hand a step over to the user |
| `browser` | desktop | the `tp-browser` workflow |
| `desktop-apps` | desktop | GUI apps: command line first, then `tp-ui`, then marks, then coordinates |

## Tools

Terminal image: `pandoc`, LibreOffice (headless `soffice`), `pdftotext`,
`python3` with `openpyxl`, `python-docx` and Pillow, `tesseract` (English and
German), `recoll`, `nextcloudcmd`, all locales.

Desktop image, in addition:

| Command | What it does |
|---|---|
| `tp-browser open URL`, `snapshot`, `click REF`, `fill REF TEXT`, `press KEY`, `text`, `screenshot`, `tabs`, `tab N`, `back`, `close` | Drives Chromium on the desktop through Playwright over CDP (bound to 127.0.0.1). Every action returns the page's interactive elements with refs. The user sees the browser and can take over. |
| `tp-screenshot [--marks] [--width N]` | Screenshot scaled to 1024 px wide; `--marks` numbers the clickable elements found through accessibility |
| `tp-act click X Y`, `dblclick X Y`, `click-mark N`, `type TEXT`, `key KEYS`, `scroll up\|down` | Mouse and keyboard in screenshot coordinates; prints whether the screen changed |
| `tp-ocr [--json]` | Words on the screen with positions (`TP_OCR_LANG`, default `eng+deu`) |
| `tp-ui tree`, `click NAME`, `set NAME TEXT` | GUI elements of desktop apps through AT-SPI |

Chromium runs with a managed policy: no password manager, sync, sign-in or
metrics. There is no ad or cookie-banner blocker: Chromium 155 no longer
loads Manifest V2 extensions such as Debian's uBlock Origin. Its own profile is `~/.config/tp-browser`. DNS stays the workspace's resolver.

## Policy settings

| Field | Effect |
|---|---|
| `locale` | `LANG` in the workspace, e.g. `de_DE.UTF-8`. Default `C.UTF-8`. |
| `keyboard` | Desktop only. XKB layout, e.g. `de`. |
| `record` | Desktop only. Records the screen at 2 fps in hourly `.mkv` files under `~/recordings`; files older than 2 days are deleted. For reviewing what a model did. Takes disk space and some CPU. |

The gateway checks the values and passes them as `OPEN_TERMINAL_LOCALE`,
`OPEN_TERMINAL_KEYBOARD` and `OPEN_TERMINAL_RECORD`; the start scripts check
them again and unset them.

## Files for the user

The model writes results to `~/outputs`. The user downloads them from Open
WebUI's terminal file browser in the chat. In a sandbox that has to happen
before the chat goes idle.

## Limits

- Open WebUI's stop button only ends the model's stream. A command that is
  already running in the workspace keeps running until it ends or the
  workspace goes idle.
- Channels get no terminal. In a shared chat, each message runs in the
  sender's own workspace.
- Sending email is not built in. Give the model no mail credentials it
  should not use.
