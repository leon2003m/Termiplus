"""Real-kernel restore regression; run ONLY in a disposable network namespace.

Requires NET_ADMIN in that namespace, iptables/ip6tables and their restore/save
tools. No Docker, external interfaces, production addresses or traffic probes.
The trusted launcher must provide resource limits and read-only source mounts.
"""
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]


def command(args, **kwargs):
    return subprocess.run(args, text=True, capture_output=True, timeout=10, **kwargs)


def checked(args, **kwargs):
    p = command(args, **kwargs)
    assert p.returncode == 0, p.stderr
    return p.stdout


def snapshot():
    return tuple("\n".join(line for line in checked([tool]).splitlines()
                            if not line.startswith("#"))
                 for tool in ("iptables-save", "ip6tables-save"))


def main():
    # Refuse any namespace with a pre-existing interface or firewall policy.
    assert checked(["ip", "-o", "link", "show"]).count("\n") == 1
    assert ": lo:" in checked(["ip", "-o", "link", "show"])
    for tool in ("iptables", "ip6tables"):
        assert "-A " not in checked([tool, "-w", "10", "-S"])
    spec = importlib.util.spec_from_file_location("policy", ROOT / "test/firewall/test_policy.py")
    policy = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(policy)
    checked(["iptables", "-w", "10", "-N", "DOCKER-USER"])
    checked(["iptables", "-w", "10", "-A", "DOCKER-USER", "-j", "RETURN"])
    for tool in ("iptables", "ip6tables"):
        checked([tool, "-w", "10", "-N", "OTHER"])
        checked([tool, "-w", "10", "-A", "OTHER", "-j", "DROP"])
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        (root / "firewall.sh").write_text(policy.SOURCE)
        (root / "egress.rules").write_text("10.2.0.0/16 443 tcp\n")
        env = {"PATH": "/usr/bin:/bin", "SCRIPT_DIR": tmp, "PORTLOCK_DIR": tmp,
               "EGRESS_RULES": str(root / "egress.rules")}

        def shell(code, success=True):
            p = command(["/bin/sh"], input="set -eu\n" + policy.VARIABLES + policy.FUNCTIONS + "\n" + code, env=env)
            assert (p.returncode == 0) == success, p.stderr
            return p.stdout

        shell("apply_ip_policy; rules_current")
        checked(["iptables", "-w", "10", "-C", "DOCKER-USER", "-j", "RETURN"])
        original = snapshot()
        for tool in ("iptables", "ip6tables"):
            checked([tool, "-w", "10", "-C", "OTHER", "-j", "DROP"])

        rendered = shell("render_ipv4")
        # A late failure must retain every old rule, including the private deny.
        broken = rendered.replace("COMMIT\n", "-A TERMIPLUS -j nonexistent-target\nCOMMIT\n")
        p = command(["iptables-restore", "-w", "10", "--noflush"], input=broken)
        assert p.returncode != 0
        assert snapshot() == original, "failed restore changed live policy"

        # Terminate after feeding a full replacement without COMMIT: no live gap.
        p = subprocess.Popen(["iptables-restore", "-w", "10", "--noflush"],
                             stdin=subprocess.PIPE, stdout=subprocess.DEVNULL,
                             stderr=subprocess.DEVNULL, text=True)
        try:
            p.stdin.write(rendered.removesuffix("COMMIT\n"))
            p.stdin.flush()
            time.sleep(0.05)
            p.kill()
            p.wait(timeout=5)
        finally:
            p.stdin.close()
            if p.poll() is None:
                p.kill()
                p.wait(timeout=5)
        assert snapshot() == original, "interrupted restore changed live policy"

        # A real missing rule invalidates the marker and is restored on retry.
        checked(["iptables", "-w", "10", "-D", "TERMIPLUS", "-i", "ot-sandbox",
                 "-s", "172.30.0.0/24", "-d", "192.168.0.0/16", "-j", "DROP"])
        shell("rules_current", success=False)
        shell("apply_ip_policy; rules_current")
        assert snapshot() == original
        shell("mac=; forwarder_mac=; offline_gateway_mac=; apply_ip_policy; rules_current")
        text = checked(["iptables", "-w", "10", "-S", "TERMIPLUS"])
        assert "-j ACCEPT" not in text
        checked(["iptables", "-w", "10", "-C", "TERMIPLUS", "-i", "ot-sandbox",
                 "-s", "172.30.0.0/24", "-d", "192.168.0.0/16", "-j", "DROP"])
        assert "-i ot-offline -j DROP" in text
        for tool in ("iptables", "ip6tables"):
            checked([tool, "-w", "10", "-C", "OTHER", "-j", "DROP"])
    print("PASS: real-kernel failed/interrupted restore retained complete policy; "
          "retry repaired missing rule; absent gateway retained denies; unrelated chains preserved.")


if __name__ == "__main__":
    main()
