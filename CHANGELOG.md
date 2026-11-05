# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.0.0-rc.3] - 2026-11-05

Native Open WebUI workspaces replace the custom desktop and port-forwarding
integration. This remains a release candidate.

### Upgrade notes

- Use Open WebUI 0.13.0 and Open Terminal 0.16.0 with the native integration
  described in [the migration guide](docs/native-integration.md). Remove the
  retired KasmVNC desktop and custom port-forwarding services and configuration.
- The default and scratch policies now both use the complete Sysbox desktop
  image. Chromium is the only browser; persistent home contents are preserved.
- Reapply the sandbox firewall and install the host requirements described in
  the Security entries below before accepting workspace traffic.
- See [supported tools](docs/tool-support.md) for the exact native/plugin
  inventory and the gateway/schema/image adaptations maintained by Termiplus.

### Added

- Deterministic live verification of all 23 Open WebUI native tools, their
  published parameters and every Desktop/Browser action, with independent GUI
  observations and coverage checks for future upstream inventory changes.

- Complete desktop image includes the standard Open Terminal workspace tools:
  inner Docker with Compose/Buildx, scientific Python and Jupyter, LibreOffice,
  document/PDF/media converters, scripting/build and session/network utilities.
  User Python and notebook kernels share libraries in a venv separate from the API.
  Tini reaps child processes. No host Docker socket or sandbox permissions added.

- Terminal image includes fdfind, rsync, bc and pipx (about 10 MB additional image size); native workspace ownership and egress limits are documented.

- Console dialogs replace browser prompts, with duration presets, an exact-name person picker and copyable share links. Terminal cards show observed activity and idle-deletion estimates; workspace help explains private access.

- Owner-only desktop thumbnails in the console, refreshed every 30 seconds while visible. Stopped desktops show Offline without starting a container.

- Workspaces can have a `label` and `description` in `policies.json`. The console and `list_workspaces` show them instead of the bare policy name; the default policies are now called Desktop and Temporary desktop. Changing them does not recreate containers.
- Docs: a system map for agents and contributors under `map/`. It covers objects, processes and change effects, cites source lines, and is built with the vendored icm-architect skill (`.claude/skills/icm-architect/`). `make map` regenerates its twins, and `make lint` fails on drift.
- Offline workspaces: a policy with `"egress": "offline"` puts its terminals on an internal-only Docker network (`deploy/docker-compose.offline.yml`, `OFFLINE_NETWORK`) with no DNS, no internet, no LAN and no terminal-to-terminal traffic; only the gateway can reach them. `firewall.sh` adds host rules for that bridge. Without the overlay, the gateway refuses offline policies instead of falling back to the internet. The netpolicy deny-list now also covers the offline subnet `172.30.1.0/24`; a stored policy with a DNS or egress entry there stops the gateway at startup until that entry is removed.

### Changed

- Document the exact 23 native tools, seven MCP tools and plugin-only console
  link, plus the maintained gateway/schema/image adaptations and drop-in scope.
- Document the focus since Open WebUI 0.13 prominently in the README: native
  workspace interaction upstream, isolation/lifecycle/storage/access in Termiplus.
  Refresh deployment, security and map docs; label pre-migration analyses as historical.
- Record the external DeepSeek review, its fixes and a separately authored,
  executed native HTTP/PTY verification harness with explicit coverage limits.
- Record frontend acceptance of text/image/PDF rendering, real model-to-viewer
  delivery, PDF download and the native Desktop/Chrome UI.

- Use Chromium as the only desktop browser and btop instead of htop; omit
  Ristretto and ImageMagick. Keep LibreOffice and XFCE Terminal.
- Offer Desktop and Temporary desktop as the two default workspace choices,
  both with native Browser, files and terminal support under Sysbox. Keep the
  persistent home volume and existing resource limits.
- Integrate Open WebUI 0.13 native workspaces with Open Terminal 0.16; route
  native Desktop, Browser, events, files, Git and app-preview protocols per user/chat.
- Replace KasmVNC and the owner desktop player with the native Desktop; remove
  custom port forwarding, its OIDC service and the `open_port` tool.
- Remove administration from model tools, including direct MCP calls by admins.
  Models may list/create snapshots but cannot delete, restore or prune backups.
- Keep expiring/revocable view-only shares as native screen images refreshed
  every five seconds, without keyboard, mouse, clipboard or audio access.

- Record the alpha checkpoint and the decision to use Open WebUI's native
  Desktop and Browser workspace, retaining termiplus isolation and storage.

- Desktop defaults now use Dolphin, matching dark Qt/GTK settings, a green accent and a compact bottom dock with Chromium, Dolphin, terminal and Kate. Existing home-folder customizations remain in place.

- MQTT discovery messages are off by default. Set `MQTT_DISCOVERY_PREFIX` to your hub's discovery prefix to keep sending them; the MQTT docs and examples no longer name a specific hub.

### Fixed

