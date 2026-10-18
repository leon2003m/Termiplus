#!/usr/bin/env bash
# Installs pinned CI tools into $HOME/.local/bin and verifies each download
# against a SHA-256 checksum stored here. Never use "latest" and never use an
# installer action for these tools: in March 2026 the trivy-action and
# setup-trivy tags were force-pushed to credential-stealing code.
#
# Updating a tool: change version and checksum together. Checksums come from
# the project's published checksum file (gitleaks, actionlint, trivy) or, where
# none is published (hadolint), from the release asset itself.
set -euo pipefail

BIN="$HOME/.local/bin"
mkdir -p "$BIN"
echo "$BIN" >> "${GITHUB_PATH:-/dev/null}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

GITLEAKS_VERSION=8.30.1
GITLEAKS_SHA256=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
ACTIONLINT_VERSION=1.7.12
ACTIONLINT_SHA256=8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8
HADOLINT_VERSION=2.15.1
HADOLINT_SHA256=c7187db94eeeeca956519a6af171adc31453941a1e777961f6e680f697c8c507
TRIVY_VERSION=0.75.0
TRIVY_SHA256=c6e65abddb348e25f10549df887045629cf28cc72453cd1c63acb717316b3f3f

fetch() { # url sha256 dest
  curl -fsSL --retry 3 -o "$3" "$1"
  echo "$2  $3" | sha256sum -c --quiet - || { echo "checksum mismatch for $1" >&2; exit 1; }
}

for tool in "$@"; do
  case "$tool" in
    gitleaks)
      fetch "https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_linux_x64.tar.gz" "$GITLEAKS_SHA256" "$TMP/gitleaks.tgz"
      tar -xzf "$TMP/gitleaks.tgz" -C "$TMP" gitleaks && install -m 0755 "$TMP/gitleaks" "$BIN/gitleaks" ;;
    actionlint)
      fetch "https://github.com/rhysd/actionlint/releases/download/v${ACTIONLINT_VERSION}/actionlint_${ACTIONLINT_VERSION}_linux_amd64.tar.gz" "$ACTIONLINT_SHA256" "$TMP/actionlint.tgz"
      tar -xzf "$TMP/actionlint.tgz" -C "$TMP" actionlint && install -m 0755 "$TMP/actionlint" "$BIN/actionlint" ;;
    hadolint)
      fetch "https://github.com/hadolint/hadolint/releases/download/v${HADOLINT_VERSION}/hadolint-linux-x86_64" "$HADOLINT_SHA256" "$TMP/hadolint"
      install -m 0755 "$TMP/hadolint" "$BIN/hadolint" ;;
    trivy)
      fetch "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-64bit.tar.gz" "$TRIVY_SHA256" "$TMP/trivy.tgz"
      tar -xzf "$TMP/trivy.tgz" -C "$TMP" trivy && install -m 0755 "$TMP/trivy" "$BIN/trivy" ;;
    *) echo "unknown tool $tool" >&2; exit 1 ;;
  esac
  echo "installed $tool"
done
