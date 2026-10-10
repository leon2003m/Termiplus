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
deploy/                     docker-compose.yml, policies.json, daemon.json, firewall.sh,
                            termiplus-firewall.service
```

## Quick start (dedicated VM)

Requirements: Linux VM, Docker 25+, Go 1.24 only if you want to run the tests.

```bash
# 1. Runtimes: install gVisor and/or Sysbox, then
sudo cp deploy/daemon.json /etc/docker/daemon.json   # or merge into your existing one
sudo systemctl restart docker
docker info | grep -i runtimes                       # must list runsc and/or sysbox-runc
#    deploy/daemon.json starts runsc with --allow-suid, without it sudo inside gVisor fails
#    (gVisor ignores setuid bits by default). If you merge by hand, keep that runtimeArgs entry.

# 2. Terminal image (push to your registry and pin by digest in production)
docker build -t ot-terminal:0.14.0 image/

# 3. Secrets, each 64 hex chars
mkdir -p deploy/secrets
for s in proxy_token gateway_api_key key_secret; do
  openssl rand -hex 32 | sudo install -m 0640 -o "$USER" -g 65534 /dev/stdin deploy/secrets/$s
done

# 4. Settings for compose (deploy/.env is gitignored)
cat > deploy/.env <<ENV
TERMINAL_IMAGE=ot-terminal:0.14.0
DOCKER_GID=$(stat -c %g /var/run/docker.sock)
ENV

# 5a. Open WebUI on the same host, sharing the "owui" Docker network:
docker network create owui        # skip if Open WebUI already has one; set OWUI_NETWORK
cd deploy && docker compose up -d --build

# 5b. Recommended: termiplus on its own VM, gateway published on a private address:
cd deploy && GATEWAY_BIND=10.20.30.40:8080 \
  docker compose -f docker-compose.yml -f docker-compose.separate-vm.yml up -d --build

# 6. Egress rules (rerun after every Docker restart, e.g. from a systemd unit)
sudo ./firewall.sh
```

7. Open WebUI → Admin Settings → Integrations → Open Terminal → add a connection:
   - URL `http://gateway:8080`, auth type **Bearer**, key = contents of `deploy/secrets/gateway_api_key`
   - Optionally a second one, `http://gateway:8080/p/scratch`, named e.g. "Sandbox (fresh per chat)"
sudo cp termiplus-firewall.service /etc/systemd/system/ && sudo systemctl enable --now termiplus-firewall
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
- WebSocket: `X-User-Id`, `X-Session-Id`, then a first message `{"type":"auth","token":"<connection key>"}`

The gateway verifies the connection key, then calls the user's container with that container's own key (HMAC of `KEY_SECRET` and the container name). For the WebSocket it reads the auth message, checks it, and resends it upstream with the container key. No container is started before authentication succeeds.

Open WebUI also sends a few requests without a user (connection check, OpenAPI spec, system prompt). Those go to a "spec" container shared by all policies with the same runtime, and only `GET /openapi.json`, `/api/config` and `/system` are allowed. `/health` is answered by the gateway. `/api/v1/*` returns 404, so Open WebUI treats the gateway as a normal terminal, not as an orchestrator. That is why plain Bearer auth is enough.

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

**Ephemeral.** One container per user, chat and policy (`ot-e-<user>-<chat>-<policy>`), no volume. Files live in the container and disappear with it. Docker deletes it as soon as it stops (`AutoRemove`, forced by the proxy). The same chat keeps its sandbox until it goes idle, then gets a fresh one. At `max_per_user`, the user's least recently used sandbox without an open terminal is deleted; if all are in use, the request gets 429. Requests without a chat id share one `nochat` sandbox per user. Open WebUI's file browser sends the chat id only once the chat is saved (after the first message): in a new chat it shows the `nochat` sandbox, while the model's first tool call already gets the chat's own sandbox.

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
Host-wide effect: the script loads `br_netfilter` and sets `net.bridge.bridge-nf-call-iptables=1`, so traffic on **every** Linux bridge of the host (VM bridges, LAN bridges) passes iptables `FORWARD` from then on, not only `ot-sandbox`. That is why termiplus expects a dedicated VM. On a shared host check your `FORWARD` policy and other bridges first.

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

