---
type: object
cluster: workspaces
universe: live
status: verified
verified: 2026-10-21 @ 17c1481
entity: image/agent/agent-setup
---

# Agent manifest and skills

What a model is told about its workspace. On every start the image's `start` runs `agent-setup`, which writes `/etc/termiplus/AGENTS.md` (mode, root, desktop, rules, tools) and links it to `~/AGENTS.md`; Open WebUI reads that file into every turn. Bundled skills are linked from `/usr/share/termiplus/skills` into `~/.agents/skills`, which Open Terminal lists at `/skills`.

## Why this shape

Open WebUI already injects `~/AGENTS.md` and lists skills, so the image only has to put files there; no gateway or Open WebUI change is needed. It is written at start because mode, sudo and desktop are only known then. It is guidance, **not a boundary**: the model can edit or delete all of it.

## Shape

- Manifest: `/etc/termiplus/AGENTS.md` via `sudo tee`; without sudo `~/.cache/termiplus/AGENTS.md`. A user's own regular `~/AGENTS.md` or skill directory is never replaced.
- Creates `~/outputs` (files for the user, downloaded through the chat's file browser).
- `tp-status` prints the manifest plus live facts.
- Desktop image adds the `browser` and `desktop-apps` skills and the `tp-*` tools; see [docs/agents.md](../../../docs/agents.md).
- Policy `locale`, `keyboard`, `record` reach the image as `OPEN_TERMINAL_*` env vars, checked by the gateway and again by `start` / `start-desktop.sh`.

Citations: `image/agent/agent-setup`, `image/agent/bin/tp-status`, `image/start:29-46`, `image/desktop/start-desktop.sh`, `internal/gateway/lifecycle.go:53-59` (Locale, Keyboard, Record)

## Connected to

- **owns:** `~/AGENTS.md` link, `~/.agents/skills/*` links, `~/outputs`
- **owned-by:** [Terminal container](terminal-container.md) start
- **joins:** [Policy](policy.md) (locale, keyboard, record), [Home volume](home-volume.md) (links live in the home)
- **looks-like-but-is-not:** the repo's own `AGENTS.md` (contributor rules)

## If you change this

- **Hits:** what every model sees each turn (keep it short: Open WebUI caps it at 32 KB, and it costs tokens on every turn); the skill list
- **Does not hit:** any security boundary, the config hash (image-only changes)

## Surfaces

| Surface | Role |
|---|---|
| image start | writes it |
| Open WebUI | reads `~/AGENTS.md` and `/skills` |
| model | reads it, runs `tp-status` |

## See

- Source: `image/agent/`, `image/desktop/skills/`, `docs/agents.md`
