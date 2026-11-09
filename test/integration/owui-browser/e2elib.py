"""Helpers for the Open WebUI browser end-to-end test (see run.sh).

Python standard library plus Playwright. Runs in the Playwright container on
the "owui" Docker network, next to a disposable stack; it talks to Open WebUI
at https://chat.test (a Caddy edge with an internal CA, hence unverified TLS
here) and to the gateway and MCP server by their compose names.
"""
import json
import os
import re
import ssl
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

OUT = os.environ.get("E2E_OUT", "/out")
SHOTS = os.path.join(OUT, "shots")
RUN_SH = os.environ.get("E2E_RUN_SH", "")
PROJECT = "termiplus-owui"
CHAT = "https://chat.test"
SSO = "https://sso.test"
_ctx = ssl._create_unverified_context()


def load_env():
    env = {}
    path = os.environ.get("E2E_ENVFILE", "/e2e.env")
    for line in open(path):
        if "=" in line:
            k, v = line.strip().split("=", 1)
            env[k] = v
    return env


ENV = load_env() if os.path.exists(os.environ.get("E2E_ENVFILE", "/e2e.env")) else {}
GW_KEY = ENV.get("E2E_GATEWAY_KEY", "")
PLUGIN_KEY = ENV.get("E2E_PLUGIN_KEY", "")
PASSWORDS = {"alice": "alice-pw-e2e", "bob": "bob-pw-e2e", "root": "root-pw-e2e"}
ADMIN = {"email": "admin@e2e.test", "password": "e2e-admin-pw-1", "name": "E2E Admin"}


def redact(s):
    s = str(s)
    for secret in (GW_KEY, PLUGIN_KEY):
        if secret:
            s = s.replace(secret, "<secret>")
    return s


def sh(cmd, timeout=300, check=False):
    """Run a command in the VM's docker (through the mounted CLI). Returns (rc, output)."""
    p = subprocess.run(cmd, shell=isinstance(cmd, str), capture_output=True, text=True, timeout=timeout)
    out = redact((p.stdout or "") + (p.stderr or ""))
    if check and p.returncode:
        raise RuntimeError("command failed: %s\n%s" % (redact(cmd), out))
    return p.returncode, out.strip()


def op(*args, timeout=900, env=None):
    full = dict(os.environ, **(env or {}))
    p = subprocess.run(["bash", RUN_SH, "op", *args], capture_output=True, text=True, timeout=timeout, env=full)
    rc, out = p.returncode, redact((p.stdout or "") + (p.stderr or ""))
    if rc:
        raise RuntimeError("op %s failed: %s" % (" ".join(args), out[-800:]))
    return out


def http(method, url, body=None, headers=None, timeout=120, raw=False):
    data = None
    h = dict(headers or {})
    if body is not None:
        data = body if isinstance(body, bytes) else json.dumps(body).encode()
        h.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, method=method, data=data, headers=h)
    t0 = time.time()
    try:
        r = urllib.request.urlopen(req, context=_ctx, timeout=timeout)
        code, payload = r.status, r.read()
    except urllib.error.HTTPError as e:
        code, payload = e.code, e.read()
    except (urllib.error.URLError, TimeoutError, ConnectionError) as e:
        code, payload = 0, str(e).encode()
    if raw:
        return code, payload, time.time() - t0
    try:
        return code, json.loads(payload or b"null")
    except ValueError:
        return code, payload.decode("utf-8", "replace")


def owui(method, path, body=None, token=None, timeout=120):
    h = {"Authorization": "Bearer " + token} if token else {}
    return http(method, CHAT + path, body, h, timeout)


def admin_token():
    code, d = owui("POST", "/api/v1/auths/signin", {"email": ADMIN["email"], "password": ADMIN["password"]})
    if code != 200:
        code, d = owui("POST", "/api/v1/auths/signup", {**ADMIN, "profile_image_url": "/user.png"})
    if code != 200 or d.get("role") != "admin":
        raise RuntimeError("bootstrap admin sign-in failed: %s %s" % (code, redact(d)))
    return d["token"]


PUBLIC = [{"principal_type": "user", "principal_id": "*", "permission": "read"}]
POLICIES = (("default", "Desktop"), ("scratch", "Temporary desktop"), ("i18n", "German desktop"))


