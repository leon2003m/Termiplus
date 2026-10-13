package mcp

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
)

// The management UI is a small web console on its own listener (UI_LISTEN_ADDR)
// under /ui/. Browsers reach it only through the sign-in proxy (desk-auth,
// oauth2-proxy cookie flow), which forwards the OIDC subject and groups; the
// subject is mapped to the Open WebUI user via the IdentityStore. Every action
// goes through the same tools, RBAC and entitlements as the MCP server. The
// person clicking is the human in the loop, so consequential actions are sent
// as confirmed after the page has asked them (window.confirm).
//
// Cross-site request forgery: the session cookie is SameSite=Lax, and every
// non-GET request must carry X-Termiplus-UI and a same-origin Origin header,
// which a foreign page cannot send without a CORS preflight this server never
// answers.

//go:embed ui
var uiFiles embed.FS

const uiPrefix = "/ui/"

const uiCSRFHeader = "X-Termiplus-UI"

// UI serves the management console.
type UI struct {
	srv    *Server
	ids    *IdentityStore
	static http.Handler
}

// NewUI returns the management UI for srv. ids maps browser identities (OIDC
// subjects) to Open WebUI users.
func NewUI(srv *Server, ids *IdentityStore) *UI {
	sub, _ := fs.Sub(uiFiles, "ui")
	return &UI{srv: srv, ids: ids, static: http.StripPrefix(uiPrefix, http.FileServer(http.FS(sub)))}
}

func (u *UI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")

	switch {
	case r.URL.Path == "/healthz":
		uiJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case strings.HasPrefix(r.URL.Path, uiPrefix+"api/"):
		u.api(w, r, strings.TrimPrefix(r.URL.Path, uiPrefix+"api/"))
	case strings.HasPrefix(r.URL.Path, uiPrefix) && r.Method == http.MethodGet:
		u.static.ServeHTTP(w, r)
	default:
		uiError(w, http.StatusNotFound, "not found")
	}
}

func (u *UI) api(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodGet && (r.Header.Get(uiCSRFHeader) != "1" || !sameOrigin(r)) {
		uiError(w, http.StatusForbidden, "cross-site request refused")
		return
	}
	// Identity comes from the sign-in proxy only (it overwrites client copies).
	p, err := ProxyHeaderAuthenticator{}.Authenticate(r)
	if err != nil {
		uiError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	owui, known := u.ids.Lookup(p.UserID)

	if rest == "me" && r.Method == http.MethodGet {
		uiJSON(w, http.StatusOK, map[string]any{"linked": known, "roles": nonNil(p.Roles)})
		return
	}
	if !known {
		uiError(w, http.StatusConflict, "your sign-in is not linked to an Open WebUI user yet: use the termiplus tool in Open WebUI once")
		return
	}
	p = Principal{UserID: owui, Roles: p.Roles}

	switch {
	case rest == "workspaces" && r.Method == http.MethodGet:
		u.run(w, r, p, "list_workspaces", nil)
	case rest == "sessions" && r.Method == http.MethodGet:
		u.run(w, r, p, "list_sessions", nil)
	case rest == "sessions" && r.Method == http.MethodPost:
		var req struct {
			Workspace  string `json:"workspace"`
			Persistent bool   `json:"persistent"`
			Label      string `json:"label"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			uiError(w, http.StatusBadRequest, "invalid request")
			return
		}
		u.run(w, r, p, "request_session", map[string]any{
			"workspace": req.Workspace, "persistent": req.Persistent, "label": req.Label, "_confirmed": true,
		})
	case strings.HasPrefix(rest, "sessions/") && r.Method == http.MethodDelete:
		u.run(w, r, p, "destroy_session", map[string]any{
			"session_id": strings.TrimPrefix(rest, "sessions/"), "_confirmed": true,
		})
	default:
		uiError(w, http.StatusNotFound, "not found")
	}
}

func (u *UI) run(w http.ResponseWriter, r *http.Request, p Principal, tool string, args map[string]any) {
	if args == nil {
		args = map[string]any{}
	}
	res, rerr := u.srv.invoke(r.Context(), p, tool, args)
	switch {
	case rerr != nil && rerr.Code == codeForbidden:
		uiError(w, http.StatusForbidden, rerr.Message)
	case rerr != nil:
		uiError(w, http.StatusBadRequest, rerr.Message)
	case res.IsError:
		uiError(w, http.StatusBadRequest, res.Content[0].Text)
	default:
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, res.Content[0].Text)
	}
}

// sameOrigin reports whether the request has exactly one Origin header whose
// host matches the Host the browser addressed.
func sameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) != 1 {
		return false
	}
	o, err := url.Parse(origins[0])
	if err != nil || (o.Scheme != "http" && o.Scheme != "https") || o.Host == "" {
		return false
	}
	return strings.EqualFold(o.Host, r.Host)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func uiJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func uiError(w http.ResponseWriter, status int, msg string) {
	uiJSON(w, status, map[string]string{"error": msg})
}
