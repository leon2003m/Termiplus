# Updatecli

Weekly dependency pull requests, replacing Dependabot. A Forgejo Actions
workflow (`.forgejo/workflows/updatecli.yml`) runs Updatecli, which reads the
manifests in this directory, compares the pinned versions in the repository
with upstream and opens pull requests against `dev`. It never pushes to `dev`
or `main` and never merges.

## How the pieces fit

| Piece | Role |
|---|---|
| `.forgejo/workflows/updatecli.yml` | Monday 04:17 UTC and manual run. Debian trixie-slim, no Node.js, no actions. Clones the repo with the job token, installs Go and Updatecli (pinned version, `sha256sum -c`), runs `updatecli pipeline apply`. |
| `updatecli-values/values.yaml` | Owner, repository, target branch, bot identity, commit footers. The one file another repo edits. |
| `updatecli-values/local.yaml` | Dry-run override: `scm.enabled: false`. |
| `docker-images.yaml` | Image `tag@digest` pins: versioned images (semver tag, then its digest) and rolling tags (digest only). |
| `tools.yaml` | Tool version and sha256 updated atomically in one PR; version-only bump for staticcheck. |
| `go.yaml` | Go toolchain across Dockerfile, CI and the workflow's own tarball; Go modules via autodiscovery. |

One YAML document is one pipeline is one pull request. Each manifest starts
with a header comment saying how to adapt it. The manifest files are Go
templates: do not write double curly braces in comments.

The scm and pull request blocks (`kind: gitea`, `kind: gitea/pullrequest`) are
repeated in every document and are identical apart from the PR title; they are
driven by `updatecli-values/values.yaml`. Updatecli has no include mechanism
for scm blocks, so copy them rather than looking for a shared file.

Updatecli clones the target branch itself, commits to a working branch named
`updatecli_<branch>_<pipelineid>` and opens (or reuses) a pull request from it.
It does not refresh an already open PR's title or body, and if the PR API call
fails the run still reports success; run with `--debug` when an expected PR is
missing.

## Secrets and variables

| Name | Kind | Purpose |
|---|---|---|
| `UPDATECLI_TOKEN` | secret | Forgejo token of the bot user: write access to the repo (push branches, open PRs), nothing else. Without it the workflow only does a dry run. |
| `REGISTRY` | variable | Registry host for the job image (`REGISTRY/dockerhub/library/debian:trixie-slim`). Same variable the CI workflow uses. |
| `INTERNAL_CA` | variable, optional | PEM of the internal root CA (public certificate) when Forgejo uses it. |

The Forgejo URL is taken from the server URL at run time (`FORGEJO_URL`), so no
hostname is committed. `scm.username` in the values file must be the user that
owns the token.

The runner must reach the upstream registries (Docker Hub, quay.io), go.dev and
github.com. If it only reaches a pull-through proxy, point the `image:` of the
*sources* at the proxy path; the target patterns stay as they are.

## Run it

Dry run, no token, no Forgejo (what the workflow does without the secret, and
what to run locally):

```sh
updatecli pipeline diff --config updatecli.d \
  --values updatecli-values/values.yaml --values updatecli-values/local.yaml
```

Real run: `UPDATECLI_TOKEN=... FORGEJO_URL=https://forge.example updatecli pipeline apply --config updatecli.d --values updatecli-values/values.yaml`.
In the workflow, "Run workflow" with `dry_run: true` forces a dry run.

Scheduled workflows run from the default branch, so `updatecli.d/` and the
workflow have to exist there too; the files edited are read from `scm.branch`.

## Adopting these templates in another repository

1. Copy `.forgejo/workflows/updatecli.yml`, `updatecli.d/` and `updatecli-values/`.
2. Edit `updatecli-values/values.yaml`: `owner`, `repository`, `branch`, and
   `footers` if commits need a sign-off (see below).
3. In each manifest keep only the documents that apply and change the file
   paths and patterns in the targets (the header comment lists what).
4. Create the secret `UPDATECLI_TOKEN`; make sure `REGISTRY` exists.
5. Run the workflow once with `dry_run: true`, read the diff, then once for real.
6. Delete Dependabot configuration.

Version and checksum pins at the top of the workflow (`UPDATECLI_*`, `GO_*`)
are maintained by `tools.yaml` and `go.yaml`; drop the Go parts if the repo has
no Go. Clone depth is the `CLONE_DEPTH` env of the workflow (0 = full history,
the default; N = shallow).

## Findings

**Sign-off and commit message.** The `gitea` scm builds the message as
`type(scope): title`, an optional `body`, an Updatecli credit line, then
`footers`. `type`, `scope`, `body` and `footers` are configurable; the title is
generated from the target `name`. Updatecli does not add `Signed-off-by`
itself, but `footers` is free text, so
`footers: "Signed-off-by: updatecli-bot <updatecli-bot@noreply.invalid>"`
(set via `scm.footers`) produces a real trailer. The commit author is
`scm.user` / `scm.email`. A sign-off made this way is a fixed identity, not
computed per commit.

**Trailers.** Updatecli adds exactly one trailer by default, "Made with love by
updatecli" line; `hidecredit: true` removes it and the manifests set it. There is no
AI-related or bot co-author trailer. Anything else in the message comes from
`body`/`footers`. (PR title is the `title` of the pull request action; the PR
body is generated unless `body` is set.)

**Terraform lock files.** Yes. The `terraform` autodiscovery crawler finds
`.terraform.lock.hcl` files and generates, per provider, a source
(`terraform/registry`) and a target `terraform/lock` that rewrites the locked
version and the provider hashes for the platforms listed in `platforms`
(default `linux_amd64`, `linux_arm64`, `darwin_amd64`, `darwin_arm64`),
fetched from the registry (it uses the tfupdate lock library; `hostname`
supports other registries, for example OpenTofu's). This is from the source and docs, not
from a run against a real lock file: check the first generated diff for the
`h1:` and `zh:` entries your Terraform/OpenTofu version expects. Template:

```yaml
autodiscovery:
  scmid: forgejo
  actionid: pr
  groupby: all
  crawlers:
    terraform:
      rootdir: infra
      platforms: [linux_amd64, linux_arm64]
```

Fallback if the hashes are not enough: let a source report the new provider
version and run the real tool in a shell target, which uses the exact lock
format and all hashes:

```yaml
targets:
  lock:
    name: 'refresh .terraform.lock.hcl'
    kind: shell
    scmid: forgejo
    disablesourceinput: true
    spec:
      command: >-
        tofu providers lock -platform=linux_amd64 -platform=linux_arm64
        -platform=darwin_amd64 -platform=darwin_arm64
      workdir: infra
      changedif:
        kind: file/checksum
        spec:
          files: [infra/.terraform.lock.hcl]
```

The shell plugin passes only an allow-listed environment (`PATH`, `HOME`, ...);
add `environments` entries for anything `tofu` needs. The tool must be
installed (pinned, `sha256sum -c`) in the workflow.

**Go vendor directory.** termiplus has none. A repository with `vendor/` adds a
shell target after the module bump: `go mod tidy && go mod vendor` with a
`file/checksum` `changedif` over `go.mod`, `go.sum` and `vendor/modules.txt`.

**Not covered here.** Updatecli's gitea action cannot set labels, reviewers or
auto-merge (by design here, no auto-merge). `OPEN_TERMINAL_VERSION` in
`image/Dockerfile` is deliberately not automated: it is a pip pin without a
checksum and the Open WebUI integration is validated against the exact
version, with the number repeated in README and docs.
