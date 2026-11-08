---
type: object
cluster: workspaces
universe: live
status: verified
verified: 2026-11-08
entity: image/agent/agent-setup
---

# Agent manifest and skills

What a model is told about its workspace, plus how the policy fields `locale`, `keyboard` and `record` reach the image. On every start the image's `start` runs `agent-setup`, which writes `/etc/termiplus/AGENTS.md` (mode, root, desktop, rules, tools) and links it to `~/AGENTS.md`; Open WebUI reads that file into every turn. Bundled skills are linked from `/usr/share/termiplus/skills` into `~/.agents/skills`, which Open Terminal lists at `/skills`.

## Why this shape

Open WebUI already injects `~/AGENTS.md` and lists skills, so the image only has to put files there; no gateway or Open WebUI change is needed. It is written at start because mode, sudo and desktop are only known then. It is guidance, **not a boundary**: the model can edit or delete all of it. termiplus ships no desktop or browser automation of its own; the desktop and browser are Open Terminal's native ones.

## Shape

- Manifest: `/etc/termiplus/AGENTS.md` via `sudo tee`; without sudo `~/.cache/termiplus/AGENTS.md`. A user's own regular `~/AGENTS.md` or skill directory is never replaced.
- Creates `~/outputs` (files for the user, downloaded through the chat's file browser).
- `tp-status` prints the manifest plus live facts.
- Desktop image adds the `desktop` skill; see [docs/agents.md](../../../docs/agents.md).
- `locale` becomes `LANG` in `start`. `keyboard` and `record` (desktop policies only) are re-validated by `start`, written to `/etc/termiplus/desktop.conf`, and applied by the desktop image's `startxfce4` wrapper, because Open Terminal strips `OPEN_TERMINAL_*` from the desktop's environment and starts the display on demand. The wrapper runs `setxkbmap` and starts `record-desktop` (ffmpeg x11grab, 2 fps, hourly `.mkv` in `~/recordings`, pruned after 2 days) as a child of the session, so it ends with the display.

Citations: `image/agent/agent-setup`, `image/agent/bin/tp-status`, `image/start` (locale, desktop.conf, agent-setup), `image/desktop/files/usr/local/bin/startxfce4`, `image/desktop/record-desktop`, `internal/gateway/lifecycle.go:24-70` (Locale, Keyboard, Record), `internal/gateway/gateway.go:182-190` (validation)

## Connected to

- **owns:** `~/AGENTS.md` link, `~/.agents/skills/*` links, `~/outputs`, `~/recordings`, `/etc/termiplus/desktop.conf`
- **owned-by:** [Terminal container](terminal-container.md) start
- **joins:** [Policy](policy.md) (locale, keyboard, record), [Home volume](home-volume.md) (links live in the home)
- **looks-like-but-is-not:** the repo's own `AGENTS.md` (contributor rules)

## If you change this

- **Hits:** what every model sees each turn (keep it short: Open WebUI caps it at 32 KB, and it costs tokens on every turn); the skill list; the desktop session start (`startxfce4` wrapper must never fail on recording or keyboard errors)
- **Does not hit:** any security boundary, the config hash for image-only changes (policy fields do change it)

## Surfaces

| Surface | Role |
|---|---|
| image start | writes it |
| desktop session (`startxfce4` wrapper) | applies keyboard, starts recording |
| Open WebUI | reads `~/AGENTS.md` and `/skills` |
| model | reads it, runs `tp-status` |

## See

- Source: `image/agent/`, `image/desktop/skills/`, `docs/agents.md`
