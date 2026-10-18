package gateway

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"termiplus/internal/naming"
)

// Port forwarding (like code-server's /proxy/<port>/): the MCP server's
// forward listener, after the browser signed in and was mapped to its Open
// WebUI user, calls /control/v1/forward/<container>/<port>/<path> with the
// control key and X-User-Id. The gateway only forwards to a running container
// of exactly that user, never to Open Terminal's API port or the desktop port
// (they have their own routes and credentials), and never starts a container.

const forwardPrefix = "forward/"

// forwardPrefixHeader carries the public path prefix (/<container>/<port>) so that
// redirects of the forwarded app can be rewritten to stay under it.
const forwardPrefixHeader = "X-Forwarded-Prefix"

// serveForward handles control-API paths below forward/. rest is
// "<container>/<port>/<path>".
func (g *Gateway) serveForward(w http.ResponseWriter, r *http.Request, userID, rest string) {
	name, tail, _ := strings.Cut(rest, "/")
	portStr, path, hasSlash := strings.Cut(tail, "/")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 || portStr != strconv.Itoa(port) {
		httpError(w, http.StatusBadRequest, "invalid port")
		return
	}
	if port == g.cfg.Port || port == desktopPort {
		httpError(w, http.StatusForbidden, "this port has its own route")
		return
	}
	inst := g.mgr.forwardTarget(name, userID)
	if inst == nil {
		// Unknown, stopped, or another user's container: all look the same.
		httpError(w, http.StatusNotFound, "no such session")
		return
	}
	if !hasSlash {
		path = ""
	}

	addr := g.mgr.addrFor(name, port)
	target := &url.URL{Scheme: "http", Host: addr}
	prefix := r.Header.Get(forwardPrefixHeader)
	if !strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "\r\n") {
		prefix = ""
	}
	local := "localhost:" + portStr
	rp := &httputil.ReverseProxy{
		Transport: forwardTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + path
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			// Dev servers expect to be asked for localhost (Vite, webpack and
			// others refuse unknown Host headers).
			pr.Out.Host = local
			for _, h := range []string{controlKeyHeader, "X-User-Id", "X-Proxy-Token",
				"X-Forwarded-User", "X-Forwarded-Groups", "X-Forwarded-Email",
				"X-Forwarded-Preferred-Username", "X-Forwarded-Access-Token"} {
				pr.Out.Header.Del(h)
			}
			dropAuthCookies(pr.Out.Header)
		},
		ModifyResponse: func(resp *http.Response) error {
			if loc := resp.Header.Get("Location"); loc != "" && prefix != "" {
				resp.Header.Set("Location", rewriteLocation(loc, local, prefix))
			}
			return nil
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("forward %q:%d: %q", name, port, err)
			httpError(w, http.StatusBadGateway, "nothing is listening on this port")
		},
	}
	// Count the connection so the reaper keeps the container while in use.
	if !g.mgr.tryTouch(inst, maxProxiedConns) {
		httpError(w, http.StatusTooManyRequests, "too many open connections to this session")
		return
	}
	defer g.mgr.touch(inst, -1)
	rp.ServeHTTP(w, r)
}

// forwardTarget returns the running instance called name if it belongs to
// userID, or nil.
func (m *manager) forwardTarget(name, userID string) *instance {
	if !naming.IsManagedContainer(name) || userID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inst := m.instances[name]
	if inst == nil || inst.userID != userID {
		return nil
	}
	return inst
}

// dropAuthCookies removes termiplus' own sign-in cookies (_termiplus*) and keeps
// the forwarded app's cookies.
func dropAuthCookies(h http.Header) {
	var keep []string
	for _, line := range h.Values("Cookie") {
		for _, c := range strings.Split(line, ";") {
			c = strings.TrimSpace(c)
			if c == "" || strings.HasPrefix(c, "_termiplus") || strings.HasPrefix(c, "_oauth2_proxy") {
				continue
			}
			keep = append(keep, c)
		}
	}
	h.Del("Cookie")
	if len(keep) > 0 {
		h.Set("Cookie", strings.Join(keep, "; "))
	}
}

// rewriteLocation keeps redirects of the forwarded app under the public prefix:
// absolute URLs to the app itself and host-relative paths get the prefix,
// anything else (other sites, already prefixed paths) is left alone.
func rewriteLocation(loc, local, prefix string) string {
	for _, scheme := range []string{"http://", "https://"} {
		if rest, ok := strings.CutPrefix(loc, scheme+local); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
			loc = rest
			if loc == "" {
				loc = "/"
			}
			break
		}
	}
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") && !strings.HasPrefix(loc, prefix+"/") {
		return prefix + loc
	}
	return loc
}
