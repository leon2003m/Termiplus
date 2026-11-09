# Browser end-to-end test through Open WebUI

Test-only and VM-only. It applies `deploy/firewall.sh`, creates and removes
Docker networks and volumes, and refuses to start next to a live deployment.
Run it only on a disposable VM, never on a host that serves users. It is not
part of CI and not part of `make check` (only its shell scripts are linted).

`remote.sh` copies the checkout to a VM and runs `run.sh` there; see the header
of `run.sh` for the sub-commands and `e2e.py` for the check names.
