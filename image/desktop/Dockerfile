# EXPERIMENTAL desktop workspace (alpha, docs/mcp.md): the terminal image plus
# an XFCE desktop streamed by KasmVNC on port 6901. Use it as TERMINAL_IMAGE:
# every container runs Open Terminal as before, and policies with
# "desktop": true also start the desktop (start-desktop.sh). The gateway
# routes /stream/<container>/ to it. Runs under gVisor and Sysbox.
ARG BASE=ghcr.io/leon2003m/termiplus-terminal:latest
FROM ${BASE}

# KasmVNC (GPL-2.0) Debian trixie package, checked against the release digest.
ARG KASMVNC_VERSION=1.5.0
ARG KASMVNC_SHA256=80b241de7dfe53bba2b7e1cc5ac8c5246d72271efa16be2d4f76607f30fab1c4
ARG DEBIAN_FRONTEND=noninteractive

LABEL org.opencontainers.image.title="termiplus-desktop" \
      org.opencontainers.image.description="EXPERIMENTAL termiplus desktop workspace (XFCE + KasmVNC), run under gVisor or Sysbox"

USER 0
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN curl -fsSL -o /tmp/kasmvnc.deb \
        "https://github.com/kasmtech/KasmVNC/releases/download/v${KASMVNC_VERSION}/kasmvncserver_trixie_${KASMVNC_VERSION}_amd64.deb" \
    && echo "${KASMVNC_SHA256}  /tmp/kasmvnc.deb" | sha256sum -c - \
    && apt-get update && apt-get install -y --no-install-recommends \
        /tmp/kasmvnc.deb xfce4-session xfwm4 xfce4-panel xfdesktop4 xfce4-terminal xfce4-settings \
        libglib2.0-bin maim python3-pil dbus-x11 x11-xserver-utils fonts-dejavu-core \
        dolphin kio-extras qt6ct kde-style-breeze kate ristretto xfce4-appfinder xfce4-whiskermenu-plugin exo-utils xdg-utils \
        firefox-esr webext-ublock-origin-firefox arc-theme papirus-icon-theme fonts-inter fonts-noto-color-emoji fonts-noto-cjk librsvg2-common \
    && rm -rf /var/lib/apt/lists/* /tmp/kasmvnc.deb \
    && usermod -aG ssl-cert user

# Desktop defaults: theme, icons, fonts, panel, default applications, Firefox
# policies and the wallpaper (files/). They apply until a user changes them in
# Settings; those changes are kept in ~/.config on the home volume.
COPY files/ /
# The wallpaper: replace files/usr/share/backgrounds/termiplus/default.svg
# before building to change it for everyone. xfdesktop shows its built-in
# default image until a user picks another one, so that path points here.
RUN dpkg-divert --quiet --local --rename --add /usr/share/backgrounds/xfce/xfce-x.svg \
    && ln -s ../termiplus/default.svg /usr/share/backgrounds/xfce/xfce-x.svg

ENV QT_QPA_PLATFORMTHEME=qt6ct \
    QT_LOGGING_RULES="*.debug=false;kf.solid.backends.udisks2.warning=false"

COPY theme.py /usr/local/libexec/termiplus/theme.py
RUN apt-get update && apt-get install -y --no-install-recommends libglib2.0-dev-bin qt6-svg-plugins libkf6iconthemes-bin thunar gvfs \
    && rm /usr/share/dbus-1/services/org.xfce.Thunar.FileManager1.service \
    && python3 /usr/local/libexec/termiplus/theme.py \
    && apt-get purge -y libglib2.0-dev-bin \
    && rm -rf /var/lib/apt/lists/*

# Restore our hidden Dolphin compatibility entry after installing the XFCE trash backend.
COPY files/usr/share/applications/thunar.desktop /usr/share/applications/thunar.desktop
COPY files/usr/share/xfce4/helpers/thunar.desktop /usr/share/xfce4/helpers/thunar.desktop

COPY start-desktop.sh /usr/local/bin/start-desktop
RUN chmod 755 /usr/local/bin/start-desktop

USER 1000:1000
# The terminal image's HEALTHCHECK (Open Terminal on 8000) still applies.
EXPOSE 8000 6901
# The terminal image's start (DNS forwarder in resolv.conf) runs first.
ENTRYPOINT ["/usr/local/libexec/termiplus/start", "/usr/local/bin/start-desktop"]
CMD []