def register_connections(tok, mcp=True):
    """Terminal and native MCP connections through Open WebUI's admin API."""
    conns = [{"id": "tp-" + p, "name": n, "enabled": True, "url": "http://gateway:8080/p/" + p,
              "path": "/openapi.json", "key": GW_KEY, "auth_type": "bearer", "config": {"access_grants": PUBLIC}}
             for p, n in POLICIES]
    code, d = owui("POST", "/api/v1/configs/terminal_servers", {"TERMINAL_SERVER_CONNECTIONS": conns}, tok)
    if code != 200:
        raise RuntimeError("terminal connections: %s %s" % (code, redact(d)))
    if mcp:
        # docs/mcp.md: Streamable HTTP through oauth2-proxy, auth OAuth (the
        # user's SSO token), plugin key and Open WebUI user id as headers.
        srv = {"url": "http://mcp-auth:4180/", "path": "", "type": "mcp", "auth_type": "system_oauth", "key": "",
               "headers": {"X-Termiplus-Owui-Key": PLUGIN_KEY, "X-Termiplus-Owui-User": "{{USER_ID}}"},
               "config": {"enable": True, "access_grants": PUBLIC},
               "info": {"id": "termiplus", "name": "Termiplus", "description": "workspaces"}}
        code, d = owui("POST", "/api/v1/configs/tool_servers", {"TOOL_SERVER_CONNECTIONS": [srv]}, tok)
        if code != 200:
            raise RuntimeError("tool server: %s %s" % (code, redact(d)))
    # Base model "stub" with native function calling, public.
    body = {"id": "stub", "base_model_id": None, "name": "Stub", "meta": {"description": "scripted"},
            "params": {"function_calling": "native"}, "access_grants": PUBLIC, "is_active": True}
    code, d = owui("POST", "/api/v1/models/create", body, tok)
    if code != 200:
        code, d = owui("POST", "/api/v1/models/model/update", body, tok)
    if code != 200:
        raise RuntimeError("model: %s %s" % (code, redact(d)))


# ---- direct protocol clients (what Open WebUI would send), for setup and oracles

def kc_token(user):
    """Access token of the owui client by password grant (test-only: the harness enabled it)."""
    code, d = http("POST", SSO + "/realms/termiplus/protocol/openid-connect/token",
                   urllib.parse.urlencode({"grant_type": "password", "client_id": "owui",
                                           "client_secret": ENV["E2E_OWUI_SECRET"], "username": user,
                                           "password": PASSWORDS[user], "scope": "openid"}).encode(),
                   {"Content-Type": "application/x-www-form-urlencoded"})
    if code != 200:
        raise RuntimeError("token for %s: %s %s" % (user, code, redact(d)))
    return d["access_token"]


