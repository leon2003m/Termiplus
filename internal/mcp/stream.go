package mcp

import (
	"html"
	"io"
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
	console    string // console base URL for the "not running" page, or ""
}

// DesktopHandler serves desktops (/stream/<session>/) and share links
// (/share/<token>/) on their own listener (STREAM_LISTEN_ADDR), which must be
// published on an origin of its own (DESKTOP_STREAM_URL) behind its own
// sign-in proxy and cookie. Desktop content is controlled by whoever runs code
// in the desktop, so it must never share an origin with the console and its
// API: same-origin script could otherwise act as the signed-in user there
// (security audit run 1). For the same reason responses lose every header that
// grants origin-wide browser state, and service workers are refused.
type DesktopHandler struct {
	proxy  *streamProxy
	shares *ShareStore
}

// NewDesktopHandler proxies desktops through the gateway at gatewayBase.
// srv provides the share store and the console URL.
func NewDesktopHandler(srv *Server, ids *IdentityStore, gatewayBase, controlKey string) (*DesktopHandler, error) {
	g, err := url.Parse(strings.TrimSuffix(gatewayBase, "/"))
	if err != nil {
		return nil, err
	}
	return &DesktopHandler{
		proxy:  &streamProxy{ids: ids, gateway: g, controlKey: controlKey, console: srv.uiBase},
		shares: srv.shares,
	}, nil
}

func (d *DesktopHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		uiJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case r.Header.Get("Service-Worker") != "":
		// A worker registered here would outlive the desktop and control the
		// whole origin for later sign-ins; KasmVNC needs none.
		forwardError(w, http.StatusForbidden, "Service workers are not allowed on desktops.")
	case strings.HasPrefix(r.URL.Path, streamPathPrefix):
		d.proxy.ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, sharePathPrefix) && d.shares != nil:
		d.proxy.serveShare(w, r, d.shares)
	default:
		forwardError(w, http.StatusNotFound, "Not found.")
	}
}

// untrustedResponseHeaders grant browser state beyond one desktop's path
// (cookies on the origin, worker scope, wiping site data) and are dropped
// from every desktop response.
var untrustedResponseHeaders = []string{"Www-Authenticate", "Set-Cookie", "Service-Worker-Allowed", "Clear-Site-Data"}

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
	session, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, streamPathPrefix), "/")
	if session == "" {
		forwardError(w, http.StatusNotFound, "Open a desktop from the console or from Open WebUI.")
		return
	}
	s.proxy(w, r, streamPathPrefix+session, session, owui, false)
}

// serveShare serves /share/<token>/: the shared desktop, view only, to any
// signed-in user (or only the one it was shared with) while the link lasts.
func (s *streamProxy) serveShare(w http.ResponseWriter, r *http.Request, shares *ShareStore) {
	p, err := ProxyHeaderAuthenticator{}.Authenticate(r)
	if err != nil {
		forwardError(w, http.StatusUnauthorized, "Not signed in.")
		return
	}
	token, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, sharePathPrefix), "/")
	sh, ok := shares.Lookup(token)
	if !ok {
		forwardError(w, http.StatusNotFound, "This share link is not valid or has expired.")
		return
	}
	if sh.ForSubject != "" && sh.ForSubject != p.UserID {
		forwardError(w, http.StatusForbidden, "This desktop was shared with someone else.")
		return
	}
	if r.URL.Path == sharePathPrefix+token+"/" && r.Method == http.MethodGet {
		log.Printf("share %s (%s of %s) opened by %s", sh.ID, sh.Session, sh.Owner, p.UserID)
	}
	// The relay (usually the desktop's WebSocket) ends when the share expires
	// or is revoked, not only when one side closes it.
	ctx, done := shares.relayContext(r.Context(), sh)
	defer done()
	s.proxy(w, r.WithContext(ctx), sharePathPrefix+token, sh.Session, sh.Owner, true)
}

// proxy passes a desktop request below base (e.g. /stream/<session>) to the
// gateway's stream of session as owner, view only if view.
func (s *streamProxy) proxy(w http.ResponseWriter, r *http.Request, base, session, owner string, view bool) {
	rest, hasSlash := strings.CutPrefix(r.URL.Path, base+"/")
	if !hasSlash {
		// KasmVNC's client uses relative URLs, so it needs the trailing slash.
		http.Redirect(w, r, base+"/", http.StatusFound)
		return
	}
	isPage := r.Method == http.MethodGet && (rest == "" || rest == "index.html" || rest == "vnc.html")
	if isPage && !r.URL.Query().Has("path") {
		// KasmVNC's client opens its WebSocket at /<path> of the host, default
		// /websockify, which is outside this session; its "path" URL parameter
		// moves it here.
		q := r.URL.Query()
		q.Set("path", strings.TrimPrefix(base, "/")+"/websockify")
		if view {
			q.Set("view_only", "1")
		}
		http.Redirect(w, r, r.URL.Path+"?"+q.Encode(), http.StatusFound)
		return
	}
	if isUpgrade(r) && !sameOrigin(r) {
		forwardError(w, http.StatusForbidden, "Cross-origin WebSocket refused.")
		return
	}
	rp := &httputil.ReverseProxy{
		Transport: streamTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(s.gateway)
			pr.Out.URL.Path = s.gateway.Path + "/control/v1/stream/" + session + "/" + rest
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = s.gateway.Host
			for _, h := range []string{"X-Forwarded-User", "X-Forwarded-Groups", "X-Forwarded-Email",
				"X-Forwarded-Preferred-Username", "X-Forwarded-Access-Token", "X-User-Id",
				"Authorization", "Cookie", owuiKeyHeader, owuiUserHeader, streamViewHeader} {
				pr.Out.Header.Del(h)
			}
			pr.Out.Header.Set(controlKeyHeaderMCP, s.controlKey)
			pr.Out.Header.Set("X-User-Id", owner)
			if view {
				pr.Out.Header.Set(streamViewHeader, "1")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			for _, h := range untrustedResponseHeaders {
				resp.Header.Del(h)
			}
			if isPage && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadGateway) {
				resp.Body.Close()
				resp.Header = http.Header{}
				page := stoppedPage(s.console, session)
				if view {
					// A viewer cannot start someone else's desktop.
					page = sharedStoppedPage
				}
				setStoppedPage(resp.Header)
				resp.Body = io.NopCloser(strings.NewReader(page))
				resp.ContentLength = int64(len(page))
			}
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

// streamViewHeader asks the gateway for the view-only desktop user. Only this
// server sets it; client copies are removed.
const streamViewHeader = "X-Stream-View"

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

// stoppedPage replaces a desktop that does not answer. The desktop origin has
// no console API, so it links to the console's desktop page (ui/desktop.html),
// which says why and offers to start it.
func stoppedPage(console, session string) string {
	link := ""
	if console != "" {
		u := console + "/ui/desktop.html?session=" + url.QueryEscape(session)
		link = `<p><a href="` + html.EscapeString(u) + `">Start it from the console</a></p>`
	}
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>termiplus desktop</title>
</head>
<body style="font-family: system-ui, sans-serif; margin: 2em">
<h1>termiplus</h1>
<p role="status">This desktop is not running (stopped, still starting, or removed).</p>
` + link + `
</body>
</html>
`
}

const sharedStoppedPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>termiplus desktop</title>
</head>
<body style="font-family: system-ui, sans-serif; margin: 2em">
<h1>termiplus</h1>
<p role="status">The shared desktop is not running right now. Ask its owner to start it, then reload this page.</p>
</body>
</html>
`

func setStoppedPage(h http.Header) {
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
}
