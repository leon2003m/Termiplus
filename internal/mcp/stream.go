package mcp

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// Desktops are served under /stream/<session>/ on the UI listener, behind the
// same sign-in proxy as the console (desk-auth). The browser's OIDC subject is
// mapped to its Open WebUI user, and the gateway passes the request on only to
// a running desktop of exactly that user (/control/v1/stream, control key),
// filling in KasmVNC's password itself. The sign-in is the only login.
//
// Cross-site WebSocket hijacking: KasmVNC does not check the Origin of a
// WebSocket and the sign-in cookie is SameSite=Lax, so upgrades must come from
// this origin.

const streamPathPrefix = "/stream/"

type streamProxy struct {
	ids        *IdentityStore
	gateway    *url.URL
	controlKey string
}

// WithStream makes the UI listener serve desktops through the gateway at
// gatewayBase.
func (u *UI) WithStream(gatewayBase, controlKey string) (*UI, error) {
	g, err := url.Parse(strings.TrimSuffix(gatewayBase, "/"))
	if err != nil {
		return nil, err
	}
	u.stream = &streamProxy{ids: u.ids, gateway: g, controlKey: controlKey}
	return u, nil
}

func (s *streamProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := ProxyHeaderAuthenticator{}.Authenticate(r)
	if err != nil {
		forwardError(w, http.StatusUnauthorized, "Not signed in.")
		return
	}
	owui, ok := s.ids.Lookup(p.UserID)
	if !ok {
		forwardError(w, http.StatusForbidden, "Your sign-in is not linked to an Open WebUI user yet: use the termiplus tool in Open WebUI once.")
		return
	}
	session, _, hasSlash := strings.Cut(strings.TrimPrefix(r.URL.Path, streamPathPrefix), "/")
	if session == "" {
		forwardError(w, http.StatusNotFound, "Open a desktop from the console or from Open WebUI.")
		return
	}
	if !hasSlash {
		// KasmVNC's client uses relative URLs, so it needs the trailing slash.
		http.Redirect(w, r, streamPathPrefix+session+"/", http.StatusFound)
		return
	}
	if isUpgrade(r) && !sameOrigin(r) {
		forwardError(w, http.StatusForbidden, "Cross-origin WebSocket refused.")
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(s.gateway)
			pr.Out.URL.Path = s.gateway.Path + "/control/v1/stream/" + strings.TrimPrefix(pr.In.URL.Path, streamPathPrefix)
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = s.gateway.Host
			for _, h := range []string{"X-Forwarded-User", "X-Forwarded-Groups", "X-Forwarded-Email",
				"X-Forwarded-Preferred-Username", "X-Forwarded-Access-Token", "X-User-Id",
				"Authorization", "Cookie", owuiKeyHeader, owuiUserHeader} {
				pr.Out.Header.Del(h)
			}
			pr.Out.Header.Set(controlKeyHeaderMCP, s.controlKey)
			pr.Out.Header.Set("X-User-Id", owui)
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Www-Authenticate")
			return nil
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("stream %s: %v", session, err)
			forwardError(w, http.StatusBadGateway, "The gateway is not reachable.")
		},
	}
	rp.ServeHTTP(w, r)
}

func isUpgrade(r *http.Request) bool {
	for _, v := range r.Header.Values("Connection") {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), "upgrade") {
				return true
			}
		}
	}
	return r.Header.Get("Upgrade") != ""
}
