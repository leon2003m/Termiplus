# termiplus

[![CI](https://github.com/leon2003m/termiplus/actions/workflows/ci.yml/badge.svg?branch=dev)](https://github.com/leon2003m/termiplus/actions/workflows/ci.yml)
[![Security](https://github.com/leon2003m/termiplus/actions/workflows/security.yml/badge.svg?branch=dev)](https://github.com/leon2003m/termiplus/actions/workflows/security.yml)
[![License: GPL-3.0](https://img.shields.io/badge/license-GPL--3.0-blue.svg)](LICENSE)

Per-user Open Terminal containers for Open WebUI. Every user gets their own isolated terminal, started on demand and stopped when idle. Users can pick a **persistent** terminal (home directory survives) or a **throwaway sandbox** per chat. A self-hosted replacement for the paid Open WebUI "Terminals" orchestrator.

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
| `image/` | Terminal image: Debian, common dev tools, passwordless sudo, Open Terminal server |
| `deploy/` | Compose file, policies, Docker runtime config, firewall script |

Go standard library only, no third-party modules.

## Layout

```
cmd/gateway/main.go         gateway entrypoint (env config)
cmd/sockproxy/main.go       socket proxy entrypoint (env config)
internal/gateway/           routing, lifecycle, idle reaper, WebSocket proxy, e2e tests
internal/sockproxy/         request filter and body validation, escape-attempt tests
internal/dockerapi/         minimal Docker API client used by the gateway
internal/naming/            container/volume naming shared by gateway and proxy
internal/config/            env and *_FILE secret helpers
image/Dockerfile            terminal image
Dockerfile                  gateway and sockproxy images (--target)
deploy/                     docker-compose.yml, policies.json, daemon.json, firewall.sh
```

## Quick start (dedicated VM)

Requirements: Linux VM, Docker 25+, Go 1.24 only if you want to run the tests.

```bash
# 1. Runtimes: install gVisor and/or Sysbox, then
sudo cp deploy/daemon.json /etc/docker/daemon.json   # or merge into your existing one
sudo systemctl restart docker
docker info | grep -i runtimes                       # must list runsc and/or sysbox-runc

# 2. Terminal image (push to your registry and pin by digest in production)
docker build -t ot-terminal:0.14.0 image/

# 3. Secrets, each 64 hex chars
mkdir -p deploy/secrets
for s in proxy_token gateway_api_key key_secret; do openssl rand -hex 32 > deploy/secrets/$s; done

# 4. Network shared with Open WebUI, then start
docker network create owui        # skip if Open WebUI already has one; set OWUI_NETWORK
cd deploy && TERMINAL_IMAGE=ot-terminal:0.14.0 docker compose up -d --build

# 5. Egress rules (rerun after every Docker restart, e.g. from a systemd unit)
sudo ./firewall.sh
```

6. Open WebUI → Admin Settings → Integrations → Open Terminal → add a connection:
   - URL `http://gateway:8080`, auth type **Bearer**, key = contents of `deploy/secrets/gateway_api_key`
   - Optionally a second one, `http://gateway:8080/p/scratch`, named e.g. "Sandbox (fresh per chat)"
   - Restrict who sees which with access grants

Open WebUI must run in the `owui` network. If it runs elsewhere, publish the gateway port behind TLS instead and keep the key server-side.

## How it works with Open WebUI

Open WebUI's admin terminal connection already sends who the user is:

- HTTP: `Authorization: Bearer <connection key>` and `X-User-Id`, plus `X-Session-Id` (the chat) when there is one
- WebSocket: `X-User-Id`, `X-Session-Id`, then a first message `{"type":"auth","token":"<connection key>"}`

The gateway verifies the connection key, then calls the user's container with that container's own key (HMAC of `KEY_SECRET` and the container name). For the WebSocket it reads the auth message, checks it, and resends it upstream with the container key. No container is started before authentication succeeds.

Open WebUI also sends a few requests without a user (connection check, OpenAPI spec, system prompt). Those go to a shared read-only "spec" container and only `GET /openapi.json`, `/api/config` and `/system` are allowed. `/health` is answered by the gateway. `/api/v1/*` returns 404, so Open WebUI treats the gateway as a normal terminal, not as an orchestrator. That is why plain Bearer auth is enough.

## Policies and storage profiles

`deploy/policies.json` defines environments. Each policy has its own URL prefix and becomes one connection in Open WebUI:

| URL | Policy | User gets |
|---|---|---|
| `http://gateway:8080` | `default` | **Persistent** terminal, gVisor |
| `http://gateway:8080/p/scratch` | `scratch` | **Ephemeral** sandbox per chat, gVisor |
| `http://gateway:8080/p/sysbox` | `sysbox` | Persistent, Sysbox (systemd or Docker inside possible), same home as `default` |

```json
{
  "default": { "runtime": "runsc", "memory_mb": 2048, "cpus": 2, "pids": 512 },
  "scratch": { "runtime": "runsc", "memory_mb": 2048, "cpus": 2, "pids": 512,
               "storage": "ephemeral", "idle_timeout": "15m", "max_per_user": 3 },
  "sysbox":  { "runtime": "sysbox-runc", "memory_mb": 4096, "cpus": 4, "pids": 1024, "home": "default" }
}
```

| Field | Meaning |
|---|---|
| `runtime`, `memory_mb`, `cpus`, `pids` | Required. Must stay within the proxy's caps |
| `storage` | `persistent` (default) or `ephemeral` |
| `home` | Persistent only. Policies with the same `home` share the user's volume |
| `idle_timeout` | Overrides `IDLE_TIMEOUT` for this policy, minimum `1m` |
| `max_per_user` | Ephemeral only. Concurrent sandboxes per user, default 3 |

**Persistent.** One container per user and policy (`ot-u-<user>-<policy>`) with a volume (`ot-home-<user>-<home>`) at `/home/user`. Idle containers are stopped, not deleted, and start again on the next request. Changing the image, a policy or `KEY_SECRET` recreates the container on its next use; the volume stays.

**Ephemeral.** One container per user, chat and policy (`ot-e-<user>-<chat>-<policy>`), no volume. Files live in the container and disappear with it. Docker deletes it as soon as it stops (`AutoRemove`, forced by the proxy). The same chat keeps its sandbox until it goes idle, then gets a fresh one. At `max_per_user`, the user's least recently used sandbox without an open terminal is deleted; if all are in use, the request gets 429. Requests without a chat id share one `nochat` sandbox per user.

## What the socket proxy allows

Everything not listed is 403.

| Call | Restriction |
|---|---|
| `GET /_ping`, `/version` | none |
| `GET /containers/json` | label filter forced to termiplus containers |
| `POST /containers/create` | Name must be `ot-u-...` or `ot-e-...`. Body decoded strictly (unknown fields rejected) and rebuilt from checked values. Only the configured image, a runtime from the allowlist (runc refused), the sandbox network, capped memory/CPU/PIDs. Persistent: exactly one volume at `/home/user` belonging to the same user. Ephemeral: no mounts, `AutoRemove` forced. Env only `OPEN_TERMINAL_*`. `Privileged:false` forced. No ports, binds, capabilities, devices, security options, namespaces |
| `POST /volumes/create` | Name `ot-home-...`, local driver, no driver options (blocks `o=bind,device=/`) |
| `GET /volumes/<name>` | termiplus volumes only |
| `GET .../json`, `POST .../start`, `POST .../stop`, `DELETE` | Target resolved by the daemon first, must carry the termiplus label and name, then addressed by full ID. `DELETE` without `v` |

Also rejected: `exec`, `attach`, image pulls, builds, connection upgrades, encoded paths, `..`, `//`, missing or duplicate `X-Proxy-Token`.

## Network isolation

`deploy/firewall.sh` adds iptables rules for the `ot-sandbox` bridge (172.30.0.0/24):

- terminals reach the internet
- terminals cannot reach each other, private ranges (10/8, 172.16/12, 192.168/16, 100.64/10, 169.254/16) or the VM itself
- the gateway (172.30.0.2) can reach every terminal
- no IPv6

## Settings

| Variable | Used by | Default |
|---|---|---|
| `TERMINAL_IMAGE` | both | required, must be identical in both |
| `SANDBOX_NETWORK` | both | `ot-sandbox` |
| `PROXY_TOKEN` / `PROXY_TOKEN_FILE` | both | optional, recommended |
| `ALLOWED_RUNTIMES` | sockproxy | `runsc,sysbox-runc` |
| `MAX_MEMORY_MB`, `MAX_CPUS`, `MAX_PIDS` | sockproxy | `8192`, `4`, `2048` |
| `DOCKER_SOCKET` | sockproxy | `/var/run/docker.sock` |
| `GATEWAY_API_KEY` / `_FILE` | gateway | required, min. 32 chars |
| `KEY_SECRET` / `_FILE` | gateway | required, min. 32 chars |
| `POLICIES_FILE` | gateway | one `default` gVisor policy |
| `DOCKER_API` | gateway | `http://sockproxy:2375` |
| `IDLE_TIMEOUT` | gateway | `30m` |
| `MAX_RUNNING` | gateway | `50` running containers in total |
| `START_TIMEOUT` | gateway | `90s` |
| `TERMINAL_PORT` | gateway | `8000` |

## Testing

```bash
go test -race ./...
```

Covers 30+ escape attempts against the proxy and an in-process chain (client → gateway → real sockproxy → fake dockerd → fake terminals): per-user routing, auth, WebSocket auth rewrite, capacity limit, idle stop and restart, config-change recreate, both storage profiles, per-user sandbox cap.

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

Verified against Docker 29 with the real Open Terminal 0.14.0 image, using runc:

- users isolated from each other, sudo works inside
- persistent homes survive container recreation; homes are shared across policies with the same `home`
- interactive terminal over WebSocket; wrong token rejected before any container starts
- ephemeral sandboxes per chat, LRU eviction at the cap, deleted when idle without leftover volumes
- firewall rules; escape attempts against the proxy all refused

Not yet verified:

1. gVisor (`runsc`) and Sysbox runtimes
2. a real Open WebUI in front (file browser, interactive terminal, tool calls)
3. whether Open WebUI's file browser always sends `X-Session-Id`; if not, ephemeral file browsing falls back to the user's `nochat` sandbox

## Known limits

- Instance state lives in gateway memory: run one gateway replica. After a restart it adopts running containers.
- `X-User-Id` is trusted because only Open WebUI knows the gateway key. Use an admin connection, never a user connection, so the key never reaches a browser.
- Disk usage is not capped: neither home volumes nor ephemeral container layers. Use a filesystem with project quotas or monitor.
- No per-user audit log beyond the gateway and proxy logs.

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
