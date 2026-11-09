#!/usr/bin/env python3
"""Browser end-to-end checks of termiplus through a real Open WebUI (see run.sh).

    python3 e2e.py [check ...]      # no argument: all checks, in order

Every check prints PASS / FAIL / BLOCKED lines with evidence; results.json and
screenshots (shots/) land in $E2E_OUT. A check is BLOCKED only when it cannot
run (the reason is recorded); it is never turned into a PASS.
"""
import json
import re
import sys
import time
import traceback

from playwright.sync_api import sync_playwright

import e2elib as L


class Blocked(Exception):
    pass


RESULTS = []
CHECKS = {}
ORDER = []
S = {"ids": {}}  # shared state: browser, user ids, tokens


def check(cid, title):
    def deco(fn):
        CHECKS[cid] = (title, fn)
        ORDER.append(cid)
        return fn
    return deco


class C:
    """One check: collects sub-assertions; FAIL when any fails."""

    def __init__(self, cid, title):
        self.cid, self.title, self.subs, self.status, self.notes = cid, title, [], None, []

    def ok(self, name, cond, evidence=""):
        self.subs.append({"name": name, "ok": bool(cond), "evidence": L.redact(str(evidence))[:600]})
        print("   %s  %s%s" % ("ok  " if cond else "FAIL", name, "" if cond else "  <- " + L.redact(str(evidence))[:300]), flush=True)
        return bool(cond)

    def note(self, text):
        self.notes.append(L.redact(text))
        print("   note: " + L.redact(text)[:300], flush=True)


def run_check(cid):
    title, fn = CHECKS[cid]
    print("\n== %s  %s" % (cid, title), flush=True)
    c = C(cid, title)
    t0 = time.time()
    try:
        fn(c)
        c.status = "PASS" if c.subs and all(s["ok"] for s in c.subs) else "FAIL"
        if not c.subs:
            c.status = "FAIL"
            c.note("no assertions ran")
    except Blocked as e:
        c.status = "BLOCKED"
        c.note(str(e))
    except Exception as e:  # a crashing check is a FAIL with the traceback, not silence
        c.status = "FAIL"
        c.note("exception: %s\n%s" % (e, traceback.format_exc()[-1500:]))
        print(traceback.format_exc()[-1500:], flush=True)
    secs = round(time.time() - t0, 1)
    RESULTS.append({"id": cid, "title": title, "status": c.status, "seconds": secs, "subs": c.subs, "notes": c.notes})
    print("%s  %s (%ss)" % (c.status, title, secs), flush=True)
    with open(L.OUT + "/results.json", "w") as f:
        json.dump(RESULTS, f, indent=1)


# --------------------------------------------------------------------- helpers

def login(user):
    key = "ctx-" + user
    if key not in S:
        ctx, page, tok = L.sso_login(S["browser"], user)
        S[key] = (ctx, page, tok)
        S["ids"][user] = L.whoami(tok).get("id", "")
    return S[key]


def fresh_page(user):
    """A new tab in the user's signed-in context."""
    ctx, _, tok = login(user)
    page = ctx.new_page()
    return page, tok


def uid(user):
    login(user)
    return S["ids"][user]


TERMINAL_IDS = {"Desktop": "tp-default", "Temporary desktop": "tp-scratch", "German desktop": "tp-i18n"}


def chat(user, script, terminal=None, tools=True, timeout=240, shot=None, extra_tools=None, ui_select=False):
    """Open a new chat, optionally pick a terminal, send a script, wait for the stub's answer.

    The terminal picker is clicked in the UI when ui_select is set; otherwise the
    selection Open WebUI itself remembers (localStorage selectedTerminalId) is set,
    which keeps the many chats of one check short."""
    page, tok = fresh_page(user)
    url_tools = ["server:mcp:termiplus"] if tools else []
    url_tools += extra_tools or []
    url = L.CHAT + "/?models=stub" + ("&tools=" + ",".join(url_tools) if url_tools else "")
    page.goto(url)
    page.wait_for_selector("#chat-input", timeout=60000)
    page.evaluate("([k, v]) => v ? localStorage.setItem(k, v) : localStorage.removeItem(k)",
                  ["selectedTerminalId", None if (terminal is None or ui_select) else TERMINAL_IDS[terminal]])
    if terminal and not ui_select:
        page.goto(url)
        page.wait_for_selector("#chat-input", timeout=60000)
    page.wait_for_timeout(800)
    if terminal and ui_select:
        page.get_by_role("button", name="Terminal").click()
        page.get_by_text(terminal, exact=True).first.click()
        page.wait_for_timeout(500)
    txt = L.send(page, script, timeout=timeout)
    page.wait_for_timeout(1500)
    img = L.shot(page, shot) if shot else None
    return page, txt, img


def console_page(user):
    """The human console (desk.test) in a signed-in browser context of the user."""
    key = "con-" + user
    if key not in S:
        cp = S["browser"].new_context(ignore_https_errors=True, viewport={"width": 1400, "height": 1000}, locale="en-US")
        page = cp.new_page()
        page.goto("https://desk.test/ui/")
        page.wait_for_url("**/realms/termiplus/**", timeout=30000)
        page.fill("#username", user); page.fill("#password", L.PASSWORDS[user]); page.click("#kc-login")
        page.wait_for_url("https://desk.test/**", timeout=60000)
        page.wait_for_timeout(2500)
        S[key] = (cp, page)
    return S[key][1]


def console_api(user, method, path, body=None):
    """A same-origin call to the console API as the browser would make it. Returns (status, text)."""
    page = console_page(user)
    r = page.evaluate("""async ([m, p, b]) => { const r = await fetch('/ui/api/' + p, {method: m,
        headers: {'X-Termiplus-UI': '1', 'Content-Type': 'application/json'}, body: b ? JSON.stringify(b) : undefined});
        return [r.status, (await r.text()).slice(0, 2000)]; }""", [method, path, body])
    return r[0], r[1]


def clear_snapshots(user, ws="default"):
    """Free the snapshot slots through the console (only humans can delete)."""
    code, text = console_api(user, "GET", "snapshots/" + ws)
    try:
        snaps = json.loads(text).get("snapshots", [])
    except ValueError:
        return 0
    for sn in snaps:
        console_api(user, "DELETE", "snapshots/%s/%s" % (ws, sn.get("id")))
    return len(snaps)


