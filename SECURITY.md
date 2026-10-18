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
| latest 1.x release | yes |
| release candidates (`-rc.N`) | until the final release of that version |
| older releases | no, please update |
| `dev` branch | best effort |

## In scope

- escaping a terminal container or bypassing gVisor/Sysbox isolation as configured by termiplus
- reaching another user's container, files or sessions
- using the socket proxy to do anything outside its allowlist
- bypassing gateway authentication or impersonating another user
- bypassing the MCP server's sign-in, roles, entitlements or confirmations, or acting as another user through the console, desktops, share links or port forwards
- script from a desktop, a share link or a forwarded app acting on the console's origin
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
- sign-in only through oauth2-proxy in front of every browser- and plugin-facing listener; the console, desktops and port forwards each on an origin of their own
- CI: secret scanning (gitleaks), Go vulnerability scanning (govulncheck), image and configuration scanning (Trivy), CodeQL, all third-party actions pinned to commit SHAs
- `main` changes only through pull requests with all checks green; releases are published only after the maintainer's approval

## Verifying a release

Every release image is signed keylessly with Sigstore cosign by this repository's release workflow and carries a build provenance attestation and an SBOM. Pin images by digest (the release notes list them) and verify before deploying:

```sh
cosign verify \
  --certificate-identity-regexp '^https://github.com/leon2003m/Termiplus/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/leon2003m/termiplus-gateway@sha256:<digest>
gh attestation verify oci://ghcr.io/leon2003m/termiplus-gateway@sha256:<digest> --repo leon2003m/Termiplus
```