Last verified 2026-10-09 on a single Linux host (kernel 6.12 LTS): Docker 29.2.1 with the containerd image store, gVisor `release-20260921.0`, Sysbox CE 0.7.1, Open Terminal 0.14.0, Open WebUI 0.11.4. Earlier runs against Docker 29 with runc covered the same functions without a sandbox runtime.

### Integration tests (`test/integration/run.sh`)

| Runtime | Result |
|---|---|
| `sysbox-runc` | **all checks pass**: auth, user isolation, sudo inside, firewall (private ranges and host blocked, internet reachable), ephemeral sandboxes, terminal WebSocket, runtime and `Privileged:false` on every terminal, persistent home survives recreation, all proxy escape attempts refused |
| `runsc` (gVisor) | **fails 2 of 16 functional checks**, so the later steps (runtime, persistence, proxy) do not run |

The two gVisor failures are runtime behaviour, not termiplus bugs, and are reproducible with a plain `docker run --runtime runsc` of the terminal image:

1. **`sudo` does not work.** gVisor ignores setuid bits unless runsc is started with `--allow-suid` (off by default). With `"runtimeArgs": ["--allow-suid"]` on the `runsc` runtime the check passes. `deploy/daemon.json` does not set it; enabling it lets processes become root inside the sandbox, which is what the image and this README intend, but it is a change to the runtime configuration and needs a maintainer decision.
2. **No DNS on the `ot-sandbox` network.** gVisor's network stack cannot reach Docker's embedded resolver `127.0.0.11` on user-defined networks ([google/gvisor#7469](https://github.com/google/gvisor/issues/7469)). Direct DNS to public resolvers and connections by IP work. runsc's `--reproduce-nat` needs the legacy iptables API and fails to start containers on an `nf_tables` host; `--reproduce-nftables` did not restore DNS. The known workarounds (own `resolv.conf` mount, `--network=host`, a resolver on another runtime) touch the proxy or network isolation and were not applied.

### Real Open WebUI in front

Deployed with the quick start, variant 5b (`docker-compose.separate-vm.yml`), Open WebUI on the same host reaching the gateway on a private bridge address. Two admin terminal connections with Bearer auth: the root URL (persistent) and `/p/scratch` (sandbox per chat). Tested through the real web UI (headless Chromium) as two regular users, terminals running under gVisor (`runsc` with `--allow-suid`, see above):

- **File browser**: lists the home directory, shows files written from the terminal or by the model
- **Interactive terminal**: works over the WebSocket; commands run, `sudo` gives uid 0 (with `--allow-suid`)
- **Model running commands**: tool calls (`run_command`) execute in the user's own terminal and the output is returned
- **Isolation**: each user gets their own container and home volume; neither user sees the other's files
- **Sandbox per chat**: the model's tool calls send `X-Session-Id` (the chat id) and get a fresh sandbox per chat
- **`X-Session-Id` from the file browser**: sent once the chat is saved (after the first message). In a new, unsaved chat the file browser sends no session id and lands in the user's `nochat` sandbox, so it shows a different sandbox than the model will use until the first message is sent
- Gateway and proxy logs showed no errors and no denials during normal use

### Known issues found

- **No job control in the interactive terminal.** Open Terminal starts `$SHELL` (falling back to `/bin/sh`, the image does not set `SHELL`) with `start_new_session=True` but does not make the PTY the controlling terminal, so the shell prints `can't access tty; job control turned off`. This is Open Terminal behaviour and happens under every runtime. Setting `ENV SHELL=/bin/bash` in `image/Dockerfile` would at least give users bash.
- **gVisor DNS and sudo**, see above. Until decided, Sysbox is the runtime that passes the full integration test.

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