def host_dialog(page, accept=True, timeout=8000):
    """Answer Open WebUI's 'Tool call' confirmation for an app tool call. Returns the dialog text or ''."""
    dlg = page.get_by_role("button", name=re.compile("^Confirm$"))
    try:
        dlg.first.wait_for(timeout=timeout)
    except Exception:
        return ""
    text = page.locator("body").inner_text()
    if accept:
        dlg.first.click()
    else:
        page.get_by_role("button", name=re.compile("^Cancel$")).first.click()
    return text


def as_list(res, key):
    return res if isinstance(res, list) else (res.get(key, []) if isinstance(res, dict) else [])


def done_text(txt):
    """The stub's final answer (STUB-DONE ... or STUB-NOTOOL ...)."""
    m = re.search(r"STUB-(DONE|NOTOOL).*", txt, re.S)
    return m.group(0) if m else txt[-600:]


def stub_log():
    rc, out = L.sh("cat %s/stub/stub.jsonl" % L.OUT)
    rows = []
    for line in out.splitlines():
        try:
            rows.append(json.loads(line))
        except ValueError:
            pass
    return rows


def run_cmd_script(cmd, wait=120):
    return "@@call run_command %s" % json.dumps({"command": cmd, "wait": wait})


def term_out(txt):
    """Command output out of a STUB-DONE answer: the tool result is JSON text."""
    d = done_text(txt)
    return d.replace("\\n", "\n").replace("\\u003c", "<")


# ---------------------------------------------------------------------- checks

@check("00_bootstrap", "Bootstrap: local admin, terminal and native MCP connections, stub model")
def bootstrap(c):
    tok = L.admin_token()
    S["admin"] = tok
    L.register_connections(tok)
    code, d = L.owui("GET", "/api/v1/terminals/", None, tok)
    ids = sorted(x["id"] for x in d) if code == 200 else []
    c.ok("terminal connections registered through the admin API", ids == ["tp-default", "tp-i18n", "tp-scratch"], ids)
    code, d = L.owui("GET", "/api/v1/configs/tool_servers", None, tok)
    c.ok("native MCP connection registered (type mcp, system_oauth, headers)",
         code == 200 and d["TOOL_SERVER_CONNECTIONS"][0].get("auth_type") == "system_oauth", code)
    code, d = L.owui("GET", "/api/models", None, tok)
    c.ok("stub model offered", code == 200 and any(m["id"] == "stub" for m in d["data"]), code)
    # The Termiplus plugin tool (for open_console), installed through the tools API.
    src = open(L.RUN_SH.rsplit("/test/integration/", 1)[0] + "/integrations/openwebui/termiplus_workspaces.py").read()
    L.owui("DELETE", "/api/v1/tools/id/termiplus_workspaces/delete", None, tok)
    code, d = L.owui("POST", "/api/v1/tools/create", {
        "id": "termiplus_workspaces", "name": "termiplus workspaces", "content": src,
        "meta": {"description": "plugin"}, "access_grants": L.PUBLIC}, tok)
    c.ok("plugin tool installed", code == 200, str(d)[:200])
    code, d = L.owui("POST", "/api/v1/tools/id/termiplus_workspaces/valves/update", {
        "mcp_url": "http://mcp-auth:4180/", "plugin_key": L.PLUGIN_KEY, "ui_url": "https://desk.test/ui/"}, tok)
    c.ok("plugin valves set", code == 200, str(d)[:200])


@check("01_sso", "SSO login through Keycloak in the browser (normal and entitled user)")
def sso(c):
    for user, groups in (("bob", set()), ("alice", {"termiplus-persist"}), ("root", {"termiplus-admin"})):
        ctx, page, tok = L.sso_login(S["browser"], user)
        S["ctx-" + user] = (ctx, page, tok)
        who = L.whoami(tok)
        S["ids"][user] = who.get("id", "")
        img = L.shot(page, "01_sso_%s" % user)
        c.ok("%s signs in through Keycloak and lands in Open WebUI as %s" % (user, who.get("email")),
             who.get("email") == "%s@example.test" % user and who.get("role") == "user", "%s %s" % (who, img))
        access = L.kc_token(user)
        import base64
        claims = json.loads(base64.urlsafe_b64decode(access.split(".")[1] + "=="))
        got = set(claims.get("groups", []))
        c.ok("%s's token carries groups %s and aud termiplus-mcp" % (user, sorted(groups)),
             {g.lstrip("/") for g in got} == groups and "termiplus-mcp" in str(claims.get("aud")),
             "groups=%s aud=%s" % (sorted(got), claims.get("aud")))


