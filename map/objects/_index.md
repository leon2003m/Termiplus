# Objects index

One line per noun. Read the line; open the card only if you need it. `stub` = no card yet, the line points at the code.

## identity

- [Principal](identity/principal.md) · verified · the caller as MCP sees it (Open WebUI id + OIDC subject + roles), set by oauth2-proxy headers; termiplus never verifies tokens itself
- [IdentityStore](identity/identity-store.md) · verified · OIDC subject → Open WebUI id, written once by the plugin, then immutable
- [Entitlements](identity/entitlements.md) · verified · which groups see which workspaces; fail closed, fatal if missing under `MCP_AUTH=proxy`
- [Service keys](identity/service-keys.md) · verified · the six secrets (gateway, control, admin, proxy token, key secret, plugin key); never interchangeable
- [Directory](identity/directory.md) · verified · Keycloak admin client for `termiplus-*` groups and users; never creates users

## workspaces

- [Policy](workspaces/policy.md) · verified · a "workspace": `policies.json` entry (runtime, limits, persistence, home group, desktop)
- [Terminal container](workspaces/terminal-container.md) · verified · a "session": `ot-u-…` persistent or `ot-e-…` per chat, created via sockproxy, reaped when idle
- [Home volume](workspaces/home-volume.md) · verified · `ot-home-…` at `/home/user`, per user and home group
- [Archive helper](workspaces/archive-helper.md) · verified · never-started `ot-a-…` container that mounts a home for tar export/import
- [Agent manifest](workspaces/agent-manifest.md) · verified · `~/AGENTS.md`, skills and `tp-*` tools that tell a model what its workspace is; guidance, not a boundary

## sandbox

- [Sockproxy rules](sandbox/sockproxy-rules.md) · verified · the only Docker socket holder; allow-list plus rebuilt create bodies
- [Network policy](sandbox/network-policy.md) · verified · admin DNS entries and internal egress allows; renders dnsmasq hosts and egress rules
- [Firewall chain](sandbox/firewall-chain.md) · verified · `deploy/firewall.sh` + systemd units: public yes, internal no, except the policy
- [DNS forwarder](sandbox/dns-forwarder.md) · verified · the only resolver terminals may use (dnsmasq on `ot-sandbox`)

## control-plane

- [MCP tools](control-plane/mcp-tools.md) · verified · the JSON-RPC tools for chat and console; gate, confirm, handle, audit
- [Share](control-plane/share.md) · verified · expiring view-only desktop link; token hash stored, viewers sign in
- [Departed](control-plane/departed.md) · verified · users disabled/deleted in Keycloak; archived after a delay, restored on return
- [Audit log](control-plane/audit-log.md) · verified · JSONL of changing tool calls (ok / error / denied)
- Stream and forward listeners · stub · desktop and port-forward proxies of the MCP server → `internal/mcp/stream.go`, `internal/mcp/forward.go`, `internal/gateway/stream.go`, `internal/gateway/forward.go`
- Management console · stub · `UI_LISTEN_ADDR`, same `invoke` as chat → `internal/mcp/ui.go`, `internal/mcp/ui/`
- StubBackend, `MCP_AUTH=dev` · stub · **leftover**: tests and local trials only → `internal/mcp/backend.go`, `cmd/mcp/main.go`

## storage

- [Backup](storage/backup.md) · verified · snapshots (pruned) and departed archives (kept) under `BACKUP_DIR`
- Data files of the MCP server · stub · identity, entitlements, shares, departed, audit: one JSON/JSONL file each under `/data` → `cmd/mcp/main.go`

## config

- [Compose overlays](config/compose-overlays.md) · verified · base compose + opt-in overlays; absence switches a feature off
- Environment settings · stub · every variable is read in one place → `cmd/gateway/main.go`, `cmd/mcp/main.go`, `cmd/sockproxy/main.go`, `internal/config/`
- Metrics and MQTT · stub · gateway counters on `METRICS_LISTEN_ADDR` and to an MQTT broker → `cmd/gateway/main.go:92-125`, `deploy/docker-compose.mqtt.yml`
- Images · stub · terminal and desktop images → `image/`, `.github/workflows/release.yml`
