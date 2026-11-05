---
type: process
status: verified
verified: 2026-11-05 (native release and desktop SBOM export)
consumes: []
produces: []
---

# release

A maintainer-pushed `v*` tag on `main` becomes signed, attested images on GHCR plus a GitHub release.

## Input → Movement → Output

The maintainer pushes `vX.Y.Z` or `vX.Y.Z-rc.N` on a commit already on `main`. `verify` checks that ancestry. `approve` waits in the protected `release` environment for a human. `images` then builds gateway, sockproxy, mcp, terminal and dns-forwarder for amd64 and arm64. `desktop` builds on the terminal image for amd64 only. Every image gets an SBOM, a provenance attestation and a cosign signature. The desktop package SBOM is generated with Trivy, attached with `cosign attest --type spdxjson`, and verified against the exact release workflow identity before publication; its inline file-level SBOM exceeds BuildKit's 80 MiB limit. Other images retain inline BuildKit SBOMs. `github-release` writes notes from `CHANGELOG.md`.

## Why this shape

Signing and attestations go to public transparency logs and cannot be taken back, so nothing publishes before a human approves. Only the maintainer tags (rule in [AGENTS.md](../../AGENTS.md)): agents never create, move or delete tags. An `-rc.N` tag is a pre-release and does not move `latest`. The desktop release matrix currently remains amd64 only. Its image now uses the native Open Terminal desktop dependencies; additional architectures need a verified native image build before enabling publication.

## Steps

1. Trigger: tag patterns for releases and rcs: `.github/workflows/release.yml:4-6`.
2. `verify`: the tag commit must be an ancestor of `origin/main`: `release.yml:24-38`.
3. `approve`: `environment: release`, required reviewer: `release.yml:40-49`.
4. `images`: a matrix of 5 images, semver tags, `latest` only when not a pre-release, both platforms, SBOM, attestation, cosign: `release.yml:51`, `:65-69`, `:85-89`, `:96`, `:115-126`.
5. `desktop`: needs `images`, pins the terminal base by digest, amd64 only: `release.yml:138`, `:160`, `:185`.
6. Desktop SBOM signing and verification: `release.yml`, `Sign and verify desktop SBOM attestation`. The downloadable Trivy SPDX package SBOM is signed against the published image digest; image signing and maximum provenance remain enabled.
7. `github-release`: notes from the `## [X.Y.Z]` section, falling back to `[Unreleased]`: `release.yml`, `github-release` and `Release notes from CHANGELOG.md`.
8. The binary version is set via `-X main.version`: `Dockerfile:14-16`.

## If you change this

- **Hits:** branch protection and rulesets (maintainer only, never changed by agents), action SHA pins (`AGENTS.md` rule), image names that deploy compose files reference, `CHANGELOG.md` section headings.
- **Does not hit:** runtime code paths.

## Surfaces

| Surface | Role |
|---|---|
| maintainer | tags, approves |
| GitHub Actions `release` environment | gate |
| GHCR | images, signatures, attestations |
| GitHub release | notes and SBOMs |

## See

- Source: `.github/workflows/release.yml`, `CHANGELOG.md`, [AGENTS.md](../../AGENTS.md) "Branches and releases"