@check("02_mcp_panel", "MCP App panel in the chat: renders, starts nothing, management tools hidden, MCP_SHARES=off")
def mcp_panel(c):
    alice = uid("alice")
    before = L.containers_of(alice)
    n0 = len(stub_log())
    page, txt, img = chat("alice", "@@call open_workspaces {}", shot="02_panel")
    d = done_text(txt)
    frame = page.frame_locator("iframe").first
    try:
        frame.get_by_text("Screen-share links").wait_for(timeout=20000)
        panel = frame.locator("body").inner_text().lower()
    except Exception as e:
        panel = "panel did not render: %s | page: %s" % (e, txt[-300:])
    c.ok("the model called open_workspaces and the panel renders inside the chat (iframe)", "workspaces" in panel and "home backups" in panel, panel[:300] + " " + str(img))
    c.ok("panel has sessions, home backups and screen-share links sections",
         all(x in panel for x in ("sessions", "home backups", "screen-share links", "workspaces you can use")), panel[:200])
    c.ok("panel lists the entitled workspaces", all(x in panel for x in ("desktop", "temporary desktop", "german desktop")), panel[:300])
    after = L.containers_of(alice)
    c.ok("opening the panel starts nothing (no container for the user)", len(after) == len(before) == 0, "before=%s after=%s" % (before, after))
    new = stub_log()[n0:]
    offered = sorted({o for r in new for o in r["offered"] if o.startswith("termiplus_")})
    c.ok("model is offered exactly one termiplus tool: open_workspaces (management tools are app-only)",
         offered == ["termiplus_open_workspaces"], offered)
    code, rpc = L.mcp_rpc("alice", S["ids"]["alice"], "tools/list")
    tools = {t["name"]: (t.get("_meta") or {}).get("ui", {}).get("visibility") for t in rpc["result"]["tools"]}
    c.ok("tools/list marks every management tool visibility=[app]",
         all(v == ["app"] for k, v in tools.items() if k != "open_workspaces") and tools.get("open_workspaces") == ["model", "app"], tools)
    # The model cannot call an app-only tool: it is not offered.
    page2, txt2, _ = chat("alice", "@@call reset_workspace {}", shot="02_hidden_tool")
    c.ok("a model script that calls reset_workspace gets 'not offered' (STUB-NOTOOL)", "STUB-NOTOOL" in txt2, done_text(txt2)[:200])
    page.close(); page2.close()
    # MCP_SHARES=off
    L.op("shares", "off")
    try:
        code, rpc = L.mcp_rpc("alice", S["ids"]["alice"], "tools/list")
        names = [t["name"] for t in rpc["result"]["tools"]]
        c.ok("MCP_SHARES=off: share_desktop is not listed", "share_desktop" not in names, names)
        page3, txt3, img3 = chat("alice", "@@call open_workspaces {}", shot="02_panel_shares_off")
        f3 = page3.frame_locator("iframe").first
        try:
            f3.get_by_text("Workspaces you can use").wait_for(timeout=20000)
            panel3 = f3.locator("body").inner_text().lower()
        except Exception as e:
            panel3 = "panel did not render: %s" % e
        c.ok("MCP_SHARES=off: the panel still renders", "workspaces you can use" in panel3, panel3[:300] + " " + str(img3))
        code, res = L.mcp("alice", S["ids"]["alice"], "open_workspaces")
        c.ok("MCP_SHARES=off: panel data says sharing=false and carries no errors",
             isinstance(res, dict) and res.get("sharing") is False and not res.get("errors"), str(res)[:300])
        code, res = L.mcp("alice", S["ids"]["alice"], "share_desktop", {"action": "list"})
        c.ok("MCP_SHARES=off: a direct share_desktop call is refused", code is not None, "%s %s" % (code, str(res)[:200]))
        page3.close()
    finally:
        L.op("shares", "on")


def panel_frame(page):
    f = page.frame_locator("iframe").first
    f.get_by_text("Screen-share links").wait_for(timeout=30000)
    # The host re-delivers the tool result after the first paint; let the panel settle.
    page.wait_for_timeout(3000)
    return f


def count_snaps(alice):
    code, sn = L.mcp("alice", alice, "manage_snapshots", {"action": "list", "workspace": "default"})
    return len(sn.get("snapshots", [])) if isinstance(sn, dict) else -1


def default_workspace(alice):
    """(session_id, container name, 'id started-at') of alice's running persistent default workspace."""
    code, res = L.mcp("alice", alice, "list_sessions")
    sid = next((x.get("session_id") for x in as_list(res, "sessions") if x.get("workspace") == "default" and x.get("persistent")), None)
    pc = [x for x in L.containers_of(alice) if x["policy"] == "default"]
    ident = L.sh("docker inspect -f '{{.Id}} {{.State.StartedAt}} {{.State.Running}}' %s" % pc[0]["name"])[1].strip() if pc else ""
    return sid, ident


def panel_wipe_button(page):
    return panel_frame(page).get_by_role("button", name=re.compile(r"reset \(wipe home\)", re.I))


@check("03a_wipe_home_default", "wipe_home with the shipped default (ALLOW_HOME_REMOVE off): not offered, refused up front, nothing changes")
def wipe_home_default(c):
    alice = uid("alice")
    out = L.run_in(alice, "default", "mkdir -p ~/e2e && echo precious > ~/e2e/keep.txt && cat ~/e2e/keep.txt")
    c.ok("test data written into alice's persistent home", "precious" in out, out[:200])
    n0 = count_snaps(alice)
    sid, ident0 = default_workspace(alice)
    c.ok("alice has a running persistent default workspace", bool(sid) and ident0.endswith("true"), "%s %s" % (sid, ident0))
    page, txt, _ = chat("alice", "@@call open_workspaces {}")
    f = panel_frame(page)
    f.get_by_role("button", name=re.compile(r"^restart$", re.I)).first.wait_for(timeout=20000)
    L.shot(page, "03a_panel_default")
    c.ok("the panel does not offer 'Reset (wipe home)'", panel_wipe_button(page).count() == 0, f.locator("body").inner_text()[:300])
    page.close()
    for attempt in (1, 2):
        code, res = L.mcp("alice", alice, "wipe_home", {"session_id": sid, "_confirmed": True})
        c.ok("a direct wipe_home call #%d gets the clear error" % attempt,
             code == "tool-error" and "factory reset is disabled" in str(res) and "ALLOW_HOME_REMOVE" in str(res) and "unavailable" not in str(res),
             "%s %s" % (code, str(res)[:300]))
    c.ok("no snapshot was added by the refused attempts", count_snaps(alice) == n0, "%s -> %s" % (n0, count_snaps(alice)))
    sid1, ident1 = default_workspace(alice)
    c.ok("no container was stopped or replaced (same id, same start time, still running)", ident1 == ident0, "%s -> %s" % (ident0, ident1))
    out = L.run_in(alice, "default", "cat ~/e2e/keep.txt")
    c.ok("the home is intact", "precious" in out, out[:200])


