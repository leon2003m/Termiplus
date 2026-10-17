package gateway

import (
	"net"
	"net/http"
	"time"
)

// Terminals, desktops and forwarded apps are controlled by their users (root
// in their own container). Every proxy towards them uses this transport, so a
// container cannot make the shared gateway buffer large response headers:
// http.DefaultTransport would accept 10 MiB per request. It also never uses an
// HTTP proxy from the environment.
const maxUpstreamHeaderBytes = 256 << 10

func newUpstreamTransport(responseHeaderTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxResponseHeaderBytes: maxUpstreamHeaderBytes,
		ResponseHeaderTimeout:  responseHeaderTimeout,
		MaxIdleConns:           100,
		IdleConnTimeout:        90 * time.Second,
	}
}

// terminalTransport serves the Open Terminal API (/p/<policy>/...). Its
// responses may legitimately take long to start (commands), so there is no
// response-header deadline; the header size is bounded.
var terminalTransport = newUpstreamTransport(0)
