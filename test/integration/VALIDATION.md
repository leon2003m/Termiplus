# termiplus v0.1.0 live validation

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
172.30.0.2 to the gateway MAC — confirm the `TERMIPLUS-AS` ebtables chain exists:
`ebtables -t filter -L TERMIPLUS-AS`.) Also confirm the **gateway still reaches
terminals normally** (a terminal session opens and streams) — the RETURN→ACCEPT
rework must not have broken the control path.

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
