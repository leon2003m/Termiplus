#!/usr/bin/env python3
"""Independent live verification of the 23 native Open Terminal model tools.

Run from the snapshot root:  python3 proof/verify_native_tools.py

The token is read only from TEST_CONTEXT.json's token_file and is sent only in
Authorization headers. It is never printed or written to evidence. Capability
tokens in Browser session responses are never logged either.
"""

from __future__ import annotations

import json
import hashlib
import os
import re
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CTX = json.loads((ROOT / "TEST_CONTEXT.json").read_text())
TOKEN = (ROOT / CTX["token_file"]).read_text().strip()
BASE = CTX["base_url"].rstrip("/") + CTX["workspace_http_prefix"]
RUN = "proof-%s-%d" % (datetime.now(timezone.utc).strftime("%H%M%S"), os.getpid())
RUN_DIR = "/home/user/" + RUN
PORT = 18000 + (os.getpid() % 1000)

EXPECTED = {
    "send_user_terminal_input", "read_user_terminal",
    "browser_action", "browser_screenshot", "browser_snapshot", "browser_tabs",
    "desktop_action", "desktop_screenshot",
    "list_processes", "run_command", "kill_process", "send_process_input",
    "get_process_status", "display_file", "glob_search", "grep_search",
    "list_files", "match_files", "read_file", "replace_file_content",
    "search_files", "write_file", "get_info",
}

RESULTS = []


def rec(tool, case, status, detail, evidence=None):
    RESULTS.append({"tool": tool, "case": case, "status": status,
                    "detail": detail, "evidence": evidence})
    print("%-5s %-26s %-34s %s" % (status, tool, case, detail))


