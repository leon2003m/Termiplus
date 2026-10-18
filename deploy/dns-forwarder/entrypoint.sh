#!/bin/sh
# Start dnsmasq as a pure forwarder for the terminal sandbox.
#
#   - upstream resolver(s) come from DNS_UPSTREAM (comma-separated), never a
#     hardcoded public resolver — the operator points this at their own
#     internal/local resolver (the local-DNS rule).
#   - static records are read from HOSTSDIR, which the gateway renders from the
#     admin network policy. dnsmasq watches that directory with inotify and
#     reloads automatically on change, so no signal or sidecar is needed.
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
IFS=$old_ifs
set +f

exec "$@"
