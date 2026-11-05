# Deploying Termiplus

Release **1.0.0** includes the MCP control plane, native desktop workspaces and
management console on top of the hardened terminal stack. These components
are enabled through Compose overlays. This guide and `setup-alpha.sh` retain
their historical filenames for compatibility; they also apply to the stable
release. Follow the security guidance in the repository README.

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

For the published amd64 desktop, set the following in `deploy/.env` after
running setup:

```dotenv
TERMIPLUS_VERSION=1.0.0
TERMINAL_IMAGE=ghcr.io/leon2003m/termiplus-desktop:1.0.0
```

Prefer digest references from the release assets when pinning deployed images.
Alternatively, build `image/` first, then `image/desktop` with that base and set
`TERMINAL_IMAGE` to that complete desktop image. The socket proxy must allow
the same image; do not change connection keys or rename home volumes.
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
  -f docker-compose.desktop.yml -f docker-compose.keycloak.yml pull
# Use the same overlay list, including existing host/network policy overlays.
docker compose -f docker-compose.yml -f docker-compose.mcp.yml \
  -f docker-compose.desktop.yml -f docker-compose.keycloak.yml up -d --no-build
cd ..
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

### Host source-identity update

The original 1.0.0 release's MAC port locks alone do not prevent a Sysbox workspace from
claiming another workspace's IPv4 address in ARP. Install the updated host script
before allowing untrusted workspaces to run. Container image updates alone are
insufficient; no Open WebUI change is required. The republished 1.0.0 release
includes the fix; replace any digest pins from the withdrawn release with the
new release's digest assets.

The host needs `ebtables`, `flock`, and `lsns` with the `NETNSID` output column,
plus Linux and iproute2 6.2 or newer for locked bridge ports. Discovery matches
the host's `link-netnsid` to Docker's PID namespace using `lsns`; workspace-owned
interface names, indices and MAC addresses are not trusted. Failed or ambiguous
discovery leaves the affected ports denied.

During a maintenance window, stop untrusted workspace workloads, update the host
checkout to the fixed revision and use that checkout's paths below (adjust the
installed unit's `ExecStart` if necessary):

```sh
sudo deploy/firewall.sh
sudo cp deploy/termiplus-portlock.service deploy/termiplus-firewall-reload.service \
  deploy/termiplus-firewall.path deploy/termiplus-firewall-watch.service \
  deploy/termiplus-firewall-watch.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl restart termiplus-portlock.service
sudo systemctl enable --now termiplus-firewall-watch.timer
sudo systemctl enable --now termiplus-firewall.path
sudo systemctl restart termiplus-firewall.path
sudo deploy/firewall.sh --lock-ports
sudo deploy/firewall.sh --if-needed
```

Both final commands must succeed once containers have settled. Inspect
`ebtables -t nat -L PREROUTING` for a bridge-scoped `TERMIPLUS-IP-S*` guard on
`ot-sandbox` and `TERMIPLUS-IP-O*` on `ot-offline` when present. Each active
chain must contain exactly the resolved port/MAC/IPv4 and ARP sender tuples,
followed by an explicit `DROP`. Never flush an attached source guard to resolve
connectivity problems: inspect Docker/namespace discovery and watcher logs.
IP policy updates now render and validate complete IPv4 and IPv6 snapshots before
committing each family atomically. An interrupted update retains that family's
previous complete rules. Only Termiplus chains and hooks are replaced; Docker
and unrelated rules are preserved. A completion marker in
`/run/termiplus-portlock/policy.applied` binds full chain contents to the script,
egress file, upstreams and service identities. Missing rules or changed policy
inputs cause the timer to retry. Static denies remain installed when the gateway
is absent. The DNS forwarder may reach only validated upstreams on TCP/UDP 53;
admin exceptions do not widen its access.

The policy path now starts the repeatable `termiplus-firewall-reload.service`.
Installing that unit and restarting the path are necessary on existing hosts;
starting an already-active boot service does not reapply a changed policy.

If the bridge was already attacked, clear the affected workspace IP's neighbour
entry inside the gateway network namespace after installing the pins, or
recreate the gateway and reapply the firewall. That prevents an existing poisoned
entry from continuing to direct requests to the wrong port. Investigate any
suspected key disclosure and follow [the security policy](../SECURITY.md#workspace-source-identity).

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
