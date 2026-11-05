"""Unprivileged source-guard regressions; no Docker or host firewall access.

A tiny ebtables command model executes the real shell functions. The independent
packet oracle checks every intermediate ruleset, including injected failures.
Real-kernel bridge/ARP coverage is in test/integration/source-pins.py.
"""
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
FIREWALL = (ROOT / "deploy/firewall.sh").read_text()
FUNCTIONS = FIREWALL[FIREWALL.index("eb_jump_exists() {"):FIREWALL.index("# sync_ports BRIDGE")]
A = "veth-a 02:00:00:00:00:0a 172.30.0.150"
V = "veth-v 02:00:00:00:00:0b 172.30.0.151"
MOCK = r'''#!/usr/bin/python3
import json,os,sys
from pathlib import Path
p=Path(os.environ['STATE']);s=json.loads(p.read_text());args=sys.argv[1:]
assert args[:2]==['-t','nat'];args=args[2:];cmd,chain,*spec=args
s['commands'].append(args)
if s.get('fail') and args[:len(s['fail'])]==s['fail']:
 p.write_text(json.dumps(s));sys.exit(1)
cs=s['chains'];rc=0
if cmd=='-N':
 if chain in cs:rc=1
 else:cs[chain]=[]
elif cmd=='-F':
 assert not any(r[-1]==chain for r in cs['PREROUTING']), 'flushed an attached guard'
 cs[chain]=[]
elif cmd in ('-A','-I'):
 if cmd=='-I':spec=spec[1:]
 if '-p' in spec:
  def v(k):return spec[spec.index(k)+1]
  proto=v('-p');rule=['-p',proto,'-s',v('-s'),'-i',v('-i')]
  for k in ('--ip-src','--arp-ip-src','--arp-mac-src'):
   if k in spec:rule += [k,v(k)]
  spec=rule+['-j',v('-j')]
 cs[chain].insert(0,spec) if cmd=='-I' else cs[chain].append(spec)
elif cmd=='-D':cs[chain].remove(spec)
elif cmd=='-L':
 if chain not in cs:rc=1
 else:
  print('Bridge chain: '+chain+', entries: '+str(len(cs[chain]))+', policy: RETURN')
  for rule in cs[chain]:print(' '.join(rule))
else:raise AssertionError('Unsupported command '+cmd)
p.write_text(json.dumps(s))
if rc==0 and cmd!='-L':
 with Path(os.environ['HISTORY']).open('a') as f:f.write(json.dumps(cs)+'\n')
sys.exit(rc)
'''


def verdict(chains, bridge, port, mac, ip, proto="IPv4", arp_mac=None):
    """Evaluate frame fields without reusing the shell's rule construction."""
    for jump in chains["PREROUTING"]:
        if jump[:2] != ["--logical-in", bridge]:
            continue
        for rule in chains[jump[-1]]:
            if rule == ["-j", "DROP"]:
                return "DROP"
            fields = dict(zip(rule[::2], rule[1::2]))
            if fields.get("-i") != port or fields.get("-s") != mac or fields.get("-p") != proto:
                continue
            source = fields.get("--ip-src", fields.get("--arp-ip-src"))
            if source != ip or (proto == "ARP" and fields.get("--arp-mac-src") != arp_mac):
                continue
            break  # RETURN to PREROUTING; another guard may still deny it.
        else:
            continue
    return "RETURN"


