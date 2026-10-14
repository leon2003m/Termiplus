#!/bin/bash
# Entrypoint of the desktop image. Always runs Open Terminal (port 8000, as in
# the terminal image), so the gateway can manage the container and Open WebUI
# can use it. When the gateway starts a policy with "desktop": true it sets
# OPEN_TERMINAL_DESKTOP_PASSWORD (derived per container from KEY_SECRET), and
# this script also starts the XFCE desktop under KasmVNC on :6901 (plain
# HTTP/WebSocket; TLS is terminated in front). KasmVNC's own basic auth for
# the single user "user" protects the stream. Without the password the image
# behaves like the terminal image.
set -euo pipefail

pw="${OPEN_TERMINAL_DESKTOP_PASSWORD:-}"
unset OPEN_TERMINAL_DESKTOP_PASSWORD

if [ -n "$pw" ]; then
  if [ "${#pw}" -lt 32 ]; then
    echo "start-desktop: OPEN_TERMINAL_DESKTOP_PASSWORD must be at least 32 characters" >&2
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
# Every browser reaches KasmVNC through the gateway, i.e. from one IP, so the
# per-IP blacklist would let anyone lock the owner out of their desktop. The
# password is 256 random bits, so guessing is not a concern without it.
security:
  brute_force_protection:
    blacklist_threshold: 0
YAML
  printf '#!/bin/sh\nexec dbus-launch --exit-with-session startxfce4\n' >"$HOME/.vnc/xstartup"
  chmod 700 "$HOME/.vnc/xstartup"
  touch "$HOME/.vnc/.de-was-selected"
  # Write permission only (no "owner"): the stream user cannot use the admin API.
  printf '%s\n%s\n' "$pw" "$pw" | kasmvncpasswd -u user -w -r >/dev/null
  unset pw
  # A stopped and restarted container keeps /tmp, and the home volume keeps
  # ~/.vnc: stale lock and pid files of display :1 would stop KasmVNC.
  rm -f /tmp/.X1-lock /tmp/.X11-unix/X1 "$HOME"/.vnc/*:1.pid
  vncserver :1 -fg -geometry 1280x800 -depth 24 &
fi
unset pw

/opt/open-terminal/bin/open-terminal run --host 0.0.0.0 --port 8000 --cwd /home/user &

# Stop the container when either process ends; the gateway recreates it.
wait -n
exit 1
