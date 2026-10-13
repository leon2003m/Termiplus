# Deploying the alpha features

The alpha track adds the MCP control plane, desktop workspaces and the
management console on top of the hardened terminal stack. Everything is
opt-in through compose overlays; the base stack (`deploy/docker-compose.yml`)
is unchanged.

| Overlay | Adds |
|---|---|
| `docker-compose.mcp.yml` | MCP server + `mcp-auth` (oauth2-proxy, bearer tokens from Open WebUI) |
| `docker-compose.desktop.yml` | `desk-auth` (oauth2-proxy, browser sign-in) for `/stream/` (desktops) and `/ui/` (console); `fwd-auth` for port forwards on their own origin |
| `docker-compose.keycloak.yml` | Keycloak + Postgres, if you do not already run an OIDC provider |

## 1. Prepare (once)

```sh
WEBUI_URL=https://chat.example.com \
KEYCLOAK_PUBLIC_URL=https://sso.example.com \
DESKTOP_PUBLIC_URL=https://desk.example.com \
FORWARD_PUBLIC_URL=https://fwd.example.com \
deploy/setup-alpha.sh
sudo chown 65534:65534 deploy/mcp-data
```

`setup-alpha.sh` creates every missing secret under `deploy/secrets/` (it
never overwrites one), renders the Keycloak realm (clients `owui`,
`termiplus-mcp`, `termiplus-desktop`; groups `termiplus-users`,
`termiplus-persist`, `termiplus-admin`), writes `deploy/.env` and prints the
Open WebUI settings. It starts nothing.

## 2. TLS and names

Put your TLS reverse proxy in front of:

- `KEYCLOAK_PUBLIC_URL` → `127.0.0.1:8180`
- `DESKTOP_PUBLIC_URL` → `127.0.0.1:4181`
- `FORWARD_PUBLIC_URL` → `127.0.0.1:4182` (must be a different origin from the
  console: another host name or port)

Open WebUI and both oauth2-proxies must reach the issuer
(`KEYCLOAK_PUBLIC_URL/realms/termiplus`) under that same URL, because token
issuers are compared literally.

## 3. Desktops (optional)

Set `TERMINAL_IMAGE` to the desktop image (`image/desktop`) and add a policy with
`"desktop": true` to `deploy/policies.json`. The socket proxy allows exactly one
image; the desktop image runs Open Terminal like the terminal image and starts a
desktop only for desktop policies.

## 4. Start

```sh
cd deploy
docker compose -f docker-compose.yml -f docker-compose.mcp.yml \
  -f docker-compose.desktop.yml -f docker-compose.keycloak.yml up -d
sudo deploy/firewall.sh   # as for the base stack
```

If `mcp-auth` or `desk-auth` start before Keycloak has finished its first boot,
their OIDC discovery fails ("Bootstrap in progress") and they restart until it
answers; `docker restart` them to skip the back-off. Both run with
`--reverse-proxy`, so they trust `X-Forwarded-*` from any client that can reach
them: keep them reachable only from your TLS proxy (they listen on 127.0.0.1
and the Open WebUI network).

Rerun `firewall.sh` whenever the gateway container is recreated (new image or
config): the gateway gets a new MAC address, and until the firewall pins the
new one, terminals cannot be reached.

## 5. Open WebUI

Set the environment printed by `setup-alpha.sh` (OIDC client `owui`,
`OAUTH_MERGE_ACCOUNTS_BY_EMAIL=true` so existing accounts keep their id and
terminal homes, `OAUTH_SESSION_TOKEN_ENCRYPTION_KEY`) and restart Open WebUI.
Install `integrations/openwebui/termiplus_workspaces.py` as a tool and set its
valves `mcp_url`, `plugin_key` and `ui_url` (see its README).

## 6. Users

In the Keycloak admin console (realm `termiplus`) create users with verified
e-mail addresses and add them to `termiplus-persist` (persistent workspaces)
or `termiplus-admin` (console administration). Workspace entitlements are
edited in the console. Each user has to use the Open WebUI tool once before the
console can show their sessions (see `docs/mcp.md`, §8).

## Verification

- `test/integration/alpha-e2e.sh` deploys exactly this (all four overlays,
  `setup-alpha.sh`, Caddy as the TLS proxy) on a throwaway host and checks the
  plugin path, the browser sign-in, the console, the desktop stream and the
  shared home. Passed 2026-10-13 under Sysbox 0.7.1 and gVisor
  release-20261005.0 (21/21 each).

- `test/integration/oidc/run.sh` and `test/integration/oidc/desktop.sh` check
  the two oauth2-proxies against a real Keycloak.
- `.github/scripts/desktop-smoke.sh` checks the desktop image.
