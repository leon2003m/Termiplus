"""Exercise the live Open WebUI-generated terminal callables, without a model.

Run inside the Open WebUI backend environment with OWUI_TEST_TOKEN set to an
existing admin's short-lived bearer token. Use a disposable per-chat terminal
connection; this creates a saved test chat, files, commands and GUI sessions.
The report contains results and a schema fingerprint, never bearer tokens or images.
See docs/native-tool-validation.md for setup and invocation.
"""

import argparse
import asyncio
import base64
from collections import Counter
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shlex
import struct
import sys
import time
import uuid

import aiohttp
from fastapi import FastAPI, Request
from PIL import Image

# Match the running backend's existing import environment; do not generate a key.
if not os.environ.get("WEBUI_SECRET_KEY"):
    secret_file = Path(
        os.environ.get("WEBUI_SECRET_KEY_FILE", "/app/backend/.webui_secret_key")
    )
    if secret_file.is_file():
        os.environ["WEBUI_SECRET_KEY"] = secret_file.read_text().strip()

from open_webui.models.config import Config
from open_webui.models.users import Users
from open_webui.utils.tools import get_terminal_tools

EXPECTED = {
    "send_user_terminal_input",
    "read_user_terminal",
    "browser_action",
    "browser_screenshot",
    "browser_snapshot",
    "browser_tabs",
    "desktop_action",
    "desktop_screenshot",
    "list_processes",
    "run_command",
    "kill_process",
    "send_process_input",
    "get_process_status",
    "display_file",
    "glob_search",
    "grep_search",
    "list_files",
    "match_files",
    "read_file",
    "replace_file_content",
    "search_files",
    "write_file",
    "get_info",
}


def require(value, message):
    if not value:
        raise AssertionError(message)


def output(data):
    return "".join(entry.get("data", "") for entry in data.get("output", []))


def png(data, size=None):
    require(
        isinstance(data, str) and data.startswith("data:image/png;base64,"),
        "PNG data URI",
    )
    raw = base64.b64decode(data.split(",", 1)[1], validate=True)
    require(raw[:8] == b"\x89PNG\r\n\x1a\n" and len(raw) >= 24, "PNG bytes")
    dimensions = struct.unpack(">II", raw[16:24])
    with Image.open(io.BytesIO(raw)) as image:
        image.load()
        if min(dimensions) > 9:
            require(
                any(lo != hi for lo, hi in image.convert("RGB").getextrema()),
                "Screenshot contains rendered pixels",
            )
    if size:
        require(dimensions == size, "PNG dimensions " + str(dimensions))
    return raw


