"""
title: termiplus workspaces
author: termiplus
description: Provision and manage your termiplus sandbox sessions. Lifecycle, backups and view-only screen shares; administration is human-only.
version: 0.6.0
required_open_webui_version: 0.13.0
license: see the termiplus repository
"""

# Native Open WebUI tool for the termiplus MCP control plane (docs/mcp.md).
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
# - Desktop, browser, files and terminal interactions use Open WebUI's native
#   terminal connection. This plugin adds only Termiplus lifecycle/storage tools
#   and a link to the human management console.
# - Needs nothing beyond the standard library and pydantic (which Open WebUI
#   ships), so it installs without extra packages.

import asyncio
import json
import re
import urllib.error
import urllib.request
from typing import Any, Awaitable, Callable, Optional

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
        ui_url: str = Field(
            default="",
            description="Browser URL of the termiplus management UI, e.g. https://desk.example.com/ui/ (optional).",
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
        persistent: Optional[bool] = None,
        label: str = "",
        __oauth_token__: Optional[dict] = None,
        __event_call__: EventCall = None,
        __event_emitter__: EventEmitter = None,
        __user__: Optional[dict] = None,
    ) -> Any:
        """
        Provision a termiplus container. This does not select a terminal connection in the chat. For a shared human/AI desktop, the user selects the workspace in Open WebUI and opens Desktop; native desktop_screenshot starts that desktop. Prefer native tools for the currently selected workspace. Persistent provisioning requires explicit human confirmation.
        :param workspace: Name of the workspace from list_workspaces.
        :param persistent: Leave out to use the workspace's own setting.
        :param label: Optional short label for the session.
        """
        args: dict = {"workspace": workspace}
        if persistent is not None:
            args["persistent"] = bool(persistent)
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

    async def reset_workspace(
        self,
        session_id: str,
        __oauth_token__: Optional[dict] = None,
        __event_call__: EventCall = None,
        __event_emitter__: EventEmitter = None,
        __user__: Optional[dict] = None,
    ) -> Any:
        """
        Start one of the user's sessions over from a fresh container. A persistent workspace keeps everything in /home/user (programs installed elsewhere are lost); a temporary sandbox is removed. Use it when a workspace is broken. Always asks the user for confirmation.
        :param session_id: The session_id from list_sessions.
        """
        return await self._run(
            "reset_workspace", {"session_id": session_id}, __oauth_token__, __event_call__, __event_emitter__, __user__
        )

    async def manage_snapshots(
        self,
        action: str,
        workspace: str,
        __oauth_token__: Optional[dict] = None,
        __event_call__: EventCall = None,
        __event_emitter__: EventEmitter = None,
        __user__: Optional[dict] = None,
    ) -> Any:
        """
        List or create home snapshots before risky changes. Creation never deletes an older snapshot. Restore, delete and pruning belong to the human management console.
        :param action: "list" or "create".
        :param workspace: The workspace name from list_workspaces.
        """
        if action not in ("list", "create"):
            return "termiplus: the AI may only list or create snapshots. Open the management console to restore or delete."
        args: dict = {"action": action, "workspace": workspace}
        return await self._run("manage_snapshots", args, __oauth_token__, __event_call__, __event_emitter__, __user__)

    async def share_desktop(
        self,
        action: str,
        session_id: str = "",
        minutes: int = 60,
        for_user: str = "",
        share_id: str = "",
        __oauth_token__: Optional[dict] = None,
        __event_call__: EventCall = None,
        __event_emitter__: EventEmitter = None,
        __user__: Optional[dict] = None,
    ) -> Any:
        """
        Let other people watch one of the user's desktop sessions, view only (they cannot type or click), through a link that expires. "create" makes a link (always asks the user for confirmation), "list" shows the active links, "revoke" ends one.
        :param action: "create", "list" or "revoke".
        :param session_id: For create: the desktop session_id from list_sessions.
        :param minutes: For create: how long the link works, 5 to 1440 (default 60).
        :param for_user: For create, optional: only this user name may watch. Empty: anyone who can sign in to termiplus and has the link.
        :param share_id: For revoke: the share_id from the list.
        """
        args: dict = {"action": action}
        if action == "create":
            args["session_id"] = session_id
            args["minutes"] = minutes
            if for_user:
                args["for_user"] = for_user
        if share_id:
            args["share_id"] = share_id
        return await self._run("share_desktop", args, __oauth_token__, __event_call__, __event_emitter__, __user__)


    async def open_console(self) -> Any:
        """
        Show the user a link to the termiplus management console, where they can see and manage their workspaces and sessions (and admins the entitlements and network policy).
        """
        url = self.valves.ui_url.strip()
        link = _link("Open the termiplus console", url) if url.startswith(("https://", "http://")) else None
        if not link:
            return "termiplus: the management console is not configured (ui_url valve)."
        return f"Give the user this link exactly as written: {link}"


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
            if tool == "request_session" and "persistent" not in args:
                # Models rarely know a workspace's persistence; take it from the
                # workspace so the server still applies its role check and the
                # confirmation for persistent sessions.
                args = {**args, "persistent": await self._workspace_persistent(args.get("workspace"), headers)}
            resp = await self._call(tool, args, headers)
            err = resp.get("error")
            if err and err.get("code") == CODE_CONFIRM_REQUIRED:
                if not await self._confirm(event_call, tool, args):
                    # Without this, models retry the call in a loop.
                    return (
                        f"termiplus: {tool} was cancelled by the user. Do not call it again; "
                        "tell the user it was cancelled and wait for them to ask again."
                    )
                resp = await self._call(tool, {**args, "_confirmed": True}, headers)
                err = resp.get("error")
            if err:
                return _describe_error(tool, err)
            result = resp.get("result") or {}
            text = "".join(c.get("text", "") for c in result.get("content", []) if isinstance(c, dict))
            if result.get("isError"):
                return f"termiplus: {tool} failed: {text}"
            return _with_result_links(tool, _with_workspace_guidance(text))
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

    async def _workspace_persistent(self, workspace: Any, headers: dict) -> bool:
        """The persistent flag of workspace, False when it is not listed (the server then rejects or allows it)."""
        resp = await self._call("list_workspaces", {}, headers)
        result = resp.get("result") or {}
        text = "".join(c.get("text", "") for c in result.get("content", []) if isinstance(c, dict))
        try:
            listed = json.loads(text) if text else []
        except ValueError:
            return False
        for ws in listed if isinstance(listed, list) else []:
            if isinstance(ws, dict) and ws.get("name") == workspace:
                return ws.get("persistent") is True
        return False

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


