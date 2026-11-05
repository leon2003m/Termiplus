"""Independent protocol checks; run inside the throwaway desktop container."""
import asyncio
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET

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
check(int.from_bytes(data[16:20], "big") == 1280 and int.from_bytes(data[20:24], "big") == 800,
      "native Linux screenshot matches the 1280x800 coordinate guidance")
check(all(shutil.which(app) for app in ("dolphin", "kate", "chromium", "xfce4-terminal", "btop")),
      "desktop applications installed")
check(not any(shutil.which(app) for app in ("firefox", "firefox-esr", "ristretto", "magick", "convert", "htop")),
      "redundant browser, image applications and htop omitted")
check(Path("/usr/share/backgrounds/termiplus/default.svg").is_file(), "Termiplus wallpaper installed")
# Inspect the actual native desktop session's display and D-Bus environment.
session_env = None
for process in Path("/proc").iterdir():
    if not process.name.isdigit():
        continue
    try:
        if (process / "comm").read_text().strip() == "xfdesktop":
            session_env = dict(part.split("=", 1) for part in
                               (process / "environ").read_bytes().decode().split("\0") if "=" in part)
            break
    except (OSError, UnicodeDecodeError):
        continue
check(session_env is not None, "native XFCE session running")
wallpapers = subprocess.check_output(["xfconf-query", "-c", "xfce4-desktop", "-lv"],
                                    env=session_env, text=True)
check("/usr/share/backgrounds/termiplus/default.svg" in wallpapers, "native desktop selects Termiplus wallpaper")
check(subprocess.check_output(["xdg-mime", "query", "default", "inode/directory"],
                              env=session_env, text=True).strip() == "org.kde.dolphin.desktop",
      "folders open in Dolphin")
check(subprocess.check_output(["xdg-mime", "query", "default", "x-scheme-handler/https"],
                              env=session_env, text=True).strip() == "chromium.desktop",
      "web links open in Chromium")
panel = subprocess.check_output(["xfconf-query", "-c", "xfce4-panel", "-lv"],
                                env=session_env, text=True)
check("whiskermenu" in panel and "chromium.desktop" in panel and " actions" not in panel,
      "native session uses configured dock without logout actions")
for app in ("texdoctk", "xfce4-mail-reader", "xfce4-session-logout"):
    check("Hidden=true" in Path("/usr/local/share/applications/" + app + ".desktop").read_text(),
          app + " omitted from application menu")
# Exercise the desktop default browser, separately from native browser automation.
with tempfile.TemporaryDirectory() as work:
    page = Path(work) / "desktop-browser.html"
    title = "Termiplus Chromium desktop smoke"
    page.write_text("<title>" + title + "</title><h1>Shared desktop browser works</h1>")
    subprocess.run(["exo-open", "--launch", "WebBrowser", page.as_uri()], env=session_env,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True, timeout=20)
    window = subprocess.check_output(["xdotool", "search", "--sync", "--onlyvisible", "--name", title],
                                     env=session_env, text=True, timeout=25).splitlines()[0]
    check(bool(window), "default Chromium renders a page in the shared desktop")
    subprocess.run(["xdotool", "windowclose", window], env=session_env, check=True)
shortcuts = subprocess.check_output(["xfconf-query", "-c", "xfce4-keyboard-shortcuts", "-lv"],
                                    env=session_env, text=True)
check("xfce4-session-logout" not in shortcuts and "xflock4" not in shortcuts,
      "logout and lock shortcuts disabled in running XFCE")
check(all(shutil.which(app) for app in (
    "docker", "cmake", "ruby", "lua5.4", "pandoc", "pdftotext", "pdflatex",
    "libreoffice", "tmux", "screen", "btop", "strace", "iostat", "netstat", "nc",
    "socat", "telnet", "man", "xmlstarlet", "zstd", "7z", "gawk", "vim")),
    "standard image workspace commands installed")
subprocess.run(["python3", "-c", "import numpy, pandas, scipy, sklearn, matplotlib, seaborn, plotly; "
                "import requests, bs4, lxml, sqlalchemy, psycopg2, yaml, toml, jsonlines, tqdm, rich; "
                "import openpyxl, weasyprint, docx, pptx, pypdf, csvkit, ipykernel"], check=True)
check(True, "standard Python libraries available to user commands")
check(Path("/proc/1/comm").read_text().strip() == "tini", "tini reaps workspace child processes")
system_prompt = json.loads(request("/system")[1])["prompt"]
check("For click, double_click and move, supply x and y" in system_prompt,
      "native model instructions explain action coordinates")
check("graphical XFCE desktop shared with the user" in system_prompt,
      "native model context announces shared graphical desktop")

