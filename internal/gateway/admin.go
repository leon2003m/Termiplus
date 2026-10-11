package gateway

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

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
