#!/usr/bin/env python3
"""Bounded positive user-shell probe using the documented native transport.

Reference: reference/open_webui_terminals.py:588-665.
  WS route:  /api/v1/terminals/<server_id>/api/terminals/<session_id>
  First message: {"type":"auth","token":"<JWT>","chat_id":"<saved chat>"}
  The relay validates the JWT, then authenticates upstream with the configured
  connection key. No token is placed in a URL, cookie, log or exception.

After the shell is attached, this calls the two advertised model tools
(HTTP endpoints) and checks that stdin reaches the shell and stdout is read.
"""
import base64
import json
import os
import re
import socket
import ssl
import struct
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parent.parent
ctx = json.loads((ROOT / "TEST_CONTEXT.json").read_text())
tok = (ROOT / ctx["token_file"]).read_text().strip()
u = urlsplit(ctx["base_url"])
host, port = u.hostname, u.port or 443
http_base = ctx["base_url"].rstrip("/") + ctx["workspace_http_prefix"]


def frame(payload: bytes, opcode=1):
    key = os.urandom(4)
    n = len(payload)
    if n < 126:
        head = bytes([0x80 | opcode, 0x80 | n])
    elif n < 65536:
        head = bytes([0x80 | opcode, 0x80 | 126]) + struct.pack(">H", n)
    else:
        head = bytes([0x80 | opcode, 0x80 | 127]) + struct.pack(">Q", n)
    return head + key + bytes(b ^ key[i % 4] for i, b in enumerate(payload))


def read_exact(s, n):
    out = b""
    while len(out) < n:
        c = s.recv(n - len(out))
        if not c:
            raise EOFError
        out += c
    return out


def read_frame(s):
    b1, b2 = read_exact(s, 2)
    n = b2 & 0x7F
    if n == 126:
        n = struct.unpack(">H", read_exact(s, 2))[0]
    elif n == 127:
        n = struct.unpack(">Q", read_exact(s, 8))[0]
    if b2 & 0x80:
        mask = read_exact(s, 4)
        data = bytes(b ^ mask[i % 4] for i, b in enumerate(read_exact(s, n)))
    else:
        data = read_exact(s, n)
    return b1 & 0x0F, data


def redact(text):
    out = []
    for word in text.split():
        out.append("<redacted>" if len(word) > 80 and word.count(".") == 2 else word)
    return " ".join(out)[:160]


def http(method, path, body=None):
    req = urllib.request.Request(
        http_base + path, method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + tok, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def main():
    result = {"route": "documented ws_terminal", "handshake": None, "events": []}
    # A terminal tab must exist; connect its PTY session, then exercise the
    # user-shell tools against that attached tab.
    st, body = http("POST", "/api/terminals", {})
    if st != 200:
        print(json.dumps({"terminal_create_status": st, "body": redact(body.decode("utf-8", "replace"))}, indent=1))
        return 1
    session_id = json.loads(body).get("id")
    result["terminal_session_created"] = bool(session_id)
    ws_path = "/api/v1/terminals/%s/api/terminals/%s" % (
        ctx["terminal_id"], urllib.parse.quote(session_id, safe=""))

    raw = socket.create_connection((host, port), timeout=10)
    s = ssl.create_default_context().wrap_socket(raw, server_hostname=host)
    s.settimeout(8)
    key = base64.b64encode(os.urandom(16)).decode()
    # The WebSocket route authenticates by first message, not by HTTP headers
    # or query/cookie parameters. Only the standard upgrade headers go here.
    headers = [
        "GET %s HTTP/1.1" % ws_path,
        "Host: %s" % host,
        "Upgrade: websocket",
        "Connection: Upgrade",
        "Sec-WebSocket-Key: " + key,
        "Sec-WebSocket-Version: 13",
        "Origin: https://" + host,
    ]
    s.sendall(("\r\n".join(headers) + "\r\n\r\n").encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        c = s.recv(4096)
        if not c:
            break
        buf += c
    result["handshake"] = buf.split(b"\r\n", 1)[0].decode("latin-1", "replace")[:80]
    if "101" not in result["handshake"]:
        s.close()
        print(json.dumps(result, indent=1))
        return 1

    s.sendall(frame(json.dumps({
        "type": "auth", "token": tok, "chat_id": ctx["chat_id"]}).encode()))

    # Drain initial upstream frames briefly; a close frame means auth failed.
    deadline = time.time() + 2.0
    while time.time() < deadline:
        try:
            s.settimeout(max(0.1, deadline - time.time()))
            op, data = read_frame(s)
        except (socket.timeout, EOFError):
            break
        if op == 8:
            code = struct.unpack(">H", data[:2])[0] if len(data) >= 2 else None
            result["events"].append({"close": code,
                                     "reason": redact(data[2:120].decode("utf-8", "replace"))})
            s.close()
            print(json.dumps(result, indent=1))
            return 1
        # Binary PTY output is expected after auth; do not print its contents.
        result["events"].append({"op": op, "len": len(data)})

    # With the shell attached, exercise the two advertised tools.
    st, body = http("POST", "/api/terminals/user/input",
                    {"input": "echo USER-SHELL-PROOF-42\n"})
    result["send_user_terminal_input"] = {"status": st,
                                          "body": redact(body.decode("utf-8", "replace"))}
    time.sleep(0.7)
    st2, body2 = http("GET", "/api/terminals/user/output")
    text = body2.decode("utf-8", "replace")
    result["read_user_terminal"] = {"status": st2,
                                    "contains_marker": "USER-SHELL-PROOF-42" in text,
                                    "bytes": len(body2)}
    s.close()
    http("DELETE", "/api/terminals/" + urllib.parse.quote(session_id, safe=""))
    print(json.dumps(result, indent=1))
    return 0 if result["read_user_terminal"]["contains_marker"] else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as e:
        print(json.dumps({"error": type(e).__name__, "detail": redact(str(e))}, indent=1))
        raise SystemExit(1)
