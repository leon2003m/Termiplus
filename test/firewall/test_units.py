"""Check policy updates activate a repeatable service, not the active boot unit."""
import configparser
from pathlib import Path
import unittest

DEPLOY = Path(__file__).resolve().parents[2] / "deploy"


def unit(name):
    parser = configparser.ConfigParser(interpolation=None)
    parser.read(DEPLOY / name)
    return parser


class PolicyReload(unittest.TestCase):
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
