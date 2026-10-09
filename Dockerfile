# Builds the gateway and the socket proxy. Pick one with --target.
#   docker build --target gateway   -t termiplus-gateway .
#   docker build --target sockproxy -t termiplus-sockproxy .
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sockproxy ./cmd/sockproxy

FROM scratch AS gateway
COPY --from=build /out/gateway /gateway
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/gateway"]

# Needs read/write access to the Docker socket, so it runs as root inside an
# otherwise empty image. It is only reachable from the gateway.
FROM scratch AS sockproxy
COPY --from=build /out/sockproxy /sockproxy
EXPOSE 2375
ENTRYPOINT ["/sockproxy"]
