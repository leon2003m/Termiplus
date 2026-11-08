#!/usr/bin/env bash
# One-time preparation for the alpha features (MCP control plane, desktops,
# management console) with the bundled Keycloak:
#
#   WEBUI_URL=https://chat.example.com \
#   KEYCLOAK_PUBLIC_URL=https://sso.example.com \
#   DESKTOP_PUBLIC_URL=https://desk.example.com \
#   DESKTOP_STREAM_URL=https://desktops.example.com \
#   deploy/setup-alpha.sh
#
# Secrets go to TERMIPLUS_SECRETS_DIR, an absolute path outside the source
# checkout (e.g. /opt/docker/termiplus/secrets; create it first with
# `sudo install -d -o "$USER" -m 0700 DIR`). Without it they go to
# deploy/secrets/, which only suits tests and quick trials.
#
# Creates every missing secret there (existing files are never overwritten),
# renders realm-termiplus.json for Keycloak's first start, writes deploy/.env
# for docker compose and prints what to set in Open WebUI and in the plugin.
# Runs no containers and changes nothing outside deploy/ and the secrets
# directory.
set -euo pipefail

DEPLOY="$(cd "$(dirname "$0")" && pwd)"
# Secrets directory: TERMIPLUS_SECRETS_DIR from the environment, else from
# deploy/.env (as compose reads it), else deploy/secrets.
SECRETS="${TERMIPLUS_SECRETS_DIR:-}"
if [ -z "$SECRETS" ] && [ -f "$DEPLOY/.env" ]; then
  SECRETS="$(sed -n 's/^TERMIPLUS_SECRETS_DIR=//p' "$DEPLOY/.env" | tail -n1)"
  SECRETS="${SECRETS#\"}"; SECRETS="${SECRETS%\"}"
  SECRETS="${SECRETS#\'}"; SECRETS="${SECRETS%\'}"
  # Compose strips inline comments and expands variables; this parser does
  # not, so refuse anything but a plain path rather than guess.
  case "$SECRETS" in
    *[[:space:]#\$]*)
      echo "$(basename "$0"): TERMIPLUS_SECRETS_DIR in deploy/.env must be a plain absolute path (no comments, spaces or variables)" >&2
      exit 1 ;;
  esac
fi
SECRETS_SET="$SECRETS"
SECRETS="${SECRETS:-$DEPLOY/secrets}"
case "$SECRETS" in
  /*) ;;
  *) echo "$(basename "$0"): TERMIPLUS_SECRETS_DIR must be an absolute path" >&2; exit 1 ;;
esac
: "${WEBUI_URL:?set WEBUI_URL (how browsers reach Open WebUI, https://...)}"
: "${KEYCLOAK_PUBLIC_URL:?set KEYCLOAK_PUBLIC_URL (https://... in front of Keycloak)}"
: "${DESKTOP_PUBLIC_URL:?set DESKTOP_PUBLIC_URL (https://... in front of desk-auth)}"
: "${DESKTOP_STREAM_URL:?set DESKTOP_STREAM_URL (https://... in front of desk-stream-auth, its own origin)}"
for u in "$WEBUI_URL" "$KEYCLOAK_PUBLIC_URL" "$DESKTOP_PUBLIC_URL" "$DESKTOP_STREAM_URL"; do
  case "$u" in
    https://*) ;;
    *) echo "setup-alpha: $u must be an https:// URL (cookies are Secure)" >&2; exit 1 ;;
  esac
  case "$u" in */) echo "setup-alpha: $u must not end with /" >&2; exit 1 ;; esac
done

if [ "${DESKTOP_STREAM_URL#https://}" = "${DESKTOP_PUBLIC_URL#https://}" ]; then
  echo "setup-alpha: DESKTOP_STREAM_URL must be another origin than DESKTOP_PUBLIC_URL (desktop content must not run on the console's origin)" >&2
  exit 1
fi

