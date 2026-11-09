#!/usr/bin/env bash
# Copy this checkout to a disposable VM and run the browser end-to-end test there.
#
#   VM_SSH='ssh -p 2222 -i ~/.local/share/vms/tp-validate/vmkey -o BatchMode=yes tp@127.0.0.1' \
#     test/integration/owui-browser/remote.sh [run.sh arguments]
#
# Results (screenshots, logs, results.json) are copied back to
# $LOCAL_OUT (default ./owui-e2e-out). Needs passwordless sudo in the VM.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
: "${VM_SSH:?set VM_SSH to the ssh command for the VM}"
LOCAL_OUT="${LOCAL_OUT:-./owui-e2e-out}"
DIR="${VM_DIR:-owui-e2e}"
# shellcheck disable=SC2086
$VM_SSH "rm -rf $DIR/repo && mkdir -p $DIR/repo"
tar -C "$ROOT" --exclude=.git --exclude=.claude --exclude=node_modules -czf - . | $VM_SSH "tar -xzf - -C $DIR/repo"
rc=0
$VM_SSH "cd $DIR/repo && sudo RUNTIME=${RUNTIME:-sysbox-runc} BUILD=${BUILD:-0} OWUI_E2E_STATE=/var/tmp/owui-e2e bash test/integration/owui-browser/run.sh ${*:-all}" || rc=$?
mkdir -p "$LOCAL_OUT"
$VM_SSH "sudo tar -C /var/tmp/owui-e2e/artifacts -czf - ." | tar -xzf - -C "$LOCAL_OUT" || true
exit "$rc"
