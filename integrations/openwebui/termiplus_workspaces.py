"""
title: termiplus workspaces
author: termiplus
description: Provision and manage your termiplus sandbox sessions. Destructive or security-sensitive actions always ask you for confirmation first.
version: 0.4.0
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
# - Desktop, port and console links hold no secret: the browser signs in at
#   the desktop/forward origin (oauth2-proxy) and the server lets only the
#   session's owner in. The model hands them to the user as Markdown links.
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
        Start (or attach to) a termiplus sandbox session. Persistent sessions need the user's permission and an explicit confirmation from the user.
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

    async def open_port(
        self,
        session_id: str,
        port: int,
        __oauth_token__: Optional[dict] = None,
        __user__: Optional[dict] = None,
    ) -> Any:
        """
        Show the user a browser link to a port inside one of their sessions (e.g. a dev server the assistant started on port 3000). Only the session's owner can open it. Not needed for the desktop: request_session already shows it.
        :param session_id: The session_id from list_sessions.
        :param port: The TCP port the app listens on inside the session.
        """
        try:
            port = int(port)
        except (TypeError, ValueError):
            return "termiplus: port must be a number."
        if not 1 <= port <= 65535:
            return "termiplus: port must be between 1 and 65535."
        result = await self._run("list_sessions", {}, __oauth_token__, None, None, __user__)
        try:
            # The JSON list is the first line; desktop links may follow.
            sessions = json.loads(result.split("\n")[0])
        except (ValueError, AttributeError):
            return result if isinstance(result, str) else "termiplus: could not list your sessions."
        match = [s for s in sessions if isinstance(s, dict) and s.get("session_id") == session_id]
        if not match:
            return f"termiplus: no session {session_id!r} of yours."
        base = str(match[0].get("forward_url") or "")
        if not base.startswith(("https://", "http://")):
            return "termiplus: port forwarding is not configured on this server."
        link = _link(f"Open port {port}", f"{base}{port}/")
        if not link:
            return "termiplus: port forwarding is not configured on this server."
        return f"Give the user this link exactly as written (only they can open it): {link}"

    async def open_console(self) -> Any:
        """
        Show the user a link to the termiplus management console, where they can see and manage their workspaces and sessions (and admins the entitlements and network policy).
        """
        url = self.valves.ui_url.strip()
        link = _link("Open the termiplus console", url) if url.startswith(("https://", "http://")) else None
        if not link:
            return "termiplus: the management console is not configured (ui_url valve)."
        return f"Give the user this link exactly as written: {link}"

    async def get_network_policy(self, __oauth_token__: Optional[dict] = None, __user__: Optional[dict] = None) -> Any:
        """
        Admins only: show the terminals' network policy, i.e. the custom DNS entries and the internal destinations terminals may reach. Internet access is always allowed and not listed.
        """
        return await self._run("get_network_policy", {}, __oauth_token__, None, None, __user__)

    async def set_network_policy(
        self,
        dns_entries: list,
        egress_allow: list,
        __oauth_token__: Optional[dict] = None,
        __event_call__: EventCall = None,
        __event_emitter__: EventEmitter = None,
        __user__: Optional[dict] = None,
    ) -> Any:
        """
        Admins only: replace the terminals' whole network policy. Call get_network_policy first and include every existing entry that should stay; anything left out is removed. Always asks the user for confirmation. Host, link-local/metadata and other-terminal addresses can never be opened.
        :param dns_entries: Custom DNS records, e.g. [{"name": "db.internal", "ip": "10.0.0.5"}].
        :param egress_allow: Internal destinations terminals may reach, e.g. [{"cidr": "10.0.0.5/32", "port": 5432, "proto": "tcp"}]. proto is tcp or udp.
        """
        if not isinstance(dns_entries, list) or not isinstance(egress_allow, list):
            return "termiplus: dns_entries and egress_allow must both be lists (use [] for none)."
        policy = {"dns_entries": dns_entries, "egress_allow": egress_allow}
        return await self._run(
            "set_network_policy", {"policy": policy}, __oauth_token__, __event_call__, __event_emitter__, __user__
        )

    async def manage_group(
        self,
        action: str,
        group: str = "",
        __oauth_token__: Optional[dict] = None,
        __user__: Optional[dict] = None,
    ) -> Any:
        """
        Admins only: list the termiplus groups (action "list"), or the members of one group (action "members" with group). Groups decide who may use persistent workspaces (termiplus-persist), administer termiplus (termiplus-admin), and which workspaces someone sees.
        :param action: "list" or "members".
        :param group: The group name for "members", e.g. termiplus-persist.
        """
        args: dict = {"action": action}
        if group:
            args["group"] = group
        return await self._run("manage_group", args, __oauth_token__, None, None, __user__)

    async def manage_user(
        self,
        action: str,
        username: str,
        group: str = "",
        __oauth_token__: Optional[dict] = None,
        __event_call__: EventCall = None,
        __event_emitter__: EventEmitter = None,
        __user__: Optional[dict] = None,
    ) -> Any:
        """
        Admins only: show a user's termiplus groups (action "show"), or add/remove them to/from a termiplus group (actions "add_to_group" / "remove_from_group" with group). Changes always ask the user for confirmation and apply at the affected user's next sign-in. Users themselves are created in the identity provider, not here.
        :param action: "show", "add_to_group" or "remove_from_group".
        :param username: The user's login name in the identity provider.
        :param group: The termiplus group for add/remove, e.g. termiplus-persist.
        """
        args: dict = {"action": action, "username": username}
        if group:
            args["group"] = group
        return await self._run("manage_user", args, __oauth_token__, __event_call__, __event_emitter__, __user__)

    async def get_audit_log(
        self,
        actor: str = "",
        tool: str = "",
        since: str = "",
        limit: int = 50,
        __oauth_token__: Optional[dict] = None,
        __user__: Optional[dict] = None,
    ) -> Any:
        """
        Admins only: show who changed what in termiplus and when (sessions started or ended, workspace entitlements, network policy, group membership), newest first, with the outcome (ok, error or denied).
        :param actor: Only entries by this user id.
        :param tool: Only this action, e.g. set_network_policy, manage_user or request_session.
        :param since: Only entries at or after this time, in RFC 3339 form, e.g. 2026-10-16T00:00:00Z.
        :param limit: How many entries to show, 1 to 500 (default 50).
        """
        args: dict = {"limit": limit}
        for k, v in (("actor", actor), ("tool", tool), ("since", since)):
            if v:
                args[k] = v
        return await self._run("get_audit_log", args, __oauth_token__, None, None, __user__)

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
            return _with_desktop_links(text)
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


def _with_desktop_links(text: str) -> str:
    """Adds an "Open desktop" link for every desktop session in a result. The
    links hold no secret: the desktop sign-in and the owner check happen on
    the server. A stream_password from an older server is dropped."""
    try:
        data = json.loads(text)
    except ValueError:
        return text
    sessions = data if isinstance(data, list) else [data]
    links, changed = [], False
    for s in sessions:
        if not isinstance(s, dict):
            continue
        if s.pop("stream_password", None) is not None:
            changed = True
        url = str(s.get("stream_url") or "")
        if not url:
            continue
        link = _link(f"Open desktop ({s.get('workspace', 'desktop')})", url)
        if link:
            links.append(link)
        else:
            s.pop("stream_url")
            changed = True
    if not links and not changed:
        return text
    out = json.dumps(data, separators=(",", ":"))
    if links:
        out += (
            "\nGive the user these links exactly as written; they open the desktop in the browser after the "
            "usual sign-in, with no password. Nothing else is needed to open the desktop.\n" + "\n".join(links)
        )
    return out


async def _emit(emitter: EventEmitter, event: dict) -> None:
    if emitter is not None:
        try:
            await emitter(event)
        except Exception:
            pass
