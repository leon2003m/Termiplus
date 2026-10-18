package mcp

import (
	"net"
	"net/http"
	"time"
)

// The desktop and forward proxies relay responses that originate in users'
// containers (through the gateway, which bounds them too). They use their own
// transports: a small response-header limit instead of Go's 10 MiB default, a
// deadline for the headers, and never an HTTP proxy from the environment.
func newRelayTransport(responseHeaderTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxResponseHeaderBytes: 256 << 10,
		ResponseHeaderTimeout:  responseHeaderTimeout,
		MaxIdleConns:           100,
		IdleConnTimeout:        90 * time.Second,
	}
}

var (
	streamTransport  = newRelayTransport(60 * time.Second)
	forwardTransport = newRelayTransport(6 * time.Minute)
)
