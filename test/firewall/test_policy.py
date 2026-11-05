"""Firewall policy regressions with dummy identities and transactional command models.

No Docker daemon or host networking access. The real restore transaction is also
exercised in a fresh network namespace by integration/firewall-policy.py.
"""
import ipaddress
import json
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SOURCE = (ROOT / "deploy/firewall.sh").read_text()
VALIDATION = SOURCE[SOURCE.index("policy_destination_ok() {"):SOURCE.index("# Bridge-netfilter")]
CURRENT = SOURCE[SOURCE.index("policy_fingerprint() {"):SOURCE.index('if [ "${1:-}" = --if-needed ]')]
RENDER = SOURCE[SOURCE.index("upstream_ok() {"):SOURCE.index("\napply_ip_policy\n")]
DISCOVERY = SOURCE[SOURCE.index("mac_of() {"):SOURCE.index("# pin_ip IP")]
PORT_DISCOVERY = SOURCE[SOURCE.index("port_macs() {"):SOURCE.index("# ebtables -C")]
FUNCTIONS = VALIDATION + CURRENT + RENDER
VARIABLES = """
BRIDGE=ot-sandbox
OFFLINE_BRIDGE=ot-offline
SANDBOX=172.30.0.0/24
GATEWAY=172.30.0.2
FORWARDER=172.30.0.3
OFFLINE_GATEWAY=172.30.1.2
OFFLINE_SUBNET=172.30.1.0/24
DNS_UPSTREAM=192.0.2.53
mac=02:00:00:00:00:02
forwarder_mac=02:00:00:00:00:03
offline_gateway_mac=02:00:00:00:01:02
port=veth-gateway
forwarder_port=veth-forwarder
offline_gateway_port=veth-offline
SCRIPT_FILE=$SCRIPT_DIR/firewall.sh
ebtables() { printf '%s\\n' "${SERVICE_PINS:-service-pins}"; }
mac_of() { echo 02:00:00:00:00:02; }
port_of() { echo veth-service; }
port_macs() { :; }
source_pins_current() { return 0; }
eb_jump_exists() { return 0; }
"""

# Updates are staged in a separate dictionary and published only at COMMIT.
# The independent verdict below checks policy effects, not string equality.
MOCK = r'''#!/usr/bin/python3
import copy,json,os,sys
from pathlib import Path
p=Path(os.environ['STATE']);s=json.loads(p.read_text());a=sys.argv[1:]
name=Path(sys.argv[0]).name;family='v6' if name.startswith('ip6') else 'v4'
assert a[:2]==['-w','10'];a=a[2:]
if name.endswith('restore'):
 assert '--noflush' in a
 stage='test' if '--test' in a else 'commit'
 text=sys.stdin.read();assert text.startswith('*filter\n') and text.endswith('COMMIT\n')
 if s.get('fail')==family+'-'+stage:sys.exit(1)
 cs=copy.deepcopy(s[family])
 for line in text.splitlines()[1:-1]:
  r=line.split()
  if r[0].startswith(':'):cs[r[0][1:]]=[]
  elif r[0]=='-A':cs[r[1]].append(r)
  elif r[0]=='-D':cs[r[1]].remove(['-A']+r[1:])
  elif r[0]=='-I':cs[r[1]].insert(int(r[2])-1,['-A',r[1]]+r[3:])
  else:raise AssertionError(line)
 if stage=='commit':s[family]=cs;p.write_text(json.dumps(s))
else:
 assert a[0]=='-S'
 if a[1] not in s[family]:sys.exit(1)
 print('-N '+a[1])
 for r in s[family][a[1]]:print(' '.join(r))
'''


