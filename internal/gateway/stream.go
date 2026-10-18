package gateway

import (
	"encoding/base64"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"termiplus/internal/naming"
)

// The desktop of a container whose policy has "desktop": true is reachable
// only through the control API: /control/v1/stream/<container>/<path> with the
// control key and the X-User-Id of the container's owner. The MCP server calls
// it after the browser signed in (desk-auth, oauth2-proxy) and was mapped to
// its Open WebUI user, and checks the Origin of WebSockets. KasmVNC's basic
// auth is filled in here with the per-container password (desktopPassword), so
// it never reaches a browser and the user signs in only once. The gateway
// never starts a container for a stream request.

const streamPrefix = "stream/"

// desktopPort is where KasmVNC listens inside a desktop container.
const desktopPort = 6901

// desktopUser is KasmVNC's user with input rights; desktopViewer may only
// watch (image/desktop/start-desktop.sh).
const (
	desktopUser   = "user"
	desktopViewer = "viewer"
)

// streamViewHeader, set by the MCP server for a shared desktop, makes the
// gateway sign in as the view-only user. Only control-key callers reach here.
const streamViewHeader = "X-Stream-View"

// serveStream handles control-API paths below stream/. rest is
// "<container>/<path>".
func (g *Gateway) serveStream(w http.ResponseWriter, r *http.Request, userID, rest string) {
	name, path, _ := strings.Cut(rest, "/")
	inst := g.mgr.streamTarget(name, userID)
	if inst == nil {
		// Unknown, stopped, not a desktop or another user's: all look the same.
		httpError(w, http.StatusNotFound, "no such desktop")
		return
	}
	addr := g.mgr.addrFor(name, desktopPort)
	target := &url.URL{Scheme: "http", Host: addr}
	creds := desktopUser + ":" + g.mgr.desktopPassword(name)
	if r.Header.Get(streamViewHeader) == "1" {
		creds = desktopViewer + ":" + g.mgr.desktopViewPassword(name)
	}
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
	rp := &httputil.ReverseProxy{
		Transport: streamTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + path
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = addr
			for _, h := range []string{controlKeyHeader, "X-User-Id", "X-Proxy-Token", "Cookie",
				"X-Forwarded-User", "X-Forwarded-Groups", "X-Forwarded-Email",
				"X-Forwarded-Preferred-Username", "X-Forwarded-Access-Token", forwardPrefixHeader, streamViewHeader} {
				pr.Out.Header.Del(h)
			}
			pr.Out.Header.Set("Authorization", auth)
		},
		ModifyResponse: func(resp *http.Response) error {
			// A rejected password must not make the browser ask the user.
			resp.Header.Del("Www-Authenticate")
			for _, h := range untrustedResponseHeaders {
				resp.Header.Del(h)
			}
			return nil
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("stream %s: %v", name, err)
			httpError(w, http.StatusBadGateway, "desktop unavailable")
		},
	}
	// Count the (usually WebSocket) connection so the reaper does not stop a
	// desktop someone is looking at.
	if !g.mgr.tryTouch(inst, maxProxiedConns) {
		httpError(w, http.StatusTooManyRequests, "too many open connections to this desktop")
		return
	}
	defer g.mgr.touch(inst, -1)
	rp.ServeHTTP(w, r)
}

// streamTarget returns the running desktop instance called name if it belongs
// to userID, or nil.
func (m *manager) streamTarget(name, userID string) *instance {
	if !naming.IsManagedContainer(name) || userID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inst := m.instances[name]
	if inst == nil || !inst.desktop || inst.userID != userID {
		return nil
	}
	return inst
}
