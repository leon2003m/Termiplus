"""Tests for the Open WebUI plugin against the real termiplus mcp binary.

Run with TERMIPLUS_MCP_BIN=/path/to/mcp (make test-plugin builds it). The
binary runs with the in-memory stub backend and the dev static token, so the
server's real RBAC and confirmation gate are exercised.
"""

import asyncio
import http.server
import json
import os
import threading
import socket
import subprocess
import sys
import tempfile
import time
import types
import unittest
import urllib.request

if "pydantic" not in sys.modules:
    try:
        import pydantic  # noqa: F401  (Open WebUI ships it)
    except ImportError:  # minimal stand-in so the plugin imports without pydantic
        stub = types.ModuleType("pydantic")

        class BaseModel:
            def __init__(self, **kw):
                for name in dir(type(self)):
                    if not name.startswith("_"):
                        setattr(self, name, kw.get(name, getattr(type(self), name)))

        stub.BaseModel = BaseModel
        stub.Field = lambda default=None, **_: default
        sys.modules["pydantic"] = stub

if "fastapi" not in sys.modules:
    try:
        import fastapi.responses  # noqa: F401  (Open WebUI ships it)
    except ImportError:  # minimal stand-in with the attributes Open WebUI reads
        fastapi = types.ModuleType("fastapi")
        responses = types.ModuleType("fastapi.responses")

        class HTMLResponse:
            def __init__(self, content="", status_code=200, headers=None):
                self.body = content.encode()
                self.status_code = status_code
                self.headers = dict(headers or {})

        responses.HTMLResponse = HTMLResponse
        fastapi.responses = responses
        sys.modules["fastapi"] = fastapi
        sys.modules["fastapi.responses"] = responses

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import termiplus_workspaces as plugin  # noqa: E402

