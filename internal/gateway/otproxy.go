package gateway

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"termiplus/internal/naming"
)

// Open Terminal's own port proxy (/proxy/<port>/<path>, listed by Open WebUI's
// Ports panel) never reaches a terminal. Open WebUI serves it on its own
// origin, so an app in the session (anything the model wrote) would run with
// the user's Open WebUI sign-in. Instead, a GET for a running session of
// exactly this user is redirected to termiplus' port forward
// (ForwardPublicURL/<container>/<port>/<path>), which has an origin of its own
// and its own sign-in. Everything else is 404. /ports stays: it only lists.

const otProxyPrefix = "/proxy"

func isOTProxy(rest string) bool {
	return rest == otProxyPrefix || strings.HasPrefix(rest, otProxyPrefix+"/")
}

// serveOTProxy answers rest = /proxy/<port>/<path> for userID's container name.
func (g *Gateway) serveOTProxy(w http.ResponseWriter, r *http.Request, userID, name, rest string) {
	const off = "port previews are served by termiplus port forwards"
	if g.cfg.ForwardPublicURL == "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		httpError(w, http.StatusNotFound, off)
		return
	}
	tail := strings.TrimPrefix(strings.TrimPrefix(rest, otProxyPrefix), "/")
	portStr, path, _ := strings.Cut(tail, "/")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 || portStr != strconv.Itoa(port) {
		httpError(w, http.StatusBadRequest, "invalid port")
		return
	}
	if port == g.cfg.Port || port == desktopPort {
		httpError(w, http.StatusForbidden, "this port has its own route")
		return
	}
	// Never starts a container: the forward would not either.
	if g.mgr.forwardTarget(name, userID) == nil {
		httpError(w, http.StatusNotFound, off)
		return
	}
	loc := g.cfg.ForwardPublicURL + "/" + name + "/" + portStr + "/" + (&url.URL{Path: path}).EscapedPath()
	if r.URL.RawQuery != "" {
		loc += "?" + r.URL.RawQuery
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, loc, http.StatusTemporaryRedirect)
}

// forwardBase checks FORWARD_PUBLIC_URL: an absolute http(s) URL without
// user info, query or fragment. It returns it without a trailing slash.
func forwardBase(s string) (string, bool) {
	if s == "" {
		return "", true
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", false
	}
	return strings.TrimRight(s, "/"), true
}

// otProxyContainer is the container a terminal request of userID would use.
func otProxyContainer(userID, sessionID, policyName string, pol Policy) string {
	if pol.ephemeral() {
		return naming.Ephemeral(userID, sessionID, policyName)
	}
	return naming.Container(userID, policyName)
}
