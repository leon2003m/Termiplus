"""
title: termiplus workspaces
author: termiplus
description: Provision and manage your termiplus sandbox sessions. Destructive or security-sensitive actions always ask you for confirmation first.
version: 0.1.0
required_open_webui_version: 0.8.6
license: see the termiplus repository
"""

# Native Open WebUI tool for the termiplus MCP control plane (docs/mcp.md §7).
#
# - Forwards the signed-in user's OIDC access token (__oauth_token__) unchanged
#   to the MCP endpoint, where oauth2-proxy verifies it. This plugin does no
#   authentication of its own.
# - With the plugin_key valve set, it also names the Open WebUI user
#   (__user__["id"]), so sessions share homes with that user's Open WebUI
#   terminals. Open WebUI fetched the token for that same user.
# - The model only sees the fixed functions below. It can never set the
#   confirmation flag: when the server answers "human confirmation required",
#   the plugin asks the user in an Open WebUI dialog and only repeats the call
#   with "_confirmed": true after an explicit yes.
# - A desktop session's stream password is for the user, never the model: the
#   plugin removes it from the tool result and shows it in an inline embed,
#   which Open WebUI renders for the user but does not send to the model.
# - Needs nothing beyond the standard library, pydantic and fastapi (which Open
#   WebUI ships), so it installs without extra packages.

import asyncio
import html
import json
import urllib.error
import urllib.request
from typing import Any, Awaitable, Callable, Optional

from fastapi.responses import HTMLResponse
from pydantic import BaseModel, Field

CODE_UNAUTHENTICATED = -32001
CODE_FORBIDDEN = -32003
CODE_CONFIRM_REQUIRED = -32010

EventCall = Optional[Callable[[dict], Awaitable[Any]]]
EventEmitter = Optional[Callable[[dict], Awaitable[None]]]


class Tools:
    class Valves(BaseModel):
        mcp_url: str = Field(
            default="http://mcp-auth:4180/",
            description="termiplus MCP endpoint behind oauth2-proxy (the mcp-auth service).",
        )
        plugin_key: str = Field(
            default="",
            description="The deployment's mcp_owui_plugin_key secret. Set it so sessions belong to the Open WebUI user.",
        )
        timeout_seconds: int = Field(
            default=150, description="Request timeout; creating a session can take a while."
        )

    def __init__(self):
        self.valves = self.Valves()

    async def list_workspaces(self, __oauth_token__: Optional[dict] = None, __user__: Optional[dict] = None) -> Any:
        """
        List the termiplus workspaces (sandbox profiles) the user can launch, and whether each one keeps a persistent home.
        """
        return await self._run("list_workspaces", {}, __oauth_token__, None, None, __user__)

    async def list_sessions(self, __oauth_token__: Optional[dict] = None, __user__: Optional[dict] = None) -> Any:
        """
        List the user's termiplus sessions (running or stopped).
        """
        return await self._run("list_sessions", {}, __oauth_token__, None, None, __user__)

    async def request_session(
        self,
        workspace: str,
        persistent: bool = False,
        label: str = "",
        __oauth_token__: Optional[dict] = None,
        __event_call__: EventCall = None,
        __event_emitter__: EventEmitter = None,
        __user__: Optional[dict] = None,
    ) -> Any:
        """
        Start (or attach to) a termiplus sandbox session. Persistent sessions need the user's permission and an explicit confirmation from the user.
        :param workspace: Name of the workspace from list_workspaces.
        :param persistent: True only for a workspace that keeps a persistent home.
        :param label: Optional short label for the session.
        """
        args: dict = {"workspace": workspace, "persistent": bool(persistent)}
        if label:
            args["label"] = label
        return await self._run("request_session", args, __oauth_token__, __event_call__, __event_emitter__, __user__)

    async def destroy_session(
        self,
        session_id: str,
        __oauth_token__: Optional[dict] = None,
        __event_call__: EventCall = None,
        __event_emitter__: EventEmitter = None,
        __user__: Optional[dict] = None,
    ) -> Any:
        """
        Remove one of the user's termiplus sessions. A persistent home volume is kept. Always asks the user for confirmation.
        :param session_id: The session_id from list_sessions.
        """
        return await self._run(
            "destroy_session", {"session_id": session_id}, __oauth_token__, __event_call__, __event_emitter__, __user__
        )

    # --- helpers (underscore: not exposed to the model) ---

    async def _run(
        self,
        tool: str,
        args: dict,
        oauth_token: Optional[dict],
        event_call: EventCall,
        emitter: EventEmitter,
        user: Optional[dict] = None,
    ) -> Any:
        token = (oauth_token or {}).get("access_token")
        if not token:
            return "termiplus: no OIDC sign-in token available. Sign in to Open WebUI with SSO and try again."
        headers = {"Authorization": f"Bearer {token}"}
        if self.valves.plugin_key:
            user_id = (user or {}).get("id")
            if not isinstance(user_id, str) or not user_id:
                return "termiplus: Open WebUI did not provide the user id; nothing was sent."
            headers["X-Termiplus-Owui-Key"] = self.valves.plugin_key
            headers["X-Termiplus-Owui-User"] = user_id
        # Never forward a caller-supplied confirmation flag.
        args = {k: v for k, v in args.items() if not k.startswith("_")}

        await _emit(emitter, {"type": "status", "data": {"description": f"termiplus: {tool}", "done": False}})
        try:
            resp = await self._call(tool, args, headers)
            err = resp.get("error")
            if err and err.get("code") == CODE_CONFIRM_REQUIRED:
                if not await self._confirm(event_call, tool, args):
                    return f"termiplus: {tool} was cancelled by the user."
                resp = await self._call(tool, {**args, "_confirmed": True}, headers)
                err = resp.get("error")
            if err:
                return _describe_error(tool, err)
            result = resp.get("result") or {}
            text = "".join(c.get("text", "") for c in result.get("content", []) if isinstance(c, dict))
            if result.get("isError"):
                return f"termiplus: {tool} failed: {text}"
            return _hide_stream_passwords(text)
        except Exception as e:  # network, HTTP or decoding problems
            return f"termiplus: {tool} failed: {e}"
        finally:
            await _emit(emitter, {"type": "status", "data": {"description": f"termiplus: {tool}", "done": True}})

    async def _confirm(self, event_call: EventCall, tool: str, args: dict) -> bool:
        if event_call is None:
            return False
        details = ", ".join(f"{k}={json.dumps(v)}" for k, v in args.items()) or "no arguments"
        answer = await event_call(
            {
                "type": "confirmation",
                "data": {
                    "title": f"termiplus: confirm {tool}",
                    "message": f"The assistant wants to run {tool} ({details}). Allow it?",
                },
            }
        )
        # Fail closed: only an explicit True counts as approval.
        return answer is True

    async def _call(self, tool: str, args: dict, auth_headers: dict) -> dict:
        body = json.dumps(
            {"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": tool, "arguments": args}}
        ).encode()
        req = urllib.request.Request(
            self.valves.mcp_url,
            data=body,
            method="POST",
            headers={"Content-Type": "application/json", **auth_headers},
        )

        def do() -> dict:
            try:
                with urllib.request.urlopen(req, timeout=self.valves.timeout_seconds) as r:
                    return json.loads(r.read(1 << 20))
            except urllib.error.HTTPError as e:
                if e.code == 401:
                    return {"error": {"code": CODE_UNAUTHENTICATED, "message": "unauthenticated"}}
                raise

        return await asyncio.to_thread(do)