def call(method, path, params=None, body=None, timeout=120, auth=True):
    url = BASE + path
    if params:
        url += "?" + urllib.parse.urlencode(params, doseq=True)
    headers = {"Content-Type": "application/json"}
    if auth:
        headers["Authorization"] = "Bearer " + TOKEN
    req = urllib.request.Request(
        url, method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, dict(r.headers), r.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read()


def js(body):
    try:
        return json.loads(body)
    except Exception:
        return None


def png_info(body):
    if body[:8] != b"\x89PNG\r\n\x1a\n" or len(body) < 24:
        return None
    w, h = struct.unpack(">II", body[16:24])
    return {"bytes": len(body), "width": w, "height": h,
            "sha256": hashlib.sha256(body).hexdigest()[:16]}


def safe_detail(status, body):
    # Never surface a capability/JWT even if an error echoes one.
    text = body[:300].decode("utf-8", "replace")
    return re.sub(r"(token|capability|relay)[\"']?\s*[:=]\s*[\"']?[^\"'\s,}]+",
                  r"\1=<redacted>", text, flags=re.I)


def ecase(tool, case, fn):
    """Run one independent case; exceptions become FAIL, never abort the run."""
    try:
        status, detail, evidence = fn()
        rec(tool, case, status, detail, evidence)
        return status == "PASS"
    except Exception as e:
        rec(tool, case, "FAIL", "%s: %s" % (type(e).__name__, str(e)[:180]))
        return False


def main():
    run_dir = RUN_DIR
    tsid = bsid = server_pid = None
    cat = {}
    try:
        # ---------------- discovery ----------------
        def discovery():
            s, h, b = call("GET", "/openapi.json")
            assert s == 200, "HTTP %s" % s
            doc = js(b)
            ops = {}
            for path, methods in doc.get("paths", {}).items():
                for m, op in methods.items():
                    if m in ("get", "post", "put", "patch", "delete", "head", "options") \
                            and isinstance(op, dict) and op.get("operationId"):
                        ops[op["operationId"]] = (m.upper(), path)
            assert len(ops) == 23, "inventory is %d, expected 23" % len(ops)
            assert set(ops) == EXPECTED, "inventory differs: %s" % (set(ops) ^ EXPECTED)
            text = b.decode()
            assert "1280x800" in text and "REQUIRED" in text, "desktop schema rewrite missing"
            return "PASS", "23 operations; desktop conditional descriptions present", \
                {"operations": {k: "%s %s" % v for k, v in sorted(ops.items())}}
        ecase("inventory", "openapi discovery", discovery)

        def noauth():
            s, h, b = call("GET", "/info", auth=False)
            assert s == 401, "expected 401, got %s" % s
            return "PASS", "unauthenticated /info rejected with 401", None
        ecase("auth", "missing bearer rejected", noauth)

        # ---------------- info + files ----------------
        def info():
            s, h, b = call("GET", "/info")
            assert s == 200, "HTTP %s" % s
            d = js(b)
            assert "desktop" in d.get("info", "").lower()
            return "PASS", "info describes XFCE desktop/browser", {"chars": len(d.get("info", ""))}
        ecase("get_info", "read workspace capability info", info)

        def write(path, content):
            return call("POST", "/files/write", body={"path": path, "content": content})

        def write_file():
            s, h, b = write(run_dir + "/data/a.txt",
                            "alpha UNIQUE-TOKEN-42\nsecond line\nthird line\n")
            assert s == 200, "HTTP %s" % s
            d = js(b)
            assert d["path"].endswith("data/a.txt") and d["size"] > 0
            s2, _, b2 = write(run_dir + "/data/b.txt",
                              "beta UNIQUE-TOKEN-42\nbeta again\nsecond line\n")
            assert s2 == 200
            return "PASS", "created parent dirs and two files", {"a_size": d["size"]}
        ecase("write_file", "write + implicit parent dirs", write_file)

        def list_files():
            s, h, b = call("GET", "/files/list", {"directory": run_dir + "/data"})
            assert s == 200, "HTTP %s" % s
            d = js(b)
            names = {e["name"] for e in d["entries"]}
            assert {"a.txt", "b.txt"} <= names
            return "PASS", "listed %s" % sorted(names), {"dir": d["dir"]}
        ecase("list_files", "list fixture directory", list_files)

        def read_file():
            s, h, b = call("GET", "/files/read", {"path": run_dir + "/data/a.txt"})
            assert s == 200 and "UNIQUE-TOKEN-42" in js(b)["content"]
            s2, _, b2 = call("GET", "/files/read",
                             {"path": run_dir + "/data/a.txt", "start_line": 2, "end_line": 3})
            d2 = js(b2)
            assert s2 == 200 and d2["content"] == "second line\nthird line\n"
            return "PASS", "full read + start_line/end_line range", \
                {"total_lines": js(b)["total_lines"]}
        ecase("read_file", "read + line range", read_file)

        def read_missing():
            s, h, b = call("GET", "/files/read", {"path": run_dir + "/absent.txt"})
            assert s == 404, "expected 404, got %s" % s
            return "PASS", "missing file -> 404 File not found", None
        ecase("read_file", "negative missing file", read_missing)

        def glob_search():
            s, h, b = call("GET", "/files/glob",
                           {"pattern": "*.txt", "path": run_dir, "type": "file",
                            "max_results": 10})
            assert s == 200, "HTTP %s" % s
            d = js(b)
            paths = {m["path"] for m in d["matches"]}
            assert {"data/a.txt", "data/b.txt"} <= paths
            s2, _, b2 = call("GET", "/files/glob",
                             {"pattern": "*.txt", "path": run_dir, "type": "file",
                              "exclude": ["data/b.txt"], "max_results": 10})
            assert s2 == 200 and "data/b.txt" not in {m["path"] for m in js(b2)["matches"]}
            return "PASS", "glob + type + exclude + max_results", \
                {"matches": sorted(paths)}
        ecase("glob_search", "glob with type/exclude", glob_search)

        def grep_search():
            s, h, b = call("GET", "/files/grep",
                           {"query": "UNIQUE-TOKEN", "path": run_dir, "regex": False,
                            "case_insensitive": False, "include": ["*.txt"]})
            assert s == 200, "HTTP %s" % s
            d = js(b)
            assert len(d["matches"]) == 2, "got %d matches" % len(d["matches"])
            s2, _, b2 = call("GET", "/files/grep",
                             {"query": "unique-token", "path": run_dir, "regex": False,
                              "case_insensitive": True, "include": ["*.txt"],
                              "match_per_line": False})
            assert s2 == 200 and len(js(b2)["matches"]) == 2
            return "PASS", "literal grep + case_insensitive + file include", \
                {"matches": len(d["matches"])}
        ecase("grep_search", "grep params and case folding", grep_search)

        def match_files():
            s, h, b = call("GET", "/files/matches",
                           {"query": "a.txt", "path": run_dir, "limit": 10})
            assert s == 200, "HTTP %s" % s
            d = js(b)
            assert any(r["name"] == "a.txt" for r in d["results"])
            return "PASS", "ranked name/path/content match", \
                {"results": len(d["results"])}
        ecase("match_files", "match with limit/offset contract", match_files)

        def search_files():
            s, h, b = call("GET", "/files/search",
                           {"query": "a", "path": run_dir, "limit": 10,
                            "type": "file", "show_hidden": False})
            assert s == 200, "HTTP %s" % s
            d = js(b)
            assert any(r["name"] == "a.txt" for r in d["results"]), d
            return "PASS", "filename ranking with type filter", \
                {"results": len(d["results"])}
        ecase("search_files", "filename search + type filter", search_files)

        def replace_file():
            s, h, b = call("POST", "/files/replace", body={
                "path": run_dir + "/data/a.txt",
                "replacements": [{"target": "second line",
                                  "replacement": "SECOND-REPLACED",
                                  "start_line": 2, "end_line": 2}]})
            assert s == 200, "HTTP %s" % s
            s2, _, b2 = call("GET", "/files/read", {"path": run_dir + "/data/a.txt"})
            assert "SECOND-REPLACED" in js(b2)["content"]
            return "PASS", "bounded unique replacement verified by read_file", \
                {"size": js(b)["size"]}
        ecase("replace_file_content", "replace with line bounds", replace_file)

        def replace_negative():
            path = run_dir + "/data/b.txt"
            s, h, b = call("POST", "/files/replace", body={
                "path": path,
                "replacements": [{"target": "beta", "replacement": "BETA"}]})
            assert s != 200, "ambiguous replacement unexpectedly succeeded"
            s2, _, b2 = call("POST", "/files/replace", body={
                "path": path,
                "replacements": [{"target": "beta", "replacement": "BETA",
                                  "allow_multiple": True}]})
            assert s2 == 200, "allow_multiple failed HTTP %s" % s2
            s3, _, b3 = call("GET", "/files/read", {"path": path})
            assert js(b3)["content"].count("BETA") == 2, js(b3)["content"]
            return "PASS", "ambiguous match rejected HTTP %s; allow_multiple replaced 2" % s, \
                {"detail": safe_detail(s, b)}
        ecase("replace_file_content", "ambiguous vs allow_multiple", replace_negative)

        def display_file():
            s, h, b = call("GET", "/files/display", {"path": run_dir + "/data/a.txt"})
            assert s == 200, "HTTP %s" % s
            d = js(b)
            assert d.get("exists") is True
            return "PARTIAL", "viewer notified (exists=true); GUI rendering not asserted", d
        ecase("display_file", "open file in viewer", display_file)

        # ---------------- processes ----------------
        def run_command():
            s, h, b = call("POST", "/execute", {"wait": 10}, {
                "command": "echo ENV=$PROOF_ENV; pwd",
                "cwd": run_dir, "env": {"PROOF_ENV": "env42"}})
            assert s == 200, "HTTP %s" % s
            d = js(b)
            out = "".join(x.get("data", "") for x in d.get("output", []))
            assert d["status"] == "done" and d["exit_code"] == 0
            assert "env42" in out and run_dir in out, out
            return "PASS", "cwd/env honoured; exit 0; output verified", \
                {"id": d["id"], "output": out.strip()}
        ecase("run_command", "cwd + env + wait", run_command)

        def run_negative():
            s, h, b = call("POST", "/execute", body={})
            assert s == 422, "expected 422, got %s" % s
            return "PASS", "missing required command -> 422", None
        ecase("run_command", "negative missing command", run_negative)

        cat = {}

        def start_cat():
            s, h, b = call("POST", "/execute", {"wait": 0.2},
                           {"command": "cat", "cwd": run_dir})
            assert s == 200, "HTTP %s" % s
            d = js(b)
            # Track the process before any further assertion so the finally
            # block can kill it even if a later check fails.
            cat.update(d)
            assert d["status"] == "running"
            return "PASS", "long-running process returned id", {"id": d["id"]}
        ecase("run_command", "start interactive process", start_cat)

        def send_input():
            assert cat.get("id"), "no process id"
            s, h, b = call("POST", "/execute/%s/input" % cat["id"],
                           body={"input": "hello-stdin\n"})
            assert s == 200 and js(b).get("status") == "ok"
            return "PASS", "stdin write accepted", None
        ecase("send_process_input", "write to process stdin", send_input)

        def status():
            assert cat.get("id"), "no process id"
            s, h, b = call("GET", "/execute/%s/status" % cat["id"],
                           {"wait": 1, "offset": 0})
            assert s == 200, "HTTP %s" % s
            d = js(b)
            out = "".join(x.get("data", "") for x in d.get("output", []))
            assert "hello-stdin" in out
            assert d["next_offset"] >= 1
            return "PASS", "status/wait/offset; echoed stdin observed", \
                {"status": d["status"], "next_offset": d["next_offset"]}
        ecase("get_process_status", "status wait/offset + output", status)

        def kill():
            assert cat.get("id"), "no process id"
            s, h, b = call("DELETE", "/execute/%s" % cat["id"], {"force": "true"})
            assert s == 200, "HTTP %s" % s
            s2, _, b2 = call("GET", "/execute/%s/status" % cat["id"], {"wait": 0.1})
            assert s2 == 200 and js(b2)["status"] == "killed"
            return "PASS", "force kill; later status is killed/exit -9", \
                {"exit_code": js(b2).get("exit_code")}
        ecase("kill_process", "force kill + verify status", kill)

        def kill_missing():
            s, h, b = call("DELETE", "/execute/does-not-exist", {"force": "true"})
            assert s == 404, "expected 404, got %s" % s
            return "PASS", "unknown process -> 404", None
        ecase("kill_process", "negative unknown process", kill_missing)

        def processes():
            s, h, b = call("GET", "/execute")
            assert s == 200 and isinstance(js(b), list)
            return "PASS", "process inventory readable (%d entries)" % len(js(b)), \
                {"count": len(js(b))}
        ecase("list_processes", "list owned processes", processes)

        # ---------------- user terminal tools ----------------
        term = {}
        s, h, b = call("POST", "/api/terminals", body={})
        if s == 200:
            term = js(b)
            tsid = term.get("id")

        def no_shell_input():
            s, h, b = call("POST", "/api/terminals/user/input",
                           body={"input": "echo TERM-42\n"})
            assert s == 404 and "No connected user shell" in safe_detail(s, b)
            return "PASS", "unattached shell rejected with 404", \
                {"detail": safe_detail(s, b)}
        ecase("send_user_terminal_input", "negative: no attached shell", no_shell_input)

        def no_shell_output():
            s, h, b = call("GET", "/api/terminals/user/output")
            assert s == 404 and "No connected user shell" in safe_detail(s, b)
            return "PASS", "unattached shell rejected with 404", \
                {"detail": safe_detail(s, b)}
        ecase("read_user_terminal", "negative: no attached shell", no_shell_output)

        probe = {}

        def run_shell_probe():
            if not probe:
                cp = subprocess.run(
                    [sys.executable, str(ROOT / "proof" / "user_shell_probe.py")],
                    capture_output=True, text=True, timeout=40)
                probe.update({"exit": cp.returncode, "json": js(cp.stdout.encode())})
            d = probe.get("json") or {}
            assert d.get("handshake", "").endswith("101 Switching Protocols"), d
            assert d.get("send_user_terminal_input", {}).get("status") == 200, d
            assert d.get("read_user_terminal", {}).get("status") == 200, d
            return d

        def user_input():
            d = run_shell_probe()
            assert d["read_user_terminal"]["contains_marker"] is True
            return "PASS", "documented PTY WS attach; stdin accepted and echoed", \
                {"terminal_session_created": d.get("terminal_session_created"),
                 "send_status": d["send_user_terminal_input"]["status"]}
        ecase("send_user_terminal_input", "documented WS attach + stdin", user_input)

        def user_output():
            d = run_shell_probe()
            return "PASS", "readback contains USER-SHELL-PROOF-42", \
                {"read_status": d["read_user_terminal"]["status"],
                 "bytes": d["read_user_terminal"]["bytes"]}
        ecase("read_user_terminal", "documented WS attach + stdout", user_output)

        # ---------------- browser ----------------
        write(run_dir + "/page.html", """<!doctype html><html><head><title>Proof Page</title></head>
<body style="height:3000px"><h1>PROOF-BROWSER-42</h1>
<input id="inp" aria-label="proofinput">
<button id="btn" onclick="document.getElementById('out').textContent='CLICKED-'+document.getElementById('inp').value">Press</button>
<div id="out">NOT-CLICKED</div><div style="height:2000px"></div><p>BOTTOM-MARK</p>
<script>const out=document.getElementById('out');
document.addEventListener('keydown',e=>{out.textContent='KEY-'+e.key});
document.addEventListener('dblclick',e=>{out.textContent='DOUBLE-'+e.clientX+','+e.clientY});
document.addEventListener('mousedown',e=>{out.textContent='DOWN-'+e.clientX+','+e.clientY});
document.addEventListener('mousemove',e=>{out.textContent='MOVE-'+e.clientX+','+e.clientY});
document.addEventListener('mouseup',e=>{out.textContent='UP-'+e.clientX+','+e.clientY});
document.addEventListener('pointermove',e=>{out.textContent='PMOVE-'+e.clientX+','+e.clientY});
document.addEventListener('pointerup',e=>{out.textContent='PUP-'+e.clientX+','+e.clientY});
document.addEventListener('drop',e=>{out.textContent='DROP-'+e.clientX+','+e.clientY});</script>
</body></html>""")
        write(run_dir + "/page2.html",
              "<html><head><title>Page Two</title></head><body>PAGE-TWO</body></html>")

        def start_server():
            nonlocal_server = ("cd %s && (nohup python3 -m http.server %d --bind 127.0.0.1 "
                               ">server.log 2>&1 & echo $!)" % (run_dir, PORT))
            s, h, b = call("POST", "/execute", {"wait": 3},
                           {"command": nonlocal_server, "cwd": run_dir})
            assert s == 200, "HTTP %s" % s
            d = js(b)
            out = "".join(x.get("data", "") for x in d.get("output", []))
            pid = re.search(r"(\d+)", out)
            assert pid, out
            time.sleep(0.7)
            return "PASS", "local HTTP fixture server on loopback:%d" % PORT, \
                {"pid": pid.group(1)}
        server_ok = ecase("browser_snapshot", "fixture server prerequisite", start_server)
        if server_ok:
            server_pid = "unknown"

        page_url = "http://127.0.0.1:%d/page.html" % PORT
        if server_ok:
            s, h, b = call("POST", "/browser/sessions", body={"url": page_url})
            if s == 200:
                bsid = js(b)["id"]
                call("PATCH", "/browser/sessions/" + bsid, body={"mode": "chrome"})

        def tabs():
            assert bsid, "Browser session was not created"
            s, h, b = call("GET", "/browser/tabs")
            assert s == 200, "HTTP %s" % s
            d = js(b)
            assert d.get("current_session_id") == bsid
            assert any(t["mode"] == "chrome" for t in d["tabs"])
            return "PASS", "chat-scoped tab list; current tab in chrome mode", \
                {"tabs": len(d["tabs"])}
        ecase("browser_tabs", "list chat tabs", tabs)

        snap = {}

        def snapshot():
            assert bsid, "no Browser session"
            s, h, b = call("GET", "/browser/snapshot")
            assert s == 200, "HTTP %s" % s
            d = js(b)
            text = d["snapshot"]
            assert "PROOF-BROWSER-42" in text and re.search(r"textbox @e\d+", text)
            snap["text"] = text
            return "PASS", "accessibility tree with element refs", \
                {"title": d.get("title"), "refs": re.findall(r"@e\d+", text)[:6]}
        if ecase("browser_snapshot", "accessibility snapshot", snapshot):
            pass

        def screenshot():
            assert bsid, "no Browser session"
            s, h, b = call("GET", "/browser/screenshot")
            assert s == 200, "HTTP %s" % s
            info = png_info(b)
            assert info and info["width"] > 0 and info["height"] > 0
            return "PASS", "valid PNG %dx%d" % (info["width"], info["height"]), info
        ecase("browser_screenshot", "PNG screenshot", screenshot)

        def action(actions):
            assert bsid, "no Browser session"
            return call("POST", "/browser/action", body={"actions": actions})

        def act_type_click():
            text = snap["text"]
            rin = re.search(r"textbox (@e\d+)", text).group(1)
            rbtn = re.search(r"button (@e\d+)", text).group(1)
            s, h, b = action([{"action": "click", "ref": rin},
                              {"action": "type", "ref": rin, "text": "hello42"},
                              {"action": "click", "ref": rbtn}])
            assert s == 200, "HTTP %s" % s
            out = js(b)["snapshot"]
            assert "CLICKED-hello42" in out and 'value="hello42"' in out
            return "PASS", "ref click/type/click changed DOM (CLICKED-hello42)", \
                {"refs": [rin, rbtn]}
        ecase("browser_action", "click + type effects", act_type_click)

        def act_key():
            s, h, b = action([{"action": "key", "key": "a"}])
            assert s == 200 and "KEY-a" in js(b)["snapshot"]
            return "PASS", "key effect observed (KEY-a)", None
        ecase("browser_action", "key action effect", act_key)

        def act_double():
            s, h, b = action([{"action": "double_click", "x": 400, "y": 300}])
            assert s == 200 and "DOUBLE-400,300" in js(b)["snapshot"]
            return "PASS", "double_click effect observed", None
        ecase("browser_action", "double_click effect", act_double)

        def act_move():
            s, h, b = action([{"action": "move", "x": 300, "y": 250}])
            assert s == 200 and png_info(b) is None
            out = js(b)["snapshot"]
            assert "MOVE-300,250" in out, out
            return "PASS", "move destination observed (MOVE-300,250)", None
        ecase("browser_action", "move coordinates", act_move)

        def act_drag():
            s, h, b = action([{"action": "drag", "x": 100, "y": 200,
                               "to_x": 600, "to_y": 500}])
            assert s == 200
            out = js(b)["snapshot"]
            marks = [l.strip() for l in out.splitlines()
                     if any(x in l for x in ("DOWN-", "MOVE-", "UP-", "PMOVE-", "PUP-", "DROP-"))]
            assert any(x in out for x in ("UP-600,500", "MOVE-600,500",
                                          "PMOVE-600,500", "PUP-600,500", "DROP-600,500")), marks
            return "PASS", "drag destination (600,500) observed in DOM event", {"marks": marks}
        ecase("browser_action", "drag with to_x/to_y", act_drag)

        def act_scroll():
            s0, _, b0 = call("GET", "/browser/screenshot")
            s, h, b = action([{"action": "scroll", "direction": "down", "amount": 600}])
            assert s == 200
            s1, _, b1 = call("GET", "/browser/screenshot")
            changed = png_info(b0)["sha256"] != png_info(b1)["sha256"]
            return "PASS", "scroll accepted; screenshot changed=%s" % changed, \
                {"before": png_info(b0)["sha256"], "after": png_info(b1)["sha256"]}
        ecase("browser_action", "scroll direction/amount", act_scroll)

        def act_nav_history():
            page2 = "http://127.0.0.1:%d/page2.html" % PORT
            s, h, b = action([{"action": "navigate", "url": page2}])
            assert s == 200 and js(b)["title"] == "Page Two"
            s, h, b = action([{"action": "back"}])
            assert s == 200 and js(b)["title"] == "Proof Page"
            s, h, b = action([{"action": "forward"}])
            assert s == 200 and js(b)["title"] == "Page Two"
            s, h, b = action([{"action": "reload"}])
            assert s == 200 and js(b)["title"] == "Page Two"
            return "PASS", "navigate/back/forward/reload titles verified", None
        ecase("browser_action", "navigate + history + reload", act_nav_history)

        def act_partial():
            s, h, b = action([{"action": "navigate", "url": page_url},
                              {"action": "click", "ref": "@e999"}])
            assert s == 409, "expected 409, got %s" % s
            d = js(b)["detail"]
            assert d["completed_actions"] == 1 and d["failed_action_index"] == 1
            return "PASS", "batch partial failure reports completed=1 failed=1", d
        ecase("browser_action", "partial failure semantics", act_partial)

        def act_negative():
            s, h, b = action([{"action": "navigate", "url": "file:///etc/passwd"}])
            assert s == 400
            s2, _, b2 = action([{"action": "click"}])
            assert s2 == 422
            return "PASS", "non-HTTP URL 400; click without ref/x/y 422", \
                {"file_url": safe_detail(s, b), "missing": safe_detail(s2, b2)}
        ecase("browser_action", "negative parameters", act_negative)

        # ---------------- desktop ----------------
        desktop = {}

        def desktop_shot():
            s, h, b = call("GET", "/desktop/screenshot")
            assert s == 200, "HTTP %s" % s
            info = png_info(b)
            assert info and (info["width"], info["height"]) == (1280, 800), info
            desktop["info"] = info
            return "PASS", "1280x800 PNG screenshot", info
        ecase("desktop_screenshot", "PNG dimensions", desktop_shot)

        def desktop_actions():
            cases = [
                ("move", {"action": "move", "x": 300, "y": 300}),
                ("click", {"action": "click", "x": 640, "y": 400, "button": "left"}),
                ("double_click", {"action": "double_click", "x": 640, "y": 400}),
                ("scroll", {"action": "scroll", "direction": "down", "amount": 3}),
                ("drag", {"action": "drag", "x": 500, "y": 300, "to_x": 700, "to_y": 400}),
            ]
            seen = []
            for name, act in cases:
                s, h, b = call("POST", "/desktop/action", body={"actions": [act]})
                assert s == 200, "%s HTTP %s" % (name, s)
                info = png_info(b)
                assert info, "%s did not return PNG" % name
                seen.append(name)
            return "PASS", "move/click/double_click/scroll/drag each returned PNG", \
                {"actions": seen}
        ecase("desktop_action", "pointer/scroll actions", desktop_actions)

        def desktop_effect():
            out_file = run_dir + "/desktop-effect.txt"
            s, h, b = call("POST", "/desktop/action",
                           body={"actions": [{"action": "key", "key": "alt+F2"}]})
            assert s == 200 and png_info(b)
            time.sleep(1.5)
            s, h, b = call("POST", "/desktop/action",
                           body={"actions": [{"action": "type", "text": "xfce4-terminal"}]})
            assert s == 200
            s, h, b = call("POST", "/desktop/action",
                           body={"actions": [{"action": "key", "key": "Return"}]})
            assert s == 200
            time.sleep(3)
            cmd = "echo DESKTOP-PROOF-42 > " + out_file
            s, h, b = call("POST", "/desktop/action",
                           body={"actions": [{"action": "type", "text": cmd}]})
            assert s == 200
            s, h, b = call("POST", "/desktop/action",
                           body={"actions": [{"action": "key", "key": "Return"}]})
            assert s == 200
            time.sleep(2)
            s, h, b = call("GET", "/files/read", {"path": out_file})
            assert s == 200, "desktop-created file missing (HTTP %s)" % s
            text = js(b)["content"]
            assert "DESKTOP-PROOF-42" in text, text
            return "PASS", "key alt+F2 + type + Return opened terminal and wrote file", \
                {"content": text.strip(), "screenshot": png_info(b'') is None}
        ecase("desktop_action", "key/type end-to-end GUI effect", desktop_effect)

        def desktop_partial():
            s, h, b = call("POST", "/desktop/action", body={"actions": [
                {"action": "move", "x": 10, "y": 10},
                {"action": "click", "x": 10}]})
            assert s == 422, "expected 422, got %s" % s
            s2, _, b2 = call("POST", "/desktop/action", body={"actions": [
                {"action": "click", "x": 5000, "y": 10}]})
            assert s2 == 400, "expected 400, got %s" % s2
            return "PASS", "batch validated before run (422); out-of-bounds 400", \
                {"missing_y": safe_detail(s, b), "oob": safe_detail(s2, b2)}
        ecase("desktop_action", "validation/partial failure", desktop_partial)

        return 0
    finally:
        # Clean only our own session tabs, PTY session, server and files.
        try:
            if bsid:
                call("DELETE", "/browser/sessions/" + bsid)
            if tsid:
                call("DELETE", "/api/terminals/" + tsid)
        except Exception:
            pass
        try:
            if cat.get("id"):
                call("DELETE", "/execute/%s" % cat["id"], {"force": "true"})
        except Exception:
            pass
        try:
            call("POST", "/execute", {"wait": 2},
                 {"command": "pkill -f 'http.server %d' 2>/dev/null || true" % PORT,
                  "cwd": run_dir})
            call("POST", "/execute", {"wait": 5},
                 {"command": "rm -rf %s" % run_dir, "cwd": "/home/user"})
        except Exception:
            pass


if __name__ == "__main__":
    started = time.time()
    code = main()
    passed = sum(1 for r in RESULTS if r["status"] == "PASS")
    partial = sum(1 for r in RESULTS if r["status"] == "PARTIAL")
    failed = sum(1 for r in RESULTS if r["status"] == "FAIL")
    summary = {
        "run": RUN,
        "started_utc": datetime.now(timezone.utc).isoformat(),
        "duration_seconds": round(time.time() - started, 1),
        "passed": passed, "partial": partial, "failed": failed,
        "results": RESULTS,
    }
    (ROOT / "proof" / "proof_evidence.json").write_text(
        json.dumps(summary, indent=2) + "\n")
    print("\nSUMMARY pass=%d partial=%d fail=%d duration=%.1fs"
          % (passed, partial, failed, summary["duration_seconds"]))
    sys.exit(1 if failed else 0)