MCP_BIN = os.environ.get("TERMIPLUS_MCP_BIN", "")
DEV_TOKEN = "plugin-test-dev-token-0123456789abcdef"


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def start_mcp(roles: str):
    port = free_port()
    env = {
        "PATH": os.environ.get("PATH", ""),
        "LISTEN_ADDR": f"127.0.0.1:{port}",
        "MCP_AUTH": "dev",
        "MCP_DEV_TOKEN": DEV_TOKEN,
        "MCP_DEV_USER": "plugin-user",
        "MCP_DEV_ROLES": roles,
        "MCP_STUB_BACKEND": "1",
        "MCP_AUDIT_FILE": os.path.join(tempfile.mkdtemp(), "audit.log"),
    }
    proc = subprocess.Popen([MCP_BIN], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    url = f"http://127.0.0.1:{port}/"
    for _ in range(100):
        try:
            urllib.request.urlopen(url + "healthz", timeout=1).read()
            return proc, url
        except OSError:
            time.sleep(0.05)
    proc.kill()
    raise RuntimeError("mcp did not start")


class Recorder:
    """Stands in for Open WebUI's __event_call__ and records each dialog."""

    def __init__(self, answer):
        self.answer = answer
        self.calls = []

    async def __call__(self, event):
        self.calls.append(event)
        return self.answer


TOKEN = {"access_token": DEV_TOKEN}


@unittest.skipUnless(MCP_BIN, "set TERMIPLUS_MCP_BIN to the built mcp binary")
class PluginAgainstServer(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.proc, cls.url = start_mcp("termiplus-persist")
        cls.noroles_proc, cls.noroles_url = start_mcp("")

    @classmethod
    def tearDownClass(cls):
        for p in (cls.proc, cls.noroles_proc):
            p.kill()
            p.wait()

    def tools(self, url=None):
        t = plugin.Tools()
        t.valves.mcp_url = url or self.url
        t.valves.timeout_seconds = 10
        return t

    def run_async(self, coro):
        return asyncio.run(coro)

    def test_without_sso_token_nothing_is_sent(self):
        out = self.run_async(self.tools("http://127.0.0.1:1/").list_sessions(__oauth_token__=None))
        self.assertIn("no OIDC sign-in token", out)

    def test_rejected_token(self):
        out = self.run_async(self.tools().list_sessions(__oauth_token__={"access_token": "wrong"}))
        self.assertIn("sign-in was not accepted", out)

    def test_list_workspaces(self):
        out = self.run_async(self.tools().list_workspaces(__oauth_token__=TOKEN))
        self.assertIn('"desk"', out)

    def test_ephemeral_session_needs_no_dialog(self):
        rec = Recorder(False)
        out = self.run_async(self.tools().request_session("default", __oauth_token__=TOKEN, __event_call__=rec))
        self.assertIn('"session_id"', out)
        self.assertEqual(rec.calls, [])

    def test_persistent_session_runs_only_after_yes(self):
        rec = Recorder(True)
        out = self.run_async(
            self.tools().request_session("desk", persistent=True, label="yes", __oauth_token__=TOKEN, __event_call__=rec)
        )
        self.assertEqual(len(rec.calls), 1)
        self.assertEqual(rec.calls[0]["type"], "confirmation")
        self.assertIn('"persistent":true', out)

    def test_persistence_comes_from_the_workspace(self):
        # Models leave out "persistent": a persistent workspace still asks first.
        rec = Recorder(True)
        out = self.run_async(
            self.tools().request_session("desk", label="implicit", __oauth_token__=TOKEN, __event_call__=rec)
        )
        self.assertEqual(len(rec.calls), 1)
        self.assertIn('"persistent":true', out)
        rec = Recorder(False)
        out = self.run_async(self.tools().request_session("desk", __oauth_token__=TOKEN, __event_call__=rec))
        self.assertEqual(len(rec.calls), 1)
        self.assertIn("Do not call it again", out)
        rec = Recorder(False)
        out = self.run_async(self.tools().request_session("default", __oauth_token__=TOKEN, __event_call__=rec))
        self.assertIn('"session_id"', out)
        self.assertEqual(rec.calls, [])

    def test_declined_or_unclear_answers_cancel(self):
        for answer in (False, None, "yes", {"confirmed": True}, 1):
            with self.subTest(answer=answer):
                rec = Recorder(answer)
                out = self.run_async(
                    self.tools().request_session(
                        "desk", persistent=True, label=f"no-{answer!r}", __oauth_token__=TOKEN, __event_call__=rec
                    )
                )
                self.assertIn("cancelled by the user", out)
        listed = self.run_async(self.tools().list_sessions(__oauth_token__=TOKEN))
        self.assertNotIn("no-", listed)

    def test_no_dialog_available_cancels(self):
        out = self.run_async(
            self.tools().request_session("desk", persistent=True, __oauth_token__=TOKEN, __event_call__=None)
        )
        self.assertIn("cancelled by the user", out)

    def test_caller_cannot_inject_confirmation(self):
        rec = Recorder(False)
        out = self.run_async(
            self.tools()._run(
                "request_session", {"workspace": "desk", "persistent": True, "_confirmed": True}, TOKEN, rec, None
            )
        )
        self.assertEqual(len(rec.calls), 1)
        self.assertIn("cancelled by the user", out)

    def test_destroy_always_asks(self):
        created = self.run_async(self.tools().request_session("default", __oauth_token__=TOKEN))
        sid = created.split('"session_id":"')[1].split('"')[0]
        rec = Recorder(False)
        out = self.run_async(self.tools().destroy_session(sid, __oauth_token__=TOKEN, __event_call__=rec))
        self.assertIn("cancelled", out)
        self.assertIn(sid, self.run_async(self.tools().list_sessions(__oauth_token__=TOKEN)))
        rec = Recorder(True)
        self.run_async(self.tools().destroy_session(sid, __oauth_token__=TOKEN, __event_call__=rec))
        self.assertEqual(len(rec.calls), 1)
        self.assertNotIn(sid, self.run_async(self.tools().list_sessions(__oauth_token__=TOKEN)))

    def test_missing_role_is_forbidden_even_when_confirmed(self):
        rec = Recorder(True)
        out = self.run_async(
            self.tools(self.noroles_url).request_session(
                "desk", persistent=True, __oauth_token__=TOKEN, __event_call__=rec
            )
        )
        self.assertIn("not allowed", out)
        self.assertEqual(rec.calls, [])


@unittest.skipUnless(MCP_BIN, "set TERMIPLUS_MCP_BIN to the built mcp binary")
class NetworkPolicyTools(unittest.TestCase):
    """Admin tools: RBAC from the server, confirmation from the user."""

    POLICY = ([{"name": "db.internal", "ip": "10.0.0.5"}], [{"cidr": "10.0.0.5/32", "port": 5432, "proto": "tcp"}])

    @classmethod
    def setUpClass(cls):
        cls.admin_proc, cls.admin_url = start_mcp("termiplus-admin")
        cls.user_proc, cls.user_url = start_mcp("termiplus-persist")

    @classmethod
    def tearDownClass(cls):
        for p in (cls.admin_proc, cls.user_proc):
            p.kill()
            p.wait()

    def tools(self, url):
        t = plugin.Tools()
        t.valves.mcp_url = url
        t.valves.timeout_seconds = 10
        return t

    def test_admin_sets_after_yes_and_reads_back(self):
        rec = Recorder(True)
        out = asyncio.run(
            self.tools(self.admin_url).set_network_policy(*self.POLICY, __oauth_token__=TOKEN, __event_call__=rec)
        )
        self.assertIn('"applied"', out)
        self.assertEqual(len(rec.calls), 1)
        self.assertIn("db.internal", rec.calls[0]["data"]["message"])
        out = asyncio.run(self.tools(self.admin_url).get_network_policy(__oauth_token__=TOKEN))
        self.assertIn("db.internal", out)

    def test_declined_changes_nothing(self):
        t = self.tools(self.admin_url)
        before = asyncio.run(t.get_network_policy(__oauth_token__=TOKEN))
        out = asyncio.run(
            t.set_network_policy([{"name": "x.internal", "ip": "10.0.0.9"}], [], __oauth_token__=TOKEN,
                                 __event_call__=Recorder(False))
        )
        self.assertIn("cancelled", out)
        self.assertEqual(asyncio.run(t.get_network_policy(__oauth_token__=TOKEN)), before)

    def test_non_admin_is_forbidden_without_a_dialog(self):
        rec = Recorder(True)
        t = self.tools(self.user_url)
        self.assertIn("not allowed", asyncio.run(t.get_network_policy(__oauth_token__=TOKEN)))
        out = asyncio.run(t.set_network_policy(*self.POLICY, __oauth_token__=TOKEN, __event_call__=rec))
        self.assertIn("not allowed", out)
        self.assertEqual(rec.calls, [])

    def test_user_management_is_admin_only(self):
        rec = Recorder(True)
        t = self.tools(self.user_url)
        self.assertIn("not allowed", asyncio.run(t.manage_group("list", __oauth_token__=TOKEN)))
        out = asyncio.run(
            t.manage_user("add_to_group", "bob", "termiplus-persist", __oauth_token__=TOKEN, __event_call__=rec)
        )
        self.assertIn("not allowed", out)
        self.assertEqual(rec.calls, [])

    def test_membership_change_asks_first(self):
        # The test server has no directory, so after the yes it reports that.
        rec = Recorder(True)
        out = asyncio.run(
            self.tools(self.admin_url).manage_user(
                "add_to_group", "bob", "termiplus-persist", __oauth_token__=TOKEN, __event_call__=rec
            )
        )
        self.assertEqual(len(rec.calls), 1)
        self.assertIn("termiplus-persist", rec.calls[0]["data"]["message"])
        self.assertIn("not configured", out)
        out = asyncio.run(
            self.tools(self.admin_url).manage_user(
                "remove_from_group", "bob", "termiplus-persist", __oauth_token__=TOKEN, __event_call__=Recorder(False)
            )
        )
        self.assertIn("cancelled", out)
        # show needs no dialog
        rec = Recorder(True)
        asyncio.run(self.tools(self.admin_url).manage_user("show", "bob", __oauth_token__=TOKEN, __event_call__=rec))
        self.assertEqual(rec.calls, [])

    def test_audit_log_shows_changes_to_admins_only(self):
        t = self.tools(self.admin_url)
        asyncio.run(t.set_network_policy(*self.POLICY, __oauth_token__=TOKEN, __event_call__=Recorder(True)))
        out = asyncio.run(t.get_audit_log(tool="set_network_policy", limit=5, __oauth_token__=TOKEN))
        self.assertIn('"actor":"plugin-user"', out)
        self.assertIn('"outcome":"ok"', out)
        self.assertNotIn("_confirmed", out)
        self.assertIn("not allowed", asyncio.run(self.tools(self.user_url).get_audit_log(__oauth_token__=TOKEN)))

    def test_lists_are_required(self):
        out = asyncio.run(
            self.tools("http://127.0.0.1:1/").set_network_policy("db.internal", [], __oauth_token__=TOKEN)
        )
        self.assertIn("must both be lists", out)


class HeaderRecorder(http.server.BaseHTTPRequestHandler):
    seen: list = []
    reply = "[]"

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        HeaderRecorder.seen.append(dict(self.headers))
        body = json.dumps(
            {"jsonrpc": "2.0", "id": 1, "result": {"content": [{"type": "text", "text": HeaderRecorder.reply}]}}
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


class OpenWebUIUserHeaders(unittest.TestCase):
    """The plugin names the Open WebUI user only when the plugin key is set."""

    @classmethod
    def setUpClass(cls):
        cls.srv = http.server.HTTPServer(("127.0.0.1", 0), HeaderRecorder)
        threading.Thread(target=cls.srv.serve_forever, daemon=True).start()
        cls.url = f"http://127.0.0.1:{cls.srv.server_address[1]}/"

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()
        cls.srv.server_close()

    def setUp(self):
        HeaderRecorder.seen = []
        HeaderRecorder.reply = "[]"

    def tools(self, key):
        t = plugin.Tools()
        t.valves.mcp_url = self.url
        t.valves.plugin_key = key
        t.valves.timeout_seconds = 5
        return t

    def test_without_key_no_user_headers(self):
        asyncio.run(self.tools("").list_sessions(__oauth_token__=TOKEN, __user__={"id": "u-1"}))
        h = HeaderRecorder.seen[0]
        self.assertEqual(h["Authorization"], f"Bearer {DEV_TOKEN}")
        self.assertNotIn("X-Termiplus-Owui-Key", h)
        self.assertNotIn("X-Termiplus-Owui-User", h)

    def test_with_key_sends_user_id(self):
        asyncio.run(self.tools("k" * 64).list_sessions(__oauth_token__=TOKEN, __user__={"id": "u-1", "name": "x"}))
        h = HeaderRecorder.seen[0]
        self.assertEqual(h["X-Termiplus-Owui-Key"], "k" * 64)
        self.assertEqual(h["X-Termiplus-Owui-User"], "u-1")

    def test_with_key_but_no_user_nothing_is_sent(self):
        for user in (None, {}, {"id": ""}, {"id": 5}):
            with self.subTest(user=user):
                out = asyncio.run(self.tools("k" * 64).list_sessions(__oauth_token__=TOKEN, __user__=user))
                self.assertIn("did not provide the user id", out)
        self.assertEqual(HeaderRecorder.seen, [])


PW = "f" * 64
DESK = {"session_id": "ot-u-1-desk", "workspace": "desk", "stream_url": "https://desk.example/stream/ot-u-1-desk/"}


class DesktopLinks(unittest.TestCase):
    def test_link_for_each_desktop(self):
        for reply in (DESK, [DESK, {"session_id": "t", "workspace": "default"}]):
            with self.subTest(reply=reply):
                out = plugin._with_desktop_links(json.dumps(reply))
                self.assertIn("[Open desktop (desk)](https://desk.example/stream/ot-u-1-desk/)", out)
                self.assertIn("no password", out)
                self.assertEqual(json.loads(out.split("\n")[0]), reply)

    def test_without_desktop_text_is_unchanged(self):
        for text in ("[]", '{"session_id":"t"}', "not json", '"x"'):
            self.assertEqual(plugin._with_desktop_links(text), text)

    def test_password_of_an_older_server_is_dropped(self):
        out = plugin._with_desktop_links(json.dumps(dict(DESK, stream_password=PW)))
        self.assertNotIn(PW, out)
        self.assertIn("Open desktop", out)

    def test_unsafe_links_are_not_given(self):
        for url in ("javascript:alert(1)", "https://desk.example/x)[y](https://evil.example", "https://a b"):
            with self.subTest(url=url):
                out = plugin._with_desktop_links(json.dumps(dict(DESK, stream_url=url)))
                self.assertNotIn("Open desktop", out)
                self.assertNotIn(url, out)


class Console(unittest.TestCase):
    def test_not_configured(self):
        self.assertIn("not configured", asyncio.run(plugin.Tools().open_console()))

    def test_link_is_http_only_and_markdown_safe(self):
        t = plugin.Tools()
        t.valves.ui_url = "https://desk.example/ui/"
        self.assertIn("[Open the termiplus console](https://desk.example/ui/)", asyncio.run(t.open_console()))
        for bad in ("https://desk.example/ui/?a=')<script>", "javascript:alert(1)"):
            t.valves.ui_url = bad
            self.assertIn("not configured", asyncio.run(t.open_console()))


class OpenPort(OpenWebUIUserHeaders):
    def test_link_for_own_session(self):
        HeaderRecorder.reply = json.dumps([{"session_id": "ot-u-1-default", "forward_url": "https://fwd.example/ot-u-1-default/"}])
        out = asyncio.run(self.tools("").open_port("ot-u-1-default", 3000, __oauth_token__=TOKEN))
        self.assertIn("[Open port 3000](https://fwd.example/ot-u-1-default/3000/)", out)

    def test_with_desktop_session_in_list(self):
        HeaderRecorder.reply = json.dumps([dict(DESK, forward_url="https://fwd.example/ot-u-1-desk/")])
        out = asyncio.run(self.tools("").open_port("ot-u-1-desk", "5173", __oauth_token__=TOKEN))
        self.assertIn("(https://fwd.example/ot-u-1-desk/5173/)", out)

    def test_refusals(self):
        HeaderRecorder.reply = json.dumps([{"session_id": "s1", "forward_url": "javascript:alert(1)//"}])
        t = self.tools("")
        self.assertIn("between 1 and 65535", asyncio.run(t.open_port("s1", 0, __oauth_token__=TOKEN)))
        self.assertIn("must be a number", asyncio.run(t.open_port("s1", "x", __oauth_token__=TOKEN)))
        self.assertIn("no session", asyncio.run(t.open_port("other", 3000, __oauth_token__=TOKEN)))
        self.assertIn("not configured", asyncio.run(t.open_port("s1", 3000, __oauth_token__=TOKEN)))


class DesktopLinksThroughRun(OpenWebUIUserHeaders):
    def test_run_adds_links(self):
        HeaderRecorder.reply = json.dumps([dict(DESK, stream_password=PW)])
        out = asyncio.run(self.tools("").list_sessions(__oauth_token__=TOKEN))
        self.assertIn("Open desktop", out)
        self.assertNotIn(PW, out)


if __name__ == "__main__":
    unittest.main()