@check("03b_wipe_home_optin", "wipe_home with ALLOW_HOME_REMOVE=1: panel offers it, abort keeps data, confirm wipes with a pre-snapshot")
def wipe_home_optin(c):
    alice = uid("alice")
    L.op("home-remove", "on")
    try:
        L.run_in(alice, "default", "mkdir -p ~/e2e && echo precious > ~/e2e/keep.txt && cat ~/e2e/keep.txt")
        n0 = count_snaps(alice)
        page, txt, _ = chat("alice", "@@call open_workspaces {}")
        f = panel_frame(page)
        btn = panel_wipe_button(page)
        btn.wait_for(timeout=20000)
        c.ok("with the opt-in the panel offers 'Reset (wipe home)'", btn.count() == 1)
        L.shot(page, "03b_panel_with_session")
        # (a) arm, never confirm: the arm times out after 5 s
        btn.click()
        page.wait_for_timeout(500)
        c.ok("first click arms the button instead of acting", "Confirm" in btn.inner_text(), btn.inner_text())
        page.wait_for_timeout(6500)
        c.ok("an unconfirmed arm disarms itself", "Reset (wipe home)" in btn.inner_text(), btn.inner_text())
        out = L.run_in(alice, "default", "cat ~/e2e/keep.txt")
        c.ok("abort #1 (armed, not confirmed): data intact", "precious" in out, out[:200])
        # (b) arm, confirm in the panel, cancel in the host's dialog
        btn.click(); btn.click()
        page.wait_for_timeout(1500)
        L.shot(page, "03b_host_dialog")
        dialog_txt = page.locator("body").inner_text()
        cancelled = False
        for name in ("Cancel", "Deny", "Decline", "No", "Reject"):
            loc = page.get_by_role("button", name=re.compile("^%s$" % name, re.I))
            if loc.count():
                loc.first.click(); cancelled = True; c.note("host dialog answered with %s" % name); break
        c.ok("the host asks before running the app tool call (dialog with a cancel button)", cancelled, dialog_txt[-300:])
        page.wait_for_timeout(2000)
        out = L.run_in(alice, "default", "cat ~/e2e/keep.txt")
        c.ok("abort #2 (host dialog cancelled): data intact", "precious" in out, out[:200])
        n1 = count_snaps(alice)
        c.ok("no snapshot was taken by the aborts", n1 == n0, "%s -> %s" % (n0, n1))
        # (c) confirm everywhere
        btn.click(); btn.click()
        page.wait_for_timeout(1500)
        for name in ("Confirm", "Allow", "Run", "Yes", "OK", "Continue", "Approve"):
            loc = page.get_by_role("button", name=re.compile("^%s$" % name, re.I))
            if loc.count():
                loc.first.click(); break
        page.wait_for_timeout(15000)
        L.shot(page, "03b_after_wipe")
        status_txt = f.locator("#status").inner_text()
        out = L.run_in(alice, "default", "ls ~/e2e/keep.txt 2>&1; echo rc=$?")
        c.ok("with ALLOW_HOME_REMOVE=1 the confirmed wipe removes the data", "No such file" in out, "panel said %r; home: %s" % (status_txt, out[:100]))
        n2 = count_snaps(alice)
        c.ok("a pre-wipe snapshot was taken first", n2 == n1 + 1, "%s -> %s" % (n1, n2))
        page.close()
    finally:
        L.op("home-remove", "off")


@check("05_terminal", "Terminal: run_command, tp-status, ~/AGENTS.md, ~/.agents/skills, office conversion")
def terminal(c):
    script = "\n".join([
        run_cmd_script("echo E2E-$((6*7)); id -un"),
    ])
    page, txt, img = chat("alice", script, terminal="Desktop", tools=False, shot="05_run_command", ui_select=True)
    out = term_out(txt)
    c.ok("run_command through the Open WebUI terminal connection runs in the sandbox", "E2E-42" in out and "user" in out, out[:300] + " " + str(img))
    page.close()
    probes = {
        "tp-status": ("tp-status | head -3", "This workspace"),
        "AGENTS.md": ("head -5 ~/AGENTS.md", "termiplus"),
        "skills": ("ls ~/.agents/skills", "documents"),
    }
    for name, (cmd, expect) in probes.items():
        page, txt, _ = chat("alice", run_cmd_script(cmd), terminal="Desktop", tools=False)
        out = term_out(txt)
        c.ok("%s: output contains %r" % (name, expect), expect in out, out[:300])
        page.close()
    page, txt, img = chat("alice", run_cmd_script(
        "mkdir -p ~/outputs/conv && cd ~/outputs/conv && "
        "python3 - <<'PY'\nimport docx\nd=docx.Document(); d.add_heading('E2E conversion',1); d.add_paragraph('termiplus docx to pdf marker 7731'); d.save('t.docx')\nPY\n"
        "printf '<html><body><h1>odt marker</h1><p>termiplus odt to pdf marker 9922</p></body></html>' > h.html && "
        "soffice --headless --convert-to odt h.html >/dev/null 2>&1; "
        "soffice --headless --convert-to pdf t.docx >/dev/null 2>&1; soffice --headless --convert-to pdf h.odt >/dev/null 2>&1; "
        "pdftotext t.pdf - | grep -c 7731; pdftotext h.pdf - | grep -c 9922; file t.pdf h.pdf | cut -c1-60", wait=240),
        terminal="Desktop", tools=False, timeout=300, shot="05_office")
    out = term_out(txt)
    c.ok("docx and odt convert to real PDFs with their text (soffice + pdftotext)",
         out.count("PDF document") == 2 and "1" in out, out[:400] + " " + str(img))
    page.close()


@check("06_sessions", "Sessions: list/request/destroy, ephemeral isolation, persistence across a restart")
def sessions(c):
    alice = uid("alice")
    code, res = L.mcp("alice", alice, "list_workspaces")
    names = sorted(w["name"] for w in as_list(res, "workspaces"))
    c.ok("list_workspaces returns the entitled workspaces", names == ["default", "i18n", "scratch"], res if not names else names)
    code, res = L.mcp("alice", alice, "request_session", {"workspace": "scratch"})
    sid = res.get("session_id") if isinstance(res, dict) else None
    c.ok("request_session (temporary scratch) returns a session", bool(sid) and code is None, "%s %s" % (code, str(res)[:300]))
    code, res = L.mcp("alice", alice, "list_sessions")
    listed = [s.get("session_id") for s in as_list(res, "sessions")]
    c.ok("list_sessions shows the new session", sid in listed, listed)
    if sid:
        code, res = L.mcp("alice", alice, "destroy_session", {"session_id": sid, "_confirmed": True})
        c.ok("destroy_session removes it", code is None, "%s %s" % (code, str(res)[:200]))
        code, res = L.mcp("alice", alice, "list_sessions")
        listed = [s.get("session_id") for s in as_list(res, "sessions")]
        c.ok("list_sessions no longer shows it", sid not in listed, listed)
    # Ephemeral isolation: two saved chats on the temporary desktop.
    p1, t1, _ = chat("alice", run_cmd_script("echo chat-one > /tmp/iso.txt; hostname; cat /tmp/iso.txt"), terminal="Temporary desktop", tools=False, shot="06_iso_chat1")
    p2, t2, _ = chat("alice", run_cmd_script("hostname; cat /tmp/iso.txt 2>&1; echo end"), terminal="Temporary desktop", tools=False, shot="06_iso_chat2")
    o1, o2 = term_out(t1), term_out(t2)
    c.ok("chat 1 wrote its own file", "chat-one" in o1, o1[:200])
    c.ok("chat 2 on the same connection does not see chat 1's file", "No such file" in o2 and "chat-one" not in o2, o2[:300])
    eph = [x for x in L.containers_of(alice) if x["policy"] == "scratch"]
    c.ok("two separate ephemeral containers exist for the two chats", len(eph) >= 2, eph)
    p1.close(); p2.close()
    # Persistence of the persistent workspace across a container restart.
    out = L.run_in(alice, "default", "echo survive-7 > ~/persist.txt; cat ~/persist.txt")
    c.ok("wrote a file in the persistent home", "survive-7" in out, out[:100])
    pc = [x for x in L.containers_of(alice) if x["policy"] == "default"]
    cid0 = L.sh("docker inspect -f '{{.Id}}' %s" % pc[0]["name"])[1] if pc else ""
    L.sh("docker rm -f %s" % pc[0]["name"] if pc else "true")
    L.op("gateway-restart")
    page, txt, _ = chat("alice", run_cmd_script("cat ~/persist.txt; hostname"), terminal="Desktop", tools=False, shot="06_persist")
    out = term_out(txt)
    pc2 = [x for x in L.containers_of(alice) if x["policy"] == "default"]
    cid1 = L.sh("docker inspect -f '{{.Id}}' %s" % pc2[0]["name"])[1] if pc2 else ""
    c.ok("after the container and the gateway restarted the file is still there", "survive-7" in out, out[:300])
    c.ok("it ran in a new container", cid0 and cid1 and cid0 != cid1, "%s -> %s" % (cid0[:12], cid1[:12]))
    page.close()