class SourcePins(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root / "ebtables").write_text(MOCK)
        (self.root / "ebtables").chmod(0o755)
        self.state = self.root / "state.json"
        self.history = self.root / "history.jsonl"
        self.state.write_text(json.dumps({"chains": {"PREROUTING": []}, "commands": []}))
        self.env = {"PATH": str(self.root) + ":/usr/bin:/bin", "STATE": str(self.state), "HISTORY": str(self.history)}

    def tearDown(self):
        self.tmp.cleanup()

    def run_shell(self, code, success=True):
        script = "set -eu\nBRIDGE=ot-sandbox\nOFFLINE_BRIDGE=ot-offline\n" + FUNCTIONS + "\n" + code
        p = subprocess.run(["/bin/sh"], input=script, text=True, env=self.env, capture_output=True, timeout=15)
        self.assertEqual(p.returncode == 0, success, p.stderr)
        return p.stdout

    def sync(self, rows=A + "\n" + V, bridge="ot-sandbox", success=True):
        return self.run_shell("sync_source_pins " + shlex.quote(bridge) + " " + shlex.quote(rows), success)

    def snapshot(self):
        return json.loads(self.state.read_text())["chains"]

    def inject_failure(self, prefix):
        s = json.loads(self.state.read_text());s["fail"] = prefix;self.state.write_text(json.dumps(s))

    def assert_spoof_denied(self, state):
        for proto in ("IPv4", "ARP"):
            for port, mac in (("veth-a", "02:00:00:00:00:0a"), ("veth-a", "02:00:00:00:00:0b"), ("new-port", "02:00:00:00:00:0a")):
                self.assertEqual(verdict(state, "ot-sandbox", port, mac, "172.30.0.151", proto, mac), "DROP")
        for proto in ("IPv6", "802_1Q", "802_1AD", "0x88b5", "LLDP"):
            self.assertEqual(verdict(state, "ot-sandbox", "veth-a", "02:00:00:00:00:0a", "172.30.0.150", proto), "DROP")

    def test_claiming_victim_ip_with_own_or_victim_mac_is_denied(self):
        self.sync();self.assert_spoof_denied(self.snapshot())
        for port, mac, ip in (A.split(), V.split()):
            for proto in ("IPv4", "ARP"):
                self.assertEqual(verdict(self.snapshot(), "ot-sandbox", port, mac, ip, proto, mac), "RETURN")
        self.assertEqual(verdict(self.snapshot(), "ot-sandbox", *A.split(), "ARP", "02:00:00:00:00:0b"), "DROP")

    def test_unknown_ports_and_all_intermediate_updates_fail_closed(self):
        self.sync()
        for line in self.history.read_text().splitlines():
            state=json.loads(line)
            if state["PREROUTING"]:self.assert_spoof_denied(state)

    def test_offline_bridge_and_unrelated_bridge(self):
        self.sync(A.replace("172.30.0.", "172.30.1."), "ot-offline")
        s=self.snapshot()
        self.assertEqual(verdict(s, "ot-offline", "veth-a", "02:00:00:00:00:0a", "172.30.1.151", "ARP", "02:00:00:00:00:0a"), "DROP")
        self.assertEqual(verdict(s, "other-bridge", "veth-a", "02:00:00:00:00:0a", "172.30.0.151"), "RETURN")

    def test_noop_does_not_duplicate_jumps_or_rebuild(self):
        self.sync();before=self.snapshot();self.sync();self.assertEqual(before,self.snapshot())
        self.run_shell("source_pins_current ot-sandbox " + shlex.quote(A+"\n"+V))

    def test_removed_or_unresolved_mapping_is_revoked(self):
        self.sync();self.sync(V)
        self.assertEqual(verdict(self.snapshot(), "ot-sandbox", *A.split()), "DROP")
        self.sync("");self.assertEqual(verdict(self.snapshot(), "ot-sandbox", *V.split()), "DROP")

    def test_same_port_address_change_revokes_old_ip(self):
        self.sync();self.sync(A.replace(".150", ".152")+"\n"+V)
        self.assertEqual(verdict(self.snapshot(), "ot-sandbox", *A.split()), "DROP")
        self.assertEqual(verdict(self.snapshot(), "ot-sandbox", *A.replace(".150", ".152").split()), "RETURN")

    def test_build_failure_keeps_old_guard_attached(self):
        self.sync();before=self.snapshot();self.inject_failure(["-A", "TERMIPLUS-IP-SA"])
        self.sync(V,success=False);self.assertEqual(before["PREROUTING"],self.snapshot()["PREROUTING"]);self.assert_spoof_denied(self.snapshot())

    def test_listing_failure_never_flushes_attached_guard(self):
        self.sync();before=self.snapshot()
        self.inject_failure(["-L", "PREROUTING"])
        self.sync(V,success=False)
        self.assertEqual(before,self.snapshot())
        self.assert_spoof_denied(self.snapshot())

    def test_conditional_callers_cannot_disable_fail_closed_errors(self):
        self.sync()
        baseline = self.state.read_text()
        for prefix in (["-F", "TERMIPLUS-IP-SA"], ["-A", "TERMIPLUS-IP-SA"],
                       ["-I", "PREROUTING"], ["-D", "PREROUTING"], ["-L", "PREROUTING"]):
            with self.subTest(command=prefix):
                self.state.write_text(baseline)
                self.inject_failure(prefix)
                self.run_shell("sync_source_pins ot-sandbox " + shlex.quote(V) + " || true")
                self.assert_spoof_denied(self.snapshot())

    def test_interrupted_swap_keeps_newest_guard_and_recovers(self):
        self.sync();self.inject_failure(["-D", "PREROUTING"]);self.sync(V,success=False)
        self.assertEqual(verdict(self.snapshot(), "ot-sandbox", *A.split()), "DROP")
        s=json.loads(self.state.read_text());s.pop("fail");self.state.write_text(json.dumps(s))
        recovery_start=len(self.history.read_text().splitlines())
        self.sync(V);self.assertEqual(len(self.snapshot()["PREROUTING"]),1)
        for line in self.history.read_text().splitlines()[recovery_start:]:
            self.assertEqual(verdict(json.loads(line),"ot-sandbox",*A.split()),"DROP")


