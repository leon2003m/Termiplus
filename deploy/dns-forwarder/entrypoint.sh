#!/bin/sh
# Start dnsmasq as a pure forwarder for the terminal sandbox.
#
#   - upstream resolver(s) come from DNS_UPSTREAM (comma-separated), never a
#     hardcoded public resolver — the operator points this at their own
#     internal/local resolver (the local-DNS rule).
#   - static records are read from HOSTSDIR, which the gateway renders from the
#     admin network policy. dnsmasq watches that directory with inotify and
#     reloads automatically on change, so no signal or sidecar is needed.
#   - terminals must not resolve private addresses or PTRs: --bogus-priv
#     answers private reverse (PTR) lookups NXDOMAIN instead of forwarding them,
#     and --stop-dns-rebind drops upstream answers in private ranges. Records
#     from HOSTSDIR are local data and are unaffected by both options.
#   - DNS_REBIND_OK_DOMAINS (optional, comma-separated) exempts domains from the
#     rebind filter, for operators who allow private egress through netpolicy.
#   - DNS_LOCAL_ZONES (optional, comma-separated) are zones that are never
#     forwarded: only local data answers, the rest is NXDOMAIN, so names in them
#     cannot be enumerated. Other internal zones may still be distinguishable.
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

# domain_list NAME VALUE: strict allowlist of DNS names (lowercase letters,
# digits, '-', '_', dots; labels 1-63 chars; name at most 253). Only leading and
# trailing whitespace of an entry is trimmed; interior whitespace is rejected.
# Anything invalid is ignored with a message naming NAME and never reaches
# dnsmasq. Sets DOMAIN_LIST to the accepted names, each followed by '/'.
# Runs with IFS=, and globbing off (set above).
domain_list() {
  DOMAIN_LIST=
  for d in $2; do
    while case $d in [[:space:]]*) true ;; *) false ;; esac; do d=${d#?}; done
    while case $d in *[[:space:]]) true ;; *) false ;; esac; do d=${d%?}; done
    [ -n "$d" ] || continue
    case $d in
      *[[:space:]]*)
        echo "dns-forwarder: ignoring malformed $1 entry" >&2
        continue
        ;;
    esac
    if [ "${#d}" -gt 253 ] \
      || printf '%s' "$d" | grep -q '[^a-z0-9._-]' \
      || printf '%s' "$d" | grep -Eq '(^\.|\.$|\.\.)' \
      || printf '%s' "$d" | grep -Eq '[^.]{64}'; then
      echo "dns-forwarder: ignoring malformed $1 entry" >&2
      continue
    fi
    DOMAIN_LIST="$DOMAIN_LIST$d/"
  done
}

# DNS_REBIND_OK_DOMAINS: exempt from the rebind filter.
domain_list DNS_REBIND_OK_DOMAINS "${DNS_REBIND_OK_DOMAINS:-}"
ok_domains=$DOMAIN_LIST
# DNS_LOCAL_ZONES: never forwarded upstream; answered from local data only,
# everything else in the zone is NXDOMAIN. One --local option may list several
# domains (--local is an alias of --server; /d1/d2/ with no address).
domain_list DNS_LOCAL_ZONES "${DNS_LOCAL_ZONES:-}"
local_zones=$DOMAIN_LIST
IFS=$old_ifs
set +f
if [ -n "$ok_domains" ]; then
  set -- "$@" "--rebind-domain-ok=/$ok_domains"
fi
if [ -n "$local_zones" ]; then
  set -- "$@" "--local=/$local_zones"
fi

exec "$@"
