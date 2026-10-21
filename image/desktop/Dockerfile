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
        dbus-x11 x11-xserver-utils fonts-dejavu-core \
    && rm -rf /var/lib/apt/lists/* /tmp/kasmvnc.deb \
    && usermod -aG ssl-cert user

# Tools for models on the desktop (docs/agents.md): Chromium, driven by tp-browser over the DevTools protocol (Playwright, no
# browser download), accessibility (AT-SPI) for tp-ui, screenshots, input
# and OCR for tp-screenshot/tp-act/tp-ocr, recording, Nextcloud and the
# LibreOffice GUI.
ARG PLAYWRIGHT_VERSION=1.55.0
RUN apt-get update && apt-get install -y --no-install-recommends \
        chromium fonts-liberation \
        at-spi2-core libatk-adaptor python3-pyatspi \
        scrot xdotool wmctrl xclip x11-xkb-utils ffmpeg \
        nextcloud-desktop libreoffice-writer libreoffice-calc libreoffice-impress libreoffice-gtk3 \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/termiplus/playwright \
    && /opt/termiplus/playwright/bin/pip install --no-cache-dir "playwright==${PLAYWRIGHT_VERSION}"

COPY start-desktop.sh /usr/local/bin/start-desktop
COPY tpdesk.py /usr/local/lib/termiplus/tpdesk.py
COPY tp-browser.py /usr/local/bin/tp-browser
COPY skills/ /usr/share/termiplus/skills/
COPY chromium-policy.py /tmp/chromium-policy.py
RUN chmod 755 /usr/local/bin/start-desktop /usr/local/bin/tp-browser /usr/local/lib/termiplus/tpdesk.py \
    && for n in tp-screenshot tp-act tp-ocr tp-ui; do ln -s /usr/local/lib/termiplus/tpdesk.py "/usr/local/bin/$n"; done \
    && python3 /tmp/chromium-policy.py && rm /tmp/chromium-policy.py

USER 1000:1000
# The terminal image's HEALTHCHECK (Open Terminal on 8000) still applies.
EXPOSE 8000 6901
# The terminal image's start (DNS forwarder in resolv.conf) runs first.
ENTRYPOINT ["/usr/local/libexec/termiplus/start", "/usr/local/bin/start-desktop"]
CMD []
