package mcp

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// people uses the same exact lookup as sharing; it never lists realm users,
// returns internal IDs/emails, or accepts a session owned by someone else.
func (u *UI) people(w http.ResponseWriter, r *http.Request, p Principal, id string) {
	if err := u.srv.entitledSession(r.Context(), p, id); err != nil {
		uiError(w, http.StatusNotFound, "no such desktop")
		return
	}
	if u.srv.dir == nil {
		uiError(w, http.StatusNotFound, "person lookup is unavailable")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if !usernamePattern.MatchString(query) {
		uiError(w, http.StatusBadRequest, "Enter an exact user name (1–255 supported characters).")
		return
	}
	now := time.Now()
	u.lookupMu.Lock()
	if u.lookups == nil {
		u.lookups = make(map[string]time.Time)
	}
	for user, at := range u.lookups {
		if now.Sub(at) > time.Minute {
			delete(u.lookups, user)
		}
	}
	at, exists := u.lookups[p.UserID]
	limited := now.Sub(at) < time.Second || (!exists && len(u.lookups) >= 1024)
	if !limited {
		u.lookups[p.UserID] = now
	}
	u.lookupMu.Unlock()
	if limited {
		w.Header().Set("Retry-After", "1")
		uiError(w, http.StatusTooManyRequests, "Wait a second before looking up another person.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	person, err := u.srv.dir.FindUser(ctx, query)
	if err != nil {
		uiError(w, http.StatusBadGateway, "person lookup unavailable")
		return
	}
	people := []map[string]string{}
	if person != nil && person.Enabled && strings.EqualFold(person.Username, query) {
		people = append(people, map[string]string{"username": person.Username})
	}
	uiJSON(w, http.StatusOK, map[string]any{"people": people})
}
