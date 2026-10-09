"""Integration checks, run inside a client container on the Open WebUI network.

Acts like the Open WebUI backend: talks to the gateway with the gateway key and
X-User-Id / X-Session-Id headers. Standard library only.

    python3 checks.py               # main checks
    python3 checks.py persistence   # after the host removed alice's container
"""

import base64
import json
import os
import socket
import struct
import sys
import time
import urllib.error
import urllib.request

GW_HOST = "gateway"
GW = f"http://{GW_HOST}:8080"
KEY = open("/s/gateway_api_key").read().strip()
RUNTIME = os.environ.get("RUNTIME", "runsc")
ALICE, BOB = "alice-it", "bob-it"

failures = []


def check(name, ok, detail=""):
    print(f"{'ok  ' if ok else 'FAIL'} {name}" + (f"  ({detail})" if detail and not ok else ""))
    if not ok:
        failures.append(name)


def call(method, path, user=None, chat=None, body=None, key=KEY):
    headers = {}
    if key is not None:
        headers["Authorization"] = "Bearer " + key
    if user:
        headers["X-User-Id"] = user
    if chat:
        headers["X-Session-Id"] = chat
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(GW + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def run(user, cmd, chat=None, prefix=""):
    """Run a shell command in the user's terminal and return its output."""
    status, body = call("POST", prefix + "/execute", user, chat, {"command": cmd})
    if status != 200:
        return f"<execute failed: {status} {body[:200]}>"
    pid = json.loads(body)["id"]
    for _ in range(100):
        status, body = call("GET", f"{prefix}/execute/{pid}/status", user, chat)
        data = json.loads(body) if status == 200 else {}
        if data.get("status") == "done":
            break
        time.sleep(0.3)
    return "".join(o.get("data", "") for o in data.get("output", [])).replace("\r", "").strip()


def ws_terminal(user, token):
    """Open an interactive terminal through the gateway, return (output, closed_with_4001)."""
    status, body = call("POST", "/api/terminals", user, "chat-ws", {})
    if status != 200:
        return f"<create failed {status}>", False
    sid = json.loads(body)["id"]

    def frame(op, data):
        mask = os.urandom(4)
        n = len(data)
        head = bytes([0x80 | op]) + (bytes([0x80 | n]) if n < 126 else bytes([0x80 | 126]) + struct.pack(">H", n))
        return head + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(data))

    s = socket.create_connection((GW_HOST, 8080), timeout=10)
    key = base64.b64encode(os.urandom(16)).decode()
    s.sendall((f"GET /api/terminals/{sid}?user_id={user} HTTP/1.1\r\nHost: {GW_HOST}\r\n"
               "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\n"
               f"Sec-WebSocket-Key: {key}\r\nX-User-Id: {user}\r\nX-Session-Id: chat-ws\r\n\r\n").encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        buf += s.recv(4096)
    if b" 101 " not in buf.split(b"\r\n", 1)[0]:
        return "<no upgrade>", False
    out = buf.split(b"\r\n\r\n", 1)[1]
    s.sendall(frame(1, json.dumps({"type": "auth", "token": token}).encode()))
    s.sendall(frame(2, b"echo ws-$((40+2))\n"))
    end = time.time() + 8
    s.settimeout(1)
    while time.time() < end and b"ws-42" not in out:
        try:
            d = s.recv(4096)
            if not d:
                break
            out += d
        except (socket.timeout, ConnectionResetError):
            if b"\x0f\xa1" in out:
                break
    s.close()
    return out.decode(errors="replace"), b"\x0f\xa1" in out  # 0x0fa1 = close code 4001


def main_checks():
    print("-- gateway auth")
    check("connection check /api/config", call("GET", "/api/config")[0] == 200)
    check("orchestrator API absent (/api/v1/policies 404)", call("GET", "/api/v1/policies")[0] == 404)
    check("no key -> 401", call("GET", "/files/cwd", ALICE, key=None)[0] == 401)
    check("wrong key -> 401", call("GET", "/files/cwd", ALICE, key="x" * 64)[0] == 401)
    check("missing user -> 400", call("GET", "/files/cwd")[0] == 400)

    print("-- per-user isolation")
    out = run(ALICE, "echo secret-of-alice > ~/note.txt && sudo id -u")
    check("sudo inside the sandbox is root (0)", out.endswith("0"), out)
    out = run(BOB, "cat ~/note.txt 2>&1 || true")
    check("bob cannot see alice's files", "secret-of-alice" not in out, out)
    alice_host = run(ALICE, "hostname -I 2>/dev/null || hostname -i").split()[0]
    out = run(BOB, f"curl -s -m 5 -o /dev/null -w '%{{http_code}}' http://{alice_host}:8000/health || echo blocked")
    check("bob cannot reach alice's container", "200" not in out, out)

    print("-- egress")
    out = run(ALICE, "curl -s -m 5 -o /dev/null -w '%{http_code}' http://10.0.0.1 || echo blocked")
    check("private range 10.0.0.0/8 blocked", "blocked" in out or out.startswith("000"), out)
    out = run(ALICE, "curl -s -m 5 -o /dev/null -w '%{http_code}' http://172.30.0.1:2375/_ping || echo blocked")
    check("VM itself blocked", "blocked" in out or out.startswith("000"), out)
    out = run(ALICE, "curl -s -m 10 -o /dev/null -w '%{http_code}' https://github.com || echo blocked")
    check("internet reachable", out.startswith(("2", "3")), out)

    if RUNTIME == "runsc":
        print("-- gVisor")
        out = run(ALICE, "dmesg 2>&1 | head -3")
        check("kernel is gVisor", "gVisor" in out, out)

    print("-- ephemeral sandboxes (scratch policy)")
    run(ALICE, "echo tmp > ~/scratch.txt", chat="chat-1", prefix="/p/scratch")
    out = run(ALICE, "cat ~/scratch.txt ~/note.txt 2>&1 || true", chat="chat-2", prefix="/p/scratch")
    check("new chat gets an empty sandbox", "tmp" not in out and "secret-of-alice" not in out, out)
    out = run(ALICE, "cat ~/scratch.txt", chat="chat-1", prefix="/p/scratch")
    check("same chat keeps its sandbox", out == "tmp", out)

    print("-- interactive terminal (WebSocket)")
    out, closed = ws_terminal(ALICE, KEY)
    check("terminal session works", "ws-42" in out, out[-200:])
    out, closed = ws_terminal(ALICE, "wrong-token")
    check("wrong token closes with 4001", closed and "ws-42" not in out, out[-200:])


def persistence_checks():
    print("-- persistent home after the container was removed")
    out = run(ALICE, "cat ~/note.txt")
    check("alice's file survived container recreation", out == "secret-of-alice", out)


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "persistence":
        persistence_checks()
    else:
        main_checks()
    if failures:
        print(f"\n{len(failures)} check(s) failed: {failures}")
        sys.exit(1)
    print("\nall checks passed")
