# Deploying the alpha features

The alpha track adds the MCP control plane, desktop workspaces and the
management console on top of the hardened terminal stack. Everything is
opt-in through compose overlays; the base stack (`deploy/docker-compose.yml`)
is unchanged.

| Overlay | Adds |
|---|---|
| `docker-compose.mcp.yml` | MCP server + `mcp-auth` (oauth2-proxy, bearer tokens from Open WebUI) |
| `docker-compose.desktop.yml` | `desk-auth` (oauth2-proxy, browser sign-in) for `/stream/` (desktops) and `/ui/` (console) |
| `docker-compose.keycloak.yml` | Keycloak + Postgres, if you do not already run an OIDC provider |

## 1. Prepare (once)

```sh
WEBUI_URL=https://chat.example.com \
KEYCLOAK_PUBLIC_URL=https://sso.example.com \
DESKTOP_PUBLIC_URL=https://desk.example.com \
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

- `test/integration/oidc/run.sh` and `test/integration/oidc/desktop.sh` check
  the two oauth2-proxies against a real Keycloak.
- `.github/scripts/desktop-smoke.sh` checks the desktop image.
