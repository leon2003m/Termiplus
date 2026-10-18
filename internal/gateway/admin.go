package gateway

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"termiplus/internal/naming"
	"termiplus/internal/netpolicy"
)

// The admin API lets an operator set the terminal network policy at runtime:
// custom DNS records and per-destination internal egress allowances. It is
// gated by a dedicated admin key (never the per-user connection key) on a
// separate header, and is reached at /admin/* before any policy routing or the
// bearer check. When no admin key and store are configured the whole surface
// returns 404, so existing deployments are unaffected.

const adminKeyHeader = "X-Admin-Key"

const maxAdminBody = 256 << 10

// serveAdmin handles /admin/*. It returns true if it fully handled the request
// (so ServeHTTP must stop); false means the path is not an admin path.
func (g *Gateway) serveAdmin(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/admin" && !strings.HasPrefix(r.URL.Path, "/admin/") {
		return false
	}
	// The feature is off unless both the key and the store are configured.
	if g.cfg.AdminKey == "" || g.cfg.NetPolicy == nil {
		httpError(w, http.StatusNotFound, "not found")
		return true
	}
	if !g.checkAdminKey(r) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return true
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/admin/homes/"); ok {
		g.serveHomes(w, r, rest)
		return true
	}
	if r.URL.Path != "/admin/netpolicy" {
		httpError(w, http.StatusNotFound, "not found")
		return true
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, g.cfg.NetPolicy.Get())
	case http.MethodPut:
		g.putNetPolicy(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
	return true
}

func (g *Gateway) putNetPolicy(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxAdminBody+1))
	if err != nil {
		httpError(w, http.StatusBadRequest, "could not read body")
		return
	}
	if len(data) > maxAdminBody {
		httpError(w, http.StatusRequestEntityTooLarge, "policy too large")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p netpolicy.Policy
	if err := dec.Decode(&p); err != nil {
		httpError(w, http.StatusBadRequest, fmt.Sprintf("invalid policy: %v", err))
		return
	}
	if dec.More() {
		httpError(w, http.StatusBadRequest, "trailing data after policy")
		return
	}
	// Put validates against the deny-list and renders the forwarder and firewall
	// files; a rejected policy changes nothing on disk.
	if err := g.cfg.NetPolicy.Put(p); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	stored := g.cfg.NetPolicy.Get()
	log.Printf("admin: network policy updated (%d dns, %d egress)", len(stored.DNS), len(stored.Egress))
	writeJSON(w, http.StatusOK, stored)
}

func (g *Gateway) checkAdminKey(r *http.Request) bool {
	got := r.Header.Get(adminKeyHeader)
	return subtle.ConstantTimeCompare([]byte(got), []byte(g.cfg.AdminKey)) == 1
}

// serveHomes handles /admin/homes/<home>[/archive] for the user in X-User-Id:
//
//	GET    .../<home>/archive  the home as a tar (entries "user/...")
//	PUT    .../<home>/archive  extract such a tar into the home (stops the
//	                           user's workspaces using it; creates the home)
//	DELETE .../<home>          delete the user's containers using the home and
//	                           the home itself (needs ALLOW_HOME_REMOVE=1 on
//	                           the socket proxy)
func (g *Gateway) serveHomes(w http.ResponseWriter, r *http.Request, rest string) {
	userID := r.Header.Get("X-User-Id")
	if !naming.UserID.MatchString(userID) || userID == specUser {
		httpError(w, http.StatusBadRequest, "valid X-User-Id required")
		return
	}
	home, sub, _ := strings.Cut(rest, "/")
	if !naming.PolicyName.MatchString(home) || len(g.mgr.policiesForHome(home)) == 0 {
		httpError(w, http.StatusNotFound, "unknown home")
		return
	}
	switch {
	case sub == "archive" && r.Method == http.MethodGet:
		g.exportHome(w, r, userID, home)
	case sub == "archive" && r.Method == http.MethodPut:
		if err := g.mgr.importHome(r.Context(), userID, home, r.Body); err != nil {
			log.Printf("admin: import home %q of %q: %q", home, userID, err)
			httpError(w, http.StatusBadGateway, "could not restore the home")
			return
		}
		log.Printf("admin: restored home %q of %q", home, userID)
		w.WriteHeader(http.StatusNoContent)
	case sub == "archive":
		methodNotAllowed(w, "GET, PUT")
	case sub == "" && r.Method == http.MethodDelete:
		if err := g.mgr.removeHome(r.Context(), userID, home); err != nil {
			log.Printf("admin: remove home %q of %q: %q", home, userID, err)
			httpError(w, http.StatusBadGateway, "could not remove the home")
			return
		}
		log.Printf("admin: removed home %q of %q", home, userID)
		w.WriteHeader(http.StatusNoContent)
	case sub == "":
		methodNotAllowed(w, "DELETE")
	default:
		httpError(w, http.StatusNotFound, "not found")
	}
}

// exportHome streams the home as a tar. Errors after the first byte can only
// end the stream early; tar's missing end marker makes that detectable.
func (g *Gateway) exportHome(w http.ResponseWriter, r *http.Request, userID, home string) {
	sw := &startWriter{w: w, header: func() {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="home-%s.tar"`, home))
	}}
	// The reader paces the export and the helper lock is held meanwhile;
	// manager.exportHome adds the deadline and the byte cap.
	err := g.mgr.exportHome(r.Context(), userID, home, sw)
	switch {
	case err == nil && !sw.started:
		sw.header()
		w.WriteHeader(http.StatusOK)
	case errors.Is(err, errNoHome) && !sw.started:
		httpError(w, http.StatusNotFound, "no such home")
	case errors.Is(err, errExportTooLarge) && !sw.started:
		httpError(w, http.StatusRequestEntityTooLarge, "the home is larger than the export limit")
	case err != nil && !sw.started:
		log.Printf("export home %q of %q: %q", home, userID, err)
		httpError(w, http.StatusBadGateway, "could not read the home")
	case err != nil:
		log.Printf("export home %q of %q: %q (stream cut)", home, userID, err)
	}
}

// startWriter sets the response headers just before the first byte.
type startWriter struct {
	w       http.ResponseWriter
	header  func()
	started bool
}

func (s *startWriter) Write(p []byte) (int, error) {
	if !s.started {
		s.started = true
		s.header()
		s.w.WriteHeader(http.StatusOK)
	}
	return s.w.Write(p)
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	httpError(w, http.StatusMethodNotAllowed, "method not allowed")
}
