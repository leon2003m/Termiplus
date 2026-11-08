# Models in a workspace

The terminal and desktop images come with what a model needs to work in a
workspace on the user's behalf: a manifest that says what the workspace is,
skills and office tools. The desktop and the browser are exactly what Open
Terminal ships (see [open-terminal-desktop-compat.md](open-terminal-desktop-compat.md));
termiplus adds no desktop or browser automation of its own. None of this is a
security boundary. The model can read and change all of it; the boundaries
stay the container, the socket proxy and the firewall.

## Manifest (`~/AGENTS.md`)

On every start, `image/agent/agent-setup` writes `/etc/termiplus/AGENTS.md`
and links it to `~/AGENTS.md`. Open WebUI reads that file into every turn of a
chat that uses the terminal (up to 32 KB). It states:

- the mode: persistent (only `/home/user` survives) or sandbox (deleted with the chat)
- whether sudo works, and whether there is a desktop
- the rules: cheapest way first, verify every action, files for the user go to
  `~/outputs`, ask before anything irreversible or outward-facing, hand over
  logins, captchas and payments to the user
- the tools below

A user's own `~/AGENTS.md` (a regular file) is never replaced. `tp-status`
prints the manifest plus live facts: disk, memory, outputs, recordings, top
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
| `desktop` | desktop | the desktop and Open Terminal's built-in browser: commands first, then browser tools, then screenshots and actions |

## Tools

Terminal image: `pandoc`, LibreOffice (headless `soffice`), `pdftotext`
(poppler), `python3` with `openpyxl`, `python-docx` and Pillow, `tesseract`
(English and German), `recoll`, all locales.

Desktop image, in addition, everything Open Terminal's desktop and browser
tools use, plus `wmctrl`, `xdotool` and `xclip` for the display.

## Policy settings

| Field | Effect |
|---|---|
| `locale` | `LANG` in the workspace, e.g. `de_DE.UTF-8`. Default `C.UTF-8`. |
| `keyboard` | Desktop only. XKB layout, e.g. `de`, applied with `setxkbmap` when the desktop starts. |
| `record` | Desktop only. Records the desktop at 2 fps in hourly `.mkv` files under `~/recordings`; files older than 2 days are deleted. For reviewing what a model did. Takes disk space and some CPU. |

The gateway checks the values and passes them as `OPEN_TERMINAL_LOCALE`,
`OPEN_TERMINAL_KEYBOARD` and `OPEN_TERMINAL_RECORD`; `image/start` checks them
again and unsets them.

Open Terminal starts the desktop on demand and gives its processes an
environment without `OPEN_TERMINAL_*` variables. So `image/start` writes the
validated keyboard and record values to `/etc/termiplus/desktop.conf` (rewritten
on every start), and the desktop image's `startxfce4` wrapper, which Open
Terminal runs inside the display, reads it: it sets the layout and starts
`record-desktop` as a child of the session. The recorder therefore exists only
while the display does, ends with it, gives up after repeated ffmpeg failures
without affecting the desktop, refuses a symlinked `~/recordings` and writes
only there. The browser's own private display is not recorded.

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
