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
        xfce4-terminal xfce4-settings thunar gvfs dbus-x11 xauth xdotool \
        scrot xclip chromium chromium-sandbox fonts-dejavu-core \
        fonts-noto-color-emoji xdg-utils pulseaudio pulseaudio-utils ffmpeg \
        dolphin kio-extras qt6ct qt6-svg-plugins kde-style-breeze libkf6iconthemes-bin \
        kate xfce4-appfinder xfce4-whiskermenu-plugin exo-utils \
        arc-theme papirus-icon-theme \
        fonts-inter fonts-noto-cjk librsvg2-common libglib2.0-bin libglib2.0-dev-bin \
    && rm -rf /var/lib/apt/lists/*

ENV OPEN_TERMINAL_ENABLE_DESKTOP=true \
    OPEN_TERMINAL_ENABLE_BROWSER=true \
    QT_QPA_PLATFORMTHEME=qt6ct \
    QT_LOGGING_RULES="*.debug=false;kf.solid.backends.udisks2.warning=false"

# Match the official Open Terminal image's workspace tools, using Debian
# packages rather than running third-party repository/bootstrap scripts.
RUN apt-get update && apt-get install -y --no-install-recommends \
        man-db net-tools netcat-openbsd socat telnet cmake ruby-full lua5.4 \
        xmlstarlet pandoc poppler-utils texlive-latex-base \
        libreoffice libreoffice-gtk3 fonts-crosextra-caladea fonts-crosextra-carlito \
        zstd p7zip-full btop strace sysstat tmux screen tini \
        iptables ipset dnsmasq gnupg apt-transport-https libcap2-bin \
        docker.io docker-compose docker-buildx \
    && rm -rf /var/lib/apt/lists/*

# User Python and notebooks share their libraries; the API server keeps its
# own venv so installing workspace packages cannot replace its dependencies.
RUN python3 -m venv /opt/workspace \
    && /opt/workspace/bin/pip install --no-cache-dir \
        numpy pandas scipy scikit-learn matplotlib seaborn plotly jupyter ipython \
        requests beautifulsoup4 lxml sqlalchemy psycopg2-binary pyyaml toml \
        jsonlines tqdm rich openpyxl weasyprint python-docx python-pptx pypdf \
        csvkit nbclient ipykernel pillow xlrd striprtf \
    && /opt/workspace/bin/python -m ipykernel install --prefix=/opt/open-terminal --name=python3 \
    && chown -R 1000:1000 /opt/workspace
RUN apt-get update && apt-get install -y --no-install-recommends \
        vim gawk diffutils patch lsof docker-cli \
    && rm -rf /var/lib/apt/lists/*
# Keep desktop applications and branding independent of the native transport.
# Seed missing preferences and repair obsolete app/session controls on startup.
COPY files/ /
COPY theme.py /usr/local/libexec/termiplus/theme.py
COPY defaults.py /usr/local/libexec/termiplus/desktop-defaults.py
RUN dpkg-divert --quiet --local --rename --add /usr/share/backgrounds/xfce/xfce-x.svg \
    && ln -s ../termiplus/default.svg /usr/share/backgrounds/xfce/xfce-x.svg \
    && rm /usr/share/dbus-1/services/org.xfce.Thunar.FileManager1.service \
    && python3 /usr/local/libexec/termiplus/theme.py \
    && apt-get purge -y libglib2.0-dev-bin \
    && chmod 755 /usr/local/bin/startxfce4 /usr/local/bin/thunar /usr/local/bin/xdotool \
    && rm -rf /var/lib/apt/lists/*
COPY prepare.py /usr/local/libexec/termiplus/prepare-desktop.py
RUN chown -R 1000:1000 /home/user
ENV PATH="/home/user/.local/bin:/opt/workspace/bin:${PATH}" \
    OPEN_TERMINAL_INFO="You have a graphical XFCE desktop shared with the user through the native Desktop tab. Desktop screenshot and action tools are available: use them to see and control graphical apps; do not assume this workspace is headless. Chromium is the installed and default web browser; Dolphin, Kate, LibreOffice and XFCE Terminal are installed. Call desktop_screenshot before interacting and use its actual pixel dimensions. This Linux desktop returns 1280x800 screenshots: x is 0..1279 and y is 0..799 in raw pixels. Convert 0..1000 normalized vision points before calling: pixel x=round(u*1280/1000), pixel y=round(v*800/1000). For example normalized (500,960) is pixel (640,768). Never repeat failed out-of-bounds coordinates. For click, double_click and move, supply x and y, for example {\"actions\":[{\"action\":\"click\",\"x\":500,\"y\":400,\"button\":\"left\"}]}. For drag, x/y are the start and to_x/to_y the destination. For type supply text; for key supply key. Correct a validation error using its details instead of repeating the same invalid call. Docker, Compose, Buildx, scientific Python, notebooks and document conversion tools are installed. End workspaces through workspace controls, not XFCE logout."
USER 1000:1000
# Both shipped policies enable desktop and browser; the gateway sets their flags.
# Retain the terminal startup, healthcheck and port. No VNC TCP listener.
ENTRYPOINT ["/usr/bin/tini", "--", "/opt/workspace/bin/python", "/usr/local/libexec/termiplus/prepare-desktop.py", "/usr/local/libexec/termiplus/start"]
CMD ["/opt/open-terminal/bin/open-terminal", "run", "--host", "0.0.0.0", "--port", "8000", "--cwd", "/home/user"]