# An existing home keeps its layout while obsolete launchers and logout controls
# are migrated. Repeat the migration to check later desktop restarts are safe.
spec = importlib.util.spec_from_file_location("defaults", "/usr/local/libexec/termiplus/desktop-defaults.py")
defaults = importlib.util.module_from_spec(spec)
spec.loader.exec_module(defaults)
with tempfile.TemporaryDirectory() as work:
    config = Path(work)
    channels = config / "xfce4/xfconf/xfce-perchannel-xml"
    channels.mkdir(parents=True)
    (channels / "xfce4-panel.xml").write_text('''<channel name="xfce4-panel" version="1.0">
      <property name="panels"><property name="panel-1"><property name="size" value="37"/>
        <property name="plugin-ids"><value value="1"/><value value="2"/></property>
      </property></property><property name="plugins"><property name="plugin-1" value="launcher">
        <property name="items"><value value="firefox-esr.desktop"/></property></property>
        <property name="plugin-2" value="actions"/></property></channel>''')
    launcher = config / "xfce4/panel/launcher-1/old.desktop"
    launcher.parent.mkdir(parents=True)
    launcher.write_text("[Desktop Entry]\nExec=exo-open --launch MailReader\n")
    (config / "xfce4/helpers.rc").write_text("WebBrowser=firefox\nCustom=keep\n")
    (channels / "xfce4-keyboard-shortcuts.xml").write_text('''<channel name="xfce4-keyboard-shortcuts">
      <property name="commands"><property name="default">
      <property name="&lt;Primary&gt;&lt;Alt&gt;Delete" type="empty"/>
      </property></property></channel>''')
    for _ in range(2):
        defaults.defaults(config)
    panel = (channels / "xfce4-panel.xml").read_text()
    check('value="37"' in panel and 'value="actions"' not in panel and "chromium.desktop" in panel,
          "saved panel layout preserved while old browser/logout controls are migrated")
    check("Custom=keep" in (config / "xfce4/helpers.rc").read_text() and "Exec=libreoffice" in launcher.read_text(),
          "saved mail launcher repaired and unrelated helper preferences retained")
    saved = ET.parse(channels / "xfce4-keyboard-shortcuts.xml")
    check(all(prop.get("type") == "string" and prop.get("value") == ""
              for group in ("default", "custom")
              for prop in saved.findall("./property[@name='commands']/property[@name='" + group + "']/property")
              if prop.get("name") == "<Primary><Alt>Delete"),
          "inherited saved logout shortcut explicitly disabled")

with tempfile.TemporaryDirectory() as work:
    config = Path(work)
    channels = config / "xfce4/xfconf/xfce-perchannel-xml"
    channels.mkdir(parents=True)
    shortcuts = channels / "xfce4-keyboard-shortcuts.xml"
    shortcuts.write_text('''<channel name="xfce4-keyboard-shortcuts">
      <property name="commands"><property name="custom">
      <property name="&lt;Primary&gt;&lt;Alt&gt;t" type="string" value="my-terminal"/>
      </property></property></channel>''')
    for _ in range(2):
        defaults.defaults(config)
    saved = ET.parse(shortcuts)
    for group in ("default", "custom"):
        bindings = {p.get("name"): (p.get("type"), p.get("value")) for p in saved.findall(
            "./property[@name='commands']/property[@name='" + group + "']/property")}
        check(all(bindings.get(key) == ("string", "")
                  for key in ("<Primary><Alt>Delete", "<Primary><Alt>l", "XF86Mail")),
              "partial saved shortcuts suppress inherited session/mail bindings in " + group)
    check(bindings["<Primary><Alt>t"] == ("string", "my-terminal"),
          "unrelated saved keyboard shortcut retained")

with tempfile.TemporaryDirectory() as work:
    config = Path(work)
    channels = config / "xfce4/xfconf/xfce-perchannel-xml"
    channels.mkdir(parents=True)
    broken = {config / "mimeapps.list": b"[DEFAULT]\nmalformed-inherited=must-not-survive\n[Default Applications\ntext/html=old.desktop;\n",
              channels / "xfce4-panel.xml": b'<channel name="xfce4-panel"><property',
              channels / "xfce4-keyboard-shortcuts.xml": b'<channel name="xfce4-keyboard-shortcuts">'}
    for path, original in broken.items():
        path.write_bytes(original)
    for _ in range(2):
        defaults.defaults(config)
    for path, original in broken.items():
        backups = list(path.parent.glob(path.name + ".invalid-*"))
        check(len(backups) == 1 and backups[0].read_bytes() == original,
              "invalid " + path.name + " preserved once without blocking desktop migration")
    ET.parse(channels / "xfce4-panel.xml")
    ET.parse(channels / "xfce4-keyboard-shortcuts.xml")
    recovered = (config / "mimeapps.list").read_text()
    check("chromium.desktop;" in recovered and "must-not-survive" not in recovered,
          "invalid MIME settings replaced with usable browser defaults")

