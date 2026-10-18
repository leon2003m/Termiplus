# Internal DNS forwarder for the terminal sandbox: dnsmasq forwarding to the
# operator's resolver, serving the admin-set static records from a watched
# hostsdir. Terminals are pinned to this resolver by the gateway; the socket
# proxy accepts no other DNS address.
# The base is pinned to a release tag; Dependabot proposes updates. Pin to a
# digest (debian:trixie-slim@sha256:...) for fully reproducible builds.
ARG BASE=debian:trixie-slim
FROM ${BASE}

ARG VERSION=dev
ARG REVISION=unknown
ARG DEBIAN_FRONTEND=noninteractive

LABEL org.opencontainers.image.title="termiplus-dns-forwarder" \
      org.opencontainers.image.description="Internal DNS forwarder (dnsmasq) for the termiplus terminal sandbox" \
      org.opencontainers.image.source="https://github.com/leon2003m/termiplus" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN apt-get update && apt-get install -y --no-install-recommends \
        dnsmasq \
    && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh /usr/local/bin/termiplus-dns
RUN chmod 0755 /usr/local/bin/termiplus-dns

# nobody: binding :53 needs CAP_NET_BIND_SERVICE, added in the compose overlay.
USER 65534:65534
EXPOSE 53/udp 53/tcp

ENTRYPOINT ["/usr/local/bin/termiplus-dns"]
