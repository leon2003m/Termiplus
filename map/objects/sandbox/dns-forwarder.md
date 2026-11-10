---
type: object
cluster: sandbox
universe: live
status: verified
verified: 2026-11-04 (native integration)
entity: deploy/dns-forwarder/entrypoint.sh
---

# DNS forwarder

A dnsmasq container at 172.30.0.3 on `ot-sandbox`, the only resolver terminals may use (netpolicy overlay). It forwards to `DNS_UPSTREAM` and serves the admin's DNS entries.

## Why this shape

Terminals must resolve names without reaching internal resolvers directly, and DNS must never be bypassed to a public resolver. So there is one forwarder:

- Its upstream is required and is the operator's own resolver. It is never hard-coded and never public.
- Its address is MAC/IP/host-port pinned in the firewall. The trusted Compose
  `dns-forwarder` label is required; a managed workspace at `.3` receives no
  forwarder privileges. Compose reserves low addresses outside the dynamic pool.
- The sockproxy refuses any container DNS setting other than exactly this address (`ALLOWED_DNS`).
- The admin's DNS entries reach it as a rendered hosts dir. dnsmasq reloads on change via inotify, so no sidecar is needed.

## Shape

- `DNS_UPSTREAM` (comma-separated, required), `HOSTSDIR` (default `/data/netpolicy/hosts`, read-only mount), `DNS_CACHE_SIZE`, `DNS_REBIND_OK_DOMAINS` (optional, validated in the script, becomes one `--rebind-domain-ok=/d1/d2/`).
- dnsmasq flags: `--no-resolv --no-hosts`, so only the listed upstreams and the hosts dir are used. `--bogus-priv --stop-dns-rebind` stop terminals enumerating internal names (see the 2026-11-10 note).
- Gateway: `ALLOWED_DNS` → `ForwarderDNS`. It is pinned into each container's `Dns` and `OPEN_TERMINAL_RESOLVER`, and it is part of the config hash.

Citations: `deploy/dns-forwarder/entrypoint.sh:1-33`, `deploy/docker-compose.netpolicy.yml:21-59`, `cmd/gateway/main.go:75`, `internal/gateway/lifecycle.go:207` (configHash), `internal/sockproxy/validate.go:166-174`

## Connected to

- **owns:** nothing persistent
- **owned-by:** the netpolicy compose overlay
- **joins:** [Network policy](network-policy.md), [Firewall chain](firewall-chain.md), [sockproxy rules](sockproxy-rules.md), [Terminal container](../workspaces/terminal-container.md)
- **looks-like-but-is-not:** Docker's embedded resolver (127.0.0.11), which terminals do not get with the overlay

## If you change this

- **Hits:** every terminal's config hash (all are recreated); firewall DNS rules; validation checks
- **Does not hit:** MCP, Keycloak

## Surfaces

| Surface | Role |
|---|---|
| terminals | query it |
| gateway | writes its hosts dir |
| operator | sets `DNS_UPSTREAM` to the local resolver |

## See

- Source: `deploy/dns-forwarder/`

## Native integration update (2026-11-04)

Native Browser runs inside the same policy container and remains subject to existing DNS/egress rules. No browser-specific firewall allowance is added. Source: internal/gateway/lifecycle.go.

## Private-answer filtering (2026-11-10)

The forwarder runs `--bogus-priv` (private PTR queries not in the hosts dir get NXDOMAIN) and `--stop-dns-rebind` (private upstream answers dropped). Hosts-dir records are local data and still resolve. `DNS_REBIND_OK_DOMAINS` opts domains out. Test: `test/firewall/test_dns_forwarder.py` (stub dnsmasq). Source: deploy/dns-forwarder/entrypoint.sh.