def _describe_error(tool: str, err: dict) -> str:
    code, msg = err.get("code"), err.get("message", "")
    if code == CODE_UNAUTHENTICATED:
        return "termiplus: your sign-in was not accepted (expired or wrong audience). Sign in again."
    if code == CODE_FORBIDDEN:
        return f"termiplus: you are not allowed to do this ({msg})."
    if code == CODE_CONFIRM_REQUIRED:
        return f"termiplus: {tool} still requires confirmation and was not run."
    return f"termiplus: {tool} failed: {msg}"


def _hide_stream_passwords(text: str) -> Any:
    """Moves desktop stream passwords out of the model-visible result into an
    inline embed for the user. Returns text unchanged when there are none."""
    try:
        data = json.loads(text)
    except ValueError:
        return text
    sessions = data if isinstance(data, list) else [data]
    desktops = []
    for s in sessions:
        if isinstance(s, dict) and s.get("stream_password"):
            desktops.append((str(s.get("session_id", "")), str(s.get("stream_url", "")), str(s.pop("stream_password"))))
            s["stream_password"] = "(shown to the user, not to you)"
    if not desktops:
        return text
    rows = "".join(
        "<div class='d'><b>{sid}</b><br>"
        "<a href='{url}' target='_blank' rel='noopener noreferrer'>Open desktop</a> <code>{url}</code><br>"
        "User <code>user</code>, password "
        "<input readonly value='{pw}' size='70' onclick='this.select()'></div>".format(
            sid=html.escape(sid), url=html.escape(url, quote=True), pw=html.escape(pw, quote=True)
        )
        for sid, url, pw in desktops
        if url.startswith(("https://", "http://"))
    )
    page = (
        "<!doctype html><meta charset='utf-8'><style>body{font:14px sans-serif;margin:8px}"
        ".d{margin:6px 0;padding:8px;border:1px solid #8884;border-radius:6px}code{font-size:12px}</style>"
        + rows
    )
    context = json.dumps(data if isinstance(data, list) else sessions[0], separators=(",", ":"))
    context += "\nThe desktop link and password are shown to the user in the chat; never ask for or repeat the password."
    return HTMLResponse(content=page, headers={"Content-Disposition": "inline"}), context


async def _emit(emitter: EventEmitter, event: dict) -> None:
    if emitter is not None:
        try:
            await emitter(event)
        except Exception:
            pass
