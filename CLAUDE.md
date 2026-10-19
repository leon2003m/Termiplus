Read and follow [AGENTS.md](AGENTS.md). It contains the rules for working in this repository, including branch, security and dependency rules.

Quick reference:

- Push only to `dev`. Never to `main`, never force-push, never tag.
- `make check` must pass before every commit.
- Do not weaken `internal/sockproxy/`, `deploy/firewall.sh` or container hardening without a test, a "Security impact" section and maintainer approval.
- Go standard library only.
- Before changing code, open the system map [map/CLAUDE.md](map/CLAUDE.md); update cited cards with the code.
