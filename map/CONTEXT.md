# How to walk this map

1. Start at [CLAUDE.md](CLAUDE.md): it routes the question and resolves colliding names.
2. For a noun, read one line in [objects/_index.md](objects/_index.md), then open only that card.
3. For a change, open [effects/CONTEXT.md](effects/CONTEXT.md) first. It names the cards to open, and the things *outside* this repo that point in.
4. Follow a card's **See** link to the source and read the code there. Cards say why and what moves; the code says how.

Do not read the whole `objects/` folder. The index exists so you don't have to.

## Universes

| Universe | Meaning here | Examples |
|---|---|---|
| **live** | In force on `dev`/`main`. Implement against these. | gateway, sockproxy, mcp, netpolicy, deploy compose files |
| **leftover** | Present, but no longer the main path. Touch only when that path is in scope. | `StubBackend` and `MCP_AUTH=dev` (`internal/mcp/backend.go`, `cmd/mcp/main.go`): tests and local trials only |
| **ghost** | Named but not wired. Do not implement against it. | none known at `a35e601`. If you find one, add an index line marked `ghost` |

## Status of a card

- `stub`: an index line only. The noun exists; nobody has written its card yet.
- `verified`: written against source on the date and commit in the card. Citations required.
- `stale`: source changed after verification; the card may be wrong. Re-verify before trusting it.

When you change code a card cites, update the card in the same PR, or mark it `stale` in the index.

## Keeping it honest

- If a card and the code disagree, the code wins: fix the card.
- If [effects/CONTEXT.md](effects/CONTEXT.md) and a card disagree, fix the card, then the index line.
- Security boundaries (sockproxy, `deploy/firewall.sh`, container hardening, gateway auth) are governed by [AGENTS.md](../AGENTS.md). The map describes them; it never relaxes them.
- Edit `map/CLAUDE.md` only, then run `make map`. `AGENTS.md` and `routing.md` in this folder are generated.
