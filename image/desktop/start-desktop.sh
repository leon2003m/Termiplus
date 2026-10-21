#!/bin/bash
# Entrypoint of the desktop image. Always runs Open Terminal (port 8000, as in
# the terminal image), so the gateway can manage the container and Open WebUI
# can use it. When the gateway starts a policy with "desktop": true it sets
# OPEN_TERMINAL_DESKTOP_PASSWORD (derived per container from KEY_SECRET), and
# this script also starts the XFCE desktop under KasmVNC on :6901 (plain
# HTTP/WebSocket; TLS is terminated in front). KasmVNC's own basic auth
# protects the stream: "user" may type and click, "viewer" (password from
# OPEN_TERMINAL_DESKTOP_VIEW_PASSWORD, for shared desktops) may only watch.
# Without the password the image behaves like the terminal image.
set -euo pipefail
pids=()

pw="${OPEN_TERMINAL_DESKTOP_PASSWORD:-}"
viewpw="${OPEN_TERMINAL_DESKTOP_VIEW_PASSWORD:-}"
keyboard="${OPEN_TERMINAL_KEYBOARD:-}"
record="${OPEN_TERMINAL_RECORD:-}"
unset OPEN_TERMINAL_DESKTOP_PASSWORD OPEN_TERMINAL_DESKTOP_VIEW_PASSWORD OPEN_TERMINAL_KEYBOARD OPEN_TERMINAL_RECORD
# Checked by the gateway as well; it ends up in a script.
if ! [[ "$keyboard" =~ ^[a-z]{2,8}$ ]]; then
  [ -z "$keyboard" ] || echo "start-desktop: ignoring malformed OPEN_TERMINAL_KEYBOARD" >&2
  keyboard=
fi

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
  # The session's D-Bus address is saved so the model's shell (not part of the
  # session) can reach the accessibility bus (tp-ui, tp-screenshot --marks).
  cat >"$HOME/.vnc/xstartup" <<XSTARTUP
#!/bin/sh
export QT_LINUX_ACCESSIBILITY_ALWAYS_ON=1 NO_AT_BRIDGE=0
exec dbus-launch --exit-with-session sh -c '
  mkdir -p "\$HOME/.cache/termiplus"
  printf "%s" "\$DBUS_SESSION_BUS_ADDRESS" >"\$HOME/.cache/termiplus/dbus"
  ${keyboard:+setxkbmap $keyboard}
  exec startxfce4'
XSTARTUP
  chmod 700 "$HOME/.vnc/xstartup"
  touch "$HOME/.vnc/.de-was-selected"
  # Write permission only (no "owner"): the stream user cannot use the admin API.
  printf '%s\n%s\n' "$pw" "$pw" | kasmvncpasswd -u user -w -r >/dev/null
  # Shared desktops (view only): read permission, no input.
  if [ "${#viewpw}" -ge 32 ]; then
    printf '%s\n%s\n' "$viewpw" "$viewpw" | kasmvncpasswd -u viewer -r >/dev/null
  else
    kasmvncpasswd -u viewer -d >/dev/null 2>&1 || true
  fi
  unset pw viewpw
  # A stopped and restarted container keeps /tmp, and the home volume keeps
  # ~/.vnc: stale lock and pid files of display :1 would stop KasmVNC.
  rm -f /tmp/.X1-lock /tmp/.X11-unix/X1 "$HOME"/.vnc/*:1.pid
  vncserver :1 -fg -geometry 1280x800 -depth 24 &
  pids=($!)
  # Opt-in recording of the screen (policy "record"): 2 frames per second,
  # one file per hour in ~/recordings, kept for two days. Matroska stays
  # playable when the container stops in the middle of a file.
  if [ "$record" = 1 ]; then
    mkdir -p "$HOME/recordings"
    (
      while :; do
        find "$HOME/recordings" -name '*.mkv' -mtime +2 -delete 2>/dev/null || true
        sleep 3600
      done
    ) &
    (
      for _ in $(seq 60); do [ -e /tmp/.X11-unix/X1 ] && break; sleep 1; done
      exec ffmpeg -nostdin -loglevel error -f x11grab -framerate 2 -i :1 \
        -c:v libx264 -preset ultrafast -crf 35 -pix_fmt yuv420p \
        -f segment -segment_time 3600 -reset_timestamps 1 -strftime 1 \
        "$HOME/recordings/%Y%m%d-%H%M%S.mkv"
    ) &
  fi
fi
unset pw viewpw

/opt/open-terminal/bin/open-terminal run --host 0.0.0.0 --port 8000 --cwd /home/user &
pids+=($!)

# Stop the container when KasmVNC or Open Terminal ends (not the recorder);
# the gateway recreates it.
wait -n "${pids[@]}"
exit 1
