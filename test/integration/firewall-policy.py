"""Real-kernel restore regression; run ONLY in a disposable network namespace.

Requires NET_ADMIN in that namespace, iptables/ip6tables and their restore/save
tools. No Docker, external interfaces, production addresses or traffic probes.
The trusted launcher must provide resource limits and read-only source mounts.
"""
import importlib.util
import sys
from pathlib import Path
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
BACKEND = sys.argv[1] if len(sys.argv) > 1 else "nft"
assert BACKEND in ("nft", "legacy"), "expected nft or legacy"


def backend_tool(name):
    for prefix in ("iptables", "ip6tables"):
        if name.startswith(prefix):
            return prefix + "-" + BACKEND + name[len(prefix):]
    return name


def command(args, **kwargs):
    return subprocess.run([backend_tool(args[0])] + args[1:], text=True, capture_output=True, timeout=10, **kwargs)


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
    for table, chain in (("filter", "TERMIPLUS-AS"), ("nat", "TERMIPLUS-PORT")):
        checked(["ebtables", "-t", table, "-N", chain, "-P", "RETURN"])
    service_pin = ["ebtables", "-t", "filter", "-A", "TERMIPLUS-AS", "-p", "IPv4",
                   "--ip-src", "172.30.0.2", "-s", "!", "02:00:00:00:00:02", "-j", "DROP"]
    checked(service_pin)
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        (root / "firewall.sh").write_text(policy.SOURCE)
        (root / "egress.rules").write_text("10.2.0.0/16 443 tcp\n")
        tools = root / "bin"
        tools.mkdir()
        for family in ("iptables", "ip6tables"):
            for suffix in ("", "-restore", "-save"):
                name = family + suffix
                (tools / name).symlink_to("/usr/bin/" + backend_tool(name))
        env = {"PATH": str(tools) + ":/usr/bin:/bin", "SCRIPT_DIR": tmp, "PORTLOCK_DIR": tmp,
               "EGRESS_RULES": str(root / "egress.rules")}

        def shell(code, success=True):
            p = command(["/bin/sh"], input="set -eu\n" + policy.VARIABLES + policy.FUNCTIONS +
                        '\nebtables() { command ebtables "$@"; }\n' + code, env=env)
            assert (p.returncode == 0) == success, p.stderr
            return p.stdout

        shell("apply_ip_policy; rules_current")
        checked(["iptables", "-w", "10", "-C", "DOCKER-USER", "-j", "RETURN"])
        original = snapshot()
        # Actual canonical ebtables output, including MAC formatting, is part of
        # the completion hash. Removing a pin must invalidate the fast path.
        deleted_pin = list(service_pin)
        deleted_pin[3] = "-D"
        checked(deleted_pin)
        shell("rules_current", success=False)
        checked(service_pin)
        shell("rules_current")
        for tool in ("iptables", "ip6tables"):
            checked([tool, "-w", "10", "-C", "OTHER", "-j", "DROP"])

        rendered = shell("render_ipv4")
        # A late failure must retain every old rule, including the private deny.
        broken = rendered.replace("COMMIT\n", "-A TERMIPLUS -j nonexistent-target\nCOMMIT\n")
        p = command(["iptables-restore", "-w", "10", "--noflush"], input=broken)
        assert p.returncode != 0
        assert snapshot() == original, "failed restore changed live policy"

        # Terminate after feeding a full replacement without COMMIT: no live gap.
        p = subprocess.Popen([backend_tool("iptables-restore"), "-w", "10", "--noflush"],
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
    print("PASS (" + BACKEND + "): real-kernel failed/interrupted restore retained complete policy; "
          "retry repaired missing rule; absent gateway retained denies; unrelated chains preserved.")


if __name__ == "__main__":
    main()