@check("08_snapshots_reset", "Snapshots and reset: manage_snapshots, reset_workspace")
def snapshots(c):
    alice = uid("alice")
    L.run_in(alice, "default", "echo snapdata > ~/snap.txt")
    code, before = L.mcp("alice", alice, "manage_snapshots", {"action": "list", "workspace": "default"})
    nb = len(before.get("snapshots", [])) if isinstance(before, dict) else -1
    freed = clear_snapshots("alice")
    c.note("freed %d earlier snapshot(s) through the console" % freed)
    code, before = L.mcp("alice", alice, "manage_snapshots", {"action": "list", "workspace": "default"})
    nb = len(before.get("snapshots", [])) if isinstance(before, dict) else -1
    page, txt, img = chat("alice", '@@call manage_snapshots {"action":"create","workspace":"default"}', shot="08_model_snapshot")
    c.ok("manage_snapshots is not offered to the model (app-only; the panel button drives it)", "STUB-NOTOOL" in txt, done_text(txt)[:120])
    page.close()
    code, res = L.mcp("alice", alice, "manage_snapshots", {"action": "create", "workspace": "default"})
    c.ok("create over MCP (as the panel button's tool call) works", code is None, "%s %s" % (code, str(res)[:200]))
    code, after = L.mcp("alice", alice, "manage_snapshots", {"action": "list", "workspace": "default"})
    na = len(after.get("snapshots", [])) if isinstance(after, dict) else -1
    c.ok("manage_snapshots list shows one more snapshot", na == nb + 1, "%s -> %s" % (nb, na))
    code, res = L.mcp("alice", alice, "manage_snapshots", {"action": "restore", "workspace": "default", "snapshot_id": "x"})
    c.ok("a model-originated restore is refused by the server (human console only)", code is not None or False, "%s %s" % (code, str(res)[:200]))
    code, res = L.mcp("alice", alice, "manage_snapshots", {"action": "delete", "workspace": "default"})
    c.ok("a forged direct 'delete' call is refused too", code is not None, "%s %s" % (code, str(res)[:200]))
    # Panel: Back up now and Restart.
    page, txt, _ = chat("alice", "@@call open_workspaces {}")
    f = panel_frame(page)
    f.get_by_role("button", name=re.compile("Back up now")).click()
    dlg = host_dialog(page)
    c.ok("the host asks before the panel's app tool call (Tool call dialog naming manage_snapshots)", "manage_snapshots" in dlg, dlg[-200:])
    page.wait_for_timeout(8000)
    code, after2 = L.mcp("alice", alice, "manage_snapshots", {"action": "list", "workspace": "default"})
    na2 = len(after2.get("snapshots", [])) if isinstance(after2, dict) else -1
    c.ok("panel 'Back up now' creates a snapshot (host asked, panel refreshed)", na2 >= na + 1 and L.shot(page, "08_panel_backup"), "%s -> %s" % (na, na2))
    pc = [x for x in L.containers_of(alice) if x["policy"] == "default"]
    cid0 = L.sh("docker inspect -f '{{.Id}}' %s" % pc[0]["name"])[1] if pc else ""
    page.close()
    code, res = L.mcp("alice", alice, "list_sessions")
    sess = [s for s in as_list(res, "sessions") if s.get("workspace") == "default"]
    sid = sess[0]["session_id"] if sess else None
    code, res = L.mcp("alice", alice, "reset_workspace", {"session_id": sid})
    c.ok("reset_workspace asks for confirmation first (-32010)", code == -32010, "%s %s" % (code, res))
    code, res = L.mcp("alice", alice, "reset_workspace", {"session_id": sid, "_confirmed": True})
    pc = [x for x in L.containers_of(alice) if x["policy"] == "default"]
    cid1 = L.sh("docker inspect -f '{{.Id}}' %s" % pc[0]["name"])[1] if pc else ""
    out = L.run_in(alice, "default", "cat ~/snap.txt")
    c.ok("reset_workspace gives a fresh container with the home kept",
         code is None and cid1 != cid0 and "snapdata" in out, "%s %s -> %s data=%s" % (code, cid0[:12], cid1[:12], out[:50]))


