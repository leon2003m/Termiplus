# Per-user terminal image. Plain Debian plus the Open Terminal server.
# Runs under gVisor (runsc) or Sysbox, so sudo inside is root only inside
# the sandbox, not on the host.
# The base is pinned to a release tag; Dependabot proposes updates. Pin to a
# digest (debian:trixie-slim@sha256:...) for fully reproducible builds.
ARG BASE=debian:trixie-slim
FROM ${BASE}

ARG OPEN_TERMINAL_VERSION=0.14.0
ARG VERSION=dev
ARG REVISION=unknown
ARG DEBIAN_FRONTEND=noninteractive

LABEL org.opencontainers.image.title="termiplus-terminal" \
      org.opencontainers.image.description="Per-user Open Terminal sandbox for termiplus (run under gVisor or Sysbox)" \
      org.opencontainers.image.source="https://github.com/leon2003m/termiplus" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN apt-get update && apt-get install -y --no-install-recommends \
        sudo ca-certificates curl wget git openssh-client jq less vim-tiny nano \
        procps iproute2 iputils-ping dnsutils unzip zip xz-utils tree file \
        build-essential pkg-config \
        python3 python3-venv python3-pip python3-dev \
        nodejs npm \
        sqlite3 ripgrep \
    && rm -rf /var/lib/apt/lists/*

# Open Terminal (MIT) in its own venv, outside the user's PATH so the model's
# pip installs never touch the server.
RUN python3 -m venv /opt/open-terminal \
    && /opt/open-terminal/bin/pip install --no-cache-dir "open-terminal==${OPEN_TERMINAL_VERSION}"

# Literal $HOME/$PATH are meant for .bashrc, not for expansion at build time.
# hadolint ignore=SC2016
RUN useradd -m -u 1000 -s /bin/bash user \
    && echo 'user ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/user \
    && chmod 0440 /etc/sudoers.d/user \
    && printf '%s\n' \
        'export PATH="$HOME/.local/bin:$PATH"' \
        'export NPM_CONFIG_PREFIX="$HOME/.local"' \
        '# Use a venv for Python packages: python3 -m venv ~/.venv && . ~/.venv/bin/activate' \
        >> /home/user/.bashrc \
    && chown -R user:user /home/user

# Interactive terminals: Open Terminal starts $SHELL and falls back to
# /bin/sh. bash-ctty starts bash with the PTY as controlling terminal, so job
# control works. Commands run by the model use /bin/sh -c and are unaffected.
COPY bash-ctty /usr/local/libexec/termiplus/bash
COPY start /usr/local/libexec/termiplus/start
RUN chmod 0755 /usr/local/libexec/termiplus/bash /usr/local/libexec/termiplus/start

USER 1000:1000
WORKDIR /home/user
# USER names the account in the tool description Open Terminal gives the
# model. Commands wait up to 60 s so their output comes back with run_command:
# without it every call returns "running" and models have to poll, which small
# ones do badly.
ENV HOME=/home/user \
    USER=user \
    SHELL=/usr/local/libexec/termiplus/bash \
    OPEN_TERMINAL_EXECUTE_TIMEOUT=60 \
    OPEN_TERMINAL_EXECUTE_DESCRIPTION="Debian container of your own; nothing here affects the host. sudo works without a password (e.g. sudo apt-get install -y PACKAGE). Waits up to 60 s and returns the output; if status is still running, use get_process_status."
EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["curl", "-fsS", "-o", "/dev/null", "http://127.0.0.1:8000/health"]

# The API key comes from OPEN_TERMINAL_API_KEY, set per container by the gateway.
# start points resolv.conf at the DNS forwarder when the gateway passes one.
ENTRYPOINT ["/usr/local/libexec/termiplus/start"]
CMD ["/opt/open-terminal/bin/open-terminal", "run", "--host", "0.0.0.0", "--port", "8000", "--cwd", "/home/user"]
