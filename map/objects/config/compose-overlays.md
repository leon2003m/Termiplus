---
type: object
cluster: config
universe: live
status: verified
verified: 2026-11-04 (native integration)
entity: deploy/docker-compose.yml
---

# Compose overlays

How a deployment is put together: one base compose file plus opt-in overlay files, each stacked with another `-f`. The control plane and its human UI are overlays. Native Desktop/Browser is
provided by the default image and policy flags without an overlay.

## Why this shape

The core gateway and sockproxy work without MCP or OIDC. The control plane,
console/share sign-in, Keycloak and network policy arrive as separate files
that join existing services through `!override`/merge. For example, without the MCP overlay `/control/v1` returns 404.

## Shape

| File | Adds | Needs |
|---|---|---|
| `docker-compose.yml` | sockproxy, gateway; networks `ot-control` (internal), `ot-sandbox`, `owui` | `.env`: `TERMINAL_IMAGE`, `DOCKER_GID`; secrets `proxy_token`, `gateway_api_key`, `key_secret` |
| `docker-compose.separate-vm.yml` | gateway without `owui`, published on `GATEWAY_BIND` (default `127.0.0.1:8080`) | base |
| `docker-compose.netpolicy.yml` | `dns-forwarder`, admin netpolicy API | base; host `firewall.sh` |
| `docker-compose.mcp.yml` | `mcp`, `mcp-auth` (oauth2-proxy), gateway control key, `BACKUP_DIR` | base, an OIDC IdP |
| `docker-compose.desktop.yml` | `desk-auth`, `desk-stream-auth`; console and image-only share listener | mcp |
| `docker-compose.keycloak.yml` | `keycloak`, `keycloak-db` | `setup-alpha.sh` once |
| `docker-compose.mcp-users.yml` | mcp ↔ Keycloak directory (role checks, recipient lookup, departed watcher) | mcp, keycloak |
| `docker-compose.mcp-netpolicy.yml` | mcp ↔ admin netpolicy API | netpolicy, mcp |
| `docker-compose.mqtt.yml` | gateway metrics to an MQTT broker | base |
| `docker-compose.offline.yml` | network `ot-offline` (internal, icc off), gateway at 172.30.1.2, `OFFLINE_NETWORK` for gateway and sockproxy | base; host `firewall.sh`; after separate-vm |

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
- **Automated edits:** the `image:` tag and `@sha256` digests of oauth2-proxy, keycloak and postgres in `docker-compose.{desktop,mcp,keycloak}.yml` are rewritten by Updatecli pull requests (`updatecli.d/docker-images.yaml`, weekly workflow `.forgejo/workflows/updatecli.yml`). Keep each `image:` line in the `name:tag@sha256:<64 hex>` form its patterns match.

## Surfaces

| Surface | Role |
|---|---|
| operator | runs `docker compose` with the chosen overlays |
| `deploy/setup-alpha.sh` | writes secrets, renders the realm |
| CI integration tests | stack overlays per test |

## See

- Source: `deploy/`
- Effects: [effects/CONTEXT.md](../../effects/CONTEXT.md)

## Native integration update (2026-11-04)

The desktop overlay now exposes only the human console and image-only share viewer. fwd-auth, the forwarding network/secrets/listener and owner /stream upstream are removed. STREAM_* and DESKTOP_STREAM_URL names remain for share configuration compatibility. Source: deploy/docker-compose.desktop.yml and cmd/mcp/main.go.

The shipped `default`/`scratch` policies both require the complete desktop image
and Sysbox. The optional desktop overlay adds the human console and screen
shares; native Desktop/Browser support does not depend on that overlay.