umask 077
mkdir -p "$SECRETS"
# The files below are readable for the containers' non-root users (bind
# mounts do not consult the directory); the directory keeps everyone else on
# the host out, also when it was created earlier with a looser umask.
chmod 0700 "$SECRETS"

# secret NAME BYTES: create $SECRETS/NAME with random hex, unless present.
secret() {
  local f="$SECRETS/$1"
  if [ -s "$f" ]; then
    echo "keep     secrets/$1"
  else
    openssl rand -hex "$2" | tr -d '\n' >"$f"
    echo "created  secrets/$1"
  fi
}
secret control_api_key 32
secret mcp_owui_plugin_key 32
secret mcp_oidc_client_secret 32
secret mcp_oidc_cookie_secret 16
secret mcp_kc_admin_client_secret 32
secret desk_oidc_client_secret 32
secret desk_oidc_cookie_secret 16
secret dstream_oidc_cookie_secret 16
secret owui_oidc_client_secret 32
secret owui_oauth_session_key 32
secret keycloak_db_password 32
secret keycloak_admin_password 24

# The services read these as non-root users.
chmod 0644 "$SECRETS"/control_api_key "$SECRETS"/mcp_* "$SECRETS"/desk_* "$SECRETS"/dstream_* "$SECRETS"/keycloak_db_password

if [ -s "$SECRETS/keycloak.env" ]; then
  echo "keep     secrets/keycloak.env"
else
  {
    echo "KC_BOOTSTRAP_ADMIN_USERNAME=admin"
    echo "KC_BOOTSTRAP_ADMIN_PASSWORD=$(cat "$SECRETS/keycloak_admin_password")"
    echo "KC_DB_PASSWORD=$(cat "$SECRETS/keycloak_db_password")"
  } >"$SECRETS/keycloak.env"
  echo "created  secrets/keycloak.env"
fi

# The realm import only runs while the realm does not exist yet, so later
# renders do not touch a running Keycloak.
python3 - "$DEPLOY/keycloak/realm-termiplus.json.tmpl" "$SECRETS/realm-termiplus.json" <<PY
import json, sys
src, dst = sys.argv[1], sys.argv[2]
values = {
    "__WEBUI_URL__": "$WEBUI_URL",
    "__DESKTOP_PUBLIC_URL__": "$DESKTOP_PUBLIC_URL",
    "__DESKTOP_STREAM_URL__": "$DESKTOP_STREAM_URL",
    "__OWUI_CLIENT_SECRET__": open("$SECRETS/owui_oidc_client_secret").read(),
    "__MCP_CLIENT_SECRET__": open("$SECRETS/mcp_oidc_client_secret").read(),
    "__MCP_ADMIN_CLIENT_SECRET__": open("$SECRETS/mcp_kc_admin_client_secret").read(),
    "__DESK_CLIENT_SECRET__": open("$SECRETS/desk_oidc_client_secret").read(),
}
text = open(src).read()
for k, v in values.items():
    text = text.replace(k, v)
json.loads(text)  # must still be valid JSON
open(dst, "w").write(text)
PY
chmod 0644 "$SECRETS/realm-termiplus.json" # read by Keycloak (uid 1000)
echo "rendered secrets/realm-termiplus.json"

if [ ! -d "$DEPLOY/mcp-data" ]; then
  mkdir -p "$DEPLOY/mcp-data"
  echo "created  mcp-data/ - make it writable for the MCP server:"
  echo "           sudo chown 65534:65534 $DEPLOY/mcp-data"
fi
# Home snapshots and departed-user archives (BACKUP_DIR of the gateway). Owner
# only: the archives hold users' files.
if [ ! -d "$DEPLOY/backups" ]; then
  mkdir -p "$DEPLOY/backups"
  chmod 0700 "$DEPLOY/backups"
  echo "created  backups/ - make it writable for the gateway:"
  echo "           sudo chown 65534:65534 $DEPLOY/backups"