class Suite:
    def __init__(self, options):
        self.options = options
        self.token = os.environ["OWUI_TEST_TOKEN"]
        self.records = []
        self.calls = Counter()
        self.successes = Counter()
        self.negatives = Counter()
        self.parameters = {}
        self.actions = {"browser": set(), "desktop": set()}
        self.root = "/tmp/tp-native-contract-" + uuid.uuid4().hex[:12]
        self.processes = set()
        self.browser_sessions = []
        self.terminal_session = None
        self.port = 3038
        self.fixture_url = f"http://127.0.0.1:{self.port}"

    async def http(self, path, data=None, method=None):
        async with self.client.request(
            method or ("POST" if data is not None else "GET"),
            self.options.base_url.rstrip("/") + path,
            json=data,
            headers={"Authorization": "Bearer " + self.token},
        ) as response:
            raw = await response.read()
            require(
                response.status < 400,
                f"UI fixture API HTTP {response.status}: {raw[:300]!r}",
            )
            return json.loads(raw) if raw else None

    async def proxy(self, path, data=None, method=None):
        return await self.http(self.proxy_root + path, data, method)

    async def tool(self, name, expected_error=None, **kwargs):
        self.calls[name] += 1
        data, _ = await asyncio.wait_for(self.tools[name]["callable"](**kwargs), 75)
        error = data.get("error") if isinstance(data, dict) else None
        if expected_error:
            require(
                error and f"HTTP error {expected_error}:" in error,
                f'{name}: expected HTTP {expected_error}, received {type(data).__name__} ({len(data) if hasattr(data, "__len__") else 0} entries/characters)',
            )
            self.negatives[name] += 1
        else:
            require(not error, f"{name}: {error}")
            self.successes[name] += 1
            self.parameters.setdefault(name, set()).update(kwargs)
        return data

    async def command(self, command, **kwargs):
        data = await self.tool("run_command", command=command, wait="15", **kwargs)
        self.processes.add(data["id"])
        require(
            data["status"] == "done" and data["exit_code"] == 0,
            "Command failed: " + output(data)[-350:],
        )
        return output(data).strip()

    async def case(self, name, fn):
        started = time.monotonic()
        try:
            await fn()
            record = {"case": name, "status": "pass"}
        except Exception as exc:
            message = str(exc).replace(self.token, "[REDACTED]")
            record = {"case": name, "status": "fail", "error": message[:1200]}
        record["seconds"] = round(time.monotonic() - started, 3)
        self.records.append(record)
        print(json.dumps(record, ensure_ascii=False), flush=True)

    async def state(self):
        text = await self.command(
            "python3 -c "
            + shlex.quote(
                f'import urllib.request;print(urllib.request.urlopen("{self.fixture_url}/state").read().decode())'
            )
        )
        return json.loads(text)

    async def reset(self):
        await self.command(
            "python3 -c "
            + shlex.quote(
                f'import urllib.request;urllib.request.urlopen("{self.fixture_url}/reset",data=b"").read()'
            )
        )

    async def observe(self, predicate):
        deadline = time.monotonic() + 12
        while time.monotonic() < deadline:
            events = await self.state()
            if predicate(events):
                return events
            await asyncio.sleep(0.15)
        raise AssertionError(
            "Expected GUI side effect missing; last events: " + str(events[-8:])
        )

    async def files(self):
        p = self.root + "/files"
        text = "Alpha one\nBeta two\nAlpha three\nGrüße 🌍\n"
        result = await self.tool(
            "write_file", path=p + "/nested/sample.txt", content=text
        )
        require(result["size"] == len(text.encode()), "UTF-8 byte count")
        read = await self.tool("read_file", path=p + "/nested/sample.txt")
        require(read["content"] == text, "Exact UTF-8 readback")
        read = await self.tool(
            "read_file", path=p + "/nested/sample.txt", start_line="2", end_line="3"
        )
        require(read["content"] == "Beta two\nAlpha three\n", "Inclusive line range")
        await self.tool("read_file", path=p + "/missing", expected_error=404)
        await self.tool(
            "read_file",
            path=p + "/nested/sample.txt",
            start_line="0",
            expected_error=422,
        )
        await self.tool("write_file", path=p + "/overwrite.txt", content="old")
        await self.tool("write_file", path=p + "/overwrite.txt", content="new")
        require(
            (await self.tool("read_file", path=p + "/overwrite.txt"))["content"]
            == "new",
            "Overwrite",
        )
        await self.tool("write_file", path=p + "/.hidden.txt", content="secret fixture")
        listing = await self.tool("list_files", directory=p)
        require(
            {"nested", "overwrite.txt", ".hidden.txt"}
            <= {x["name"] for x in listing["entries"]},
            "Directory entries",
        )
        await self.tool("list_files", directory=p + "/missing", expected_error=404)
        await self.tool(
            "replace_file_content",
            path=p + "/nested/sample.txt",
            replacements=[{"target": "Alpha", "replacement": "X"}],
            expected_error=400,
        )
        require(
            (await self.tool("read_file", path=p + "/nested/sample.txt"))["content"]
            == text,
            "Ambiguous replace atomic",
        )
        await self.tool(
            "replace_file_content",
            path=p + "/nested/sample.txt",
            replacements=[
                {
                    "target": "Alpha",
                    "replacement": "First",
                    "start_line": 1,
                    "end_line": 1,
                },
                {"target": "Alpha", "replacement": "Third", "allow_multiple": True},
            ],
        )
        require(
            (await self.tool("read_file", path=p + "/nested/sample.txt"))["content"]
            == text.replace("Alpha one", "First one").replace(
                "Alpha three", "Third three"
            ),
            "Sequential scoped replacement",
        )
        await self.tool(
            "replace_file_content",
            path=p + "/missing",
            replacements=[{"target": "x", "replacement": "y"}],
            expected_error=404,
        )
        grep = await self.tool(
            "grep_search",
            query="beta",
            path=p,
            case_insensitive=True,
            regex=False,
            include="*.txt",
            max_results=1,
        )
        require(
            len(grep["matches"]) == 1 and grep["matches"][0]["line"] == 2,
            "Literal case-insensitive grep",
        )
        grep = await self.tool(
            "grep_search",
            query="^(First|Third)",
            path=p,
            regex=True,
            match_per_line=False,
        )
        require(len(grep["matches"]) == 1, "Regex file-only grep")
        await self.tool("grep_search", query="[", path=p, expected_error=400)
        await self.tool(
            "grep_search", query="x", path=p + "/missing", expected_error=404
        )
        found = await self.tool(
            "search_files", query="sample", path=p, type="file", limit=1
        )
        require(
            [x["name"] for x in found["results"]] == ["sample.txt"], "Filename search"
        )
        hidden = await self.tool(
            "search_files", query="hidden", path=p, show_hidden=True
        )
        require(len(hidden["results"]) == 1, "Hidden filename search")
        require(
            not (await self.tool("search_files", query="hidden", path=p))["results"],
            "Hidden default exclusion",
        )
        require(
            len((await self.tool("search_files", path=p, type="directory"))["results"])
            == 1,
            "Directory type filter",
        )
        await self.tool("search_files", path=p, type="invalid", expected_error=422)
        matched = await self.tool(
            "match_files", query="Beta two", path=p, offset=0, limit=1, show_hidden=True
        )
        require(
            "sample.txt" in json.dumps(matched) and "Beta two" in json.dumps(matched),
            "Content-ranked search",
        )
        await self.tool("match_files", query=" ", path=p, expected_error=400)
        await self.tool(
            "match_files", query="x", path=p + "/missing", expected_error=404
        )
        glob = await self.tool(
            "glob_search",
            pattern="*.txt",
            path=p,
            type="file",
            exclude="overwrite.txt",
            max_results=3,
        )
        require(
            "sample.txt" in json.dumps(glob)
            and "overwrite.txt" not in json.dumps(glob),
            "Glob exclusion",
        )
        await self.tool(
            "glob_search", pattern="*", path=p + "/missing", expected_error=404
        )
        # Exercise limits/pagination against more matches than the limit.
        first = await self.tool("match_files", query=".txt", path=p, limit=1, offset=0)
        second = await self.tool("match_files", query=".txt", path=p, limit=1, offset=1)
        require(
            len(first["results"]) == len(second["results"]) == 1
            and first["results"][0]["path"] != second["results"][0]["path"]
            and first["next_offset"] == 1,
            "Ranked result pagination advances",
        )
        require(
            not (await self.tool("match_files", query="secret fixture", path=p))[
                "results"
            ],
            "Hidden content excluded",
        )
        require(
            len(
                (
                    await self.tool(
                        "match_files", query="secret fixture", path=p, show_hidden=True
                    )
                )["results"]
            )
            == 1,
            "Hidden content included",
        )
        require(
            len((await self.tool("search_files", path=p, limit=1))["results"]) == 1,
            "Filename result limit",
        )
        limited = await self.tool("glob_search", pattern="*", path=p, max_results=1)
        require(
            len(limited["matches"]) == 1 and limited["truncated"], "Glob result limit"
        )
        limited = await self.tool(
            "grep_search", query="^(First|Third)", path=p, max_results=1
        )
        require(
            len(limited["matches"]) == 1 and limited["truncated"], "Grep result limit"
        )
        await self.tool(
            "write_file", path=p + "/repeated.txt", content="same same same"
        )
        await self.tool(
            "replace_file_content",
            path=p + "/repeated.txt",
            replacements=[
                {"target": "same", "replacement": "changed", "allow_multiple": True}
            ],
        )
        require(
            (await self.tool("read_file", path=p + "/repeated.txt"))["content"]
            == "changed changed changed",
            "Replace all occurrences",
        )
        encoded = p + "/spaces # + Grüße?.txt"
        await self.tool("write_file", path=encoded, content="Encoded path round trip")
        require(
            (await self.tool("read_file", path=encoded))["content"]
            == "Encoded path round trip",
            "Query serialization preserves special path characters",
        )
        display = await self.tool(
            "display_file", path=p + "/nested/sample.txt", inline=True, page=1
        )
        require(
            display["exists"] and display["path"] == p + "/nested/sample.txt",
            "Viewer metadata through OWUI adapter",
        )
        require(
            not (await self.tool("display_file", path=p + "/missing"))["exists"],
            "Missing viewer metadata",
        )
        image = self.root + "/tiny.png"
        await self.command(
            "python3 -c "
            + shlex.quote(
                'from PIL import Image;Image.new("RGB",(8,9),(20,40,60)).save('
                + repr(image)
                + ")"
            )
        )
        png(await self.tool("read_file", path=image), (8, 9))
        await self.command(
            "python3 -c "
            + shlex.quote(
                "from pathlib import Path;Path("
                + repr(p + "/binary.bin")
                + ').write_bytes(b"\\x00\\xff")'
            )
        )
        await self.tool("read_file", path=p + "/binary.bin", expected_error=415)

    async def processes_test(self):
        result = await self.tool(
            "run_command",
            command='printf "%s:" "$TP_CONTRACT"; pwd; printf "stderr-token\\n" >&2',
            cwd=self.root,
            env={"TP_CONTRACT": "env-ok"},
            wait="10",
        )
        require(
            result["exit_code"] == 0
            and "env-ok:" + self.root in output(result)
            and "stderr-token" in output(result),
            "CWD, env, stdout/stderr",
        )
        result = await self.tool("run_command", command="exit 7", wait="10")
        require(result["exit_code"] == 7, "Nonzero process exit retained")
        result = await self.tool(
            "run_command", command='printf "tail-token\\n"', wait="10", tail="1"
        )
        require(
            len(result["output"]) == 1 and "tail-token" in output(result),
            "Run command tail limit",
        )
        proc = await self.tool(
            "run_command",
            command='python3 -u -c \'print("READY",flush=True); s=input(); print("REPLY:"+s,flush=True)\'',
            wait="0",
        )
        pid = proc["id"]
        self.processes.add(pid)
        require(proc["status"] == "running", "Asynchronous execution")
        require(
            pid in {x["id"] for x in await self.tool("list_processes")},
            "Process listing",
        )
        await self.tool("send_process_input", process_id=pid, input="hello native\n")
        result = await self.tool(
            "get_process_status", process_id=pid, wait="10", offset=0
        )
        require(
            result["exit_code"] == 0 and "REPLY:hello native" in output(result),
            "Real stdin round trip",
        )
        result2 = await self.tool(
            "get_process_status", process_id=pid, offset=result["next_offset"], wait="0"
        )
        require(not result2["output"], "Independent offset cursor")
        tail = await self.tool("get_process_status", process_id=pid, tail="1", wait="0")
        require(len(tail["output"]) <= 1, "Tail limit")
        await self.tool(
            "send_process_input", process_id=pid, input="late\n", expected_error=400
        )
        for force in (False, True):
            code = "import signal,time,sys; "
            code += (
                "signal.signal(signal.SIGTERM,signal.SIG_IGN); "
                if force
                else "signal.signal(signal.SIGTERM,lambda *_: (print('TERM_CAUGHT',flush=True),sys.exit(23))); "
            )
            code += "print('SIGNAL_READY',flush=True); time.sleep(8)"
            proc = await self.tool(
                "run_command",
                command="exec python3 -u -c " + shlex.quote(code),
                wait="0",
            )
            self.processes.add(proc["id"])
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                ready = await self.tool(
                    "get_process_status", process_id=proc["id"], wait="0"
                )
                if "SIGNAL_READY" in output(ready):
                    break
                await asyncio.sleep(0.05)
            require("SIGNAL_READY" in output(ready), "Signal handler installed")
            started = time.monotonic()
            await self.tool("kill_process", process_id=proc["id"], force=force)
            require(
                time.monotonic() - started < 4, "Kill completed before fixture timeout"
            )
            killed = await self.tool(
                "get_process_status", process_id=proc["id"], wait="0"
            )
            require(killed["status"] == "killed", "Killed status")
            if force:
                require(
                    killed["exit_code"] in (-9, 137),
                    "SIGKILL kills a SIGTERM-ignoring process",
                )
            else:
                require(
                    killed["exit_code"] == 23 and "TERM_CAUGHT" in output(killed),
                    "SIGTERM reaches installed handler",
                )
        for name in ("get_process_status", "send_process_input", "kill_process"):
            args = {"process_id": "missing-contract-process"}
            if name == "send_process_input":
                args["input"] = "x"
            await self.tool(name, expected_error=404, **args)
        await self.tool("run_command", command="true", wait="-1", expected_error=422)

    async def fixture(self):
        await self.tool(
            "write_file",
            path=self.root + "/fixture.py",
            content=Path(__file__).with_name("native-tool-fixture.py").read_text(),
        )
        proc = await self.tool(
            "run_command",
            command="exec python3 "
            + shlex.quote(self.root + "/fixture.py")
            + " "
            + str(self.port),
            wait="0",
        )
        self.processes.add(proc["id"])
        await self.observe(lambda events: events == [])

    async def desktop_start(self):
        png(await self.tool("desktop_screenshot"), (1280, 800))
        # The fixture uses its own Chromium profile, never the user's browser.
        script = """import os,subprocess
from pathlib import Path
for p in Path('/proc').iterdir():
 try:
  if (p/'comm').read_text().strip()=='xfdesktop':
   env=dict(s.split('=',1) for s in (p/'environ').read_bytes().decode().split('\\0') if '=' in s);break
 except (OSError,UnicodeError):pass
subprocess.run(['chromium','--no-first-run','--disable-dev-shm-usage','--kiosk','--user-data-dir=PROFILE','URL'],env=env,check=True)
""".replace(
            "PROFILE", self.root + "/chromium"
        ).replace(
            "URL", self.fixture_url + "/a"
        )
        await self.tool("write_file", path=self.root + "/gui.py", content=script)
        proc = await self.tool(
            "run_command",
            command="exec python3 " + shlex.quote(self.root + "/gui.py"),
            wait="0",
        )
        self.gui_pid = proc["id"]
        self.processes.add(self.gui_pid)
        await self.observe(lambda e: any(x.get("type") == "painted" for x in e))

    async def act(self, kind, action, **kwargs):
        data = await self.tool(kind + "_action", actions=[{"action": action, **kwargs}])
        if kind == "desktop":
            png(data, (1280, 800))
        self.actions[kind].add(action)
        return data

    async def pointer(self, kind, action, button="left"):
        if action == "move":
            await self.act(kind, "move", x=410, y=460)
        await self.reset()
        args = {"x": 150, "y": 130}
        if action != "move":
            args["button"] = button
        if action == "drag":
            args.update(x=150, y=350, to_x=450, to_y=350)
        await self.act(kind, action, **args)
        if action == "move":
            await self.observe(
                lambda e: any(
                    x.get("type") == "mousemove"
                    and x.get("x") == 150
                    and x.get("y") == 130
                    for x in e
                )
            )
        elif action == "drag":
            await self.observe(
                lambda e: any(
                    x.get("type") == "mouseup"
                    and x.get("x") == 450
                    and x.get("y") == 350
                    for x in e
                )
            )
            await self.observe(
                lambda e: any(
                    x.get("type") == "mousedown" and x.get("target") == "drag"
                    for x in e
                )
                and any(
                    x.get("type") == "mousemove"
                    and x.get("x") == 450
                    and x.get("y") == 350
                    and x.get("buttons") == 1
                    for x in e
                )
            )
        elif action == "double_click":
            await self.observe(
                lambda e: any(
                    x.get("type") == "dblclick" and x.get("target") == "probe"
                    for x in e
                )
            )
        else:
            await self.observe(
                lambda e: any(
                    x.get("type") == "mouseup"
                    and x.get("button") == {"left": 0, "middle": 1, "right": 2}[button]
                    and x.get("target") == "probe"
                    for x in e
                )
            )

    async def type_keys(self, kind):
        await self.reset()
        await self.act(kind, "click", x=170, y=240)
        await self.act(kind, "type", text="old content to replace")
        await self.act(kind, "key", key="ctrl+a")
        await self.act(kind, "type", text="Grüße native 🌍")
        await self.observe(
            lambda e: any(
                x.get("type") == "input" and x.get("value") == "Grüße native 🌍"
                for x in e
            )
        )
        await self.act(kind, "key", key="Return" if kind == "desktop" else "Enter")
        await self.observe(
            lambda e: any(
                x.get("type") == "keydown" and x.get("key") == "Enter" for x in e
            )
        )

    async def scroll(self, kind, direction):
        before = await self.state()
        initial = next(
            (x for x in reversed(before) if x.get("type") == "scroll"),
            {"x": 400, "y": 400},
        )
        await self.reset()
        await self.act(
            kind,
            "scroll",
            x=720,
            y=230,
            direction=direction,
            amount=2 if kind == "desktop" else 100,
        )
        axis = "y" if direction in ("up", "down") else "x"
        sign = 1 if direction in ("down", "right") else -1
        await self.observe(
            lambda e: any(
                x.get("type") == "scroll" and sign * (x[axis] - initial[axis]) > 0
                for x in e
            )
        )

    async def invalid_actions(self, kind):
        for actions, status in [
            ([], 422),
            ([{"action": "wait"}], 422),
            ([{"action": "click", "to_x": 2, "to_y": 2}], 422),
            ([{"action": "drag", "x": 10, "y": 10}], 422),
            ([{"action": "type"}], 422),
            ([{"action": "key", "key": "bad key"}], 422),
            (
                [{"action": "click", "x": 20000, "y": 20}],
                400 if kind == "desktop" else 422,
            ),
            ([{"action": "move", "x": 0, "y": 0, "unknown": True}], 422),
            ([{"action": "key", "key": "Escape"}] * 51, 422),
        ]:
            await self.tool(kind + "_action", actions=actions, expected_error=status)
        # Whole-batch validation must prevent even the preceding valid click.
        await self.reset()
        await self.tool(
            kind + "_action",
            actions=[
                {"action": "click", "x": 150, "y": 130},
                {"action": "click", "x": 1280 if kind == "desktop" else 16384, "y": 20},
            ],
            expected_error=400,
        )
        require(
            not any(e.get("type") == "mouseup" for e in await self.state()),
            "Invalid batch had no click side effect",
        )

    async def batches(self, kind):
        await self.reset()
        actions = [{"action": "click", "x": 150, "y": 130}] * 3
        data = await self.tool(kind + "_action", actions=actions)
        if kind == "desktop":
            png(data, (1280, 800))
        await self.observe(
            lambda e: sum(
                x.get("type") == "mouseup" and x.get("target") == "probe" for x in e
            )
            == 3
        )
        # Separate requests at an unchanged pointer position catch --sync hangs.
        for _ in range(3):
            started = time.monotonic()
            await self.act(kind, "click", x=150, y=130)
            require(
                time.monotonic() - started < 5,
                "Repeated click exceeded its bounded deadline",
            )
        await self.reset()
        bad = (
            {"action": "key", "key": "NoSuchKeyContract"}
            if kind == "desktop"
            else {"action": "click", "ref": "@e999999"}
        )
        if kind == "desktop":
            await self.tool(kind + "_action", actions=[bad], expected_error=502)
        data = await self.tool(
            kind + "_action",
            actions=[{"action": "click", "x": 150, "y": 130}, bad],
            expected_error=502 if kind == "desktop" else 409,
        )
        error = json.loads(data["error"].split(": ", 1)[1])["detail"]
        require(
            error["completed_actions"] == 1 and error["failed_action_index"] == 1,
            "Partial batch progress metadata",
        )
        await self.observe(
            lambda e: sum(
                x.get("type") == "mouseup" and x.get("target") == "probe" for x in e
            )
            == 1
        )
        await self.act(kind, "key", key="Escape")

    async def desktop_stop(self):
        await self.tool("kill_process", process_id=self.gui_pid)
        await asyncio.sleep(0.5)

    async def browser_start(self):
        require(
            (await self.tool("browser_tabs"))["tabs"] == [], "Fresh chat browser empty"
        )
        await self.tool("browser_snapshot", expected_error=404)
        await self.tool("browser_screenshot", expected_error=404)
        await self.tool(
            "browser_action", actions=[{"action": "reload"}], expected_error=404
        )
        s = await self.proxy("/browser/sessions", {"url": self.fixture_url + "/a"})
        self.browser_sessions.append(s["id"])
        await self.proxy(
            "/browser/sessions/" + s["id"], {"mode": "chrome", "active": True}, "PATCH"
        )
        await self.observe(lambda e: any(x.get("type") == "ready" for x in e))
        tabs = await self.tool("browser_tabs")
        require(
            tabs["current_session_id"] == s["id"] and len(tabs["tabs"]) == 1,
            "Selected native Browser tab",
        )
        png(await self.tool("browser_screenshot", session_id=s["id"]))
        snap = await self.tool("browser_snapshot", session_id=s["id"])
        require("Native fixture A" in str(snap), "Browser accessibility snapshot")

    async def navigation(self):
        for action, page in [
            ("navigate", "B"),
            ("back", "A"),
            ("forward", "B"),
            ("reload", "B"),
        ]:
            await self.reset()
            kwargs = {"url": self.fixture_url + "/b"} if action == "navigate" else {}
            await self.act("browser", action, **kwargs)
            await self.observe(
                lambda e: any(
                    x.get("type") == "ready" and x.get("page") == page for x in e
                )
            )
            require(
                "Native fixture " + page in str(await self.tool("browser_snapshot")),
                "History/navigation snapshot",
            )

    async def refs(self):
        snap = await self.tool("browser_snapshot")
        tree = snap if isinstance(snap, str) else snap["snapshot"]
        # Snapshot format is inspected, not guessed or replaced with DOM IDs.
        match = re.search(r'^\s*textbox\s+(@e\d+)\s+"Message"', tree, re.MULTILINE)
        require(match, "Message textbox ref in snapshot: " + tree[:500])
        ref = match.group(1)
        await self.reset()
        await self.act("browser", "type", ref=ref, text="Reference text")
        await self.observe(
            lambda e: any(
                x.get("type") == "input" and x.get("value") == "Reference text"
                for x in e
            )
        )
        await self.act("browser", "type", ref=ref, text="Replaced by ref")
        await self.observe(
            lambda e: any(
                x.get("type") == "input" and x.get("value") == "Replaced by ref"
                for x in e
            )
        )
        snap = await self.tool("browser_snapshot")
        button = re.search(
            r'^\s*button\s+(@e\d+)\s+"Probe"', snap["snapshot"], re.MULTILINE
        )
        require(button, "Probe button ref")
        await self.reset()
        await self.act("browser", "click", ref=button.group(1))
        await self.observe(
            lambda e: any(
                x.get("type") == "click" and x.get("target") == "probe" for x in e
            )
        )
        await self.tool(
            "browser_action",
            actions=[{"action": "click", "ref": "@e999999"}],
            expected_error=409,
        )
        await self.tool(
            "browser_snapshot",
            session_id="missing-contract-session",
            expected_error=404,
        )
        await self.tool(
            "browser_screenshot",
            session_id="missing-contract-session",
            expected_error=404,
        )
        await self.tool(
            "browser_action",
            session_id="missing-contract-session",
            actions=[{"action": "reload"}],
            expected_error=404,
        )
        await self.tool(
            "browser_action",
            actions=[{"action": "navigate", "url": "file:///etc/passwd"}],
            expected_error=400,
        )
        await self.tool(
            "browser_action",
            actions=[{"action": "key", "key": "UnknownKeyName"}],
            expected_error=400,
        )

    async def tab_selection(self):
        first = self.browser_sessions[0]
        second = await self.proxy("/browser/sessions", {"url": self.fixture_url + "/a"})
        self.browser_sessions.append(second["id"])
        await self.proxy(
            "/browser/sessions/" + second["id"],
            {"mode": "chrome", "active": False},
            "PATCH",
        )
        await self.proxy("/browser/sessions/" + first, {"active": True}, "PATCH")
        tabs = await self.tool("browser_tabs")
        require(
            len(tabs["tabs"]) == 2 and tabs["current_session_id"] == first,
            "Two tabs with selected first",
        )
        snap = await self.tool("browser_snapshot", session_id=second["id"])
        require("Native fixture A" in snap["snapshot"], "Explicit second tab")
        result = await self.tool(
            "browser_action",
            session_id=second["id"],
            actions=[{"action": "navigate", "url": self.fixture_url + "/b"}],
        )
        require(
            result["session_id"] == second["id"], "Action routed to explicit second tab"
        )
        require(
            (await self.tool("browser_snapshot"))["session_id"] == first,
            "Default tab selection preserved",
        )

    async def user_shell(self):
        await self.tool("read_user_terminal", expected_error=404)
        await self.tool(
            "send_user_terminal_input", input="ignored\n", expected_error=404
        )
        shell = await self.proxy("/api/terminals", {})
        self.terminal_session = shell["id"]
        ws_url = (
            self.options.base_url.replace("http://", "ws://")
            .replace("https://", "wss://")
            .rstrip("/")
            + f'/api/v1/terminals/{self.options.terminal_id}/api/terminals/{shell["id"]}'
        )
        async with self.client.ws_connect(ws_url) as ws:
            await ws.send_json(
                {"type": "auth", "token": self.token, "chat_id": self.chat_id}
            )
            await ws.send_json({"type": "activity", "active": True})
            # Wait for a real PTY frame, proving the UI websocket is attached.
            frame = await asyncio.wait_for(ws.receive(), 15)
            require(frame.type == aiohttp.WSMsgType.BINARY, "Shell WebSocket output")
            await self.tool("read_user_terminal")
            await self.tool(
                "send_user_terminal_input", input="printf 'shell-%s\\n' 'contract-ok'\n"
            )
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                data = await self.tool("read_user_terminal")
                if "shell-contract-ok" in data["output"]:
                    break
                await asyncio.sleep(0.1)
            require(
                "shell-contract-ok" in data["output"], "Connected user shell round trip"
            )
        # Native PTYs are removed asynchronously on UI websocket disconnect.
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            shells = await self.proxy("/api/terminals")
            if shell["id"] not in {s["id"] for s in shells}:
                break
            await asyncio.sleep(0.05)
        require(
            shell["id"] not in {s["id"] for s in shells},
            "Shell closes on UI disconnect",
        )
        self.terminal_session = None

    async def cleanup(self):
        failures = []
        for sid in self.browser_sessions:
            try:
                await self.proxy("/browser/sessions/" + sid, method="DELETE")
            except Exception as exc:
                failures.append(str(exc))
        if self.terminal_session:
            try:
                shells = await self.proxy("/api/terminals")
                if self.terminal_session in {s["id"] for s in shells}:
                    await self.proxy(
                        "/api/terminals/" + self.terminal_session, method="DELETE"
                    )
            except Exception as exc:
                failures.append(str(exc))
        try:
            running = {
                p["id"]
                for p in await self.tool("list_processes")
                if p["status"] == "running"
            }
        except Exception as exc:
            failures.append(str(exc))
            running = self.processes
        for pid in self.processes & running:
            try:
                await self.tool("kill_process", process_id=pid)
            except Exception as exc:
                failures.append(str(exc))
        try:
            await self.command("rm -rf -- " + shlex.quote(self.root))
        except Exception as exc:
            failures.append(str(exc))
        require(not failures, "Cleanup failures: " + str(failures))

    async def coverage(self):
        require(
            set(self.tools) == EXPECTED,
            "Native tool inventory changed: " + str(set(self.tools) ^ EXPECTED),
        )
        require(
            all(self.successes[name] for name in self.tools),
            "Tools without positive calls: "
            + str(set(self.tools) - set(self.successes)),
        )
        for name, tool in self.tools.items():
            fields = set(tool["spec"]["parameters"].get("properties", {}))
            require(
                fields <= self.parameters.get(name, set()),
                name
                + " untested parameters: "
                + str(fields - self.parameters.get(name, set())),
            )
        for kind in self.actions:
            declared = set(
                self.tools[kind + "_action"]["spec"]["parameters"]["properties"][
                    "actions"
                ]["items"]["properties"]["action"]["enum"]
            )
            require(
                self.actions[kind] == declared,
                kind
                + " action coverage missing: "
                + str(declared - self.actions[kind]),
            )

    async def run(self):
        async with aiohttp.ClientSession(
            timeout=aiohttp.ClientTimeout(total=150)
        ) as self.client:
            user = await self.http("/api/v1/auths/")
            require(user["role"] == "admin", "Use authorized admin test credentials")
            connections = await Config.get("terminal_server.connections", []) or []
            connection = next(
                c for c in connections if c.get("id") == self.options.terminal_id
            )
            require(
                connection.get("url", "").rstrip("/").endswith("/p/scratch"),
                "Contract suite requires the disposable scratch policy; refusing a persistent workspace",
            )
            app = FastAPI()
            app.state.redis = None
            request = Request({"type": "http", "app": app, "headers": []})
            chat = await self.http(
                "/api/v1/chats/new",
                {
                    "chat": {
                        "title": "Native deterministic tool verification",
                        "models": [self.options.model_id],
                        "history": {"messages": {}, "currentId": None},
                        "messages": [],
                    }
                },
            )
            self.chat_id = chat["id"]
            self.proxy_root = (
                f"/api/v1/terminals/{self.options.terminal_id}/chats/{self.chat_id}"
            )
            self.tools, _ = await get_terminal_tools(
                request,
                self.options.terminal_id,
                await Users.get_user_by_id(user["id"]),
                {"__metadata__": {"chat_id": self.chat_id}},
            )
            inventory = {n: t["spec"] for n, t in self.tools.items()}
            print("Resolved native tools:", ", ".join(sorted(inventory)), flush=True)
            try:
                async def guidance():
                    for name in ("desktop_action", "desktop_screenshot"):
                        description = inventory[name].get("description", "")
                        require("1280x800" in description and "RAW PIXELS" in description,
                                name + " live native coordinate guidance")
                    fields = inventory["desktop_action"]["parameters"]["properties"]["actions"]["items"]["properties"]
                    for axis in ("x", "y"):
                        require("REQUIRED" in fields[axis].get("description", "")
                                and "PIXEL" in fields[axis]["description"],
                                "Live native " + axis + " coordinate field guidance")
                    require("Drag destination" in fields["to_x"].get("description", ""),
                            "Live native drag field guidance")

                await self.case("live published Desktop coordinate guidance", guidance)
                await self.command("mkdir -p " + shlex.quote(self.root))

                async def info():
                    require(
                        bool((await self.tool("get_info"))["info"]),
                        "Operator desktop info",
                    )

                await self.case("get_info environment", info)
                await self.case(
                    "filesystem tools, UTF-8, searches, replacements, images and errors",
                    self.files,
                )
                await self.case(
                    "process tools, stdin, cursors, exits and errors",
                    self.processes_test,
                )
                await self.case("local DOM oracle starts", self.fixture)
                await self.case(
                    "shared Desktop starts with own Chromium fixture",
                    self.desktop_start,
                )
                for kind in ("desktop", "browser"):
                    if kind == "browser":
                        await self.case(
                            "native Browser UI prerequisite and tools",
                            self.browser_start,
                        )
                    for action in ("move", "click", "double_click", "drag"):
                        await self.case(
                            kind + " " + action,
                            lambda k=kind, a=action: self.pointer(k, a),
                        )
                    for button in ("middle", "right"):
                        await self.case(
                            kind + " click " + button,
                            lambda k=kind, b=button: self.pointer(k, "click", b),
                        )
                    await self.case(
                        kind + " Unicode text and keys",
                        lambda k=kind: self.type_keys(k),
                    )
                    for direction in ("down", "up", "right", "left"):
                        await self.case(
                            kind + " scroll " + direction,
                            lambda k=kind, d=direction: self.scroll(k, d),
                        )
                    await self.case(
                        kind
                        + (
                            " repeated clicks, batches and partial failure"
                            if kind == "desktop"
                            else " repeated clicks, batches and partial failure"
                        ),
                        lambda k=kind: self.batches(k),
                    )
                    await self.case(
                        kind + " validation and batch preflight",
                        lambda k=kind: self.invalid_actions(k),
                    )
                    if kind == "desktop":
                        await self.case(
                            "close fixture Chromium before native Browser",
                            self.desktop_stop,
                        )
                await self.case(
                    "Browser navigation/back/forward/reload", self.navigation
                )
                await self.case("Browser element refs and session errors", self.refs)
                await self.case(
                    "Browser multiple tabs and explicit session routing",
                    self.tab_selection,
                )
                await self.case(
                    "user shell tools through connected native UI websocket",
                    self.user_shell,
                )
                await self.case(
                    "every discovered tool and advertised action covered", self.coverage
                )
            finally:
                await self.case(
                    "cleanup own test files, processes and UI sessions", self.cleanup
                )
            report = {
                "tool_count": len(inventory),
                "schema_sha256": hashlib.sha256(
                    json.dumps(inventory, sort_keys=True).encode()
                ).hexdigest(),
                "tools": {
                    name: {
                        "calls": self.calls[name],
                        "positive": self.successes[name],
                        "negative": self.negatives[name],
                        "parameters": sorted(self.parameters.get(name, set())),
                    }
                    for name in sorted(inventory)
                },
                "actions": {k: sorted(v) for k, v in self.actions.items()},
                "cases": self.records,
            }
            Path(self.options.output).write_text(
                json.dumps(report, ensure_ascii=False, indent=2)
            )
            passed = all(r["status"] == "pass" for r in self.records)
            print(
                "RESULT",
                "PASS" if passed else "FAIL",
                len(self.records),
                "cases",
                flush=True,
            )
            return passed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:8080")
    parser.add_argument(
        "--terminal-id", required=True, help="Dedicated disposable per-chat connection"
    )
    parser.add_argument("--model-id", default="termiplus-test")
    parser.add_argument("--output", default="/tmp/native-tool-contract-report.json")
    parser.add_argument(
        "--token-stdin",
        action="store_true",
        help="Read bearer token from first stdin line",
    )
    options = parser.parse_args()
    if options.token_stdin:
        os.environ["OWUI_TEST_TOKEN"] = sys.stdin.readline().strip()
    if not os.environ.get("OWUI_TEST_TOKEN"):
        parser.error("Supply OWUI_TEST_TOKEN or --token-stdin")
    return asyncio.run(Suite(options).run())


if __name__ == "__main__":
    raise SystemExit(0 if main() else 1)
