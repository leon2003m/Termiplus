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

// streamTransport and forwardTransport serve desktops and forwarded apps,
// whose requests the user can open in any number: besides the header limit,
// a container must answer with headers within a deadline.
var (
	streamTransport  = newUpstreamTransport(30 * time.Second)
	forwardTransport = newUpstreamTransport(5 * time.Minute)
)

// maxProxiedConns caps concurrent desktop-stream and forwarded connections
// per container, so one user cannot tie up the shared gateway with them.
const maxProxiedConns = 32

// untrustedResponseHeaders grant browser state beyond one desktop's or app's
// path (cookies, service-worker scope, wiping site data). They are removed
// from desktop responses; the MCP server's desktop origin removes them too.
var untrustedResponseHeaders = []string{"Set-Cookie", "Service-Worker-Allowed", "Clear-Site-Data"}

// tryTouch counts one more open connection on inst unless it already has
// limit; the caller releases it with touch(inst, -1).
func (m *manager) tryTouch(inst *instance, limit int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst.conns >= limit {
		return false
	}
	inst.conns++
	inst.lastUsed = time.Now()
	return true
}
