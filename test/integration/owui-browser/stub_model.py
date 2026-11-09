#!/usr/bin/env python3
"""Deterministic OpenAI-compatible stub model for the Open WebUI browser test.

Python standard library only. It serves /v1/models and streaming (or plain)
/v1/chat/completions and never calls a model. The last user message is a
script, one directive per line:

    @@call <tool name> <json arguments>

Step N of the script is issued as a tool call once N tool results followed the
user message; after the last step the stub answers with a text that starts with
STUB-DONE and quotes the final tool result, so a test can wait for it. A user
message without directives gets "STUB-REPLY". Tool names are resolved against
the tools the request offers (exact, then a suffix match, so Open WebUI's
prefixing does not matter). If the model was never offered the tool, the stub
says so (STUB-NOTOOL) instead of calling it, which is how a test proves that a
tool is hidden from the model.

Every request is appended to /logs/stub.jsonl (offered tool names, step, what
was answered), so a test can inspect what the model was shown.
"""
import json
import os
import re
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG = os.environ.get("STUB_LOG", "/logs/stub.jsonl")
MODEL = "stub"
CALL = re.compile(r"^\s*@@call\s+(\S+)\s*(\{.*\})?\s*$")
_lock = threading.Lock()
_counter = [0]


def log(entry):
    entry["t"] = time.time()
    try:
        with _lock, open(LOG, "a") as f:
            f.write(json.dumps(entry) + "\n")
    except OSError:
        pass


def text_of(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(p.get("text", "") for p in content if isinstance(p, dict) and p.get("type") == "text")
    return ""


def tool_names(req):
    out = []
    for t in req.get("tools") or []:
        fn = t.get("function") or {}
        if fn.get("name"):
            out.append(fn["name"])
    return out


def resolve(name, offered):
    if name in offered:
        return name
    for o in offered:
        if o.endswith("_" + name) or o.endswith("__" + name) or o.endswith("." + name) or o.endswith(":" + name):
            return o
    return None


def decide(req):
    """Return ("tool", name, args_json) or ("text", text)."""
    msgs = req.get("messages") or []
    users = [i for i, m in enumerate(msgs) if m.get("role") == "user"]
    if not users:
        return ("text", "STUB-REPLY")
    # The script is the last user message that carries directives; Open WebUI may
    # append its own user messages (an image from a screenshot tool, say) after it.
    scripted = [i for i in users if "@@call" in text_of(msgs[i].get("content"))]
    last_user = scripted[-1] if scripted else users[-1]
    prompt = text_of(msgs[last_user].get("content"))
    # Open WebUI's own task prompts (title, tags, follow-ups) get inert JSON.
    if "### Task:" in prompt:
        if "title" in prompt.lower():
            return ("text", '{"title": "E2E chat"}')
        if "follow" in prompt.lower():
            return ("text", '{"follow_ups": []}')
        if "tags" in prompt.lower():
            return ("text", '{"tags": ["e2e"]}')
        return ("text", "{}")
    calls = []
    for line in prompt.splitlines():
        m = CALL.match(line)
        if m:
            calls.append((m.group(1), m.group(2) or "{}"))
    tools = [m for m in msgs[last_user + 1:] if m.get("role") == "tool"]
    step = len(tools)
    if not calls:
        return ("text", "STUB-REPLY")
    if step < len(calls):
        name, args = calls[step]
        real = resolve(name, tool_names(req))
        if real is None:
            return ("text", "STUB-NOTOOL %s offered=%s" % (name, ",".join(tool_names(req))))
        return ("tool", real, args)
    last = text_of(tools[-1].get("content")) if tools else ""
    return ("text", "STUB-DONE " + last[-1500:].replace("\n", " "))


def chunk(delta, finish=None):
    return {
        "id": "chatcmpl-stub", "object": "chat.completion.chunk", "created": int(time.time()), "model": MODEL,
        "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
    }


class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):  # quiet
        pass

    def _json(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        if self.path.rstrip("/").endswith("/models"):
            self._json(200, {"object": "list", "data": [{"id": MODEL, "object": "model", "created": 0, "owned_by": "stub"}]})
        else:
            self._json(200, {"status": "ok"})

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        try:
            req = json.loads(self.rfile.read(n) or b"{}")
        except ValueError:
            return self._json(400, {"error": "bad json"})
        if not self.path.rstrip("/").endswith("/chat/completions"):
            return self._json(404, {"error": "not found"})
        d = decide(req)
        log({"offered": tool_names(req), "decision": d[:2] if d[0] == "tool" else ["text", d[1][:200]],
             "nmsgs": len(req.get("messages") or [])})
        stream = bool(req.get("stream"))
        if d[0] == "tool":
            with _lock:
                _counter[0] += 1
                cid = "call_%d" % _counter[0]
            message = {"role": "assistant", "content": None, "tool_calls": [
                {"id": cid, "type": "function", "function": {"name": d[1], "arguments": d[2]}}]}
            finish = "tool_calls"
            deltas = [chunk({"role": "assistant", "content": None, "tool_calls": [
                {"index": 0, "id": cid, "type": "function", "function": {"name": d[1], "arguments": d[2]}}]}),
                chunk({}, "tool_calls")]
        else:
            message = {"role": "assistant", "content": d[1]}
            finish = "stop"
            deltas = [chunk({"role": "assistant", "content": ""}), chunk({"content": d[1]}), chunk({}, "stop")]
        if not stream:
            return self._json(200, {"id": "chatcmpl-stub", "object": "chat.completion", "created": int(time.time()),
                                    "model": MODEL, "choices": [{"index": 0, "message": message, "finish_reason": finish}],
                                    "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        body = b"".join(b"data: " + json.dumps(c).encode() + b"\n\n" for c in deltas) + b"data: [DONE]\n\n"
        self.wfile.write(("%x\r\n" % len(body)).encode() + body + b"\r\n0\r\n\r\n")
        self.wfile.flush()


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 9000), H).serve_forever()
