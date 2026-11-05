package mcp

import (
	"context"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"termiplus/internal/preview"
)

const controlKeyHeaderMCP = "X-Control-Key"

// DesktopHandler now serves only expiring, read-only screen shares. The owner
// works in Open WebUI's native Desktop tab; there is no second desktop proxy.
type DesktopHandler struct {
	srv        *Server
	gateway    string
	controlKey string
}

func NewDesktopHandler(srv *Server, _ *IdentityStore, gatewayBase, controlKey string) (*DesktopHandler, error) {
	u, err := url.Parse(strings.TrimSuffix(gatewayBase, "/"))
	if err != nil {
		return nil, err
	}
	return &DesktopHandler{srv: srv, gateway: u.String(), controlKey: controlKey}, nil
}

var shareClient = &http.Client{Transport: newRelayTransport(15e9), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (d *DesktopHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		uiJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet || isUpgrade(r) || r.Header.Get("Service-Worker") != "" {
		forwardError(w, 403, "Only viewing is allowed.")
		return
	}
	if !strings.HasPrefix(r.URL.Path, sharePathPrefix) || d.srv.shares == nil {
		forwardError(w, 404, "Not found.")
		return
	}
	p, err := (ProxyHeaderAuthenticator{}).Authenticate(r)
	if err != nil {
		forwardError(w, 401, "Not signed in.")
		return
	}
	token, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, sharePathPrefix), "/")
	sh, ok := d.srv.shares.Lookup(token)
	if !ok {
		forwardError(w, 404, "This share link is not valid or has expired.")
		return
	}
	if sh.ForSubject != "" && sh.ForSubject != p.UserID {
		forwardError(w, 403, "This screen was shared with someone else.")
		return
	}
	switch rest {
	case "":
		log.Printf("share %q viewed by %q", sh.ID, p.UserID)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Shared screen</title><h1>Shared screen · view only</h1><p>Updates every five seconds. Keyboard, mouse, clipboard and audio are not shared.</p><img id="screen" alt="Shared desktop"><p id="status"></p><script src="`+sharePathPrefix+token+`/viewer.js" defer></script>`)
	case "viewer.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, `const image=document.getElementById("screen"); const status=document.getElementById("status"); function refresh(){if(document.hidden)return; image.src=location.pathname.replace(/\/?$/, "/")+"screen.png?t="+Date.now();} image.onload=()=>status.textContent=""; image.onerror=()=>{image.removeAttribute("src");status.textContent="Screen unavailable, share expired or revoked.";}; refresh(); setInterval(refresh,5000); document.addEventListener("visibilitychange",refresh);`)
	case "screen.png":
		relayCtx, done := d.srv.shares.relayContext(r.Context(), sh)
		defer done()
		ctx, cancel := context.WithTimeout(relayCtx, 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.gateway+"/control/v1/screen/"+url.PathEscape(sh.Session), nil)
		if err != nil {
			forwardError(w, 502, "Screen unavailable.")
			return
		}
		req.Header.Set(controlKeyHeaderMCP, d.controlKey)
		req.Header.Set("X-User-Id", sh.Owner)
		resp, err := shareClient.Do(req)
		if err != nil {
			forwardError(w, 502, "Screen unavailable.")
			return
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, preview.MaxScreenBytes+1))
		if err != nil || resp.StatusCode != 200 {
			forwardError(w, 502, "Screen unavailable.")
			return
		}
		data, err = preview.Screen(data)
		if err != nil {
			forwardError(w, 502, "Invalid screen.")
			return
		}
		// Revocation/expiry also wins over a response that arrived during cancellation.
		if _, ok := d.srv.shares.Lookup(token); !ok || ctx.Err() != nil {
			forwardError(w, 404, "Share expired or revoked.")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	default:
		forwardError(w, 404, "Not found.")
	}
}

func isUpgrade(r *http.Request) bool { return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") }

func forwardError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "<!doctype html><meta charset=utf-8><title>termiplus</title><p>"+html.EscapeString(msg)+"</p>")
}