@check("09_console", "Human console at open_console")
def console(c):
    page, txt, img = chat("alice", "@@call open_console {}", tools=False, extra_tools=["termiplus_workspaces"], shot="09_open_console")
    hrefs = page.evaluate("[...document.querySelectorAll('#messages-container a')].map(a => a.href)")
    url = [h for h in hrefs if "desk.test/ui" in h]
    c.ok("the plugin's open_console returns the console URL (a link in the chat)", bool(url), "%s %s" % (hrefs[:4], done_text(txt)[:200]))
    page.close()
    ctx, p0, _ = login("alice")
    cp = ctx.browser.new_context(ignore_https_errors=True, viewport={"width": 1400, "height": 1000}, locale="en-US")
    page = cp.new_page()
    page.goto("https://desk.test/ui/")
    page.wait_for_url("**/realms/termiplus/**", timeout=30000)
    page.fill("#username", "alice"); page.fill("#password", L.PASSWORDS["alice"]); page.click("#kc-login")
    page.wait_for_url("https://desk.test/**", timeout=60000)
    page.wait_for_timeout(4000)
    img = L.shot(page, "09_console")
    body = page.locator("body").inner_text()
    c.ok("the console opens after SSO and shows alice's workspaces/sessions", "Termiplus" in body or "termiplus" in body or "Workspace" in body, body[:300] + " " + str(img))
    code, d, _ = L.http("GET", "https://desk.test/ui/api/me", None, {"Cookie": "; ".join("%s=%s" % (k["name"], k["value"]) for k in cp.cookies())}, raw=True)
    c.ok("console links alice to her Open WebUI id", b'"linked":true' in d.replace(b" ", b""), d[:200])
    cp.close()


@check("10_normal_user", "Normal user: no persist/admin rights; the server refuses, not just the UI")
def normal_user(c):
    bob, alice, root = uid("bob"), uid("alice"), uid("root")
    code, res = L.mcp("bob", bob, "request_session", {"workspace": "default", "persistent": True, "_confirmed": True})
    c.ok("bob requesting a persistent session is forbidden (-32003)", code == -32003, "%s %s" % (code, res))
    for tool, args in (("set_entitlements", {"default": "allow", "workspaces": {}}), ("get_audit_log", {}), ("get_network_policy", {}),
                       ("manage_user", {"action": "list"}), ("manage_group", {"action": "list"})):
        code, res = L.mcp("bob", bob, tool, args)
        c.ok("bob calling admin tool %s is refused" % tool, code in (-32003, -32602, -32601, -32000) or code == "tool-error", "%s %s" % (code, str(res)[:150]))
    code, res = L.mcp("alice", alice, "set_entitlements", {"default": "allow", "workspaces": {}})
    c.ok("alice (persist, not admin) cannot set entitlements either", code is not None, "%s %s" % (code, str(res)[:150]))
    code, res = L.mcp("root", root, "get_audit_log", {})
    c.ok("admin tools are not reachable over MCP even for the admin (human console only)", code == -32003, "%s %s" % (code, str(res)[:120]))
    same = {"default": "allow", "workspaces": {}}
    code, text = console_api("root", "PUT", "admin/entitlements", same)
    c.ok("control: the admin's console session can save entitlements (200)", code == 200, "%s %s" % (code, text[:150]))
    for user in ("bob", "alice"):
        code, text = console_api(user, "PUT", "admin/entitlements", same)
        c.ok("%s's console session cannot save entitlements: the server answers 403" % user, code == 403, "%s %s" % (code, text[:150]))
        code, text = console_api(user, "GET", "admin/audit")
        c.ok("%s's console session cannot read the audit log (403)" % user, code == 403, "%s %s" % (code, text[:150]))
    code, text = console_api("bob", "POST", "sessions", {"workspace": "default", "persistent": True})
    c.ok("bob cannot request a persistent session through the console either (403)", code == 403, "%s %s" % (code, text[:150]))
    # The model route as bob: only the entry tool is offered, panel loads.
    n0 = len(stub_log())
    page, txt, img = chat("bob", "@@call open_workspaces {}", shot="10_bob_panel")
    offered = sorted({o for r in stub_log()[n0:] for o in r["offered"] if o.startswith("termiplus_")})
    c.ok("bob's chat offers only open_workspaces", offered == ["termiplus_open_workspaces"], offered)
    c.ok("bob's panel call works", "STUB-DONE" in txt, done_text(txt)[:200])
    page.close()
    # Without the plugin key a token alone is not accepted as an Open WebUI identity.
    code, res = L.mcp("bob", bob, "list_sessions", key=False)
    c.ok("a valid token without the plugin key cannot name an Open WebUI user", code is not None or (isinstance(res, dict) and False), "%s %s" % (code, str(res)[:150]))


@check("11_policies", "Policies: locale, keyboard, record take effect; invalid values are refused")
def policies(c):
    alice = uid("alice")
    page, txt, img = chat("alice", run_cmd_script("echo LANG=$LANG; locale 2>&1 | head -2"), terminal="German desktop", tools=False, shot="11_locale")
    out = term_out(txt)
    c.ok("locale de_DE.UTF-8 is applied (LANG)", "LANG=de_DE.UTF-8" in out, out[:300])
    page.close()
    page, txt, _ = chat("alice", '@@call desktop_screenshot {}\n' + run_cmd_script(
        "sleep 6; p=$(pgrep -f xfce4-session | head -1); export $(tr '\\0' '\\n' < /proc/$p/environ | grep -E '^(DISPLAY|XAUTHORITY)=' | xargs); setxkbmap -query 2>&1 | head -8; ls ~/recordings 2>&1 | head -3; pgrep -a ffmpeg | head -2"),
        terminal="German desktop", tools=False, timeout=200, shot="11_desktop")
    out = term_out(txt)
    c.ok("keyboard layout de is applied to the desktop (setxkbmap -query)", re.search(r"layout:\s+de", out) is not None, out[:400])
    c.ok("record: the desktop recording exists in ~/recordings", ".mkv" in out or "ffmpeg" in out, out[:400])
    page.close()
    # Invalid values: the gateway refuses to start, with a message naming the policy and field.
    bad = {
        "locale": ('{"default":{"runtime":"%s","memory_mb":512,"cpus":1,"pids":256},"x":{"runtime":"%s","memory_mb":512,"cpus":1,"pids":256,"locale":"de_DE"}}', "locale must look like"),
        "keyboard": ('{"default":{"runtime":"%s","memory_mb":512,"cpus":1,"pids":256},"x":{"runtime":"%s","memory_mb":512,"cpus":1,"pids":256,"desktop":true,"keyboard":"DE;x"}}', "keyboard must be"),
        "record without desktop": ('{"default":{"runtime":"%s","memory_mb":512,"cpus":1,"pids":256},"x":{"runtime":"%s","memory_mb":512,"cpus":1,"pids":256,"record":true}}', "need desktop"),
    }
    import os
    rt = os.environ.get("RUNTIME", "sysbox-runc")
    for name, (doc, expect) in bad.items():
        path = "%s/bad-policies.json" % L.OUT
        open(path, "w").write(doc % (rt, rt))
        rc, out = L.sh(["docker", "run", "--rm", "--network", "none", "-v", "%s:/p.json:ro" % path.replace(L.OUT, os.environ.get("OWUI_E2E_STATE", "/var/tmp/owui-e2e") + "/artifacts"),
                        "-e", "POLICIES_FILE=/p.json", "-e", "GATEWAY_API_KEY=" + "a" * 40, "-e", "KEY_SECRET=" + "b" * 40,
                        "-e", "TERMINAL_IMAGE=x", "ghcr.io/leon2003m/termiplus-gateway:it"])
        c.ok("invalid %s is refused at startup with a clear message" % name, rc != 0 and expect in out, "rc=%s %s" % (rc, out[-200:]))