class PortWatcher(unittest.TestCase):
    def test_multicast_flooding_is_checked_and_disabled(self):
        source = FIREWALL[FIREWALL.index("port_flags_ok() {"):FIREWALL.index("# port_macs BRIDGE")]
        for flag in ("on", "off"):
            script = ('set -eu\nbridge() {\n'
                      'if [ "$1" = -d ]; then echo " locked on learning off flood off mcast_flood '
                      + flag + ' "; else echo "$*"; fi; }\n' + source + '\nlock_port veth-dummy\n')
            p = subprocess.run(["/bin/sh"], input=script, capture_output=True, text=True,
                               timeout=5, env={"PATH": "/usr/bin:/bin"})
            self.assertEqual(p.returncode, 0, p.stderr)
            self.assertEqual(p.stdout.strip(), "link set dev veth-dummy locked on learning off flood off mcast_flood off"
                             if flag == "on" else "")

    def test_lock_failure_never_mutates_source_pins(self):
        source = FIREWALL[FIREWALL.index("sync_all() {"):FIREWALL.index("# port_worker keeps")]
        with tempfile.TemporaryDirectory() as tmp:
            script = ('set -eu\nBRIDGE=ot-sandbox\nOFFLINE_BRIDGE=ot-offline\n'
                      'flock() { return 1; }\n'
                      'sync_ports() { echo unsafe-mutation; }\n' + source + '\nsync_all || exit 7\n')
            result = subprocess.run(["/bin/sh"], input=script, text=True, capture_output=True,
                                    timeout=5, env={"PATH": "/usr/bin:/bin", "PORTLOCK_DIR": tmp})
            self.assertEqual(result.returncode, 7, result.stderr)
            self.assertEqual(result.stdout, "")

    def test_deleted_event_keeps_actual_interface_name(self):
        source = FIREWALL[FIREWALL.index("link_event_port() {"):FIREWALL.index("# --lock-ports:")]
        for prefix in ("", "Deleted "):
            line = prefix + "17: vethabc@if2: <UP> master ot-sandbox state UP"
            result = subprocess.run(["/bin/sh"], input=source+"\nlink_event_port "+shlex.quote(line),
                                    text=True, capture_output=True, timeout=5,
                                    env={"PATH": "/usr/bin:/bin"})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), "vethabc")

    def test_worker_refreshes_service_pins_and_propagates_failure(self):
        source = FIREWALL[FIREWALL.index("port_worker() {"):FIREWALL.index("# ip monitor prefixes")]
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            command = root / "firewall.sh"
            log = root / "calls"
            command.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$CALLS"\nexit "$APPLY_RC"\n')
            command.chmod(0o755)
            for rc in (0, 1):
                script = 'set -eu\nsync_all() { return 0; }\n' + source + '\nport_worker\n'
                p = subprocess.run(["/bin/sh"], input=script, text=True, capture_output=True, timeout=5,
                                   env={"PATH": "/usr/bin:/bin", "PORTLOCK_DIR": tmp, "SCRIPT_DIR": tmp,
                                        "CALLS": str(log), "APPLY_RC": str(rc)})
                self.assertEqual(p.returncode, rc, p.stderr)
            self.assertEqual(log.read_text().splitlines(), ["--if-needed", "--if-needed"])


class PortDiscovery(unittest.TestCase):
    """Resolve identities without trusting any namespace-controlled links."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.net = self.root / "net"
        (self.net / "ot-sandbox/brif/veth-a").mkdir(parents=True)
        host = self.net / "veth-a"
        host.mkdir()
        (host / "ifindex").write_text("17")
        (host / "iflink").write_text("2")
        self.data = {"rows": "workspace-a 02:00:00:00:00:0a 172.30.0.150/24",
                     "pid": "123", "nsid": "7", "link": "17: veth-a@if2: <UP> master ot-sandbox link/ether 02:11:22:33:44:55 brd ff:ff:ff:ff:ff:ff link-netnsid 7"}
        mock = """#!/usr/bin/python3
