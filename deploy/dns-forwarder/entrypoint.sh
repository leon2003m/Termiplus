#!/bin/sh
# Start dnsmasq as a pure forwarder for the terminal sandbox.
#
#   - upstream resolver(s) come from DNS_UPSTREAM (comma-separated), never a
#     hardcoded public resolver — the operator points this at their own
#     internal/local resolver (the local-DNS rule).
#   - static records are read from HOSTSDIR, which the gateway renders from the
#     admin network policy. dnsmasq watches that directory with inotify and
#     reloads automatically on change, so no signal or sidecar is needed.
#   - terminals must not enumerate the operator's internal names: --bogus-priv
#     answers private reverse (PTR) lookups NXDOMAIN instead of forwarding them,
#     and --stop-dns-rebind drops upstream answers in private ranges. Records
#     from HOSTSDIR are local data and are unaffected by both options.
#   - DNS_REBIND_OK_DOMAINS (optional, comma-separated) exempts domains from the
#     rebind filter, for operators who allow private egress through netpolicy.
set -eu

: "${DNS_UPSTREAM:?set DNS_UPSTREAM to your internal resolver (never a public one)}"
HOSTSDIR=${HOSTSDIR:-/data/netpolicy/hosts}

# The gateway owns and creates HOSTSDIR; tolerate a read-only mount here.
mkdir -p "$HOSTSDIR" 2>/dev/null || true

set -- dnsmasq --keep-in-foreground --no-resolv --no-hosts \
  --port=53 \
  --hostsdir="$HOSTSDIR" \
  --cache-size="${DNS_CACHE_SIZE:-1000}" \
  --pid-file= \
  --bogus-priv \
  --stop-dns-rebind \
  --log-facility=-

# DNS_UPSTREAM is one or more comma-separated resolver addresses. Disable
# globbing and split on commas; word splitting here is intentional.
set -f
old_ifs=$IFS
IFS=,
for up in $DNS_UPSTREAM; do
  up=$(printf '%s' "$up" | tr -d ' ')
  if [ -n "$up" ]; then
    set -- "$@" --server="$up"
  fi
done

# DNS_REBIND_OK_DOMAINS: strict allowlist of DNS names (lowercase letters,
# digits, '-', '_', dots; labels 1-63 chars; name at most 253). Anything else is
# ignored with a message and never reaches dnsmasq.
ok_domains=
for d in ${DNS_REBIND_OK_DOMAINS:-}; do
  d=$(printf '%s' "$d" | tr -d ' ')
  [ -n "$d" ] || continue
  if [ "${#d}" -gt 253 ] \
    || printf '%s' "$d" | grep -q '[^a-z0-9._-]' \
    || printf '%s' "$d" | grep -Eq '(^\.|\.$|\.\.)' \
    || printf '%s' "$d" | grep -Eq '[^.]{64}'; then
    echo "dns-forwarder: ignoring malformed DNS_REBIND_OK_DOMAINS entry" >&2
    continue
  fi
  ok_domains="$ok_domains$d/"
done
IFS=$old_ifs
set +f
if [ -n "$ok_domains" ]; then
  set -- "$@" "--rebind-domain-ok=/$ok_domains"
fi

exec "$@"