fi
# The MCP server refuses to start without an entitlements file (MCP_AUTH=proxy).
# Start closed: no workspace for anyone until an admin grants them in the
# console (Admin -> Entitlements).
if [ ! -e "$DEPLOY/mcp-data/entitlements.json" ]; then
  if printf '{\n  "default": "deny",\n  "workspaces": {}\n}\n' >"$DEPLOY/mcp-data/entitlements.json" 2>/dev/null; then
    echo "created  mcp-data/entitlements.json (deny all; grant workspaces in the console)"
  else
    echo "cannot write mcp-data/entitlements.json; create it from deploy/entitlements.example.json"
  fi
fi

ISSUER="$KEYCLOAK_PUBLIC_URL/realms/termiplus"
ENVFILE="$DEPLOY/.env"
if [ -e "$ENVFILE" ] && ! grep -q '^# written by setup-alpha.sh' "$ENVFILE"; then
  echo "deploy/.env exists and was not written by this script; add these yourself:"
  ENVFILE=/dev/stdout
fi
{
  echo "# written by setup-alpha.sh"
  echo "MCP_OIDC_ISSUER_URL=$ISSUER"
  echo "KEYCLOAK_PUBLIC_URL=$KEYCLOAK_PUBLIC_URL"
  echo "DESKTOP_PUBLIC_URL=$DESKTOP_PUBLIC_URL"
  echo "DESKTOP_STREAM_URL=$DESKTOP_STREAM_URL"
  [ -z "$SECRETS_SET" ] || echo "TERMIPLUS_SECRETS_DIR=$SECRETS"
} >"$ENVFILE"
[ "$ENVFILE" = /dev/stdout ] || echo "wrote    deploy/.env"

cat <<EOF

Next steps
==========
1. TLS reverse proxy: $KEYCLOAK_PUBLIC_URL -> 127.0.0.1:8180 (Keycloak),
   $DESKTOP_PUBLIC_URL -> 127.0.0.1:4181 (desk-auth, console),
   $DESKTOP_STREAM_URL -> 127.0.0.1:4183 (desk-stream-auth, screen shares).
   Open WebUI and the oauth2-proxies must reach $ISSUER under that same URL.
2. Start (TERMINAL_IMAGE = the complete desktop image; the
   mcp-users overlay enables human administration of group membership):
     docker compose -f docker-compose.yml -f docker-compose.mcp.yml \\
       -f docker-compose.desktop.yml -f docker-compose.keycloak.yml \\
       -f docker-compose.mcp-users.yml up -d
3. Keycloak admin console ($KEYCLOAK_PUBLIC_URL/admin, user admin, password in
   $SECRETS/keycloak_admin_password): realm "termiplus", create users,
   put them into termiplus-persist / termiplus-admin as needed.
4. Open WebUI environment (then restart it). Existing local accounts with the
   same e-mail are merged, so their Open WebUI ids - and terminal homes - stay.
   offline_access keeps the termiplus tool working while Open WebUI's own
   login lasts; without it the token Open WebUI forwards ends with Keycloak's
   browser session (30 min idle) and the tool asks users to sign in again:
     OAUTH_CLIENT_ID=owui
     OAUTH_CLIENT_SECRET=<$SECRETS/owui_oidc_client_secret>
     OPENID_PROVIDER_URL=$ISSUER/.well-known/openid-configuration
     OAUTH_PROVIDER_NAME=Keycloak
     OAUTH_SCOPES=openid email profile offline_access
     ENABLE_OAUTH_SIGNUP=true
     OAUTH_MERGE_ACCOUNTS_BY_EMAIL=true
     OAUTH_SESSION_TOKEN_ENCRYPTION_KEY=<$SECRETS/owui_oauth_session_key>
5. Open WebUI tool (integrations/openwebui/termiplus_workspaces.py), valves:
     mcp_url    = http://mcp-auth:4180/
     plugin_key = <$SECRETS/mcp_owui_plugin_key>
     ui_url     = $DESKTOP_PUBLIC_URL/ui/
EOF
