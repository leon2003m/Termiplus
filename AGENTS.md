# Rules for AI agents

These rules apply to every AI coding agent working in this repository (Claude Code, Codex, Copilot, others). They are not suggestions. If a task would require breaking one, stop and ask the maintainer (@leon2003m).

## Branches and releases

- Work on `dev` or on a feature branch created from `dev`. Push only to those.
- Never push to `main`. Changes reach `main` only through a pull request from `dev` that the maintainer approves.
- Never force-push, never rewrite published history, never delete branches you did not create.
- Never create, move or delete tags. Releases are tagged by the maintainer.
- Never change repository settings, rulesets, secrets or workflow permissions.

## Before every commit

```bash
make check        # gofmt, go vet, staticcheck, go test -race, shellcheck, hadolint, actionlint
```

All of it must pass. Do not commit with failing tests, and do not skip or delete a test to make it pass.

## Commits and pull requests

- Conventional Commits: `feat:`, `fix:`, `sec:`, `ci:`, `docs:`, `test:`, `refactor:`, `chore:`.
- Small, focused commits. One logical change each.
- Update `CHANGELOG.md` under `## [Unreleased]` for anything user-visible.
- Pull request descriptions say what changed, why, and how it was tested.

## Security boundaries (do not weaken)

termiplus runs code written by AI models for many users. These parts are the security boundary:

- `internal/sockproxy/`: the Docker API allowlist and request validation
- `deploy/firewall.sh`: sandbox egress rules
- container hardening in `Dockerfile`, `image/Dockerfile` and `deploy/*.yml` (non-root users, read-only filesystems, dropped capabilities, `no-new-privileges`, resource limits)
- authentication and user routing in `internal/gateway/`

Changes that loosen any of them (allow a new Docker endpoint or field, add a capability, mount, port, network, runtime, env var, remove a limit, accept a weaker key) need:

1. a test that shows why the change is necessary,
2. an explanation in the pull request under a heading "Security impact",
3. the maintainer's explicit approval.

Tightening is always welcome.

The runtime `runc` must never be allowed in production configuration. `ALLOW_RUNC_INSECURE` exists only for local tests.

## Dependencies

- Go code uses the standard library only. Do not add third-party Go modules without approval.
- GitHub Actions must be pinned to a full commit SHA with the version in a comment. Verify the SHA against the upstream tag before pinning. Never use a tag or branch reference.
- Tools downloaded in CI (scanners, linters) are pinned to a version and verified with a SHA-256 checksum stored in the workflow.
- Container base images are pinned to a tag at minimum; prefer a digest.

## Secrets

- Never commit secrets, keys, tokens or real hostnames. `deploy/secrets/` is gitignored.
- Never print secrets in logs, test output or CI summaries.
- Test fixtures use obviously fake values.

## Code provenance and licensing

- termiplus is GPL-3.0. Write code yourself. Do not copy code from Open WebUI, Open WebUI Terminals or any project with an incompatible license.
- Reading other projects to understand a protocol is fine; copying their code is not.

## Style

- `gofmt`, standard Go naming, errors wrapped with context (`fmt.Errorf("...: %w", err)`).
- No panics in request paths. Log denials with the reason.
- Keep the README, `deploy/` examples and settings tables in sync with the code.
