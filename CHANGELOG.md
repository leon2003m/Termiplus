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
- Docker inside Sysbox workspaces: `start-docker` in the terminal image starts a daemon once the user installed Docker (the entrypoint restarts it with the container), and the tool description tells the model so under Sysbox only.

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
