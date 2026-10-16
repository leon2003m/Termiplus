#!/usr/bin/env bash
# Adds the termiplus-mcp-admin client (service account for manage_user /
# manage_group) to an existing termiplus realm in the bundled Keycloak. New
# installs get it from setup-alpha.sh's realm; Keycloak imports a realm only
# once, so existing ones need this. Idempotent.
#
#   deploy/keycloak/add-mcp-admin-client.sh [keycloak container]
#
# Uses the bootstrap admin from deploy/secrets/keycloak.env and creates
# deploy/secrets/mcp_kc_admin_client_secret if it is missing.
set -euo pipefail

DEPLOY="$(cd "$(dirname "$0")/.." && pwd)"
CTR="${1:-termiplus-keycloak-1}"
SECRET_FILE="$DEPLOY/secrets/mcp_kc_admin_client_secret"
CLIENT=termiplus-mcp-admin

if [ ! -s "$SECRET_FILE" ]; then
  (umask 077 && openssl rand -hex 32 | tr -d '\n' >"$SECRET_FILE")
  echo "created  secrets/mcp_kc_admin_client_secret"
fi
chmod 0644 "$SECRET_FILE" # read by the MCP server (uid 65534)

admin_user="$(sed -n 's/^KC_BOOTSTRAP_ADMIN_USERNAME=//p' "$DEPLOY/secrets/keycloak.env")"
admin_pw="$(sed -n 's/^KC_BOOTSTRAP_ADMIN_PASSWORD=//p' "$DEPLOY/secrets/keycloak.env")"
kc() { docker exec -i "$CTR" /opt/keycloak/bin/kcadm.sh "$@"; }
# kcadm reads the password from KC_CLI_PASSWORD, so it is never on a command line.
KC_CLI_PASSWORD="$admin_pw" docker exec -i -e KC_CLI_PASSWORD "$CTR" /opt/keycloak/bin/kcadm.sh \
  config credentials --server http://localhost:8080 --realm master --user "$admin_user" >/dev/null

id="$(kc get clients -r termiplus -q clientId="$CLIENT" --fields id --format csv --noquotes | head -1)"
if [ -z "$id" ]; then
  kc create clients -r termiplus -f - >/dev/null <<JSON
{"clientId": "$CLIENT", "name": "termiplus MCP user management (service account)", "enabled": true,
 "publicClient": false, "serviceAccountsEnabled": true, "standardFlowEnabled": false,
 "directAccessGrantsEnabled": false, "secret": "$(cat "$SECRET_FILE")"}
JSON
  echo "created  client $CLIENT"
else
  kc update "clients/$id" -r termiplus -f - >/dev/null <<JSON
{"secret": "$(cat "$SECRET_FILE")", "serviceAccountsEnabled": true}
JSON
  echo "updated  client $CLIENT (secret from secrets/mcp_kc_admin_client_secret)"
fi
kc add-roles -r termiplus --uusername "service-account-$CLIENT" --cclientid realm-management \
  --rolename view-users --rolename query-groups --rolename manage-users
echo "granted  view-users, query-groups, manage-users to service-account-$CLIENT"
