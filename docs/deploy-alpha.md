# Deploying the alpha features

The alpha track adds the MCP control plane, desktop workspaces and the
management console on top of the hardened terminal stack. Everything is
opt-in through compose overlays; the base stack (`deploy/docker-compose.yml`)
is unchanged.

| Overlay | Adds |
|---|---|
| `docker-compose.mcp.yml` | MCP server + `mcp-auth` (oauth2-proxy, bearer tokens from Open WebUI) |
| `docker-compose.desktop.yml` | `desk-auth` (oauth2-proxy, browser sign-in) for `/ui/` (console); `desk-stream-auth` for `/share/` (image-only screen shares) on a separate origin |
| `docker-compose.keycloak.yml` | Keycloak + Postgres, if you do not already run an OIDC provider |

## 1. Prepare (once)

```sh
WEBUI_URL=https://chat.example.com \
KEYCLOAK_PUBLIC_URL=https://sso.example.com \
DESKTOP_PUBLIC_URL=https://desk.example.com \
DESKTOP_STREAM_URL=https://desktops.example.com \
deploy/setup-alpha.sh
sudo chown -R 65534:65534 deploy/mcp-data deploy/backups
```

`setup-alpha.sh` creates every missing secret under `deploy/secrets/` (it
never overwrites one), renders the Keycloak realm (clients `owui`,
`termiplus-mcp`, `termiplus-desktop`; groups `termiplus-users`,
`termiplus-persist`, `termiplus-admin`), writes `deploy/.env` and prints the
Open WebUI settings. It also creates `deploy/mcp-data/entitlements.json`, which
denies every workspace until an admin grants them in the console, and
`deploy/backups/` for home snapshots (mode 0700). It starts nothing.

## 2. TLS and names

Put your TLS reverse proxy in front of:

- `KEYCLOAK_PUBLIC_URL` → `127.0.0.1:8180`
- `DESKTOP_PUBLIC_URL` → `127.0.0.1:4181` (console)
- `DESKTOP_STREAM_URL` → `127.0.0.1:4183` (screen shares; must be
  a different origin from the console, preferably another host name: whoever
  runs code in a desktop controls what it serves, and that must never run on
  the console's origin. The MCP server refuses to start if the two match.)


Open WebUI and both oauth2-proxies must reach the issuer
(`KEYCLOAK_PUBLIC_URL/realms/termiplus`) under that same URL, because token
issuers are compared literally.

## 3. Desktop workspaces

Build `image/` first, then `image/desktop` with that base. Set `TERMINAL_IMAGE`
to the complete desktop image; the socket proxy must allow that image.
The shipped `deploy/policies.json` offers **Desktop** (`default`, persistent)
and **Temporary desktop** (`scratch`, per chat). Both use Sysbox and enable
native Desktop and Browser. Their memory/CPU/PID limits remain in effect.

The image includes XFCE, TigerVNC over its private Unix socket, Dolphin,
Kate, Chromium as its sole browser, audio support, the dark Termiplus theme,
and the Termiplus wallpaper. The native desktop starts on demand. Missing user
preferences are seeded when it starts; personal themes and wallpaper choices
are preserved. Chromium's sandbox stays enabled.

When consolidating an older installation, keep `default` and its existing home
volume name. `desk` and `sysbox` previously shared that home; back up container
root filesystems before removing those redundant policies. Back up ephemeral
workspaces before retiring `tempdesk` or recreating `scratch`; they have no home
volume. Update Open WebUI's existing `default`/`scratch` connection names to
Desktop/Temporary desktop and remove duplicate connections. Keep connection
credentials and access grants. No home volume needs renaming or deletion.

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

## 7. Native connection and migration

Configure a terminal connection to `http://gateway:8080/p/default` with the gateway
API key and appropriate Open WebUI connection access rules. Select it in the
chat, then open the native Desktop/Browser tabs. Use `/p/scratch` for Temporary
desktop. The assistant uses the same
connection's native tools. For ephemeral profiles use a saved chat and let native
requests create that chat's workspace; MCP-created ephemeral sessions are separate.

The console manages lifecycle, backups, access and screen shares. It no longer
opens its own interactive desktop or forwards arbitrary app ports. The retained
`DESKTOP_STREAM_URL` setting now means the **screen-share origin**. Remove old
forward-service settings and routes when upgrading. See [migration](mcp.md#migration-from-the-alpha-checkpoint).

## Verification

`make check` validates Go, plugin and configuration code. The native image smoke
test is `.github/scripts/desktop-smoke.sh IMAGE [RUNTIME]`. OIDC fixture tests
are in `test/integration/oidc/`; `alpha-e2e.sh` requires a dedicated throwaway
host and applies the host firewall. Historical alpha desktop results do not
establish native Desktop/Browser compatibility; report the actual checks run.