import json,os,sys
from pathlib import Path
s=json.loads(Path(os.environ['FIXTURE']).read_text())
name=Path(sys.argv[0]).name
if name=='ip':
 assert sys.argv[1:]==['-o','link','show','master','ot-sandbox']
 if s.get('link_error'):sys.exit(1)
 print(s['link'])
elif name=='lsns':
 assert sys.argv[1:]==['--type','net','--task','123','--noheadings','--raw','--output','NETNSID']
 if s.get('nsid_error'):sys.exit(1)
 print(s['nsid'])
elif sys.argv[1:3]==['network','inspect']:
 assert sys.argv[3]=='ot-sandbox' and '.IPv4Address' in sys.argv[-1]
 if s.get('unavailable'):sys.exit(1)
 print(s['rows'])
else:
 assert sys.argv[1:3]==['inspect','-f']
 print(s['pid'])
"""
        for name in ("docker", "lsns", "ip"):
            command = self.root / name
            command.write_text(mock)
            command.chmod(0o755)
        self.fixture = self.root / "fixture.json"

    def tearDown(self):
        self.tmp.cleanup()

    def discover(self):
        self.fixture.write_text(json.dumps(self.data))
        source = FIREWALL[FIREWALL.index("port_macs() {"):FIREWALL.index("# ebtables -C")]
        # Redirect sysfs for the separate absent-bridge test. Discovery itself
        # uses only Docker, lsns and host ip output, never namespace commands.
        source = source.replace("/sys/class/net", str(self.net))
        result = subprocess.run(["/bin/sh"], input="set -eu\n" + source + "\nport_macs ot-sandbox\n",
                                text=True, capture_output=True, timeout=5,
                                env={"PATH": str(self.root) + ":/usr/bin:/bin", "FIXTURE": str(self.fixture)})
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip()

    def test_docker_assigned_ip_is_bound_to_host_namespace(self):
        self.assertEqual(self.discover(), A)
        self.data["nsid"] = "8"
        self.assertEqual(self.discover(), "")
        self.data["nsid"] = "unassigned"
        self.assertEqual(self.discover(), "")

    def test_ambiguous_ports_and_duplicate_claims_are_denied(self):
        self.data["link"] += "\n" + self.data["link"].replace("veth-a", "veth-b")
        self.assertEqual(self.discover(), "")
        self.data["link"] = self.data["link"].splitlines()[0]
        self.data["rows"] += "\nworkspace-b 02:00:00:00:00:0b 172.30.0.151/24"
        self.assertEqual(self.discover(), "")

    def test_host_discovery_errors_and_partial_results_are_denied(self):
        for flag in ("link_error", "nsid_error", "unavailable"):
            with self.subTest(flag=flag):
                self.data[flag] = True
                self.assertEqual(self.discover(), "")
                del self.data[flag]

    def test_incomplete_stopped_and_unavailable_containers_are_not_admitted(self):
        self.data["pid"] = "0"
        self.assertEqual(self.discover(), "")
        self.data["pid"] = "123"
        self.data["rows"] = "workspace-a 02:00:00:00:00:0a"
        self.assertEqual(self.discover(), "")
        self.data["unavailable"] = True
        self.assertEqual(self.discover(), "")

    def test_absent_bridge_is_guarded_before_first_port_can_appear(self):
        command = self.root / "ebtables"
        command.write_text(MOCK)
        command.chmod(0o755)
        state = self.root / "state.json"
        state.write_text(json.dumps({"chains": {"PREROUTING": []}, "commands": []}))
        source = FIREWALL[FIREWALL.index("eb_jump_exists() {"):FIREWALL.index("# sync_all runs")]
        source = source.replace("/sys/class/net", str(self.net))
        result = subprocess.run(["/bin/sh"], input="set -eu\nBRIDGE=ot-sandbox\nOFFLINE_BRIDGE=ot-offline\n" + source + "\nsync_ports ot-offline\n",
                                text=True, capture_output=True, timeout=5,
                                env={"PATH": str(self.root) + ":/usr/bin:/bin", "STATE": str(state),
                                     "HISTORY": str(self.root / "history.jsonl")})
        self.assertEqual(result.returncode, 0, result.stderr)
        chains = json.loads(state.read_text())["chains"]
        self.assertEqual(verdict(chains, "ot-offline", "future-port", *A.split()[1:]), "DROP")


if __name__ == "__main__":
    unittest.main()
