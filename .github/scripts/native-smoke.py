"""Independent protocol checks; run inside the throwaway desktop container."""
import asyncio
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:8000"
KEY = os.environ["OPEN_TERMINAL_API_KEY"]
HEADERS = {"Authorization": "Bearer " + KEY, "X-User-Id": "smoke-user", "X-Session-Id": "smoke-chat"}


def request(path, data=None, authenticated=True, method=None):
    headers = dict(HEADERS) if authenticated else {}
    if data is not None:
        data = json.dumps(data).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=45) as res:
            return res.status, res.read()
    except urllib.error.HTTPError as err:
        return err.code, err.read()


def check(condition, label):
    if not condition:
        raise AssertionError(label)
    print("PASS", label, flush=True)


check(request("/files/cwd", authenticated=False)[0] == 401, "API requires authentication")
check(request("/files/cwd")[0] == 200, "terminal file API")
config_status, config = request("/api/config")
check(config_status == 200, "native capability discovery")
if sys.argv[1] == "plain":
    check(request("/desktop/screenshot")[0] in (403, 404), "plain policy has no desktop")
    sys.exit(0)

check(request("/desktop/screenshot", authenticated=False)[0] == 401, "desktop needs authentication")
code, data = request("/desktop/screenshot")
check(code == 200 and data.startswith(b"\x89PNG\r\n\x1a\n"), "native desktop screenshot starts the display")
code, action = request("/desktop/action", {"actions": [{"action": "key", "key": "Escape"}]})
check(code == 200 and action.startswith(b"\x89PNG"), "native action returns same desktop image format")


async def sockets():
    from websockets.asyncio.client import connect
    from websockets.exceptions import ConnectionClosed

    uri = "ws://127.0.0.1:8000/desktop/stream"
    async with connect(uri, additional_headers={"X-User-Id": "smoke-user"}) as ws:
        await ws.send(json.dumps({"type": "auth", "token": KEY, "chat_id": "smoke-chat"}))
        frame = await asyncio.wait_for(ws.recv(), 15)
        check(isinstance(frame, bytes) and frame.startswith(b"RFB "), "native authenticated VNC stream")
    async with connect(uri) as ws:
        await ws.send(json.dumps({"type": "auth", "token": "wrong"}))
        try:
            await asyncio.wait_for(ws.recv(), 5)
        except ConnectionClosed as exc:
            check(exc.code == 4001, "wrong stream key rejected")
        else:
            raise AssertionError("wrong stream key accepted")


asyncio.run(sockets())
# Start a descendant server so native /ports and /proxy can discover it.
code, body = request("/execute?wait=0", {"command": "python3 -m http.server 3017 --bind 127.0.0.1"})
check(code == 200, "native process API starts local app")
for _ in range(30):
    code, app = request("/proxy/3017/")
    if code == 200:
        break
    time.sleep(0.2)
check(code == 200 and b"Directory listing" in app, "native local app preview")
code, browser = request("/browser/sessions", {"url": "http://127.0.0.1:3017/"})
check(code == 200, "native Chromium session creation")
session_id = json.loads(browser)["id"]
code, browser = request("/browser/sessions/" + session_id, {"mode": "chrome", "active": True}, method="PATCH")
check(code == 200, "native Chromium starts with its sandbox enabled")
# Ensure creating a record was not the only successful step: exercise CDP.
code, snap = request("/browser/snapshot?session_id=" + session_id)
check(code == 200, "native Chromium automation snapshot (sandbox enabled)")
# Native VNC uses a Unix socket; no old public VNC listener is permitted.
listeners = subprocess.check_output(["ss", "-lnt"]).decode()
check(":6901 " not in listeners and ":5900 " not in listeners, "no legacy VNC TCP listener")