- Suppress inherited logout/lock/mail shortcuts in partial saved XFCE profiles.
  Preserve malformed desktop configuration files before restoring usable defaults,
  so damaged persistent settings do not prevent the desktop from starting.
- Report unknown X11 key names as failed Desktop actions, with partial-batch
  progress, instead of returning a successful screenshot after ignoring input.
- Prevent native Desktop input timeouts when clicking the current pointer position;
  confirm pointer movement with a bounded position check.
- Repair saved default applications and obsolete launchers; seed the configured
  dock before XFCE generates stock panels. Hide unsupported mail/TeX GUI entries
  and session-ending controls, including logout keyboard shortcuts.
- Announce the shared graphical desktop explicitly in native model context,
  including raw pixel coordinates for its 1280×800 screenshots.
- Explain native Desktop action coordinates in OpenAPI field descriptions and
  upstream system context; release abandoned managed Chromium profile locks after recreation.
- Restore Dolphin, Chromium, Kate, the Termiplus theme, app dock and wallpaper
  in the native desktop image. Initialize the wallpaper on TigerVNC displays
  while preserving personal desktop preferences.
- Run native Desktop/Browser CI tests under Sysbox so Chromium keeps its sandbox.

### Security

- The gateway's address on the sandbox bridge can no longer be taken over by a terminal that forges both the gateway's IP and its MAC. `firewall.sh` now also pins 172.30.0.2 (and the DNS forwarder and the offline gateway) to the container's own bridge port with ebtables in nat `PREROUTING` (chain `TERMIPLUS-PORT`), before the bridge learns MACs. The port is looked up in the container's network namespace. **Hosts now need `nsenter` (util-linux)**; without it, or without the gateway's port, `firewall.sh` refuses and keeps the previous pins. The watch timer re-pins a restarted gateway. (#13)
- Terminals can no longer take over each other's MAC address on the sandbox or offline bridge. Before, a terminal with root could forge another terminal's MAC, the bridge moved that MAC to its own port, and the gateway's replies to the other terminal, including its API key, went to the forger. `firewall.sh` now turns MAC learning off and locks every terminal port, with a static FDB entry for the MAC Docker assigned. New terminals are locked within a second by the new `termiplus-portlock.service` (`firewall.sh --watch-ports`), and `firewall.sh --lock-ports` does one pass. **Hosts now need Linux and iproute2 6.2 or newer**; without them `firewall.sh` exits with an error after applying the other rules.
- A running workspace that gains a network its policy does not allow (`docker network connect` on the host) is removed within a minute, and at gateway startup instead of being adopted. The check lists containers from Docker each time, so a removal or inspection that fails is retried on the next pass. Before, only containers checked on their next request were recreated.
- Snapshots: a create that would delete the oldest snapshot now needs the human confirmation at the gateway too (`?prune=1` on the control API, 409 without it). Before, two overlapping unconfirmed `manage_snapshots create` calls could both pass the MCP check and delete it. The console is unchanged. (security audit run 2)
- Health checks no longer follow redirects: a container whose `/health` answers 3xx is not ready, and the gateway never requests the redirect target, which may be on a network the terminal cannot reach. (security audit run 2)
- Home downloads end at `EXPORT_TIMEOUT` even when the client stops reading. Before, a stalled download held the home's archive helper and an export slot until the client went away. (security audit run 2)

## [1.0.0-rc.2] - 2026-10-18

Same code as 1.0.0-rc.1, whose release run stopped before the GitHub release because the desktop image failed to build for arm64. The rc.1 images that were already pushed (gateway, sockproxy, mcp, terminal, dns-forwarder) are superseded by rc.2. Upgrade notes and changes: see 1.0.0-rc.1 below.

### Fixed

- Release: the desktop image is built for linux/amd64 only. KasmVNC is installed from its amd64 package; the other images stay multi-arch.

## [1.0.0-rc.1] - 2026-10-18

First release candidate of termiplus 1.0: per-user sandboxed Open Terminal workspaces for Open WebUI, managed over MCP, with OIDC sign-in, desktops and a management console. Everything is new; the entries below list what it contains.

### Upgrade notes

From an alpha deployment:

