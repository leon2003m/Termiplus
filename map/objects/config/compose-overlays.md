---
type: object
cluster: config
universe: live
status: verified
verified: 2026-10-19 @ a35e601
entity: deploy/docker-compose.yml
---

# Compose overlays

How a deployment is put together: one base compose file plus opt-in overlay files, each stacked with another `-f`. Every alpha feature is an overlay. Without it the base stays unchanged.

## Why this shape

The 0.x core (gateway plus sockproxy) must keep working for people who want only terminals. So every alpha feature (MCP, desktops, Keycloak, network policy) arrives as its own file, joins existing services through `!override`/merge, and its absence switches the feature off. For example, without the MCP overlay `/control/v1` returns 404.

## Shape

| File | Adds | Needs |
|---|---|---|
| `docker-compose.yml` | sockproxy, gateway; networks `ot-control` (internal), `ot-sandbox`, `owui` | `.env`: `TERMINAL_IMAGE`, `DOCKER_GID`; secrets `proxy_token`, `gateway_api_key`, `key_secret` |
| `docker-compose.separate-vm.yml` | gateway without `owui`, published on `GATEWAY_BIND` (default `127.0.0.1:8080`) | base |
| `docker-compose.netpolicy.yml` | `dns-forwarder`, admin netpolicy API | base; host `firewall.sh` |
| `docker-compose.mcp.yml` | `mcp`, `mcp-auth` (oauth2-proxy), gateway control key, `BACKUP_DIR` | base, an OIDC IdP |
| `docker-compose.desktop.yml` | `desk-auth`, `desk-stream-auth`, `fwd-auth`; console, stream and forward listeners | mcp |
| `docker-compose.keycloak.yml` | `keycloak`, `keycloak-db` | `setup-alpha.sh` once |
| `docker-compose.mcp-users.yml` | mcp ↔ Keycloak admin (`manage_user`, `manage_group`, departed) | mcp, keycloak |
| `docker-compose.mcp-netpolicy.yml` | mcp ↔ admin netpolicy API | netpolicy, mcp |
| `docker-compose.mqtt.yml` | gateway metrics to an MQTT broker | base |

Policy data lives in `deploy/policies.json` ([Policy](../workspaces/policy.md)). Secrets live in `deploy/secrets/` (gitignored). Host pieces (`firewall.sh`, systemd units, `daemon.json`, `sysctl.d/`) are installed outside compose.

Citations: `deploy/docker-compose.yml:1-20`, `:26`, `:55`, `:77`, `:87-102`, `deploy/docker-compose.separate-vm.yml:1-16`, `deploy/docker-compose.netpolicy.yml:1-6`, `deploy/docker-compose.mcp.yml:1-5`, `:47`, `:83`, `:114-118`, `deploy/docker-compose.desktop.yml:1-6`, `:56`, `:102`, `:150`, `deploy/docker-compose.keycloak.yml:1-6`, `deploy/docker-compose.mcp-users.yml:1-6`, `deploy/docker-compose.mcp-netpolicy.yml:1-6`, `deploy/docker-compose.mqtt.yml:1-5`

## Connected to

- **owns:** service wiring, networks, secret mounts, resource limits
- **owned-by:** the operator's `docker compose -f … up` line
- **joins:** every service; [Firewall chain](../sandbox/firewall-chain.md) (network names and addresses), [DNS forwarder](../sandbox/dns-forwarder.md), [Service keys](../identity/service-keys.md)
- **looks-like-but-is-not:** the per-user terminal containers, which the gateway creates through the sockproxy and compose never lists

## If you change this

- **Hits:** `deploy/firewall.sh` (subnets, fixed addresses such as `172.30.0.2`), README quick start, `test/integration/*` start lines, live deployments that stack these files
- **Does not hit:** Go code, unless an environment variable name changes

## Surfaces

| Surface | Role |
|---|---|
| operator | runs `docker compose` with the chosen overlays |
| `deploy/setup-alpha.sh` | writes secrets, renders the realm |
| CI integration tests | stack overlays per test |

## See

- Source: `deploy/`
- Effects: [effects/CONTEXT.md](../../effects/CONTEXT.md)
