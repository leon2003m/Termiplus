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

### Changed

- `test/integration/run.sh` refuses to run on a host that already has termiplus containers, home volumes or the `ot-sandbox` network, because its cleanup removes all of them.
- `test/integration/run.sh` runs every stage and reports all failures at the end.
- Interactive terminals start bash with the PTY as controlling terminal (job control works); the image sets `SHELL`.
- User-less spec requests (connection check, OpenAPI, system prompt) share one container per runtime instead of starting one per policy.
- Quick start creates the secrets readable for the owner and the services' group only, independent of the umask.

### Security

- Gateway and socket proxy run as non-root with read-only filesystems, all capabilities dropped and `no-new-privileges`.
- gVisor: `deploy/daemon.json` starts runsc with `--allow-suid` so sudo works inside the sandbox as documented. Elevation stays inside the gVisor sandbox (root of the sandbox kernel, not the host).
- Socket proxy accepts `HostConfig.Dns` only if it is exactly the one configured resolver (`ALLOWED_DNS`, the internal forwarder); any other address, a second entry, `DnsSearch` or `DnsOptions` are refused. Empty keeps `Dns` refused as before. Part of #12.