@check("12_firewall", "Firewall allow/deny from a workspace (inside the VM only)")
def firewall(c):
    alice = uid("alice")
    page, txt, img = chat("alice", run_cmd_script(
        "echo PUBLIC=$(curl -s -m 10 -o /dev/null -w '%{http_code}' https://example.com); "
        "echo OWUI=$(curl -s -m 5 -o /dev/null -w '%{http_code}' http://open-webui:8080/health); "
        "echo STUB=$(curl -s -m 5 -o /dev/null -w '%{http_code}' http://stub:9000/); "
        "echo GATEWAYIP=$(curl -s -m 5 -o /dev/null -w '%{http_code}' http://172.30.0.2:8080/health); "
        "echo KEYCLOAK=$(curl -s -m 5 -o /dev/null -w '%{http_code}' http://keycloak:8080/); "
        "echo HOSTBR=$(curl -s -m 5 -o /dev/null -w '%{http_code}' http://172.30.0.1:22/)", wait=120),
        terminal="Desktop", tools=False, shot="12_firewall")
    out = term_out(txt)
    m = dict(re.findall(r"(\w+)=(\d+)", out))
    c.ok("allow: a public destination is reachable from the workspace", m.get("PUBLIC", "000") not in ("000",), out[:300])
    for name in ("OWUI", "STUB", "KEYCLOAK"):
        c.ok("deny: %s on the Open WebUI network is unreachable (000)" % name, m.get(name) == "000", out[:300])
    c.ok("deny: the sandbox bridge's host address is unreachable", m.get("HOSTBR") == "000", out[:300])
    c.ok("deny: the gateway's sandbox address does not serve the workspace", m.get("GATEWAYIP") in ("000", "401", "403"), out[:300])
    page.close()


@check("07_desktop_tab", "Open WebUI desktop tab: screenshot, file browser, app/port preview, Kate")
def desktop_tab(c):
    alice = uid("alice")
    page, txt, img = chat("alice", run_cmd_script("echo seed > ~/e2e-seed.txt; echo ok"), terminal="Desktop", tools=False)
    if not page.get_by_role("button", name="New tab").count():
        page.get_by_role("button", name="Tabs").first.click()
    try:
        page.get_by_text("AGENTS.md").first.wait_for(timeout=20000)
    except Exception:
        pass
    page.wait_for_timeout(1500)
    body = page.locator("body").inner_text()
    c.ok("file browser tab lists the home directory (AGENTS.md, e2e-seed.txt)", "AGENTS.md" in body and "e2e-seed.txt" in body, L.shot(page, "07_files") + " " + body[-200:])
    page.get_by_role("button", name="New tab").click()
    page.wait_for_timeout(600)
    page.get_by_text("Desktop", exact=True).last.click()
    canvas = page.locator("canvas").first
    try:
        canvas.wait_for(timeout=60000)
        page.wait_for_timeout(8000)
        png = canvas.screenshot()
        import zlib
        distinct = len(set(png[i:i + 3] for i in range(100, min(len(png), 20000), 3)))
        c.ok("Desktop tab shows a live desktop (canvas, non-blank)", len(png) > 3000 and distinct > 50, "png=%d bytes %s" % (len(png), L.shot(page, "07_desktop")))
    except Exception as e:
        c.ok("Desktop tab shows a live desktop (canvas, non-blank)", False, "%s %s" % (e, L.shot(page, "07_desktop_fail")))
    # Kate launches in the shared desktop.
    out = L.run_in(alice, "default", "p=$(pgrep -f xfce4-session | head -1); export $(tr '\\0' '\\n' < /proc/$p/environ | grep -E '^(DISPLAY|XAUTHORITY)=' | xargs); (nohup kate >/tmp/kate.log 2>&1 &); sleep 10; pgrep -a kate | head -2; tail -3 /tmp/kate.log")
    c.ok("Kate launches on the desktop and keeps running", "kate" in out, out[:300])
    page.wait_for_timeout(3000)
    c.ok("screenshot with Kate open", bool(L.shot(page, "07_desktop_kate")), "")
    page.close()
    # App / port preview through Open WebUI's terminal proxy.
    L.run_in(alice, "default", "pkill -f 'http.server 301[7]'; echo preview-ok > /tmp/index.html; echo ready")
    # As a model's run_command would: the app is a child of Open Terminal's process, so /ports finds it.
    L.native(alice, "default", "execute?wait=0", "POST", {"command": "cd /tmp && python3 -m http.server 3017 --bind 127.0.0.1"}, session="saved-chat")
    time.sleep(4)
    _, tok = fresh_page("alice")
    code, d = L.owui("GET", "/api/v1/terminals/tp-default/ports", None, tok)
    c.ok("Open WebUI's /ports lists the running app", code == 200 and "3017" in json.dumps(d), "%s %s" % (code, str(d)[:200]))
    code, d, _ = L.http("GET", L.CHAT + "/api/v1/terminals/tp-default/proxy/3017/", None, {"Authorization": "Bearer " + tok}, raw=True)
    c.ok("app preview through Open WebUI's proxy returns the page", code == 200 and b"preview-ok" in d, "%s %s" % (code, d[:150]))


