"""Run dns-forwarder/entrypoint.sh with a stub dnsmasq that prints its argv."""
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[2] / "deploy" / "dns-forwarder" / "entrypoint.sh"


def run(**env):
    with tempfile.TemporaryDirectory() as tmp:
        stub = Path(tmp) / "dnsmasq"
        stub.write_text('#!/bin/sh\nfor a in "$@"; do printf "%s\\n" "$a"; done\n')
        stub.chmod(0o755)
        full = {"PATH": tmp + ":/usr/bin:/bin", "HOSTSDIR": tmp + "/hosts"}
        full.update(env)
        proc = subprocess.run(["/bin/sh", str(SCRIPT)], env=full, capture_output=True, text=True)
        return proc.returncode, proc.stdout.splitlines(), proc.stderr, tmp


class Entrypoint(unittest.TestCase):
    def test_base_flags_and_unchanged_behaviour(self):
        rc, args, _, tmp = run(DNS_UPSTREAM="192.0.2.1, 192.0.2.2")
        self.assertEqual(rc, 0)
        self.assertEqual(args[0], "--keep-in-foreground")
        for flag in ("--bogus-priv", "--stop-dns-rebind", "--no-resolv", "--no-hosts",
                     "--hostsdir=" + tmp + "/hosts", "--server=192.0.2.1", "--server=192.0.2.2"):
            self.assertIn(flag, args)
        self.assertFalse([a for a in args if a.startswith("--rebind-domain-ok")])

    def test_upstream_required(self):
        rc, args, _, _ = run()
        self.assertNotEqual(rc, 0)
        self.assertEqual(args, [])

    def test_valid_rebind_domains(self):
        rc, args, err, _ = run(DNS_UPSTREAM="192.0.2.1",
                               DNS_REBIND_OK_DOMAINS="corp.example, a-b_c.internal.,x.y".replace("internal.,", "internal,"))
        self.assertEqual(rc, 0, err)
        self.assertEqual([a for a in args if a.startswith("--rebind-domain-ok")],
                         ["--rebind-domain-ok=/corp.example/a-b_c.internal/x.y/"])

    def test_malformed_rebind_domains_rejected(self):
        bad = ["Corp.Example", "a/b", "a.b/c", "..", ".a.com", "a.com.", "a..com", "a;b",
               "a$(id).com", "-x" + "é", "x" * 64 + ".com", ("a" * 60 + ".") * 5 + "com", "/", "*"]
        for entry in bad:
            rc, args, err, _ = run(DNS_UPSTREAM="192.0.2.1", DNS_REBIND_OK_DOMAINS="ok.example," + entry)
            self.assertEqual(rc, 0, entry)
            self.assertEqual([a for a in args if a.startswith("--rebind-domain-ok")],
                             ["--rebind-domain-ok=/ok.example/"], entry)
            self.assertNotIn("malformed", "".join(args))
            self.assertIn("ignoring malformed", err, entry)
            self.assertFalse([a for a in args if entry in a and entry != "/" and entry != ".."], entry)

    def test_only_malformed_means_no_flag(self):
        rc, args, err, _ = run(DNS_UPSTREAM="192.0.2.1", DNS_REBIND_OK_DOMAINS="A/B")
        self.assertEqual(rc, 0)
        self.assertFalse([a for a in args if a.startswith("--rebind-domain-ok")])
        self.assertIn("ignoring malformed", err)

    def _flag(self, args, prefix):
        return [a for a in args if a.startswith(prefix)]

    def test_local_zones(self):
        rc, args, err, _ = run(DNS_UPSTREAM="192.0.2.1", DNS_LOCAL_ZONES="leonm.internal,corp.example")
        self.assertEqual(rc, 0, err)
        self.assertEqual(self._flag(args, "--local"), ["--local=/leonm.internal/corp.example/"])

    def test_local_zones_single_and_unset(self):
        _, args, _, _ = run(DNS_UPSTREAM="192.0.2.1", DNS_LOCAL_ZONES=" corp.example ")
        self.assertEqual(self._flag(args, "--local"), ["--local=/corp.example/"])
        for env in ({}, {"DNS_LOCAL_ZONES": ""}, {"DNS_LOCAL_ZONES": " , "}):
            _, args, _, _ = run(DNS_UPSTREAM="192.0.2.1", **env)
            self.assertEqual(self._flag(args, "--local"), [])

    def test_local_zones_malformed_rejected(self):
        for entry in ["Corp.Example", "a/b", "..", ".a.com", "a.com.", "a..com", "a;b", "*", "/"]:
            rc, args, err, _ = run(DNS_UPSTREAM="192.0.2.1", DNS_LOCAL_ZONES="ok.example," + entry)
            self.assertEqual(rc, 0, entry)
            self.assertEqual(self._flag(args, "--local"), ["--local=/ok.example/"], entry)
            self.assertIn("ignoring malformed DNS_LOCAL_ZONES", err, entry)
        _, args, err, _ = run(DNS_UPSTREAM="192.0.2.1", DNS_LOCAL_ZONES="A/B")
        self.assertEqual(self._flag(args, "--local"), [])

    def test_interior_whitespace_rejected(self):
        for var, prefix in (("DNS_REBIND_OK_DOMAINS", "--rebind-domain-ok"), ("DNS_LOCAL_ZONES", "--local")):
            for bad in ("b c.example", "b\tc.example", "b\nc.example"):
                rc, args, err, _ = run(DNS_UPSTREAM="192.0.2.1", **{var: "ok.example," + bad})
                self.assertEqual(rc, 0, (var, bad))
                self.assertEqual(self._flag(args, prefix), [prefix + "=/ok.example/"], (var, bad))
                self.assertIn("ignoring malformed " + var, err, (var, bad))

    def test_edge_whitespace_trimmed(self):
        for var, prefix in (("DNS_REBIND_OK_DOMAINS", "--rebind-domain-ok"), ("DNS_LOCAL_ZONES", "--local")):
            rc, args, err, _ = run(DNS_UPSTREAM="192.0.2.1", **{var: " \ta.example\t , b.example "})
            self.assertEqual(rc, 0, err)
            self.assertEqual(self._flag(args, prefix), [prefix + "=/a.example/b.example/"], var)
            self.assertEqual(err, "")


if __name__ == "__main__":
    unittest.main()
