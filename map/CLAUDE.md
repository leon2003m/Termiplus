# termiplus system map

A walkable map of this repo for agents who will change it. The source tree is
the truth; every card cites `path:line` and stops there. Edit this file only:
`AGENTS.md` and `routing.md` next to it are generated twins (`make map`), and
`make lint` fails if they drift.

Verified against commit `a35e601` on 2026-10-19. Lines may move; the symbol
names in each citation are the stable anchor.

## Where to go

| Question | Open |
|---|---|
| How do I walk this map, what are live / leftover / ghost? | [CONTEXT.md](CONTEXT.md) |
| What is X (a noun)? | [objects/_index.md](objects/_index.md), then one card |
| How does X move (request, create, tool call, stream, departure, release)? | [processes/](processes/) |
| I am changing X, what else moves? | [effects/CONTEXT.md](effects/CONTEXT.md) |
| Which node types and fields are allowed? | [_meta/schema.md](_meta/schema.md) |
| Adding a card | copy [_templates/object.md](_templates/object.md) or [_templates/process.md](_templates/process.md) |
| Rules for commits, security boundaries, releases | [../AGENTS.md](../AGENTS.md) (not this map) |

## The system in five lines

1. **gateway** (`cmd/gateway`, `internal/gateway`) is what Open WebUI talks to as a terminal: `/p/<policy>/...` with the gateway API key. It starts one container per user and policy (or per chat) and proxies to it.
2. **sockproxy** (`cmd/sockproxy`, `internal/sockproxy`) is the only holder of the Docker socket. It allows a handful of endpoints and rebuilds every create request. The gateway never reaches Docker directly.
3. **mcp** (`cmd/mcp`, `internal/mcp`) is the alpha control plane: MCP tools for chat, a management console, desktop streams and port forwards. It calls the gateway's `/control/v1/` API; it never touches Docker.
4. **netpolicy** (`internal/netpolicy`) plus `deploy/firewall.sh` and `deploy/dns-forwarder` decide what a terminal may reach. Public egress is allowed, internal is denied except admin-set entries.
5. **deploy/** is compose files (base plus overlays), host firewall and systemd units. **image/** is the terminal and desktop images.

## Names that collide (read before any card)

| Word you see | What it is in code | Card |
|---|---|---|
| **workspace** (MCP tools, plugin, console) | a gateway **Policy** name from `policies.json` | [Policy](objects/workspaces/policy.md) |
| **session** (MCP, `session_id`) | a terminal **container** name, `ot-u-…` (persistent) or `ot-e-…` (ephemeral) | [Terminal container](objects/workspaces/terminal-container.md) |
| **session** (`X-Session-Id` from Open WebUI) | the **chat** id; picks the ephemeral container, ignored for persistent | [Terminal container](objects/workspaces/terminal-container.md) |
| **home** | a Docker volume `ot-home-…` mounted at `/home/user`; one per user and `Policy.home` group | [Home volume](objects/workspaces/home-volume.md) |
| **archive helper** | a never-started container `ot-a-…` that mounts one home so the gateway can tar it | [Archive helper](objects/workspaces/archive-helper.md) |
| **archive** (departed user) | a stored tar of a home under `BACKUP_DIR/archived/`, not the helper | [Backup](objects/storage/backup.md) |
| **user id** | the **Open WebUI user id** (`X-User-Id`), the key for every name hash | [Principal](objects/identity/principal.md) |
| **subject** | the **OIDC subject** from Keycloak, mapped once to an Open WebUI id | [IdentityStore](objects/identity/identity-store.md) |
| **persist**, `termiplus-persist` | an MCP role (Keycloak group) needed to *request* a persistent session; which workspaces a user sees is Entitlements | [Principal](objects/identity/principal.md) |
| **policy** (sandbox network) | `netpolicy.Policy`: DNS entries and egress allows, not a terminal Policy | [Network policy](objects/sandbox/network-policy.md) |
| **key** | six different secrets (gateway, control, admin, proxy token, key secret, plugin key); never interchangeable | [Service keys](objects/identity/service-keys.md) |
| **share** | an expiring view-only link to someone's desktop, not a file share | [Share](objects/control-plane/share.md) |