def cold_run(c, timeout_s, stress=False):
    """Restart every service as after a host reboot (no workspace containers), then the
    first chat of a user with a terminal. Returns a dict of what was observed."""
    import datetime
    t_start = time.time()
    since = datetime.datetime.fromtimestamp(t_start - 2, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S")
    L.op("stack-restart", env={"OWUI_TOOL_TIMEOUT": str(timeout_s), "COLD_STRESS": "1" if stress else "0"})
    t_up = time.time()
    # The first user signs in (Keycloak may still be settling) and starts a chat.
    for _ in range(120):  # Keycloak is a JVM: it may still be starting
        if L.http("GET", L.SSO + "/realms/termiplus/.well-known/openid-configuration")[0] == 200:
            break
        time.sleep(2)
    t_kc = time.time()
    for attempt in range(3):
        try:
            ctx, page0, tok = L.sso_login(S["browser"], "alice", timeout=240)
            break
        except Exception:
            if attempt == 2:
                raise
            time.sleep(5)
    t_login = time.time()
    page = ctx.new_page()
    page.goto(L.CHAT + "/?models=stub")
    page.wait_for_selector("#chat-input", timeout=60000)
    page.evaluate("localStorage.setItem('selectedTerminalId','tp-default')")
    page.goto(L.CHAT + "/?models=stub")
    page.wait_for_selector("#chat-input", timeout=60000)
    page.wait_for_timeout(800)
    t_chat0 = time.time()
    txt = L.send(page, run_cmd_script("echo COLD-OK-$((20+22))"), timeout=150)
    t_done = time.time()
    img = L.shot(page, "04_cold_first_chat_t%d%s" % (timeout_s, "_stress" if stress else ""))
    gw = L.sh("docker logs -t --since %s %s-gateway-1 2>&1 | grep -E 'created|started|ensure|proxy|no longer' | tail -12" % (since, L.PROJECT))[1]
    ow = L.sh("docker logs -t --since %s tp-owui 2>&1 | grep -i -E 'terminal|timed out|Timeout|spec' | grep -v 'version.json' | tail -12" % since)[1]
    open("%s/cold_t%d%s.log" % (L.OUT, timeout_s, "_stress" if stress else ""), "w").write("gateway:\n%s\n\nopen-webui:\n%s\n" % (gw, ow))
    ctx.close()
    ok = "COLD-OK-42" in txt
    bad = re.findall(r"[^\n]*(?:unavailable|no available tools|not found|Failed|failed)[^\n]*", txt)
    return {"ok": ok, "bad": bad[:3], "stack_up_s": round(t_up - t_start, 1), "login_s": round(t_login - t_up, 1), "keycloak_ready_s": round(t_kc - t_up, 1),
            "first_chat_s": round(t_done - t_chat0, 1), "img": img, "gateway": gw, "owui": ow, "tail": done_text(txt)[:200]}


@check("04_cold_start", "First chat after a cold stack: no 'Terminal unavailable'; spec-sandbox timing")
def cold_start(c):
    res = {}
    for t, stress in ((10, False), (60, False), (10, True), (60, True)):
        label = "%ds%s" % (t, ", cold cache + busy CPUs" if stress else "")
        res[(t, stress)] = cold_run(c, t, stress)
        r = res[(t, stress)]
        c.note("[%s] stack restart %.1fs, SSO login %.1fs (Keycloak ready after %.1fs), first chat with terminal %.1fs, ok=%s, errors=%s" % (
            label, r["stack_up_s"], r["login_s"], r["keycloak_ready_s"], r["first_chat_s"], r["ok"], r["bad"]))
        c.note("  gateway: %s" % r["gateway"].replace("\n", " | ")[:700])
        c.note("  open-webui: %s" % r["owui"].replace("\n", " | ")[:700])
        c.ok("[%s] the first chat after a cold stack starts its terminal (no 'Terminal unavailable')" % label,
             r["ok"] and not r["bad"], "first chat %.1fs; %s %s" % (r["first_chat_s"], r["tail"], r["img"]))
        if stress:
            time.sleep(60)  # let the burners end before the next run
    S["ctx-alice"] = None
    S.pop("ctx-alice")
    S.pop("con-alice", None)
    # Leave the stack as the other checks expect it (60 s).


@check("13_mcp_transport", "MCP responses through oauth2-proxy arrive complete (backs the panel's 'MCP app request failed')")
def mcp_transport(c):
    import http.client as hc
    tok = L.kc_token("alice")
    ids = uid("alice")
    bad, n, kinds = 0, 3000, {}
    for i in range(n):
        try:
            con = hc.HTTPConnection("mcp-auth", 4180, timeout=30)
            con.request("POST", "/", json.dumps({"jsonrpc": "2.0", "id": 1, "method": "resources/read", "params": {"uri": "ui://termiplus/workspaces"}}),
                        {"Authorization": "Bearer " + tok, "X-Termiplus-Owui-Key": L.PLUGIN_KEY, "X-Termiplus-Owui-User": ids, "Content-Type": "application/json"})
            r = con.getresponse()
            body = r.read()
            json.loads(body)
            if i == 0:
                c.note("resources/read: HTTP %s, %s bytes, Content-Length=%s, Transfer-Encoding=%s" % (r.status, len(body), r.getheader("Content-Length"), r.getheader("Transfer-Encoding")))
        except Exception as e:
            bad += 1
            kinds[type(e).__name__] = kinds.get(type(e).__name__, 0) + 1
    c.ok("%d sequential resources/read calls (16 KB) through mcp-auth all arrive complete" % n, bad == 0,
         "%d of %d truncated %s; the proxy logs 'ReverseProxy read error during body copy: use of closed network connection'" % (bad, n, kinds))


def main():
    want = sys.argv[1:] or ORDER
    import os
    os.makedirs(L.OUT + "/shots", exist_ok=True)
    with sync_playwright() as p:
        S["browser"] = p.chromium.launch(args=["--no-sandbox"])
        try:
            if "00_bootstrap" not in want and "admin" not in S:
                S["admin"] = L.admin_token()
            for cid in ORDER:
                if cid in want or (cid == "00_bootstrap" and cid not in want and False):
                    run_check(cid)
        finally:
            S["browser"].close()
    print("\n==== summary")
    for r in RESULTS:
        print("%-8s %s" % (r["status"], r["title"]))
    return 1 if any(r["status"] == "FAIL" for r in RESULTS) else 0


if __name__ == "__main__":
    sys.exit(main())
