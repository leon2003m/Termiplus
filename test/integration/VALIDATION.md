# Firewall validation

## Current source-identity regression (2026-11-05)

`source-pins.py` builds real Linux bridges and attacker/victim/gateway network
namespaces with dummy addresses and an obviously fake bearer. It reproduces
own-MAC/victim-IP ARP poisoning and HTTP credential interception before loading
the actual source-guard functions, then checks prevention on both bridges,
including forged victim MACs, idempotent refresh and unresolved mappings.

```sh
make test-firewall  # portable rule-state and injected-failure tests; also in CI
sudo unshare --net python3 -I -B test/integration/source-pins.py
```

Run the kernel check only in a fresh isolated network namespace on a disposable
Linux environment with Python, iproute2, util-linux and ebtables installed. It
refuses a namespace containing any interface other than loopback and does not
use Docker, real users or real keys. This test exercises actual bridge learning,
ARP, HTTP and ebtables, but not Docker/sysfs port discovery or complete host
firewall installation. It is separate from the historical acceptance below.

On 2026-11-05, all 19 portable tests and the isolated kernel regression passed
using ebtables-nft. The original interception was reproduced with a dummy key
before the guard and prevented after it. The precise runner limits and tested
discovery paths are recorded under Results below. Legacy ebtables and a complete
Docker/Sysbox attack scenario were not exercised by this focused regression.

A fresh DeepSeek security-review session was requested without test results.
OpenCode rejected all three available routes before any review: V4.1 Flash Free
returned HTTP 429 (free quota), OpenCode Go V4.1 Flash returned HTTP 429 (weekly
quota), and the paid V4.1 Flash route returned HTTP 402 (insufficient funds).
**No DeepSeek security review of this patch has completed.** Prior native-tool
functional review is not a security audit of bridge isolation.

## Historical termiplus v0.1.0 live validation

This is the historical runtime/firewall acceptance record. Its results do not
establish current native Desktop/Browser compatibility; see
[native validation](../../docs/native-tool-validation.md) for the current
Open WebUI 0.13 / Open Terminal 0.16 checks. These host-firewall scenarios have
not been rerun as part of that native tool validation.

Scenarios to run against a **real Docker host** before tagging v0.1.0. Unit and
in-process tests cover the code paths; these prove the security model holds with
a live runtime, iptables/ebtables and Docker networking in the loop — which the
Go tests cannot.

Run everything **twice**: once with gVisor and once with Sysbox.

```sh
RUNTIME=sysbox-runc test/integration/run.sh   # must pass fully
RUNTIME=runsc        test/integration/run.sh   # gVisor (experimental for v0.1.0)
```

The base harness (`run.sh` + `checks.py`) already covers service hardening, the
runtime pin, the persistent-home round-trip and the socket-proxy escape attempts.
The scenarios below are the **security acceptance set**; where the base harness
does not yet exercise one it is marked **(manual)** with the commands to run by
hand from the host (or from a throwaway container on `ot-sandbox`).

The scenarios marked **(manual)** are automated by `validate.sh` (base stack +
netpolicy overlay, an internal service at `10.99.0.5` in a network namespace
routed like a LAN host). It needs root and changes the host firewall, so run it
on a throwaway host or VM, after `run.sh`:

```sh
sudo BUILD=0 RUNTIME=sysbox-runc test/integration/validate.sh
sudo BUILD=0 RUNTIME=runsc       test/integration/validate.sh
```

Host prerequisite: **ebtables** must be installed, or `firewall.sh` now exits
non-zero by design (see V2).

## Acceptance bar for v0.1.0

