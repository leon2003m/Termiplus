package gateway

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"termiplus/internal/naming"
)

// The desktop stream of a container whose policy has "desktop": true is
// reachable under /stream/<container>/. The gateway only routes: KasmVNC in
// the container checks its own basic auth against the per-container password
// (desktopPassword), which only the control API hands out, to the session's
// owner. In the deployment the browser additionally signs in through
// oauth2-proxy before it reaches this path (deploy/docker-compose.desktop.yml).
// The gateway never starts a container for a stream request and never adds
// credentials of its own.

const streamPrefix = "/stream/"

// desktopPort is where KasmVNC listens inside a desktop container.
const desktopPort = 6901

// serveStream handles /stream/*. It returns true if it handled the request.
func (g *Gateway) serveStream(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, streamPrefix) {
		return false
	}
	name, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, streamPrefix), "/")
	inst := g.mgr.streamTarget(name)
	if inst == nil {
		// Unknown, stopped, not a desktop or not managed: all look the same.
		httpError(w, http.StatusNotFound, "not found")
		return true
	}
	if rest == "" && !strings.HasSuffix(r.URL.Path, "/") {
		// KasmVNC's client uses relative URLs, so it needs the trailing slash.
		http.Redirect(w, r, streamPrefix+name+"/", http.StatusMovedPermanently)
		return true
	}
	// KasmVNC does not check the Origin of a WebSocket, and browsers send
	// cached basic-auth credentials with cross-site WebSocket handshakes, so a
	// foreign page could otherwise drive the desktop (cross-site WebSocket
	// hijacking). Only same-origin upgrades pass.
	if isWebSocket(r) && !sameOrigin(r) {
		httpError(w, http.StatusForbidden, "cross-origin WebSocket refused")
		return true
	}
	addr := g.mgr.addrFor(name, desktopPort)
	target := &url.URL{Scheme: "http", Host: addr}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + rest
			pr.Out.URL.RawPath = ""
			pr.Out.Host = addr
			// The browser's basic auth for KasmVNC passes through; cookies
			// (the sign-in proxy's session) and gateway headers do not.
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("X-Proxy-Token")
			pr.Out.Header.Del("X-User-Id")
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("stream %s: %v", name, err)
			httpError(w, http.StatusBadGateway, "desktop unavailable")
		},
	}
	// Count the (usually WebSocket) connection so the reaper does not stop a
	// desktop someone is looking at.
	g.mgr.touch(inst, 1)
	defer g.mgr.touch(inst, -1)
	rp.ServeHTTP(w, r)
	return true
}

// sameOrigin reports whether the request has exactly one Origin header whose
// host matches the Host the browser addressed.
func sameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) != 1 {
		return false
	}
	u, err := url.Parse(origins[0])
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// streamTarget returns the running desktop instance called name, or nil.
func (m *manager) streamTarget(name string) *instance {
	if !naming.IsManagedContainer(name) {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inst := m.instances[name]
	if inst == nil || !inst.desktop {
		return nil
	}
	return inst
}
