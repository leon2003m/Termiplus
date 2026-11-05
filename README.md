# termiplus

[![CI](https://github.com/leon2003m/termiplus/actions/workflows/ci.yml/badge.svg?branch=dev)](https://github.com/leon2003m/termiplus/actions/workflows/ci.yml)
[![Security](https://github.com/leon2003m/termiplus/actions/workflows/security.yml/badge.svg?branch=dev)](https://github.com/leon2003m/termiplus/actions/workflows/security.yml)
[![License: GPL-3.0](https://img.shields.io/badge/license-GPL--3.0-blue.svg)](LICENSE)

> [!WARNING]
> **Experimental. Do not use in production.** termiplus is a personal project and test environment. Most of the code was written with AI assistance and has not been independently security reviewed, so expect serious security bugs. It runs untrusted code and controls Docker on the host, so one such bug can expose the host, other users' files or your network. Run it only on a machine you can wipe, keep it off the public internet, and keep real users and real data away from it. Found a problem? See [SECURITY.md](SECURITY.md).

Per-user Open Terminal containers for Open WebUI. Every user gets an isolated desktop workspace with browser, files and terminal, started on demand and stopped when idle. Users can pick a **persistent** desktop (home directory survives) or a **temporary desktop** per chat. A self-hosted replacement for the paid Open WebUI "Terminals" orchestrator.

## Focus since Open WebUI 0.13

Open WebUI's native workspace now provides Desktop, Browser, files, Git,
terminals and app previews through Open Terminal 0.16. Termiplus focuses on
isolating and routing each user's workspace, its lifecycle, persistent homes,
backups and access management. **KasmVNC and our separate interactive desktop
player and port-forwarding UI have been removed.**

Choose **Desktop** or **Temporary desktop** in Open WebUI; both include the same
native desktop support. The human management console and expiring view-only
screen shares remain. See [native integration](docs/native-integration.md) and
[tool validation](docs/native-tool-validation.md).

Termiplus plugs into a normal terminal-server connection without an Open WebUI
core fork. It supports **23 native model tools**, plus **8 optional Workspaces
plugin methods** (seven backed by MCP, one console link). The gateway does
annotate Desktop descriptions in `/openapi.json`, and the workspace image has
desktop/input wrappers. See [supported tools and maintained adaptations](docs/tool-support.md)
for the exact names, prerequisites and integration boundaries.

```
                        Bearer key + X-User-Id (+ X-Session-Id)
Open WebUI backend ───────────────────────────────▶ gateway :8080
                                                     │   │
                     per-user key, HTTP + WebSocket  │   │  filtered Docker API
                                                     ▼   ▼
                  ot-u-<user>-<policy>  (persistent)    sockproxy :2375 ──▶ /var/run/docker.sock
                  ot-e-<user>-<chat>-<policy>  (ephemeral)
                  Open Terminal under gVisor or Sysbox, network ot-sandbox
```

| Component | What it does |
|---|---|
| `cmd/gateway` | Checks Open WebUI's key, routes each request to that user's container (creating or starting it), proxies HTTP and the interactive terminal WebSocket, stops or deletes idle containers |
| `cmd/sockproxy` | Deny-by-default filter in front of the Docker socket. Only lets the gateway create and manage terminal containers, nothing else |
| `image/` | Base image: Debian, common dev tools, passwordless sudo, Open Terminal server |
| `image/desktop/` | Complete default workspace: XFCE, Chromium, Dolphin, office tools, Python/notebooks and Docker inside Sysbox |
| `cmd/mcp` | Workspace lifecycle/storage tools, human management console and view-only screen shares |
| `deploy/` | Compose file, policies, Docker runtime config, firewall script |

Go standard library only, no third-party modules.

## Layout

```
cmd/gateway/main.go         gateway entrypoint (env config)
cmd/sockproxy/main.go       socket proxy entrypoint (env config)
internal/gateway/           routing, lifecycle, idle reaper, WebSocket proxy, e2e tests
internal/sockproxy/         request filter and body validation, escape-attempt tests
cmd/mcp/main.go            MCP control plane entrypoint
internal/mcp/              tools, identity, entitlements, console and screen shares
internal/dockerapi/         minimal Docker API client used by the gateway
internal/naming/            container/volume naming shared by gateway and proxy
internal/config/            env and *_FILE secret helpers
image/Dockerfile            base terminal image
image/desktop/             complete default workspace image
Dockerfile                  gateway and sockproxy images (--target)
deploy/                     docker-compose.yml, policies.json, daemon.json, firewall.sh,
                            termiplus-firewall-watch.{service,timer}, termiplus-firewall.path,
                            termiplus-firewall-reload.service,
                            termiplus-portlock.service,
                            docker-compose.netpolicy.yml (opt-in), dns-forwarder/,
                            docker-compose.mcp-netpolicy.yml (opt-in: human console network policy),
                            docker-compose.mcp-users.yml (opt-in: identity directory backend),
                            docker-compose.offline.yml (opt-in: offline workspaces)
internal/netpolicy/         admin network policy: validation, persistence, config rendering
```

## Release images

The current stable release is **1.0.0** on `main`, targeting Open WebUI 0.13.0
and Open Terminal 0.16.0. See [the changelog](CHANGELOG.md#100---2026-11-05)
and [deployment guide](docs/deploy-alpha.md) for installation and upgrade notes.
The project security warning above continues to apply.

For an installation using the published images, set these in `deploy/.env`:

```dotenv
TERMIPLUS_VERSION=1.0.0
TERMINAL_IMAGE=ghcr.io/leon2003m/termiplus-desktop:1.0.0
```

Pin images to the digest references attached to the release for reproducible
rollouts. The complete desktop requires amd64 and Sysbox; service and base
terminal images also support arm64. Use Compose with `--no-build` when deploying
published images. The local builds below are an alternative for development.

## Quick start (dedicated VM)

Requirements: Linux VM, Docker 25+ and Sysbox for the bundled desktop policies.
Use Open WebUI 0.13+ and Open Terminal 0.16.0. Go 1.24+ is needed for Go checks.

```bash
# 1. Runtime: install Sysbox for the bundled desktop policies, then
sudo cp deploy/daemon.json /etc/docker/daemon.json   # or merge into your existing one
sudo systemctl restart docker
docker info | grep -i runtimes                       # must list sysbox-runc for the bundled policies
#    deploy/daemon.json starts runsc with --allow-suid, without it sudo inside gVisor fails
#    (gVisor ignores setuid bits by default). If you merge by hand, keep that runtimeArgs entry.
#    gVisor terminals have no DNS without the netpolicy overlay (internal DNS forwarder, see
#    "Custom DNS and internal egress" below): deploy it whenever a policy uses runsc. Sysbox
#    terminals resolve with or without it.

# 2. Build the base and complete workspace (pin your deployed image by digest)
docker build -t ot-terminal:0.16.0 image/
docker build --build-arg BASE=ot-terminal:0.16.0 -t ot-desktop:0.16.0 image/desktop

# 3. Secrets, each 64 hex chars. gateway and sockproxy run as uid/gid 65534 and
#    read them through Compose's file secrets, which keep the host file mode:
#    readable for you and group 65534 only, independent of your umask.
mkdir -p deploy/secrets
for s in proxy_token gateway_api_key key_secret; do
  openssl rand -hex 32 | sudo install -m 0640 -o "$USER" -g 65534 /dev/stdin deploy/secrets/$s
done

# 4. Settings for compose (deploy/.env is gitignored)
cat > deploy/.env <<ENV
TERMINAL_IMAGE=ot-desktop:0.16.0
DOCKER_GID=$(stat -c %g /var/run/docker.sock)
ENV

# 5a. Open WebUI on the same host, sharing the "owui" Docker network:
docker network create owui        # skip if Open WebUI already has one; set OWUI_NETWORK
cd deploy && docker compose up -d --build

# 5b. Recommended: termiplus on its own VM, gateway published on a private address:
cd deploy && GATEWAY_BIND=10.20.30.40:8080 \
  docker compose -f docker-compose.yml -f docker-compose.separate-vm.yml up -d --build

# 6. Egress rules. They must be reapplied after every reboot, Docker restart and
#    restarted gateway (its MAC and bridge port are pinned). The watch timer checks every 20 s and
#    reapplies only when something is missing or stale (adjust ExecStart if the
#    repo is not in /opt/termiplus):
sudo ./firewall.sh
sudo cp termiplus-firewall-watch.service termiplus-firewall-watch.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now termiplus-firewall-watch.timer
#    Terminal identity: pin each port to its Docker MAC and IPv4/ARP source,
#    deny unresolved ports, and keep MAC learning disabled (needs Linux and
#    iproute2 >= 6.2):
sudo cp termiplus-portlock.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now termiplus-portlock.service
```

7. Open WebUI → Admin Settings → Integrations → Open Terminal → add a connection:
   - URL `http://gateway:8080`, auth type **Bearer**, key = contents of `deploy/secrets/gateway_api_key`
   - Optionally a second one, `http://gateway:8080/p/scratch`, named "Temporary desktop"
   - Restrict who sees which with access grants

With 5a, Open WebUI must run in the `owui` network. With 5b the gateway speaks plain HTTP on `GATEWAY_BIND`: put it on a private interface (VLAN, WireGuard) or behind a TLS reverse proxy, and allow only the Open WebUI host in the VM and Proxmox firewalls. Never expose the gateway key to browsers.

### Container hardening

| Service | User | Filesystem | Capabilities | Other |
|---|---|---|---|---|
| gateway | 65534 (nobody) | read-only, image is `scratch` with one static binary | all dropped | `no-new-privileges`, memory/CPU/PID limits, healthcheck |
| sockproxy | 65534 plus the socket's group (`DOCKER_GID`), not root | read-only, `scratch` | all dropped | `no-new-privileges`, limits, internal network only |
| terminals | 1000 with sudo inside the sandbox (gVisor: needs `--allow-suid`, root of the sandbox kernel only) | writable | Docker defaults | gVisor or Sysbox, default seccomp/AppArmor (overrides refused by the proxy), memory/CPU/PID limits, no ports, sandbox network behind `firewall.sh` |

## How it works with Open WebUI

Open WebUI's admin terminal connection already sends who the user is:

- HTTP: `Authorization: Bearer <connection key>` and `X-User-Id`, plus `X-Session-Id` (the chat) when there is one
- PTY/Desktop/events WebSocket: identity/context headers, then a first message `{"type":"auth","token":"<connection key>"}`; native chat-scoping fields are checked consistently
- Native Browser capability WebSocket: connection bearer and identity/context headers; application frames pass through without an injected Desktop auth message

The gateway verifies the connection key, then calls the user's container with that container's own key (HMAC of `KEY_SECRET` and the container name). For first-frame-authenticated WebSockets it reads the auth message, checks it, and resends it upstream with the container key. Browser capability relays use the matching upstream header-authentication protocol. No container is started before authentication succeeds.

Open WebUI also sends a few requests without a user (connection check, OpenAPI spec, system prompt). Those go to a "spec" container shared only by policies with the same runtime, image, egress and Desktop/Browser flags, and only `GET /openapi.json`, `/api/config` and `/system` are allowed. `/health` is answered by the gateway. `/api/v1/*` returns 404, so Open WebUI treats the gateway as a normal terminal, not as an orchestrator. Desktop descriptions in the returned OpenAPI schema are annotated as documented in the [integration contract](docs/tool-support.md).

## Native Desktop and Browser

Use Open WebUI **0.13+** and the bundled Open Terminal **0.16.0** image. Native
terminal connections provide Desktop, Browser, files, Git, events and local app
previews. Termiplus supplies isolation, user/chat routing, homes and lifecycle.
Both shipped policies include desktop and browser support. Select the workspace's connection in the chat; human and assistant then use
the same native display and tools. MCP provisioning does not switch connections.

The human console retains backups, lifecycle, access and expiring image-only
screen shares. AI tools cannot administer network policy/users/groups or delete,
restore or prune snapshots. The legacy Kasm player and separate port-forwarding
service are removed. See [native integration](docs/native-integration.md),
[deployment](docs/deploy-alpha.md) and [MCP](docs/mcp.md).

## Policies and storage profiles

`deploy/policies.json` defines environments. Each policy has its own URL prefix and becomes one connection in Open WebUI:

| URL | Policy | User gets |
|---|---|---|
| `http://gateway:8080` | `default` | **Desktop**, persistent home, Sysbox |
| `http://gateway:8080/p/scratch` | `scratch` | **Temporary desktop**, a fresh workspace per chat, Sysbox |

Both include XFCE, Dolphin, Chromium, Kate, the Termiplus theme and wallpaper,
and Chromium for native Browser automation. Docker, Compose and Buildx are
preinstalled; Sysbox runs a private Docker daemon inside each workspace.
Scientific Python, Jupyter, LibreOffice, PDF/media converters and standard CLI
utilities are included too; see the [image comparison](docs/open-terminal-image-review.md).
The base `image/` is a build dependency; deploy the complete `image/desktop` image.
There are no separate terminal-only choices in the shipped configuration.

```json
{
  "default": { "runtime": "sysbox-runc", "memory_mb": 2048, "cpus": 2, "pids": 512,
               "desktop": true },
  "scratch": { "runtime": "sysbox-runc", "memory_mb": 2048, "cpus": 2, "pids": 512,
               "desktop": true, "storage": "ephemeral", "idle_timeout": "15m", "max_per_user": 3 }
}
```

| Field | Meaning |
|---|---|
| `runtime`, `memory_mb`, `cpus`, `pids` | Required. Must stay within the proxy's caps |
| `label`, `description` | Optional. Name and short help text shown in the console and by `list_workspaces` (at most 60 and 300 bytes, one line). Changing them does not recreate containers |
| `desktop` | Enables native Desktop and Browser; true in both shipped policies |
| `storage` | `persistent` (default) or `ephemeral` |
| `home` | Persistent only. Policies with the same `home` share the user's volume |
| `idle_timeout` | Overrides `IDLE_TIMEOUT` for this policy, minimum `1m` |
| `max_per_user` | Ephemeral only. Concurrent sandboxes per user, default 3 |
| `image` | Optional. Runs this policy on another image than `TERMINAL_IMAGE`, e.g. a Python or Node image. The socket proxy must list it in `EXTRA_IMAGES` |
| `egress` | `internet` (default) or `offline`: no internet, no LAN, no DNS. Needs the offline overlay, see *Offline workspaces* |

**Persistent.** One container per user and policy (`ot-u-<user>-<policy>`) with a volume (`ot-home-<user>-<home>`) at `/home/user`. Idle containers are stopped, not deleted, and start again on the next request. Changing the image reference, a policy or `KEY_SECRET` recreates the container on its next use; the volume stays. A new image under the same tag (after a pull or rebuild) is picked up when a stopped workspace starts again: it is recreated on the new image with the same volume. A running workspace is never recreated for that. Anything installed outside `/home/user` is lost on recreation.

**Ephemeral.** One container per user, chat and policy (`ot-e-<user>-<chat>-<policy>`), no volume. Files live in the container and disappear with it. Docker deletes it as soon as it stops (`AutoRemove`, forced by the proxy). The same chat keeps its sandbox until it goes idle, then gets a fresh one. At `max_per_user`, the user's least recently used sandbox without an open terminal is deleted; if all are in use, the request gets 429. Requests without a chat id share one `nochat` sandbox per user. Open WebUI's file browser sends the chat id only once the chat is saved (after the first message): in a new chat it shows the `nochat` sandbox, while the model's first tool call already gets the chat's own sandbox.

### More than one image

Each policy may name its own `image`, for example a data-science image next to the default terminal:

```json
"python": { "runtime": "runsc", "memory_mb": 4096, "cpus": 2, "pids": 512,
            "home": "default", "image": "ghcr.io/you/termiplus-python:1.0" }
```

List every such image in the socket proxy's `EXTRA_IMAGES` (comma-separated, exact references, ideally pinned by digest); the proxy refuses any other. Build them `FROM` the termiplus terminal image (`image/Dockerfile`) so Open Terminal, the start script and the user stay the same. Policies with the same `home` share the user's files across images. Changing a policy's image recreates its containers on their next use; the home stays. Policies without `image` keep using `TERMINAL_IMAGE` and are not recreated by this feature.

### Offline workspaces

A policy with `"egress": "offline"` runs its terminals on a second network, `ot-offline`, that has no route out at all: no internet, no LAN, no DNS, no host services. Only the gateway can reach them, so Open WebUI, the file browser and the console keep working. Useful for untrusted code or data that must not leave the machine. Docker's embedded resolver (127.0.0.11) does not forward outside names on an internal network: a lookup there returns SERVFAIL, so DNS cannot be used as a side channel either.

```json
"offline": { "runtime": "runsc", "memory_mb": 2048, "cpus": 2, "pids": 512, "egress": "offline" }
```

Add `deploy/docker-compose.offline.yml` (after `docker-compose.separate-vm.yml` if you use it) and re-run `deploy/firewall.sh`. The network is a Docker internal network with `enable_icc:false`; `firewall.sh` drops everything on its bridge except gateway → terminal, with the gateway's address pinned to its MAC and its bridge port. Root inside a sandbox can forge its IP or MAC but cannot move its container to another bridge. Without the overlay, a policy with `"egress": "offline"` stops the gateway from starting; it never falls back to the internet. Switching a policy between `internet` and `offline` recreates its containers on their next use (the home stays), and a container found on the wrong network, or on any extra one, is recreated instead of reused; a running one that gains an extra network (`docker network connect` on the host) is removed within a minute. Installing packages in an offline workspace does not work; build them into an image (`image`) instead.

### Disk quota for home volumes

Set `HOME_VOLUME_SIZE` on the socket proxy (for example `20G`; units `M`, `G`, `T`, at least `64M`) and every **new** home volume gets that hard limit. A full home then fails with "No space left on device" inside the workspace instead of filling the host. Verified under gVisor, Sysbox and runc. The proxy adds the limit itself, so the gateway cannot ask for a different size or other driver options.

Docker enforces it with XFS project quotas, so `/var/lib/docker/volumes` must be on XFS mounted with `prjquota`. Docker checks this when it starts. Without it, Docker refuses to create the volume and the workspace does not start ("quota size requested but no quota support"); the limit is never skipped silently. If the host is not on XFS, a loopback file works on any filesystem (sparse, so it only uses what is written):

```bash
sudo systemctl stop docker.socket docker
sudo touch /var/lib/termiplus-volumes.xfs
sudo chattr +C /var/lib/termiplus-volumes.xfs 2>/dev/null || true   # btrfs: no copy-on-write for the image
sudo truncate -s 500G /var/lib/termiplus-volumes.xfs                 # upper bound for all volumes together
sudo mkfs.xfs -q /var/lib/termiplus-volumes.xfs
sudo mv /var/lib/docker/volumes /var/lib/docker/volumes.old
sudo mkdir /var/lib/docker/volumes
echo '/var/lib/termiplus-volumes.xfs /var/lib/docker/volumes xfs loop,prjquota 0 0' | sudo tee -a /etc/fstab
sudo mount /var/lib/docker/volumes
sudo cp -a /var/lib/docker/volumes.old/. /var/lib/docker/volumes/
sudo systemctl start docker
```

This moves **all** Docker volumes on the host, not only termiplus ones. Remove `volumes.old` once everything works.

Limits:

- Existing volumes keep no limit: Docker sets the quota only when it creates a volume. To limit an existing home, copy its contents into a new volume (remove the workspace container first, then the old volume).
- Ephemeral sandboxes and the container layer of persistent workspaces (everything outside `/home/user`) are not covered.
- `df` inside the workspace shows the whole filesystem, not the quota.

### Disk quota for everything else (container layer)

`CONTAINER_ROOTFS_SIZE` (same format, e.g. `10G`) caps each terminal's writable container layer: everything outside `/home/user`, and all of an ephemeral sandbox. The proxy forces it as `HostConfig.StorageOpt` on every container it creates. Docker supports this with the `overlay2` storage driver when its data root (`/var/lib/docker`) is on XFS mounted with `pquota`; otherwise it refuses to create the container, so again nothing runs without the limit. The loopback-XFS recipe above works for the whole data root too (mount it at `/var/lib/docker` instead of `/var/lib/docker/volumes`, with `pquota`), but it moves all images and containers, and Docker installations that use the containerd image store may not support the option at all.

Verified in a VM with Docker 26 and `overlay2` on XFS: writes stop at the limit with "No space left on device" under gVisor (its root overlay is file-backed, `overlay2=root:self`), Sysbox and in ephemeral sandboxes. Changing the setting applies to containers created afterwards.

## What the socket proxy allows

Everything not listed is 403.

| Call | Restriction |
|---|---|
| `GET /_ping`, `/version` | none |
| `GET /containers/json` | label filter forced to termiplus containers |
| `POST /containers/create` | Name must be `ot-u-...` or `ot-e-...`. Body decoded strictly (unknown fields rejected) and rebuilt from checked values. Only the configured image, a runtime from the allowlist (runc refused), the sandbox network, capped memory/CPU/PIDs. Persistent: exactly one volume at `/home/user` belonging to the same user. Ephemeral: no mounts, `AutoRemove` forced. Env only `OPEN_TERMINAL_*`. `Privileged:false` forced. No ports, binds, capabilities, devices, security options, namespaces |
| `POST /volumes/create` | Name `ot-home-...`, local driver, no driver options from the caller (blocks `o=bind,device=/`). With `HOME_VOLUME_SIZE` the proxy adds exactly `size=<that value>` |
| `GET /volumes/<name>` | termiplus volumes only |
| `DELETE /volumes/<name>` | Only with `ALLOW_HOME_REMOVE=1` (off by default), `ot-home-...` only, never forced: Docker refuses a volume any container still uses |
| `GET /images/<image>/json` | exactly `TERMINAL_IMAGE` or an `EXTRA_IMAGES` entry, no query. The proxy answers with the image ID only; labels, environment and history are not passed on |
| `GET .../json`, `POST .../start`, `POST .../stop`, `DELETE` | Target resolved by the daemon first, must carry the termiplus label and name, then addressed by full ID. `DELETE` without `v` |
| `GET`/`PUT .../archive?path=/home/user` | Only on archive helpers (`ot-a-...`): containers that mount exactly one home of the same user and that the proxy never starts. Exactly that path. Uploaded tars are rewritten entry by entry: only files, directories, symlinks and hard links with clean relative names, nothing below a symlink of the same archive, no device files, no extended attributes or setuid bits |
| `GET /termiplus/stats` | The proxy's own allowed/denied/failed counters (for the gateway's metrics) |

Containers are created with the configured image(s) only; `CONTAINER_ROOTFS_SIZE` adds a forced `StorageOpt` size. Archive helpers are created like persistent terminals but must mount the home they are named after.

Also rejected: `exec`, `attach`, any other image call, image pulls, builds, connection upgrades, encoded paths, `..`, `//`, missing or duplicate `X-Proxy-Token`.

## Network isolation

`deploy/firewall.sh` adds iptables rules for the `ot-sandbox` bridge (172.30.0.0/24):

- terminals reach the internet
- terminals cannot reach each other, private ranges (10/8, 172.16/12, 192.168/16, 100.64/10, 169.254/16) or the VM itself
- the gateway (172.30.0.2) can reach every terminal
- every container on `ot-sandbox` and `ot-offline` is bound to its Docker-assigned
  bridge port, MAC and IPv4/ARP sender address before bridge learning; unknown
  ports are denied, preventing one workspace from claiming another's address
- no IPv6: v6 is dropped on FORWARD, INPUT and (via the v6 bridge-netfilter sysctl) container-to-container, so terminals have no IPv6 to each other, the host or beyond

The source guards require the host script and both firewall/port watcher services;
container image updates alone do not install them. See the
[host source-identity update](docs/deploy-alpha.md#host-source-identity-update).

Host-wide effect: the script loads `br_netfilter` and sets `net.bridge.bridge-nf-call-iptables=1`, so traffic on **every** Linux bridge of the host (VM bridges, LAN bridges) passes iptables `FORWARD` from then on, not only `ot-sandbox`. That is why termiplus expects a dedicated VM. On a shared host check your `FORWARD` policy and other bridges first.

## Custom DNS and internal egress (admin network policy)

An optional, opt-in feature lets an operator set, at runtime and without a redeploy:

- **custom DNS entries** (internal names, split-horizon) resolvable inside terminals, and
- **per-destination internal egress**: allow a terminal to reach one internal service (e.g. a database at `10.0.0.5:5432`).

The egress model is unchanged for the internet and tightened for everything else: **public egress stays allowed by default; private/internal destinations are denied by default** and opened only by an explicit entry. A **fixed deny-list can never be opened** by any entry — loopback, link-local including the metadata address `169.254.169.254`, the CGNAT range, the sandbox subnet itself (other terminals, the gateway, the forwarder, the host bridge) and the offline subnet `172.30.1.0/24`.

How it fits together:

- An internal **DNS forwarder** (`dnsmasq`) forwards to *your* resolver (`DNS_UPSTREAM`, never a hardcoded public one) and serves the custom records. The gateway renders the records into the forwarder's watched `hostsdir`, which reloads automatically. Terminals are pinned to this forwarder via `HostConfig.Dns`; the socket proxy accepts no other resolver address. This is also what gives terminals a working resolver under gVisor.
- The gateway renders the egress allowances into `deploy/netpolicy/egress.rules`; `firewall.sh` turns each validated line into a `RETURN` rule placed **after** the fixed deny-list drops, so no entry can reach a never-allowed range. `termiplus-firewall.path` starts the repeatable `termiplus-firewall-reload.service` when the file changes; the boot service may already be active.

Enable it (adds the overlay to either base compose):

```sh
# in deploy/
mkdir -p netpolicy && sudo chown 65534:65534 netpolicy       # the gateway writes here
openssl rand -hex 32 > secrets/admin_api_key                 # distinct from gateway_api_key
echo "DNS_UPSTREAM=192.168.1.1" >> .env                       # your resolver

docker compose -f docker-compose.yml -f docker-compose.netpolicy.yml up -d

sudo cp termiplus-firewall.path termiplus-firewall-reload.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now termiplus-firewall.path
sudo systemctl restart termiplus-firewall.path   # reload an existing installation's target
```

The admin API lives on the gateway and is gated by `ADMIN_API_KEY` on the `X-Admin-Key` header (never the connection key; it must differ). It returns 404 unless the key and store are configured.

```sh
# read the current policy
curl -H "X-Admin-Key: $ADMIN_API_KEY" http://gateway:8080/admin/netpolicy

# set DNS records and internal egress (a rejected policy changes nothing)
curl -X PUT -H "X-Admin-Key: $ADMIN_API_KEY" http://gateway:8080/admin/netpolicy -d '{
  "dns_entries":  [{"name": "db.internal", "ip": "10.0.0.5"}],
  "egress_allow": [{"cidr": "10.0.0.5/32", "port": 5432, "proto": "tcp"}]
}'
```

`cidr` is a single host (`/32`) or a subnet; `port` `0` means every port; `proto` is `tcp` or `udp`. An entry overlapping the deny-list is refused with a 400 and nothing changes.

### Back up, restore and delete homes (admin API)

With the admin API on, an operator can copy a user's home out and back in, for backups or moving a user, and delete it:

```sh
# the home "default" of Open WebUI user <id> as a tar (entries start with user/)
curl -H "X-Admin-Key: $ADMIN_API_KEY" -H "X-User-Id: <id>" \
     http://gateway:8080/admin/homes/default/archive -o home.tar
# put it back (creates the home if needed; the user's workspaces using it are stopped first)
curl -X PUT --data-binary @home.tar -H "X-Admin-Key: $ADMIN_API_KEY" -H "X-User-Id: <id>" \
     http://gateway:8080/admin/homes/default/archive
# delete the user's containers using the home and the home itself (needs ALLOW_HOME_REMOVE=1 on the proxy)
curl -X DELETE -H "X-Admin-Key: $ADMIN_API_KEY" -H "X-User-Id: <id>" http://gateway:8080/admin/homes/default
```

The gateway reads and writes the home through a short-lived archive helper container that mounts only that volume and is never started, so a process in the running workspace cannot interfere with the copy; a running workspace keeps running during a backup (files changed meanwhile may be caught half-written). A restore adds and overwrites files; files that are not in the archive stay. A tar that does not come from a backup is checked by the proxy (see the table above).

## Metrics and MQTT

The gateway counts what it runs and does, without user ids or names: running and stopped workspaces, running sandboxes, open sessions, users with something running, starts, failed requests, idle stops, deleted sandboxes, refusals at `MAX_RUNNING`, image-update recreations, and the socket proxy's allowed, denied and failed calls.

- **Prometheus:** set `METRICS_LISTEN_ADDR` (e.g. `:9100`) and scrape `/metrics` on that port. It is a separate listener; publish it only to your monitoring network.
- **MQTT:** add `deploy/docker-compose.mqtt.yml` with `MQTT_URL` (e.g. `mqtt://broker.local:1883`), optional `MQTT_USERNAME` and `deploy/secrets/mqtt_password`. The gateway publishes the numbers as JSON to `termiplus/state` (retained, every `MQTT_INTERVAL`, default 30s) and `online`/`offline` to `termiplus/status` (also its last will). Set `MQTT_DISCOVERY_PREFIX` to your hub's discovery prefix to also send MQTT discovery messages; the values then appear as sensors of a "termiplus" device. Off by default. `mqtts://` uses TLS; `MQTT_CA_FILE` adds your own CA. The overlay gives the gateway a network with a route out to reach the broker.

## Settings

| Variable | Used by | Default |
|---|---|---|
| `TERMINAL_IMAGE` | both | required, must be identical in both |
| `SANDBOX_NETWORK` | both | `ot-sandbox` |
| `PROXY_TOKEN` / `PROXY_TOKEN_FILE` | both | optional, recommended |
| `ALLOWED_RUNTIMES` | sockproxy | `runsc,sysbox-runc` |
| `MAX_MEMORY_MB`, `MAX_CPUS`, `MAX_PIDS` | sockproxy | `8192`, `4`, `2048` |
| `EXTRA_IMAGES` | sockproxy | empty; further images policies may name with `image`, comma-separated |
| `CONTAINER_ROOTFS_SIZE` | sockproxy | empty (no limit); cap on each terminal's container layer, e.g. `10G` |
| `ALLOW_HOME_REMOVE` | sockproxy | empty; `1` lets the gateway delete home volumes |
| `EXPORT_TIMEOUT` | gateway | `2h`; upper bound for one home export (download, snapshot, archive) |
| `EXPORT_MAX_BYTES` | gateway | derived from `HOME_VOLUME_SIZE` (quota + 1/8 + 64M), else `0` (no cap); caps both raw tar bytes and cumulative logical member sizes, e.g. `25G`. Oversized members are refused at their header; sparse holes count at full size despite barely charging the home quota |
| `EXPORT_CONCURRENCY` | gateway | `4`; home exports running at once across all users, further users wait within their deadline. Each user may have only one running or queued export; overlapping requests return 429 |
| `METRICS_LISTEN_ADDR` | gateway | empty (off); Prometheus `/metrics` listener, e.g. `:9100` |
| `MQTT_URL`, `MQTT_USERNAME`, `MQTT_PASSWORD` / `_FILE`, `MQTT_CLIENT_ID`, `MQTT_TOPIC_PREFIX`, `MQTT_DISCOVERY_PREFIX`, `MQTT_INTERVAL`, `MQTT_CA_FILE` | gateway | MQTT off unless `MQTT_URL` is set; defaults `termiplus-gateway`, `termiplus`, empty (no discovery), `30s` |
| `HOME_VOLUME_SIZE` | sockproxy | empty (no limit); quota for new home volumes, e.g. `20G` (needs XFS project quotas, see *Disk quota for home volumes*) |
| `DOCKER_SOCKET` | sockproxy | `/var/run/docker.sock` |
| `GATEWAY_API_KEY` / `_FILE` | gateway | required, min. 32 chars |
| `KEY_SECRET` / `_FILE` | gateway | required, min. 32 chars |
| `POLICIES_FILE` | gateway | one `default` gVisor policy |
| `DOCKER_API` | gateway | `http://sockproxy:2375` |
| `IDLE_TIMEOUT` | gateway | `30m` |
| `MAX_RUNNING` | gateway | `50` running containers in total |
| `START_TIMEOUT` | gateway | `90s` |
| `TERMINAL_PORT` | gateway | `8000` |
| `ALLOWED_DNS` | both | empty; the forwarder address terminals are pinned to (set by the netpolicy overlay) |
| `OFFLINE_NETWORK` | both | empty; the internal-only network for `"egress": "offline"` policies (set by the offline overlay to `ot-offline`) |
| `ADMIN_API_KEY` / `_FILE` | gateway | empty (admin API off); min. 32 chars, must differ from `GATEWAY_API_KEY` |
| `NETPOLICY_FILE`, `NETPOLICY_DNSMASQ_FILE`, `NETPOLICY_EGRESS_FILE` | gateway | under `/data/netpolicy/` |
| `DNS_UPSTREAM` | dns-forwarder | required with the overlay; your resolver(s), comma-separated, never public |
| `FORWARDER_IP` | overlay | `172.30.0.3` (must be inside `172.30.0.0/24`) |

## Testing

```bash
go test -race ./...
```

Covers 30+ escape attempts against the proxy and an in-process chain (client → gateway → real sockproxy → fake dockerd → fake terminals): per-user routing, auth, WebSocket auth rewrite, capacity limit, idle stop and restart, config-change recreate, both storage profiles, per-user sandbox cap.

End-to-end against a real Docker daemon (builds images, starts the stack, applies the firewall, checks isolation, storage profiles, WebSocket, proxy refusals and that terminals run under the requested runtime):

```bash
test/integration/run.sh                     # gVisor
RUNTIME=sysbox-runc test/integration/run.sh
```

CI runs this under gVisor for every pull request to `main`.

For a quick manual test without gVisor or Sysbox, run the proxy with `ALLOWED_RUNTIMES=runc ALLOW_RUNC_INSECURE=1` and policies using `"runtime": "runc"`. Never do this in production: runc gives sudo inside the container real root on the host kernel.

Smoke test from any container in the `owui` network:

```bash
KEY=$(cat deploy/secrets/gateway_api_key)
curl -H "Authorization: Bearer $KEY" http://gateway:8080/api/config
curl -H "Authorization: Bearer $KEY" -H "X-User-Id: alice" http://gateway:8080/files/cwd
curl -H "Authorization: Bearer $KEY" -H "X-User-Id: alice" -H "Content-Type: application/json" \
     -d '{"command":"sudo id -u; ls ~"}' http://gateway:8080/execute
```

## Status

The native integration was verified locally on 2026-11-05 with Open WebUI
0.13.0, Open Terminal 0.16.0 and Sysbox. `make check`, the complete native image
smoke test and two consecutive deterministic runs covering all 23 published
native tools pass. See [validation scope and reproduction](docs/native-tool-validation.md)
and [migration status](docs/native-integration.md). These checks do not establish
that every model will complete arbitrary GUI tasks.
The [DeepSeek code review](docs/reviews/2026-11-05-deepseek-native-review.md)
records five addressed findings. A fresh DeepSeek session also built and ran
an [independent native HTTP/PTY proof](test/native-independent/INDEPENDENT_PROOF.md):
42 checks passed, with file-viewer rendering left unproved.

### Historical runtime baseline (October 2026)

The following results describe the earlier terminal stack, before the native
Desktop/Browser migration; they are not current image validation.

Verified 2026-10-09 on a single Linux host (kernel 6.12 LTS): Docker 29.2.1 with the containerd image store, gVisor `release-20260921.0`, Sysbox CE 0.7.1, Open Terminal 0.14.0, Open WebUI 0.11.4. Earlier runs against Docker 29 with runc covered the same functions without a sandbox runtime.

### Historical integration tests (`test/integration/run.sh`)

| Runtime | Result |
|---|---|
| `sysbox-runc` | **all checks pass**: auth, user isolation, sudo inside, firewall (private ranges and host blocked, internet reachable), ephemeral sandboxes, terminal WebSocket, runtime and `Privileged:false` on every terminal, persistent home survives recreation, all proxy escape attempts refused |
| `runsc` (gVisor) | **fails 2 of 16 functional checks**, so the later steps (runtime, persistence, proxy) do not run |

The two gVisor failures are runtime behaviour, not termiplus bugs, and are reproducible with a plain `docker run --runtime runsc` of the terminal image:

1. **`sudo` does not work** unless runsc is started with `--allow-suid`; `deploy/daemon.json` now sets it (see Security in the changelog).
2. **No DNS on the `ot-sandbox` network without the forwarder.** gVisor's network stack cannot reach Docker's embedded resolver `127.0.0.11` on user-defined networks ([google/gvisor#7469](https://github.com/google/gvisor/issues/7469)). Direct DNS to public resolvers and connections by IP work. runsc's `--reproduce-nat` needs the legacy iptables API and fails to start containers on an `nf_tables` host; `--reproduce-nftables` did not restore DNS. With the netpolicy overlay (internal DNS forwarder) DNS works under gVisor: the gateway passes the forwarder to the image, whose entrypoint writes it into `resolv.conf`.

The full security acceptance set (`test/integration/VALIDATION.md`, automated by `validate.sh`) passed under both Sysbox and gVisor on 2026-10-13; see the results there.

### Historical Open WebUI checks

Deployed with the quick start, variant 5b (`docker-compose.separate-vm.yml`), Open WebUI on the same host reaching the gateway on a private bridge address. Two admin terminal connections with Bearer auth: the root URL (persistent) and `/p/scratch` (sandbox per chat). Tested through the real web UI (headless Chromium) as two regular users, terminals running under gVisor (`runsc` with `--allow-suid`, see above):

- **File browser**: lists the home directory, shows files written from the terminal or by the model
- **Interactive terminal**: works over the WebSocket; commands run, `sudo` gives uid 0 (with `--allow-suid`)
- **Model running commands**: tool calls (`run_command`) execute in the user's own terminal and the output is returned
- **Isolation**: each user gets their own container and home volume; neither user sees the other's files
- **Sandbox per chat**: the model's tool calls send `X-Session-Id` (the chat id) and get a fresh sandbox per chat
- **`X-Session-Id` from the file browser**: sent once the chat is saved (after the first message). In a new, unsaved chat the file browser sends no session id and lands in the user's `nochat` sandbox, so it shows a different sandbox than the model will use until the first message is sent
- Gateway and proxy logs showed no errors and no denials during normal use

### Issues recorded in that baseline

- **Interactive shell/job control:** the old image fell back to `/bin/sh` and emitted a job-control warning. The current image sets `SHELL` to its Bash wrapper and initializes the controlling PTY; see `image/Dockerfile` and `image/bash-ctty`.
- **gVisor DNS and sudo**, see above. Both are solved by configuration: `deploy/daemon.json` (`--allow-suid`) and the netpolicy overlay (DNS). With both, gVisor passes the full acceptance set; the base stack alone has no DNS under gVisor.

## Known limits

- gVisor policies need the netpolicy overlay for DNS (#3). Without it, terminals under runsc connect by IP only.
- Instance state lives in gateway memory: run one gateway replica. After a restart it adopts running containers.
- `X-User-Id` is trusted because only Open WebUI knows the gateway key. Use an admin connection, never a user connection, so the key never reaches a browser.
- Disk usage is capped only where `HOME_VOLUME_SIZE` (homes, set at creation) and `CONTAINER_ROOTFS_SIZE` (container layers) apply, which needs XFS project quotas.
- The audit log (`get_audit_log`) covers changes made through the MCP server: chat and the management console. Direct calls to the gateway's admin API only appear in the gateway log.

## Development

- Branches: work happens on `dev`; `main` only changes through reviewed pull requests from `dev`. Releases are tags `vX.Y.Z` on `main`.
- `make check` runs everything CI checks. See [CONTRIBUTING.md](CONTRIBUTING.md).
- AI agents follow [AGENTS.md](AGENTS.md).
- Security issues: see [SECURITY.md](SECURITY.md), please report privately.
- Changes: [CHANGELOG.md](CHANGELOG.md).

## Licensing

termiplus is licensed under the GNU General Public License v3.0, see [LICENSE](LICENSE).

- **This code** was written from scratch for this project. It uses only the Go standard library (BSD-3-Clause). No code was copied from Open WebUI, Open Terminal or the Terminals project; their sources were only read to learn the request and WebSocket protocol for interoperability.
- **Terminal image**: Open Terminal is MIT, its Python dependencies are permissive. The Debian base contains GPL tools (bash, coreutils, ...). Internal use creates no obligations. If you ship the image to customers, keep the MIT notices and be able to point to Debian's sources for the GPL packages.
- Not legal advice.
