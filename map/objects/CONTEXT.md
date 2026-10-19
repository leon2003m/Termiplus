# objects/

Nouns of termiplus, one card each, grouped in six clusters. Start at [_index.md](_index.md). Do not list this folder.

| Cluster | Holds | Owning code |
|---|---|---|
| identity | who is calling and what they may see | `internal/mcp/auth.go`, `identities.go`, `entitlements.go`, `directory.go` |
| workspaces | policies, containers, homes | `internal/gateway/`, `internal/naming/`, `deploy/policies.json` |
| sandbox | what a terminal may do and reach | `internal/sockproxy/`, `internal/netpolicy/`, `deploy/firewall.sh`, `deploy/dns-forwarder/` |
| control-plane | MCP tools and what they create | `internal/mcp/` |
| storage | data that outlives a container | `internal/gateway/backups.go` |
| config | how a deployment is assembled | `deploy/`, `cmd/*/main.go` |

A card answers: what it is, why it has this shape, what it touches, and what breaks if you change it. It cites `path:line`. For *how*, follow the citation into the code.