- **Sysbox:** every scenario V1–V15 passes.
- **gVisor:** V1–V7 and V10–V15 pass, and **V8 passes** (the #3 DNS fix). Any
  remaining gVisor-only failure is documented and gVisor ships labeled
  *experimental*.

Record PASS/FAIL per runtime for each scenario and paste the output back.

---

## Group A — Cross-tenant isolation

Set up two terminals on `ot-sandbox` as different users (A, B). Learn B's IP:
`docker inspect -f '{{.NetworkSettings.Networks.ot-sandbox.IPAddress}}' <B>`.

### V1 — terminal → terminal is blocked (#14) **(manual)**
From A: `ping -c1 <B>`, `nc -zv <B> 22`, `curl -m3 http://<B>/`. Then **stop the
firewall unit** (`systemctl stop termiplus-firewall`) and repeat.
**PASS:** all attempts fail in both cases — including with no host firewall
applied, which proves `enable_icc:false` is fail-closed and not reliant on
`firewall.sh`.

### V2 — gateway source-IP / ARP spoofing is blocked (#13) **(manual)**
From A as root: `ip addr add 172.30.0.2/24 dev eth0` and try to reach B; and
`nping --arp --arp-sender-ip 172.30.0.2 <B>` / a forged-source IPv4 packet to B
(`hping3 -a 172.30.0.2 <B>`).
**PASS:** every spoofed-source attempt to B is dropped. (ebtables pins
172.30.0.2 to the gateway MAC and to the gateway's bridge port — confirm both
ebtables chains exist: `ebtables -t filter -L TERMIPLUS-AS` and
`ebtables -t nat -L TERMIPLUS-PORT`. A also tries with the gateway's MAC
forged; that must fail too.) Also confirm the **gateway still reaches
terminals normally** (a terminal session opens and streams) — the RETURN→ACCEPT
rework must not have broken the control path.

### V16 — a terminal cannot take over another terminal's MAC
After the terminals start, run `deploy/firewall.sh --lock-ports` (the
`termiplus-portlock.service` watcher does this live). From A as root set eth0
to B's MAC and IP and send traffic.
**PASS:** every terminal port shows `locked on` and `learning off` in
`bridge -d link`; each terminal's MAC has a static FDB entry on its own port
and B's entry stays on B's port while A forges it; the gateway keeps reaching
B (its replies, which carry B's API key, never go to A) and reaches A again
once A has its own MAC back. Needs Linux and iproute2 6.2 or newer.

### V3 — persistent-volume ownership (#16)
Covered by `checks.py persistence` for the honest case. **(manual)** negative
case: pre-create a volume named like another user's home with a foreign
`termiplus.user` label, then request that user's persistent policy.
**PASS:** the gateway refuses (non-200) and the foreign volume is untouched.

## Group B — egress / network policy

### V4 — never-openable deny-list **(manual)**
From a terminal and as root, attempt each: `127.0.0.1` (host loopback),
`169.254.169.254` (metadata), a `100.64.0.0/10` address, `172.30.0.0/24` hosts
other than the forwarder `172.30.0.3`, `::1`/`fe80::`, and the IPv4-mapped form
`::ffff:127.0.0.1`.
**PASS:** every one fails to connect.

### V5 — br_netfilter ordering & persistence (#17) **(manual)**
On a fresh boot: confirm `sysctl net.bridge.bridge-nf-call-iptables` and
`-ip6tables` read `1` and that `/etc/sysctl.d/90-termiplus-bridge.conf` is
installed. Simulate module-unavailable (`sysctl -w ...=0` then re-run
`firewall.sh`): the script must **exit non-zero and install no rules** rather
than leave DROPs with the sysctl off.
**PASS:** no fail-open window; sysctls persist across reboot; firewall fails
closed when the guarantee can't be met.

### V6 — default-deny internal + admin whitelist (#12) **(manual)**
Pick an internal host (e.g. `10.0.0.5:5432`). From a terminal, confirm it's
blocked. `PUT /admin/netpolicy` adding `10.0.0.5 5432 tcp`; confirm `:5432`
reachable but `:5433` and other internal hosts are not. Remove → blocked again.
Then `PUT` an entry for a deny-list CIDR (e.g. `172.30.0.0/24`).
**PASS:** reach/again-blocked behaviors hold; the deny-list entry is **rejected**.

### V7 — public egress works **(manual)**
From a terminal: `curl -m5 https://1.1.1.1`.
**PASS:** connects (public default-allow intact).

## Group C — DNS (#3 / forwarder)

### V8 — gVisor DNS via the forwarder (#3) — the headline gVisor fix **(manual)**
Under `runsc`, in a terminal: `getent hosts example.com` and
`curl -I https://example.com`.
**PASS:** resolves and connects via `172.30.0.3`; `127.0.0.11` is neither used
nor reachable.

### V9 — custom DNS entry live-reload **(manual)**
`PUT /admin/netpolicy` adding host `db.internal → 10.0.0.5`. Within a few
seconds, `getent hosts db.internal` → `10.0.0.5`. Remove → NXDOMAIN.
**PASS:** add and removal take effect without restarting the forwarder.

### V10 — DNS cannot launder egress **(manual)**
Add a custom name pointing at a deny-listed IP; resolve it, then connect.
**PASS:** resolution may succeed but the connection is still dropped.

## Group D — Docker API confinement (sockproxy)

### V11 — deny-by-default Docker API
Covered by the "socket proxy escape attempts" stage in `run.sh`. Confirm the
attempts (privileged create, host bind-mount, exec into another container, host
network, `--privileged`) all return 4xx and only whitelisted calls succeed.
**PASS:** every forbidden call is rejected.

## Group E — auth fail-closed

### V12 — admin API key (#12) **(manual)**
No key → 404/401; wrong key → 401; the normal connection API key as admin key →
401; correct dedicated key → 200. Confirm the gateway **refuses to boot** with
an admin key <32 chars or equal to the API key.
**PASS:** all hold.

### V13 — mandatory PROXY_TOKEN (#15) **(manual)**
Start sockproxy with no `PROXY_TOKEN` → refuses to start; `<32` chars → refuses;
valid → starts.
**PASS:** fail-closed on missing/weak token.

## Group F — sudo & persistence

### V14 — root-in-container ≠ root-on-host **(manual)**
As a terminal, `sudo -i`, then attempt: mount host filesystems, read the host's
`/proc/1`, `mknod` a device, write through any mount, load a kernel module.
**PASS:** sudo works inside; every host-escape attempt is blocked (runsc
`--allow-suid` and Sysbox).

### V15 — persistent vs non-persistent + profiles
Persistence covered by `checks.py persistence`. **(manual)** non-persistent:
write a file, recreate, confirm it's gone and the configured profile apps are
present.
**PASS:** persistence toggle and profile provisioning behave as specified.

---

### Notes for the #13/#14 rework

`firewall.sh` now converts the gateway→terminal rule to an **ACCEPT** pinned to
the gateway MAC via ebtables, and the sandbox bridge runs **enable_icc:false**.
This changes FORWARD-path semantics, so **V1, V2 and V7 are the gating checks**:
confirm (a) terminals still cannot reach each other or spoof the gateway, and
(b) the gateway→terminal control path and terminal→internet egress both still
work. If ebtables negation syntax differs on the host's ebtables build
(`-s ! <mac>`), adjust and note it.

---

## Results

### 2026-11-05, isolated source-identity regression

`source-pins.py` passed on Linux 6.18.60-1-lts with ebtables 1.8.13
(`nf_tables`), in disposable user/PID/network/mount namespaces. Docker metadata
was stubbed; host veth discovery, `lsns NETNSID`, bridge rules, ARP and HTTP
were real. Both `ot-sandbox` and `ot-offline` reproduced victim-IP takeover
and interception of an obviously fake bearer before enforcement. After applying
the source guards, own-MAC/victim-IP and combined MAC/IP forgery were blocked,
the gateway reached the actual victim, and unresolved mappings stayed denied.
Host namespace discovery also ignored forged links inside the attacker namespace.

The runner used no external network, Docker daemon or real identities. Source
and tools were read-only; writable scratch was capped at 64 MiB, with 1 GiB RAM,
one CPU, 64 tasks, a 90-second deadline and mount syscalls denied after setup.
This validates the source guards and namespace discovery, not the entire Docker,
Sysbox, DNS or systemd deployment. `make map` and `make check` also passed in a
separate bounded offline sandbox, including Go race tests, 30 plugin tests and
19 unprivileged firewall tests.

### 2026-10-13, throwaway VM

Debian 13 VM (QEMU/KVM, user-mode networking), kernel 6.12.111, Docker 26.1.5,
iptables/ebtables 1.8.11 (nf_tables), Sysbox CE 0.7.1, gVisor
release-20261005.0 (`runsc install -- --allow-suid`, as `deploy/daemon.json`).

| | Sysbox | gVisor |
|---|---|---|
| `run.sh` | all checks pass | all pass except "internet reachable" (no DNS without the forwarder, #3; IP connections work) |
| V1–V7, V10, V12–V15 (`validate.sh`, V11 via `run.sh`) | PASS | PASS |
| V8, V9 (DNS via the forwarder) | PASS with the firewall and resolver fixes below | PASS with the firewall and resolver fixes below |

Found and fixed during this run:

- With the netpolicy overlay, terminals could not reach the DNS forwarder at
  all: the #13/#14 rework drops every sandbox->sandbox packet, the forwarder
  included, and the forwarder's queries to a private upstream hit the private-
  range drop. `firewall.sh` now allows exactly terminal -> forwarder:53 and
  forwarder -> `DNS_UPSTREAM`:53, and pins the forwarder's IP to its MAC.
- gVisor: `HostConfig.Dns` does not change `resolv.conf` (Docker keeps
  `127.0.0.11`, which gVisor cannot reach). The gateway now also passes the
  forwarder as `OPEN_TERMINAL_RESOLVER`, and the image's entrypoint writes it
  into `resolv.conf`.

### 2026-10-18, v1.0.0-rc.1 (branch `release/v1.0.0`), same VM

Same VM and versions as 2026-10-13. Tree: alpha + PR #18 + the release fixes.

| | Sysbox | gVisor |
|---|---|---|
| V1–V10, V12–V15 (`validate.sh`) | PASS (66 checks) | PASS (67 checks) |
| V11 and the base checks (`run.sh`) | PASS (28 checks) | PASS in CI (`integration (gvisor)`) |
| Full stack (`alpha-e2e.sh`: Keycloak, TLS, plugin path, console, desktops on their own origin, port forwards, admin console) | PASS (36 checks) | PASS (36 checks) |

Found and fixed during this run:

- PR #18 removed the global `ESTABLISHED,RELATED ACCEPT`; replies from a
  terminal to the gateway and DNS answers from the forwarder are sandbox ->
  sandbox and hit the terminal <-> terminal drop. With the firewall applied
  V1, V2, V8, V9 and V15 failed (the gateway reached no terminal, DNS was
  dead). `firewall.sh` now accepts only replies on connections the gateway
  opened and the forwarder's answers on established DNS connections.
- PR #18 dropped the executable bit of `firewall.sh` (and `oidc/run.sh`), so
  the systemd units would fail with "Permission denied"; `make lint` now
  checks it.
- The gateway in the MCP overlay stayed unhealthy without a writable
  `deploy/backups` (snapshots); `setup-alpha.sh` creates it.
- `setup-alpha.sh` now creates the `entitlements.json` the MCP server needs
  since PR #18 (deny all).
