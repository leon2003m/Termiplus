#!/bin/bash
# Starts the XFCE desktop under KasmVNC on :6901 (plain HTTP/WebSocket; TLS is
# terminated in front of the sandbox). The stream is protected by KasmVNC's
# own basic auth for the single user "user"; VNC_PW must be a long random
# secret supplied by whoever starts the container.
set -euo pipefail

if [ "${#VNC_PW}" -lt 32 ]; then
  echo "start-desktop: VNC_PW must be set to at least 32 characters" >&2
  exit 1
fi

mkdir -p "$HOME/.vnc"
cat >"$HOME/.vnc/kasmvnc.yaml" <<'YAML'
network:
  protocol: http
  interface: 0.0.0.0
  websocket_port: 6901
  ssl:
    require_ssl: false
    pem_certificate: ~
    pem_key: ~
YAML
printf '#!/bin/sh\nexec dbus-launch --exit-with-session startxfce4\n' >"$HOME/.vnc/xstartup"
chmod 700 "$HOME/.vnc/xstartup"
touch "$HOME/.vnc/.de-was-selected"

# Write permission only (no "owner"): the stream user cannot use the admin API.
printf '%s\n%s\n' "$VNC_PW" "$VNC_PW" | kasmvncpasswd -u user -w -r >/dev/null
unset VNC_PW

exec vncserver :1 -fg -geometry 1280x800 -depth 24
