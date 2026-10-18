# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Gateway that routes Open WebUI terminal traffic to per-user Open Terminal containers, including the interactive terminal WebSocket.
- Filtering Docker socket proxy (deny by default, request bodies rebuilt from validated fields).
- Persistent and ephemeral (per-chat) storage profiles, configurable per policy.
- Terminal image based on Debian with Open Terminal and passwordless sudo, intended for gVisor or Sysbox.
- Deploy files: Docker Compose, runtime config, sandbox firewall script.
- Project standards: agent rules, security policy, contribution guide, issue and pull request templates.
- CI: lint, tests, image builds, secret and vulnerability scanning with reports, gVisor integration tests, signed releases with SBOMs.
- `deploy/termiplus-firewall.service`: example unit that reapplies the sandbox firewall at boot and after Docker restarts.
- Internal `netpolicy` package: validates admin-set custom DNS entries and per-destination egress allow rules, and enforces a fixed deny-list (loopback, link-local incl. the metadata address, CGNAT, the sandbox subnet) that no entry can open. Groundwork for #12.
- `netpolicy` store that persists the policy to disk and atomically renders the forwarder records (`dnsmasq` `address=` lines) and the firewall's allowed internal destinations from validated values only.
- Gateway admin API `GET`/`PUT /admin/netpolicy`: set custom DNS entries and per-destination internal egress at runtime. Gated by a dedicated `ADMIN_API_KEY` on the `X-Admin-Key` header (never the per-user connection key); off and returning 404 unless that key is configured. Part of #12.
- Gateway pins terminals to the internal resolver via `HostConfig.Dns` when `ALLOWED_DNS` is set; the socket proxy independently accepts only that one address.
- Internal DNS forwarder image (`deploy/dns-forwarder/`, dnsmasq) that forwards to the operator's resolver (`DNS_UPSTREAM`, never a hardcoded public one) and serves the admin-set records from a watched `hostsdir` (auto-reload, no signal).
- Opt-in Compose overlay `deploy/docker-compose.netpolicy.yml` that adds the forwarder and wires the admin API, the rendered-policy volume and the DNS pin. Without it the base deployment is unchanged (no forwarder, admin API returns 404).
- `deploy/termiplus-firewall.path`: reapplies the sandbox firewall when the rendered egress rules change.
- MCP and Open WebUI plugin tools `manage_user` / `manage_group` for `termiplus-admin` users: show `termiplus-*` groups and their members, add or remove users (with confirmation; the last admin cannot be removed). Uses Keycloak's admin API as the new service-account client `termiplus-mcp-admin` (`setup-alpha.sh` for new realms, `deploy/keycloak/add-mcp-admin-client.sh` for existing ones), opt-in via `deploy/docker-compose.mcp-users.yml`.
- Open WebUI plugin tools `get_network_policy` / `set_network_policy` for `termiplus-admin` users (set always asks for confirmation), and the opt-in overlay `deploy/docker-compose.mcp-netpolicy.yml` that gives the MCP server the gateway's admin key. Completes the admin side of #12.
- Stopped persistent workspaces are recreated on their next start when the image tag now points to a newer image (home volume kept, running workspaces untouched). The socket proxy allows `GET /images/<configured image>/json` for this and returns only the image ID.
- Docker inside Sysbox workspaces: installing `docker.io` starts the daemon (apt hook), the entrypoint restarts it with the container, `start-docker` starts it by hand, and the tool description tells the model so under Sysbox only.
- Disk quota for home volumes: with `HOME_VOLUME_SIZE` (e.g. `20G`) the socket proxy puts that hard limit on every new home volume (Docker's local-driver `size` option, XFS project quotas). The proxy adds the option itself; driver options from the caller stay refused. README explains the host setup, including a loopback XFS file for other filesystems.
- Policies may run their own image (`"image"`); the socket proxy allows exactly `TERMINAL_IMAGE` plus `EXTRA_IMAGES`.
- `CONTAINER_ROOTFS_SIZE`: the socket proxy caps every terminal's writable container layer (forced `StorageOpt` size; needs `overlay2` on XFS with `pquota`). Verified under gVisor, Sysbox and for ephemeral sandboxes.
- Admin API `GET`/`PUT /admin/homes/<home>/archive` (back up and restore a user's home as a tar) and `DELETE /admin/homes/<home>` (with `ALLOW_HOME_REMOVE=1` on the proxy). File access goes through never-started archive helper containers (`ot-a-...`); the proxy allows archive calls only on them, only for `/home/user`, and rewrites uploaded tars entry by entry.
- Gateway metrics: Prometheus endpoint (`METRICS_LISTEN_ADDR`) and an MQTT publisher with Home Assistant discovery (`MQTT_URL`, `deploy/docker-compose.mqtt.yml`). Minimal MQTT 3.1.1 client in the standard library. The socket proxy counts allowed, denied and failed calls (`GET /termiplus/stats`).
- Audit log in the MCP server (`MCP_AUDIT_FILE`, on in `deploy/docker-compose.mcp.yml`): every state-changing tool call from chat or the console, with actor, arguments and outcome, including refused calls. `termiplus-admin` users read it with the new `get_audit_log` tool (MCP and plugin 0.4.0) or in the console.
- Workspace reset and home backups (alpha): `reset_workspace`, `manage_snapshots` (list, create, restore, delete; download links) in MCP, plugin 0.5.0 and the console. Snapshots are gzip'd tars in the gateway's `BACKUP_DIR`, `SNAPSHOT_KEEP` per home.
- View-only shared desktops (alpha): `share_desktop` creates expiring links (`/share/<token>/`, optionally for one user) that sign in to KasmVNC as the new read-only user `viewer`. The desktop image creates it from `OPEN_TERMINAL_DESKTOP_VIEW_PASSWORD`.
- Departed users (alpha): with `MCP_DEPARTED_AFTER`, homes of users disabled or deleted in Keycloak are archived and their workspaces deleted after that time, and restored when a disabled user returns; `manage_departed` for admins.

### Security

- Gateway: a terminal's WebSocket upgrade response is read through a 64 KiB budget, so a container can no longer make the shared gateway buffer an endless status line or header (OOM of the gateway for all users).
- Gateway: the terminal HTTP proxy uses a dedicated transport with a 256 KiB response-header limit (was Go's 10 MiB default) and never an environment HTTP proxy.
- Gateway: container starts reserve their slot atomically, so concurrent requests can no longer exceed `MAX_RUNNING` or an ephemeral policy's `max_per_user`.
- Network policy: DNS entry IPs with an IPv6 zone are refused (they skipped the never-allowed check), and the hosts file is rendered only from the canonical, checked address.
- Home exports (admin API downloads, backups) have an absolute deadline (`EXPORT_TIMEOUT`, default 2h), and work that waits for a home's archive helper (removing or archiving the home) gives up with its context instead of waiting behind a slowly read download.
- **Desktops on their own origin** (alpha, breaking for the desktop overlay): desktop content is controlled by whoever runs code in the desktop, and on the console's origin it could call the console API as the signed-in user or register a service worker over the console. Desktops and share links now have their own listener (`STREAM_LISTEN_ADDR`), sign-in proxy (`desk-stream-auth`, cookie `_termiplus_dstream`) and URL (`DESKTOP_STREAM_URL`); the console proxy serves only `/ui/`. The MCP server refuses to start if `CONSOLE_PUBLIC_URL` and `STREAM_PUBLIC_URL` share an origin. Existing deployments need `DESKTOP_STREAM_URL`, a TLS route to `127.0.0.1:4183`, the secret `dstream_oidc_cookie_secret` (`setup-alpha.sh` creates it) and the redirect URI `${DESKTOP_STREAM_URL}/oauth2/callback` on the `termiplus-desktop` client (added to the realm template; existing realms need it added by hand).
- Desktops origin: service-worker script requests are refused, and `Set-Cookie`, `Service-Worker-Allowed` and `Clear-Site-Data` from desktops are dropped by the gateway and the MCP server.
- Desktop and port-forward relays use dedicated transports with response-header limits and deadlines, and a container can hold at most 32 proxied connections at once (429 beyond).
- Revoking a shared desktop link, or its expiry, ends the connections viewers have open through it.
- Port forwards: only container names are accepted as sessions, so the trailing-slash redirect can no longer point to another site.
- `manage_snapshots create` asks for confirmation when it would delete the oldest snapshot (`SNAPSHOT_KEEP` reached).
- Home exports stop at `EXPORT_MAX_BYTES` (default: `HOME_VOLUME_SIZE` + 1/8 + 64M), so a sparse file, which costs almost nothing against the home quota, can no longer stream an unbounded tar through the shared gateway and socket proxy. At most `EXPORT_CONCURRENCY` (4) exports run at once, and `EXPORT_TIMEOUT` now applies to snapshots and departed-user archives too, not only downloads.
- `setup-alpha.sh` and `add-mcp-admin-client.sh` make `deploy/secrets/` mode 0700.

### Changed

- `firewall.sh --if-needed` and `termiplus-firewall-watch.{service,timer}` reapply the sandbox firewall within 20 s when it is missing or a pinned MAC is stale (reboot, Docker restart, recreated gateway). The boot-time unit alone could run before the gateway existed and then, by design, install nothing.

- gVisor DNS with the netpolicy overlay works: the gateway passes the forwarder as `OPEN_TERMINAL_RESOLVER` and the terminal image's new entrypoint writes it into `/etc/resolv.conf`, because Docker keeps its embedded resolver there, which gVisor cannot reach (#3).
- `test/integration/validate.sh` automates the live security scenarios V1-V15 of `VALIDATION.md`; `run.sh` also removes a leftover `ot-sandbox` network.

- `test/integration/run.sh` refuses to run on a host that already has termiplus containers, home volumes or the `ot-sandbox` network, because its cleanup removes all of them.
- `test/integration/run.sh` runs every stage and reports all failures at the end.
- Interactive terminals start bash with the PTY as controlling terminal (job control works); the image sets `SHELL`.
- User-less spec requests (connection check, OpenAPI, system prompt) share one container per runtime instead of starting one per policy.
- Quick start creates the secrets readable for the owner and the services' group only, independent of the umask.
- `deploy/firewall.sh` applies the admin-set internal egress allowances from `deploy/netpolicy/egress.rules`: each validated `CIDR PORT PROTO` line becomes a `RETURN` rule placed after the fixed deny-list drops (loopback, link-local/metadata, CGNAT, the sandbox subnet), which it also re-checks, so no entry can open a never-allowed range. Private ranges stay default-deny; public egress stays default-allow.

### Security

- Gateway and socket proxy run as non-root with read-only filesystems, all capabilities dropped and `no-new-privileges`.
- gVisor: `deploy/daemon.json` starts runsc with `--allow-suid` so sudo works inside the sandbox as documented. Elevation stays inside the gVisor sandbox (root of the sandbox kernel, not the host).
- Socket proxy accepts `HostConfig.Dns` only if it is exactly the one configured resolver (`ALLOWED_DNS`, the internal forwarder); any other address, a second entry, `DnsSearch` or `DnsOptions` are refused. Empty keeps `Dns` refused as before. Part of #12.
- netpolicy: egress rules and DNS entries written as IPv4-mapped IPv6 (`::ffff:a.b.c.d`) can no longer bypass the deny-list; egress CIDRs must be plain IPv4 and DNS entry IPs are checked against the deny-list. (security audit)
- Gateway rejects non-canonical request paths (`..`, `//`) before routing, so a `/x/../api/v1/...` path cannot skip the `/api/v1` admin-API denylist and be normalized back by the terminal. (security audit)
- Firewall now blocks IPv6 on INPUT and container-to-container (v6 bridge-netfilter sysctl), not only FORWARD, so the "no IPv6" isolation actually holds for terminal->host and terminal<->terminal link-local v6. (security audit)
- Sandbox isolation no longer fails open without the host firewall: the `ot-sandbox` bridge runs with `enable_icc: false`, so Docker drops terminal<->terminal and terminal->gateway traffic even if `firewall.sh` is not applied. (#14)
- The gateway->terminal allow can no longer be ridden by a terminal spoofing the gateway's IP or ARP: `firewall.sh` pins 172.30.0.2 to the gateway container's MAC with ebtables (IPv4 and ARP) and installs no rules if ebtables is missing or the gateway is not running. **Hosts now need `ebtables`.** (#13)
- `firewall.sh` loads `br_netfilter` and verifies `bridge-nf-call-ip(6)tables=1` before installing any DROP rule and exits without rules if it cannot; `deploy/sysctl.d/90-termiplus-bridge.conf` keeps the settings across reboots. Previously a missing module left the DROP rules in place but bypassed. (#17)
- The gateway verifies a persistent home volume's owner label before reusing it, as it already did for containers, so a user-hash collision cannot hand one user another user's home. (#16)
- The socket proxy refuses to start without `PROXY_TOKEN` (at least 32 characters) instead of running unauthenticated. (#15)
- With the netpolicy overlay, `firewall.sh` lets terminals reach the DNS forwarder (udp/tcp 53 only) and the forwarder reach its `DNS_UPSTREAM` (port 53 only), and pins the forwarder's IP to its MAC like the gateway's. Before, the #13/#14 rework dropped all DNS to the forwarder. (live validation)
