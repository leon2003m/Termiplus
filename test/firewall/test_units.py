"""Check policy updates activate a repeatable service, not the active boot unit."""
import configparser
import ipaddress
import re
from pathlib import Path
import unittest

DEPLOY = Path(__file__).resolve().parents[2] / "deploy"


def unit(name):
    parser = configparser.ConfigParser(interpolation=None)
    parser.read(DEPLOY / name)
    return parser


class PolicyReload(unittest.TestCase):
    def test_dynamic_pools_cannot_allocate_service_addresses(self):
        for filename, subnet in (("docker-compose.yml", "172.30.0"),
                                 ("docker-compose.offline.yml", "172.30.1")):
            text = (DEPLOY / filename).read_text()
            match = re.search(r"- subnet: " + re.escape(subnet) + r"\.0/24\n +ip_range: (\S+)", text)
            self.assertIsNotNone(match, filename)
            pool = ipaddress.ip_network(match.group(1))
            self.assertTrue(pool.subnet_of(ipaddress.ip_network(subnet + ".0/24")))
            for host in (1, 2, 3, 4, 127):
                self.assertNotIn(ipaddress.ip_address(subnet + "." + str(host)), pool)
            self.assertIn(ipaddress.ip_address(subnet + ".150"), pool)

    def test_policy_changes_run_repeatable_reload(self):
        path = unit("termiplus-firewall.path")
        target = path["Path"]["Unit"]
        self.assertNotEqual(target, "termiplus-firewall.service")
        reload = unit(target)
        self.assertEqual(reload["Service"]["Type"], "oneshot")
        self.assertFalse(reload.getboolean("Service", "RemainAfterExit", fallback=False))
        self.assertEqual(reload["Service"]["ExecStart"], "/opt/termiplus/deploy/firewall.sh")
        self.assertTrue(unit("termiplus-firewall.service").getboolean("Service", "RemainAfterExit"))


if __name__ == "__main__":
    unittest.main()
