package mcp

import (
	"html"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"termiplus/internal/naming"
)

// Forwarder serves port forwards (FWD_LISTEN_ADDR) behind their own sign-in
// proxy (fwd-auth, on its own origin): /<session>/<port>/<path> reaches
// <port> in the signed-in user's session container, like code-server's
// /proxy/<port>/. The browser's OIDC subject is mapped to the Open WebUI user
// (IdentityStore), and the gateway forwards only to that user's running
// containers (/control/v1/forward, control key). Forwarded apps share this
// origin with each other but never with the console or the desktops.
type Forwarder struct {
	ids        *IdentityStore
	gateway    *url.URL
	controlKey string
	srv        *Server
}

// NewForwarder returns the forward handler for the gateway at gatewayBase.
func NewForwarder(ids *IdentityStore, gatewayBase, controlKey string, servers ...*Server) (*Forwarder, error) {
	u, err := url.Parse(strings.TrimSuffix(gatewayBase, "/"))
	if err != nil {
		return nil, err
	}
	f := &Forwarder{ids: ids, gateway: u, controlKey: controlKey}
	if len(servers) > 0 {
		f.srv = servers[0]
	}
	return f, nil
}

func (f *Forwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		uiJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	p, err := ProxyHeaderAuthenticator{}.Authenticate(r)
	if err != nil {
		forwardError(w, http.StatusUnauthorized, "Not signed in.")
		return
	}
	owui, ok := f.ids.Lookup(p.UserID)
	if !ok {
		forwardError(w, http.StatusForbidden, "Your sign-in is not linked to an Open WebUI user yet: use the termiplus tool in Open WebUI once.")
		return
	}
	session, tail, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	port, _, hasSlash := strings.Cut(tail, "/")
	// The session must be a container name: anything else (a backslash, an
	// encoded slash) could turn the trailing-slash redirect below into a
	// redirect to another site (security audit run 1).
	if !naming.IsManagedContainer(session) || port == "" {
		forwardError(w, http.StatusNotFound, "Open /<session>/<port>/ - the console lists your sessions.")
		return
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || port != strconv.Itoa(n) {
		forwardError(w, http.StatusBadRequest, "Invalid port.")
		return
	}
	if f.srv != nil {
		if err := f.srv.entitledSession(r.Context(), Principal{UserID: owui, Roles: p.Roles}, session); err != nil {
			forwardError(w, http.StatusNotFound, "No such session or access is not permitted.")
			return
		}
	}
	prefix := "/" + session + "/" + port
	if !hasSlash {
		// Apps use relative URLs, so the base needs its trailing slash.
		q := ""
		if r.URL.RawQuery != "" {
			q = "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, prefix+"/"+q, http.StatusFound)
		return
	}

	rp := &httputil.ReverseProxy{
		Transport: forwardTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(f.gateway)
			pr.Out.URL.Path = f.gateway.Path + "/control/v1/forward" + pr.In.URL.Path
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = f.gateway.Host
			for _, h := range []string{"X-Forwarded-User", "X-Forwarded-Groups", "X-Forwarded-Email",
				"X-Forwarded-Preferred-Username", "X-Forwarded-Access-Token", "X-User-Id",
				owuiKeyHeader, owuiUserHeader} {
				pr.Out.Header.Del(h)
			}
			pr.Out.Header.Set(controlKeyHeaderMCP, f.controlKey)
			pr.Out.Header.Set("X-User-Id", owui)
			pr.Out.Header.Set("X-Forwarded-Prefix", prefix)
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("forward %q: %q", prefix, err)
			forwardError(w, http.StatusBadGateway, "The gateway is not reachable.")
		},
	}
	rp.ServeHTTP(w, r)
}

// controlKeyHeaderMCP is the gateway's control key header (gateway.controlKeyHeader).
const controlKeyHeaderMCP = "X-Control-Key"

func forwardError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8><title>termiplus</title><p>" + html.EscapeString(msg) + "</p>\n"))
}
