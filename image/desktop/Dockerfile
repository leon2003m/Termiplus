# Native Open Terminal desktop dependencies. Open Terminal starts the display
# on demand and carries its authenticated desktop/browser APIs on port 8000.
ARG BASE=ghcr.io/leon2003m/termiplus-terminal:latest
FROM ${BASE}
ARG DEBIAN_FRONTEND=noninteractive
LABEL org.opencontainers.image.title="termiplus-desktop" \
      org.opencontainers.image.description="Native Open Terminal desktop workspace (XFCE + TigerVNC)"
USER 0
RUN apt-get update && apt-get install -y --no-install-recommends \
        tigervnc-standalone-server xfce4-session xfwm4 xfce4-panel xfdesktop4 \
        xfce4-terminal xfce4-settings thunar mousepad dbus-x11 xauth xdotool \
        scrot xclip chromium chromium-sandbox fonts-dejavu-core \
        fonts-noto-color-emoji xdg-utils pulseaudio pulseaudio-utils ffmpeg \
    && rm -rf /var/lib/apt/lists/*
USER 1000:1000
# Desktop and browser stay disabled unless the gateway enables the policy.
# Inherit the terminal entrypoint, healthcheck and port. No VNC TCP listener.