# Links are handed to the model as Markdown; only plain http(s) URLs qualify.
_SAFE_URL = re.compile(r"^https?://[^\s()<>\[\]'\"`\\]+$")


def _link(label: str, url: str) -> Optional[str]:
    return f"[{label}]({url})" if _SAFE_URL.match(url) else None


def _with_workspace_guidance(text: str) -> str:
    """Never advertise legacy streams or forward old desktop credentials."""
    try:
        data = json.loads(text)
    except (ValueError, TypeError):
        return text
    sessions = data if isinstance(data, list) else [data]
    changed, desktop = False, False
    for session in sessions:
        if not isinstance(session, dict):
            continue
        for key in ("stream_password", "stream_url", "forward_url"):
            if key in session:
                session.pop(key)
                changed = True
        desktop = desktop or session.get("desktop") is True
    if changed:
        text = json.dumps(data, separators=(",", ":"))
    if desktop:
        text += "\nUse this workspace's terminal connection in Open WebUI and open its Desktop tab. The native desktop_screenshot and desktop_action tools share that screen with the user. Provisioning a workspace does not switch the chat's connection."
    return text


def _with_result_links(tool: str, text: str) -> str:
    """Adds Markdown links for share links and home downloads in a result."""
    if tool not in ("share_desktop", "manage_snapshots"):
        return text
    try:
        data = json.loads(text.split("\n", 1)[0])
    except ValueError:
        return text
    if not isinstance(data, dict):
        return text
    links = []
    if tool == "share_desktop" and data.get("url"):
        link = _link("Shared desktop (view only)", str(data["url"]))
        if link:
            links.append(link)
    if tool == "manage_snapshots":
        if data.get("current_home_download_url"):
            link = _link("Download the current home", str(data["current_home_download_url"]))
            if link:
                links.append(link)
        snaps = data.get("snapshots") if isinstance(data.get("snapshots"), list) else [data]
        for sn in snaps:
            if isinstance(sn, dict) and sn.get("download_url"):
                link = _link(f"Download snapshot {sn.get('id', '')}", str(sn["download_url"]))
                if link:
                    links.append(link)
    if not links:
        return text
    return text + "\nGive the user these links exactly as written; they open in the browser after the usual sign-in.\n" + "\n".join(links)


async def _emit(emitter: EventEmitter, event: dict) -> None:
    if emitter is not None:
        try:
            await emitter(event)
        except Exception:
            pass
