# Per-user terminal image. Plain Debian plus the Open Terminal server.
# Runs under gVisor (runsc) or Sysbox, so sudo inside is root only inside
# the sandbox, not on the host.
ARG BASE=debian:trixie-slim
FROM ${BASE}

ARG OPEN_TERMINAL_VERSION=0.14.0
ARG DEBIAN_FRONTEND=noninteractive

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

RUN useradd -m -u 1000 -s /bin/bash user \
    && echo 'user ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/user \
    && chmod 0440 /etc/sudoers.d/user \
    && printf '%s\n' \
        'export PATH="$HOME/.local/bin:$PATH"' \
        'export NPM_CONFIG_PREFIX="$HOME/.local"' \
        '# Use a venv for Python packages: python3 -m venv ~/.venv && . ~/.venv/bin/activate' \
        >> /home/user/.bashrc \
    && chown -R user:user /home/user

USER user
WORKDIR /home/user
ENV HOME=/home/user
EXPOSE 8000

# The API key comes from OPEN_TERMINAL_API_KEY, set per container by the gateway.
CMD ["/opt/open-terminal/bin/open-terminal", "run", "--host", "0.0.0.0", "--port", "8000", "--cwd", "/home/user"]