def verdict(text, chain="TERMIPLUS", src="172.30.0.150", dst="192.168.1.5",
            proto="tcp", dport="443", sport="40000", state="NEW",
            incoming="ot-sandbox", outgoing="eth0"):
    """Small independent packet matcher for the filter rules used here."""
    for line in text.splitlines():
        r = line.split()
        if r[:2] != ["-A", chain]:
            continue
        fields = dict(zip(r[2::2], r[3::2]))
        if any(key in fields and fields[key] != value for key, value in
               (("-i", incoming), ("-o", outgoing), ("-p", proto),
                ("--dport", dport), ("--sport", sport))):
            continue
        if any(key in fields and ipaddress.ip_address(value) not in
               ipaddress.ip_network(fields[key]) for key, value in (("-s", src), ("-d", dst))):
            continue
        if "--ctstate" in fields and state not in fields["--ctstate"].split(","):
            continue
        return fields["-j"]
    return "RETURN"


class Policy(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.state = self.root / "state.json"
        self.state.write_text(json.dumps({"v4": {"DOCKER-USER": [], "INPUT": [],
                                                  "OTHER": [["-A", "OTHER", "-j", "DROP"]]},
                                         "v6": {"FORWARD": [], "INPUT": []}}))
        for name in ("iptables", "ip6tables", "iptables-restore", "ip6tables-restore"):
            p = self.root / name
            p.write_text(MOCK)
            p.chmod(0o755)
        ip = self.root / "ip"
        ip.write_text('#!/bin/sh\nprintf "1: dummy inet 192.0.2.53/32 scope global dummy\\n"\n')
        ip.chmod(0o755)
        (self.root / "firewall.sh").write_text(SOURCE)
        self.rules = self.root / "egress.rules"
        self.rules.write_text("10.2.0.0/16 443 tcp\n")
        self.env = {"PATH": str(self.root) + ":/usr/bin:/bin", "STATE": str(self.state),
                    "SCRIPT_DIR": str(self.root), "PORTLOCK_DIR": str(self.root),
                    "EGRESS_RULES": str(self.rules)}

    def tearDown(self):
        self.tmp.cleanup()

    def shell(self, code, success=True):
        p = subprocess.run(["/bin/sh"], input="set -eu\n" + VARIABLES + FUNCTIONS + "\n" + code,
                           env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(p.returncode == 0, success, p.stderr)
        return p.stdout

    def test_canonical_cidr_and_never_range_overlap(self):
        good = ("10.0.0.0/8", "192.0.2.1/32", "192.168.0.0/16", "172.31.0.0/16")
        bad = ("999.1.1.1/32", "1.2.3.4/33", "1.2.3.4/00", "010.0.0.0/8",
               "1.2.3.4/24", "127.0.0.0/8", "100.64.0.0/9", "172.0.0.0/8",
               "0.0.0.0/0", "169.254.1.1/32", "172.30.1.2/32", "1.2.3.4/32/1")
        code = "\n".join("policy_destination_ok " + shlex.quote(x) for x in good)
        code += "\n" + "\n".join("! policy_destination_ok " + shlex.quote(x) for x in bad)
        self.shell(code)

    def test_invalid_policy_lines_cannot_open_denied_ranges(self):
        self.rules.write_text("0.0.0.0/0 any tcp\n172.0.0.0/8 80 tcp\n"
                              "10.1.2.3/24 80 tcp\n10.2.0.0/16 443 tcp extra\n"
                              "192.168.1.0/24 70000 tcp\n10.2.0.0/16 443 tcp")
        text = self.shell("render_ipv4")
        self.assertEqual(verdict(text, dst="10.2.1.1"), "RETURN")
        for dst in ("10.1.2.3", "192.168.1.2", "127.0.0.1", "172.30.1.150", "100.64.1.1"):
            self.assertEqual(verdict(text, dst=dst), "DROP", dst)

    def test_forwarder_can_only_reach_upstream_dns_even_with_admin_allow(self):
        self.rules.write_text("10.2.0.0/16 any tcp\n")
        text = self.shell("render_ipv4")
        for proto in ("udp", "tcp"):
            self.assertEqual(verdict(text, src="172.30.0.3", dst="192.0.2.53",
                                     proto=proto, dport="53"), "RETURN")
        for dst, port in (("192.0.2.53", "443"), ("198.51.100.1", "53"), ("10.2.1.1", "80")):
            for state in ("NEW", "ESTABLISHED"):
                self.assertEqual(verdict(text, src="172.30.0.3", dst=dst,
                                         dport=port, state=state), "DROP")
        self.assertEqual(verdict(text, chain="TERMIPLUS-IN", src="172.30.0.3",
                                 dst="192.0.2.53", dport="443", state="ESTABLISHED"), "DROP")
        self.assertEqual(verdict(text, chain="TERMIPLUS-IN", src="172.30.0.3",
                                 dst="192.0.2.53", dport="53", proto="udp"), "RETURN")

    def test_static_denies_do_not_depend_on_gateway_or_offline_bridge(self):
        text = self.shell("mac=; forwarder_mac=; offline_gateway_mac=; render_ipv4")
        for dst in ("172.30.0.151", "192.168.1.1", "10.1.1.1", "169.254.169.254"):
            self.assertEqual(verdict(text, dst=dst), "DROP")
        self.assertEqual(verdict(text, src="172.30.0.2", dst="172.30.0.150", outgoing="ot-sandbox"), "DROP")
        self.assertEqual(verdict(text, incoming="ot-offline", src="172.30.1.150"), "DROP")
        self.assertEqual(verdict(text, chain="TERMIPLUS-IN", incoming="ot-offline", state="ESTABLISHED"), "DROP")
        self.assertEqual(verdict(text, src="172.30.0.3", dst="198.51.100.1"), "RETURN")
        self.assertEqual(verdict(text, dst="198.51.100.1"), "RETURN")

    def test_gateway_connections_and_terminal_dns_keep_working(self):
        text = self.shell("render_ipv4")
        self.assertEqual(verdict(text, src="172.30.0.2", dst="172.30.0.150",
                                 outgoing="ot-sandbox"), "ACCEPT")
        self.assertEqual(verdict(text, dst="172.30.0.2", state="ESTABLISHED",
                                 outgoing="ot-sandbox"), "ACCEPT")
        self.assertEqual(verdict(text, dst="172.30.0.2", outgoing="ot-sandbox"), "DROP")
        for proto in ("udp", "tcp"):
            self.assertEqual(verdict(text, dst="172.30.0.3", proto=proto,
                                     dport="53", outgoing="ot-sandbox"), "ACCEPT")
        self.assertEqual(verdict(text, incoming="ot-offline", outgoing="ot-offline",
                                 src="172.30.1.2", dst="172.30.1.150"), "ACCEPT")

    def test_completed_policy_detects_missing_rule_input_changes_and_wrong_hook(self):
        self.shell("apply_ip_policy; rules_current")
        baseline = self.state.read_text()
        s = json.loads(baseline)
        s["v4"]["TERMIPLUS"] = [r for r in s["v4"]["TERMIPLUS"] if "192.168.0.0/16" not in r]
        self.state.write_text(json.dumps(s))
        self.shell("rules_current", success=False)
        self.state.write_text(baseline)
        self.shell("DNS_UPSTREAM=192.0.2.54; rules_current", success=False)
        self.rules.write_text("10.3.0.0/16 443 tcp\n")
        self.shell("rules_current", success=False)
        self.rules.write_text("10.2.0.0/16 443 tcp\n")
        s = json.loads(baseline)
        s["v4"]["DOCKER-USER"].insert(0, ["-A", "DOCKER-USER", "-j", "ACCEPT"])
        self.state.write_text(json.dumps(s))
        self.shell("rules_current", success=False)

    def test_service_pin_contents_and_renamed_script_are_checked(self):
        self.shell("apply_ip_policy; rules_current")
        self.shell("SERVICE_PINS=missing-pin; rules_current", success=False)
        (self.root / "installed-firewall").write_text(SOURCE)
        self.shell('SCRIPT_FILE=$SCRIPT_DIR/installed-firewall; apply_ip_policy; rules_current')
        with (self.root / "installed-firewall").open("a") as f:
            f.write("\n# changed installed script\n")
        self.shell('SCRIPT_FILE=$SCRIPT_DIR/installed-firewall; rules_current', success=False)

    def test_workspace_at_forwarder_address_gets_no_service_privileges(self):
        # Real service_owned checks Docker metadata, not merely the address.
        docker = self.root / "docker"
        docker.write_text("""#!/bin/sh
if [ "$1" = network ]; then echo 'workspace-id 02:00:00:00:00:03 172.30.0.3/24'
else echo 'dns-forwarder true'; fi
""")
        docker.chmod(0o755)
        text = self.shell("""
port_macs() { printf '%s\\n' 'veth-workspace 02:00:00:00:00:03 172.30.0.3'; }
""" + DISCOVERY + """
snapshot_services
[ -z "$forwarder_mac" ] && [ -z "$forwarder_port" ]
render_ipv4
""")
        for proto in ("udp", "tcp"):
            self.assertEqual(verdict(text, dst="172.30.0.3", dport="53", proto=proto,
                                     outgoing="ot-sandbox"), "DROP")
        self.assertEqual(verdict(text, src="172.30.0.3", dst="198.51.100.1"), "RETURN")
        self.assertNotIn("-s 172.30.0.3 -j DROP", text)
        self.assertNotIn("-d 172.30.0.3 -p udp --dport 53 -j ACCEPT", text)

    def test_absent_then_present_gateway_cannot_certify_unrendered_identity(self):
        # One observation supplies service pins, rendered rules and marker.
        # The next invocation observes the new gateway and must reapply.
        code = PORT_DISCOVERY + DISCOVERY + """
source_guard() { :; }
ip() {
  if [ "$2" = link ]; then
    echo '7: veth-new@if2: <UP> master ot-sandbox link-netnsid 5'
  fi
}
docker() {
  if [ "$1" = network ]; then
    n=0; [ ! -f "$PORTLOCK_DIR/discovery-count" ] || read -r n <"$PORTLOCK_DIR/discovery-count"
    n=$((n+1)); echo "$n" >"$PORTLOCK_DIR/discovery-count"
    if [ "$3" = ot-sandbox ] && [ "$n" -gt 1 ]; then
      echo 'gateway 02:00:00:00:00:02 172.30.0.2/24'
    fi
  else echo 123; fi
}
lsns() { echo 5; }
snapshot_discovery
snapshot_services
[ -z "$mac" ]
apply_ip_policy
rules_current
render_ipv4
unset PORT_SNAPSHOT_READY
snapshot_discovery
snapshot_services
[ "$mac" = 02:00:00:00:00:02 ]
! rules_current
"""
        text = self.shell(code)
        self.assertNotIn("-s 172.30.0.2 -d 172.30.0.0/24 -j ACCEPT", text)
        # Exactly one network discovery per bridge per snapshot, regardless of
        # fingerprinting, rendering, rules_current or repeated service lookups.
        self.assertEqual((self.root / "discovery-count").read_text().strip(), "4")

    def test_restore_failure_keeps_complete_old_policy_and_retry_repairs(self):
        self.shell("apply_ip_policy")
        baseline = self.state.read_text()
        for fail in ("v4-test", "v6-test", "v4-commit", "v6-commit"):
            with self.subTest(fail=fail):
                s = json.loads(baseline)
                s["fail"] = fail
                self.state.write_text(json.dumps(s))
                self.rules.write_text("10.3.0.0/16 443 tcp\n")
                self.shell("apply_ip_policy", success=False)
                self.assertFalse((self.root / "policy.applied").exists())
                after = json.loads(self.state.read_text())
                text = "\n".join(" ".join(r) for r in after["v4"]["TERMIPLUS"])
                self.assertEqual(verdict(text), "DROP")
                self.assertEqual(after["v4"]["OTHER"], json.loads(baseline)["v4"]["OTHER"])
                if fail != "v6-commit":
                    self.assertEqual(after["v4"], json.loads(baseline)["v4"])
                after.pop("fail")
                self.state.write_text(json.dumps(after))
                self.shell("apply_ip_policy; rules_current")
                self.assertEqual(len(json.loads(self.state.read_text())["v4"]["DOCKER-USER"]), 1)


if __name__ == "__main__":
    unittest.main()
