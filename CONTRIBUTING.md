# Contributing

## Workflow

1. Branch from `dev` (`feat/...`, `fix/...`).
2. Make your change with tests.
3. Run `make check`.
4. Open a pull request against `dev`.
5. `dev` is merged into `main` by the maintainer. Releases are tagged on `main` as `vMAJOR.MINOR.PATCH`.

AI agents: follow [AGENTS.md](AGENTS.md).

## Development

Requirements: Go 1.24+, Docker. For `make check` also `staticcheck`, `shellcheck`, `hadolint`, `actionlint` and `gitleaks` (CI installs pinned versions; locally use your package manager).

```bash
make test       # go test -race ./...
make check      # everything CI runs on code and config
make build      # build gateway, sockproxy and terminal images
make scan       # gitleaks + govulncheck + trivy (if installed)
```

Optional local hooks: `pre-commit install` (see `.pre-commit-config.yaml`).

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`, `sec:`, `ci:`, `docs:`, `test:`, `refactor:`, `chore:`. Breaking changes: `feat!:` plus a `BREAKING CHANGE:` footer.

## Changelog

Add user-visible changes to `CHANGELOG.md` under `## [Unreleased]`.

## Security-relevant changes

Anything that touches the socket proxy, firewall, authentication or container hardening needs a "Security impact" section in the pull request. See [AGENTS.md](AGENTS.md#security-boundaries-do-not-weaken).

## License

By contributing you agree that your contribution is licensed under the GPL-3.0, like the rest of the project.