def mcp(user, owui_id, name, args=None, token=None, key=True, timeout=180):
    """tools/call on the MCP server through oauth2-proxy. Returns (error_code|None, text|dict)."""
    token = token or kc_token(user)
    h = {"Authorization": "Bearer " + token, "X-Termiplus-Owui-User": owui_id}
    if key:
        h["X-Termiplus-Owui-Key"] = PLUGIN_KEY
    code, d = http("POST", "http://mcp-auth:4180/", {"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                   "params": {"name": name, "arguments": args or {}}}, h, timeout)
    if code != 200:
        return code, d
    if isinstance(d, dict) and d.get("error"):
        return d["error"]["code"], d["error"].get("message", "")
    res = d.get("result", {})
    text = (res.get("content") or [{}])[0].get("text", "")
    try:
        parsed = json.loads(text)
    except ValueError:
        parsed = text
    if res.get("isError"):
        return "tool-error", parsed
    return None, parsed


def mcp_rpc(user, owui_id, method, params=None, token=None):
    token = token or kc_token(user)
    code, d = http("POST", "http://mcp-auth:4180/", {"jsonrpc": "2.0", "id": 1, "method": method, "params": params or {}},
                   {"Authorization": "Bearer " + token, "X-Termiplus-Owui-Key": PLUGIN_KEY,
                    "X-Termiplus-Owui-User": owui_id})
    return code, d


def native(owui_id, policy, path, method="GET", body=None, session="saved-chat", timeout=180, raw=False):
    """A request as Open WebUI's terminal relay sends it to the gateway."""
    h = {"Authorization": "Bearer " + GW_KEY, "X-User-Id": owui_id, "X-Session-Id": session}
    return http(method, "http://gateway:8080/p/%s/%s" % (policy, path), body, h, timeout, raw=raw)


def run_in(owui_id, policy, command, session="saved-chat", timeout=180):
    """Run a shell command in a user's workspace through the gateway; returns its output text."""
    code, d = native(owui_id, policy, "execute?wait=%d" % min(timeout, 60), "POST", {"command": command}, session, timeout)
    if code != 200 or not isinstance(d, dict):
        return "HTTP %s %s" % (code, str(d)[:300])
    out = d.get("output") or d.get("logs") or d
    if isinstance(out, list):
        out = "".join(x.get("data", str(x)) if isinstance(x, dict) else str(x) for x in out)
    return str(out)


def managed_containers():
    rc, out = sh("docker ps -a --filter label=termiplus.managed=true --format '{{.Names}}|{{.Label \"termiplus.user\"}}|{{.State}}|{{.Label \"termiplus.policy\"}}'")
    rows = []
    for line in out.splitlines():
        p = line.split("|")
        if len(p) == 4:
            rows.append({"name": p[0], "user": p[1], "state": p[2], "policy": p[3]})
    return rows


def containers_of(owui_id):
    return [c for c in managed_containers() if c["user"] == owui_id]


def gw_log(since="10m"):
    return sh("docker logs --since %s %s-gateway-1 2>&1 | tail -60" % (since, PROJECT))[1]


# ---- browser helpers

def shot(page, name):
    os.makedirs(SHOTS, exist_ok=True)
    path = os.path.join(SHOTS, re.sub(r"[^A-Za-z0-9_.-]", "_", name) + ".png")
    try:
        page.screenshot(path=path)
    except Exception as e:  # a screenshot must never fail a check
        return "screenshot failed: %s" % e
    return "shots/" + os.path.basename(path)


def sso_login(browser, user, timeout=90):
    """Sign in through Keycloak in a fresh context; returns (context, page, owui token)."""
    ctx = browser.new_context(ignore_https_errors=True, viewport={"width": 1400, "height": 1000}, locale="en-US", timezone_id="UTC")
    page = ctx.new_page()
    page.goto(CHAT + "/auth")
    page.get_by_role("button", name=re.compile("Keycloak")).click()
    page.wait_for_url("**/realms/termiplus/**", timeout=30000)
    page.fill("#username", user)
    page.fill("#password", PASSWORDS[user])
    page.click("#kc-login")
    page.wait_for_url(CHAT + "/**", timeout=timeout * 1000)
    page.wait_for_selector("#chat-input", timeout=timeout * 1000)
    return ctx, page, page.evaluate("localStorage.getItem('token')")


def whoami(token):
    code, d = owui("GET", "/api/v1/auths/", None, token)
    return d if code == 200 else {}


def open_chat(page, terminal=None, tools=True, model="stub"):
    """Start a new chat with the stub model, the Termiplus MCP tool on, and optionally a terminal."""
    url = CHAT + "/?models=" + model + ("&tools=server:mcp:termiplus" if tools else "")
    page.goto(url)
    page.wait_for_selector("#chat-input", timeout=60000)
    page.wait_for_timeout(800)
    if terminal:
        page.get_by_role("button", name="Terminal").click()
        page.get_by_text(terminal, exact=True).first.click()
        page.wait_for_timeout(500)


def send(page, script, timeout=240, done=("STUB-DONE", "STUB-NOTOOL"), count_before=None):
    """Type a script (one or more @@call lines) and wait for the stub's final text.

    Returns the text of the last assistant message ('' on timeout)."""
    box = page.locator("#chat-input")
    box.click()
    lines = script.split("\n")
    for i, line in enumerate(lines):
        page.keyboard.insert_text(line)
        if i < len(lines) - 1:
            page.keyboard.press("Shift+Enter")
    page.keyboard.press("Enter")
    deadline = time.time() + timeout
    while time.time() < deadline:
        page.wait_for_timeout(1000)
        try:
            txt = page.locator("#messages-container").inner_text()
        except Exception:
            continue
        # The user's own text contains none of the markers.
        if any(m in txt.split("@@call")[-1] for m in done) or any(m in txt for m in done):
            return txt
    try:
        return page.locator("#messages-container").inner_text()
    except Exception:
        return ""
