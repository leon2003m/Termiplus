# Schema (closed)

Only these node types exist in this map. A new type needs a change here first.

| Type | Lives in | Frontmatter |
|---|---|---|
| `object` | `objects/<cluster>/<name>.md` | `type: object`, `cluster`, `universe`, `status`, `entity` |
| `process` | `processes/<name>.md` | `type: process`, `status`, `consumes: []`, `produces: []` |
| catalog / index | `CLAUDE.md`, `CONTEXT.md`, `objects/_index.md`, `effects/CONTEXT.md` | none |

## Closed values

- `cluster`: `identity` · `workspaces` · `sandbox` · `control-plane` · `storage` · `config`
- `universe`: `live` · `leftover` · `ghost`
- `status`: `stub` · `verified` · `stale`
- `verified`: `<YYYY-MM-DD> @ <commit>`, required when `status: verified`
- `entity`: repo-relative path of the file that owns the noun
- `consumes` / `produces`: relative links to object cards

## Required sections

- object: one sentence · Why this shape · Shape (with `Citations:`) · Connected to (owns / owned-by / joins / looks-like-but-is-not) · If you change this (Hits / Does not hit) · Surfaces · See
- process: one sentence · Input → Movement → Output · Why this shape · Steps (cited) · If you change this · Surfaces · See

A `verified` card names its date and commit and cites `path:line`. Hits / Does not hit are first-order only.
