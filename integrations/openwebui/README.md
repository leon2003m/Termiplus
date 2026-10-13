# Open WebUI plugin: termiplus workspaces

`termiplus_workspaces.py` is a native Open WebUI **Tool** that lets the
assistant list, start and remove the signed-in user's termiplus sessions
through the MCP control plane (`docs/mcp.md`).

## What it guarantees

- **No auth of its own.** It forwards the user's SSO access token
  (`__oauth_token__`) unchanged. oauth2-proxy in front of the MCP server
  verifies it.
- **Agent proposes, human disposes.** The model only sees four fixed functions
  and cannot set the confirmation flag. When the server answers "confirmation
  required" (persistent sessions, removing a session), the plugin shows an Open
  WebUI dialog and repeats the call only after an explicit **yes**. Any other
  answer, or no dialog at all, cancels.
- With `plugin_key` set it also sends the Open WebUI user id, so sessions use
  the same homes as that user's Open WebUI terminals. Without a user id it
  sends nothing.
- RBAC stays on the server: without the `termiplus-persist` group a persistent
  session is refused even if the user confirms.

## Install

Requirements: Open WebUI **>= 0.8.6** signed in through OIDC (Keycloak), with
`OAUTH_SESSION_TOKEN_ENCRYPTION_KEY` set, and the MCP overlay
(`deploy/docker-compose.mcp.yml`) running on the Open WebUI network.

1. Admin Panel → Workspace → Tools → **+**, paste `termiplus_workspaces.py`, save.
2. In the tool's **Valves**, set `plugin_key` to the content of
   `deploy/secrets/mcp_owui_plugin_key` (sessions then belong to the Open WebUI
   user and share homes with their Open WebUI terminals), and `mcp_url` if it
   differs from the default `http://mcp-auth:4180/`.
3. Enable the tool for the models that should manage workspaces.

## Tests

`make test-plugin` (part of `make check` and CI) runs the plugin against the
real `mcp` binary (stub backend, dev token): dialogs, declined and unclear
answers, injected confirmation flags, role checks and token rejection.