- **Entitlements file required.** With `MCP_AUTH=proxy` the MCP server refuses to start without `MCP_ENTITLEMENTS_FILE` (`deploy/mcp-data/entitlements.json`). `setup-alpha.sh` creates one that denies everything; for an existing deployment create it from `deploy/entitlements.example.json` (or `{"default":"allow","workspaces":{}}` to keep the old open behaviour) and owned by uid 65534.
- **Desktops on their own origin.** Set `DESKTOP_STREAM_URL`, route it with TLS to `127.0.0.1:4183` (`desk-stream-auth`), run `setup-alpha.sh` once for the secret `dstream_oidc_cookie_secret`, and add `${DESKTOP_STREAM_URL}/oauth2/callback` as a redirect URI of the `termiplus-desktop` client in existing realms.
- an identity file that maps two subjects to one Open WebUI user id makes the MCP server refuse to start; remove the stale entry first (docs/mcp.md).
- **Snapshot directory.** The gateway in the MCP overlay needs `deploy/backups/` owned by uid 65534 (`setup-alpha.sh` creates it; then `sudo chown 65534:65534 deploy/backups`), or it stays unhealthy with `backup dir: ... permission denied`.
- **Run `deploy/firewall.sh` once by hand** after upgrading (as root): the watch timer only reapplies missing rules, so a host keeps the previous rule set, including the old global `ESTABLISHED,RELATED ACCEPT`, until a full run.
- **Numeric settings are range-checked** at startup (see Security); a deployment with an out-of-range value stops with a message naming the setting.

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
- **MCP control plane** (`cmd/mcp`, image `termiplus-mcp`): Open WebUI's models and the management console list workspaces and request, list, reset and remove sessions over MCP; admins manage entitlements, users, groups, the network policy and departed users. No file or exec tools: the agent works inside its session.
- **Sign-in with OIDC** (Keycloak first, any OIDC provider): oauth2-proxy verifies the token in front of the MCP server (`mcp-auth`), the console (`desk-auth`), the desktops (`desk-stream-auth`) and port forwards (`fwd-auth`); groups `termiplus-persist` and `termiplus-admin` are roles. `deploy/docker-compose.keycloak.yml`, realm template and `deploy/setup-alpha.sh` set it up; `docs/deploy-alpha.md` walks through it.
- **Open WebUI plugin** (`integrations/openwebui/`): native tools that pass the signed-in user's token, with a confirmation dialog for every destructive or security-sensitive action.
- **Desktops** (image `termiplus-desktop`): the terminal image plus XFCE streamed by KasmVNC, on an origin of their own, under gVisor or Sysbox; view-only share links.
- **Management console** (`/ui/`): users see and control their sessions, homes and snapshots; admins edit entitlements, the network policy and read the audit log.
- **Port forwards** on an origin of their own (`FWD_LISTEN_ADDR`, `fwd-auth`): a user opens a dev server in their own session; other users and Open Terminal's own ports are refused.
- **Workspace entitlements** per group (`MCP_ENTITLEMENTS_FILE`), editable by admins in the console or with `set_entitlements`.
- Release images also for `termiplus-mcp`, `termiplus-desktop` and `termiplus-dns-forwarder`; release candidates (`vX.Y.Z-rc.N`) are published as pre-releases and never tagged `latest`; publishing waits for the maintainer's approval in the protected `release` environment.

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
- MCP: the binding between an OIDC subject and an Open WebUI user id is now one-to-one and immutable. A plugin call that would rebind either side is rejected (`-32003`) before any tool runs, and a call whose binding cannot be stored fails closed (`-32603`). Previously the newest pair won, so a valid token plus the plugin key could move a linked user's home to another subject.
- MCP: `MCP_OWUI_PLUGIN_KEY` now requires `MCP_IDENTITY_FILE`; without it the server refuses to start instead of trusting the plugin-supplied user id without a binding.
- MCP: the identity file is fsynced before and after the atomic rename.
- Logs quote every request-derived value (`%q`), so user ids, names, paths or error texts containing line breaks cannot forge extra log lines (CodeQL go/log-injection).
- Numeric settings are range-checked at startup and the program refuses to start outside the range, instead of wrapping on conversion or overflowing when scaled: `TERMINAL_PORT` 1-65535, `MAX_RUNNING` 0-100000, `SNAPSHOT_KEEP` 0-10000, `EXPORT_CONCURRENCY` 0-1024, `MAX_MEMORY_MB` 1-16777216, `MAX_CPUS` 1-4096, `MAX_PIDS` 1-4194304, `MCP_AUDIT_MAX_MB` 0-1048576 (CodeQL go/incorrect-integer-conversion).
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
- MCP (PR #18): a missing `MCP_ENTITLEMENTS_FILE` with `MCP_AUTH=proxy` stops startup instead of opening every workspace; the file is parsed strictly (unknown fields, unknown workspace names or empty group lists are refused), and entitlements are checked again when sessions are listed, reset, opened as desktops and port-forwarded, so a revoked entitlement also hides existing sessions.
- MCP (PR #18): console writes and desktop WebSocket upgrades compare the browser's full origin (scheme and host) against the configured public URL, the console's for console writes and the desktops' (`STREAM_PUBLIC_URL`) for desktop WebSockets.
- Firewall (PR #18): no global `ESTABLISHED,RELATED ACCEPT` in Docker's forward chain, which applied to every Docker network and let long-lived connections outlive the current egress policy. Only replies to the gateway's own connections and the DNS forwarder's answers are accepted inside the sandbox network (live validation V1: without them the gateway could not reach any terminal once the firewall was applied); Docker handles all other reply traffic.
- Network policy (PR #18): rendered files are staged and published together; if a later step fails, files already published are rolled back.