# Reused container PIDs must not preserve abandoned Chromium profile locks.
spec = importlib.util.spec_from_file_location("prepare", "/usr/local/libexec/termiplus/prepare-desktop.py")
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)
with tempfile.TemporaryDirectory() as work:
    root = Path(work)
    for name in ("abandoned", "active", "unmanaged"):
        profile = root / name
        profile.mkdir()
        (profile / ".open-terminal.lock").write_bytes(b"managed\n" if name != "unmanaged" else b"")
        (profile / "SingletonLock").symlink_to("terminal-" + str(os.getpid()))
    with (root / "active/.open-terminal.lock").open("r+") as lease:
        fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
        prepare.clean_profiles(root)
        check(not (root / "abandoned/SingletonLock").is_symlink()
              and (root / "active/SingletonLock").is_symlink()
              and (root / "unmanaged/SingletonLock").is_symlink(),
              "abandoned browser locks released; active and unmanaged profiles preserved")

with tempfile.TemporaryDirectory(dir=Path.home()) as work:
    root = Path(work)
    document = root / "sample.docx"
    subprocess.run(["python3", "-c", "from docx import Document; import sys; "
                    "d=Document(); d.add_paragraph('Workspace document smoke test'); d.save(sys.argv[1])",
                    str(document)], check=True)
    subprocess.run(["libreoffice", "-env:UserInstallation=file://" + work + "/office", "--headless",
                    "--convert-to", "pdf", "--outdir", work, str(document)], check=True, capture_output=True)
    check((root / "sample.pdf").read_bytes().startswith(b"%PDF"), "actual Office to PDF conversion")
    notebook = root / "analysis.ipynb"
    notebook.write_text(json.dumps({"nbformat": 4, "nbformat_minor": 5, "metadata": {}, "cells": [
        {"cell_type": "code", "id": "analysis", "metadata": {}, "execution_count": None,
         "outputs": [], "source": "import pandas as pd; import matplotlib; print(pd.Series([2,3]).sum())"}]}))
    status, result = request("/notebooks", {"path": str(notebook)})
    check(status == 200, "native notebook kernel starts")
    notebook_id = json.loads(result)["id"]
    try:
        status, result = request("/notebooks/" + notebook_id + "/execute", {"cell_index": 0})
        result = json.loads(result)
        check(status == 200 and result["status"] == "ok" and "5" in json.dumps(result["outputs"]),
              "native notebook executes analysis with user Python libraries")
    finally:
        request("/notebooks/" + notebook_id, method="DELETE")
    subprocess.run(["docker", "info"], check=True, capture_output=True)
    subprocess.run(["docker", "compose", "version"], check=True, capture_output=True)
    subprocess.run(["docker", "buildx", "version"], check=True, capture_output=True)
    (root / "hello.c").write_text('#include <stdio.h>\nint main(void) { puts("nested-docker-ok"); return 0; }\n')
    subprocess.run(["gcc", "-static", "-o", str(root / "hello"), str(root / "hello.c")], check=True)
    (root / "Dockerfile").write_text('FROM scratch\nCOPY hello /hello\nENTRYPOINT ["/hello"]\n')
    subprocess.run(["docker", "build", "--network=none", "-t", "workspace-smoke", work],
                   check=True, capture_output=True)
    output = subprocess.check_output(["docker", "run", "--rm", "--network=none", "workspace-smoke"], text=True)
    check(output.strip() == "nested-docker-ok", "inner Docker builds and runs without a host socket")
code, action = request("/desktop/action", {"actions": [{"action": "key", "key": "NoSuchKeyContract"}]})
check(code == 502 and json.loads(action)["detail"]["completed_actions"] == 0,
      "unknown X11 key fails rather than reporting a successful screenshot")
code, action = request("/desktop/action", {"actions": [
    {"action": "move", "x": 1100, "y": 600},
    {"action": "key", "key": "NoSuchKeyContract"},
]})
detail = json.loads(action)["detail"]
check(code == 502 and detail["completed_actions"] == 1 and detail["failed_action_index"] == 1,
      "desktop failure reports partial batch progress")
code, action = request("/desktop/action", {"actions": [{"action": "key", "key": "Escape"}]})
check(code == 200 and action.startswith(b"\x89PNG"), "native action returns same desktop image format")
for _ in range(3):
    started = time.monotonic()
    code, action = request("/desktop/action", {"actions": [{"action": "click", "x": 1100, "y": 600}]})
    check(code == 200 and time.monotonic() - started < 5,
          "repeated click at unchanged pointer position succeeds without input timeout")


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
