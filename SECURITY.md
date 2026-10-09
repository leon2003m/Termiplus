# Security policy

termiplus runs untrusted, AI-generated code on behalf of many users. Security reports are taken seriously.

## Reporting a vulnerability

Do not open a public issue for security problems.

Report privately through GitHub: **Security → Report a vulnerability** on this repository. If that is not available, contact the maintainer @leon2003m directly on GitHub.

Please include:

- what an attacker can do (for example: escape a terminal container, reach another user's container, reach the Docker socket, reach internal networks)
- steps to reproduce, with the configuration used (runtime, policy, versions)
- any proof-of-concept

You will get an acknowledgement within 7 days. Fixes for confirmed issues are released as soon as possible, and you will be credited unless you prefer not to be.

## Supported versions

| Version | Supported |
|---|---|
| latest release on `main` | yes |
| older releases | no, please update |
| `dev` branch | best effort |

## In scope

- escaping a terminal container or bypassing gVisor/Sysbox isolation as configured by termiplus
- reaching another user's container, files or sessions
- using the socket proxy to do anything outside its allowlist
- bypassing gateway authentication or impersonating another user
- reaching private networks or the host from a terminal despite `deploy/firewall.sh`

## Out of scope

- anything a user can do inside their own sandbox (including root via sudo; that is by design)
- vulnerabilities in gVisor, Sysbox, Docker or the Linux kernel themselves (report upstream)
- deployments that ignore the documented setup, for example running with `runc` or exposing the gateway key to browsers

## Hardening in place

- deny-by-default Docker API proxy that rebuilds every create request from validated fields
- per-container API keys, gateway key never reaches the containers
- gVisor or Sysbox runtimes; `runc` is refused
- sandbox network without access to private ranges, other terminals or the host
- non-root, read-only service containers with all capabilities dropped
- CI: secret scanning (gitleaks), Go vulnerability scanning (govulncheck), image and configuration scanning (Trivy), all third-party actions pinned to commit SHAs
