# Effects: I am changing X, what else moves?

Find the row for what you are changing. Open the cards it names, read their **If you change this**, then the code. First-order only: each card lists its own neighbours.

## Inside this repo

| Changing | Open | Watch for |
|---|---|---|
| Container create body, mounts, caps, runtime | [Sockproxy rules](../objects/sandbox/sockproxy-rules.md), [sockproxy-create](../processes/sockproxy-create.md), [Terminal container](../objects/workspaces/terminal-container.md) | security boundary: needs a test, a Security impact section and maintainer approval ([AGENTS.md](../../AGENTS.md)) |
| `policies.json` fields, policy loading | [Policy](../objects/workspaces/policy.md), [request-to-terminal](../processes/request-to-terminal.md) | the sockproxy rebuilds the create body and must accept the new field |
| Container, volume or helper naming | [Terminal container](../objects/workspaces/terminal-container.md), [Home volume](../objects/workspaces/home-volume.md), [Archive helper](../objects/workspaces/archive-helper.md) | existing homes on live hosts keep their old names; a rename orphans them |
| Gateway auth, API key, `X-User-Id` handling | [Service keys](../objects/identity/service-keys.md), [Principal](../objects/identity/principal.md), [request-to-terminal](../processes/request-to-terminal.md) | security boundary; never hand-write token checks, oauth2-proxy does that |
| Network policy format, deny list | [Network policy](../objects/sandbox/network-policy.md), [netpolicy-apply](../processes/netpolicy-apply.md), [Firewall chain](../objects/sandbox/firewall-chain.md) | Go renderer and `firewall.sh` parse the same file |
| `deploy/firewall.sh`, systemd units | [Firewall chain](../objects/sandbox/firewall-chain.md), [DNS forwarder](../objects/sandbox/dns-forwarder.md) | installed hosts run the script from their checkout (below) |
| DNS forwarder | [DNS forwarder](../objects/sandbox/dns-forwarder.md) | upstream is always the operator's own resolver, never a public one |
| An MCP tool (add, gate, confirm) | [MCP tools](../objects/control-plane/mcp-tools.md), [mcp-tool-call](../processes/mcp-tool-call.md), [Audit log](../objects/control-plane/audit-log.md) | the Open WebUI plugin and the console list tools by name |
| Identity mapping, entitlements, roles | [IdentityStore](../objects/identity/identity-store.md), [Entitlements](../objects/identity/entitlements.md), [Principal](../objects/identity/principal.md), [Directory](../objects/identity/directory.md) | fail closed; Keycloak group names are referenced by live realms |
| Desktop streams, shares | [Share](../objects/control-plane/share.md), [desktop-stream](../processes/desktop-stream.md) | origins: console, desktop and forward stay separate |
| Port forwards, Open Terminal's `/proxy/<port>/` | [port-forward](../processes/port-forward.md) | never forward the API or desktop port; `/proxy` never reaches a terminal, so Open WebUI never serves user apps on its own origin |
| Backups, archives, departure | [Backup](../objects/storage/backup.md), [Departed](../objects/control-plane/departed.md), [user-departure](../processes/user-departure.md) | user data retention; `ALLOW_HOME_REMOVE` |
| Compose files, overlays, env names | [Compose overlays](../objects/config/compose-overlays.md) | live hosts combine overlays and a local override (below) |
| Release workflow, image names, CHANGELOG headings | [release](../processes/release.md) | the maintainer tags; agents never do |
| A cited source line | the card that cites it | update the card in the same PR, or mark it `stale` in [objects/_index.md](../objects/_index.md) |

## Outside this repo (things that point in)

These are not in the tree, and a change can break them silently. Name the impact in the PR.

| Consumer | Depends on |
|---|---|
| A live compose project on the host | base `deploy/docker-compose.yml` plus overlays and a git-excluded local override; service, volume, network and secret names; env variable names |
| Installed firewall units | `/opt/termiplus/deploy/firewall.sh` and the `termiplus-firewall*.{service,path,timer}` units run straight from the checkout; script arguments, `egress.rules` path |
| Open WebUI terminal connections | the gateway URL shape `/p/<policy>/…`, the gateway API key, `X-User-Id` / `X-Session-Id` headers, policy names; the Ports panel's `/ports` and `/proxy/<port>/` (redirected to the forward origin) |
| The Open WebUI MCP tool (plugin) | its valves `mcp_url`, `plugin_key`, `ui_url`; tool names and argument shapes; the `_confirmed` convention |
| Reverse proxy in front | the oauth2-proxy ports and paths for MCP, console, desktop and forward origins |
| Keycloak realm | client ids, `termiplus-*` group names, the roles `termiplus-persist` / `termiplus-admin` |
| A local `deploy/policies.json` edit | policy names and fields; operators keep a live edit that is not committed |
| Existing Docker volumes and backups | volume names `ot-home-…` and the `BACKUP_DIR` layout |
