#!/usr/bin/python3
"""Build-time helper: writes Chromium's managed policy for the desktop image,
so Chromium does not offer to save passwords, sign in or phone home.
"""

import json
import os

OUT = "/etc/chromium/policies/managed/termiplus.json"

policy = {
    "PasswordManagerEnabled": False,
    "MetricsReportingEnabled": False,
    "BrowserSignin": 0,
    "SyncDisabled": True,
    "PromotionsEnabled": False,
    "DefaultBrowserSettingEnabled": False,
}

os.makedirs(os.path.dirname(OUT), exist_ok=True)
with open(OUT, "w") as f:
    json.dump(policy, f, indent=2)
os.chmod(OUT, 0o644)
print(OUT)
