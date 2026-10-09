# syntax=docker/dockerfile:1
# Builds the gateway and the socket proxy. Pick one with --target:
#   docker build --target gateway   -t termiplus-gateway .
#   docker build --target sockproxy -t termiplus-sockproxy .
ARG GO_VERSION=1.27

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=${VERSION}" -o /out/gateway ./cmd/gateway \
 && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=${VERSION}" -o /out/sockproxy ./cmd/sockproxy

# ---------------------------------------------------------------------------
# Both final images are empty except for one static binary: no shell, no
# package manager, no libc. They run as nobody (65534).
FROM scratch AS base
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/leon2003m/termiplus" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.vendor="termiplus"
USER 65534:65534

FROM base AS gateway
LABEL org.opencontainers.image.title="termiplus-gateway" \
      org.opencontainers.image.description="Routes Open WebUI terminal traffic to per-user Open Terminal containers"
COPY --from=build /out/gateway /gateway
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD ["/gateway", "-healthcheck"]
ENTRYPOINT ["/gateway"]

# Needs access to the Docker socket. Instead of running as root, grant the
# socket's group at runtime: `group_add: ["<docker gid>"]` (see deploy/).
FROM base AS sockproxy
LABEL org.opencontainers.image.title="termiplus-sockproxy" \
      org.opencontainers.image.description="Deny-by-default filter in front of the Docker socket"
COPY --from=build /out/sockproxy /sockproxy
EXPOSE 2375
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD ["/sockproxy", "-healthcheck"]
ENTRYPOINT ["/sockproxy"]
