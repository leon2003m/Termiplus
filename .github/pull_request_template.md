## What

<!-- What does this change do? -->

## Why

<!-- Problem or motivation. Link issues with "Closes #123". -->

## How it was tested

<!-- Commands run, scenarios checked. -->

## Security impact

<!-- Required if this touches internal/sockproxy, internal/gateway auth/routing,
deploy/firewall.sh or container hardening. Otherwise write "None". -->

## Checklist

- [ ] `make check` passes
- [ ] Tests added or updated
- [ ] `CHANGELOG.md` updated under `[Unreleased]` (if user-visible)
- [ ] Docs and `deploy/` examples updated (if behavior or settings changed)
- [ ] No secrets, real hostnames or credentials in the diff
- [ ] Targets `dev` (only `dev` → `main` PRs target `main`)
