package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sort"
	"sync"
	"time"
)

// Departed users: a user who is disabled in the identity provider (Keycloak),
// or who was known here and has been deleted there, is "departed" from the
// first check that sees it. After MCP_DEPARTED_AFTER the MCP server has the
// gateway archive their homes (a compressed backup each) and delete their
// containers and, where the socket proxy allows it, their homes. When a
// disabled user is enabled again, their archived homes are restored. Admins
// see and drive this with manage_departed. Every action is in the audit log
// (actor "termiplus").

// DepartedEntry is one departed user.
type DepartedEntry struct {
	Subject    string          `json:"subject"`
	Username   string          `json:"username,omitempty"`
	Reason     string          `json:"reason"` // "disabled" or "deleted"
	Since      time.Time       `json:"since"`
	ArchivedAt *time.Time      `json:"archived_at,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"` // the gateway's answer per user id
	Error      string          `json:"error,omitempty"`  // last failed attempt
}

// DepartedStore keeps departed users in a file.
type DepartedStore struct {
	path string

	mu      sync.Mutex
	entries map[string]*DepartedEntry // by subject
}

// OpenDepartedStore loads path or starts empty.
func OpenDepartedStore(path string) (*DepartedStore, error) {
	s := &DepartedStore{path: path, entries: map[string]*DepartedEntry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*DepartedEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("departed store %s: %w", path, err)
	}
	for _, e := range list {
		s.entries[e.Subject] = e
	}
	return s, nil
}

func (s *DepartedStore) saveLocked() error {
	list := make([]*DepartedEntry, 0, len(s.entries))
	for _, e := range s.entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Subject < list[j].Subject })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, data)
}

func (s *DepartedStore) list() []DepartedEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DepartedEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// departed holds the cleanup's configuration (WithDeparted).
type departed struct {
	store *DepartedStore
	after time.Duration
	now   func() time.Time
	run   sync.Mutex // one check or admin action at a time
}

// WithDeparted enables the departed-user cleanup: homes are archived after
// `after`. It needs the directory (WithDirectory) and a gateway backend.
func (s *Server) WithDeparted(store *DepartedStore, after time.Duration) *Server {
	s.departed = &departed{store: store, after: after, now: time.Now}
	return s
}

// RunDeparted checks every interval until ctx ends.
func (s *Server) RunDeparted(ctx context.Context, interval time.Duration) {
	for {
		if err := s.CheckDeparted(ctx); err != nil {
			log.Printf("departed users: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// gatewayUserIDs are the ids the user's workspaces may be keyed by: the Open
// WebUI user id the plugin reported for the subject, and the subject itself
// (sessions created without the plugin key).
func (s *Server) gatewayUserIDs(subject string) []string {
	ids := []string{subject}
	if s.ids != nil {
		if owui, ok := s.ids.Lookup(subject); ok && owui != subject {
			ids = append([]string{owui}, ids...)
		}
	}
	return ids
}

var systemPrincipal = Principal{UserID: "termiplus", Subject: "system"}

// CheckDeparted runs one round: record newly departed users, restore users
// who came back, and archive those departed for longer than the limit.
func (s *Server) CheckDeparted(ctx context.Context) error {
	d := s.departed
	if d == nil || s.dir == nil {
		return errors.New("departed-user cleanup is not configured")
	}
	hb, err := s.homeBackend()
	if err != nil {
		return err
	}
	d.run.Lock()
	defer d.run.Unlock()
	users, err := s.dir.Users(ctx)
	if err != nil {
		return err // never guess from a partial listing
	}
	now := d.now().UTC()
	bySub := map[string]DirUser{}
	for _, u := range users {
		bySub[u.ID] = u
	}

	d.store.mu.Lock()
	changed := false
	var comeBack []*DepartedEntry
	for _, u := range users {
		e := d.store.entries[u.ID]
		switch {
		case !u.Enabled && e == nil:
			d.store.entries[u.ID] = &DepartedEntry{Subject: u.ID, Username: u.Username, Reason: "disabled", Since: now}
			changed = true
		case u.Enabled && e != nil:
			delete(d.store.entries, u.ID)
			changed = true
			if e.ArchivedAt != nil {
				comeBack = append(comeBack, e)
			}
		}
	}
	// Known here, gone there. An empty listing is treated as an error of the
	// directory, not as everyone leaving.
	if s.ids != nil && len(users) > 0 {
		for _, sub := range s.ids.Subjects() {
			if _, ok := bySub[sub]; !ok && d.store.entries[sub] == nil {
				d.store.entries[sub] = &DepartedEntry{Subject: sub, Reason: "deleted", Since: now}
				changed = true
			}
		}
	}
	var due []string
	for sub, e := range d.store.entries {
		if e.ArchivedAt == nil && now.Sub(e.Since) >= d.after {
			due = append(due, sub)
		}
	}
	if changed {
		if err := d.store.saveLocked(); err != nil {
			d.store.mu.Unlock()
			return err
		}
	}
	d.store.mu.Unlock()

	for _, e := range comeBack {
		res, err := s.userHomes(ctx, hb, e.Subject, "restore")
		s.audit(systemPrincipal, s.findTool("manage_departed"), map[string]any{"action": "restore", "subject": e.Subject, "username": e.Username, "automatic": true}, outcome(err), err)
		log.Printf("departed users: %s (%s) is back: restored %s (err %v)", e.Username, e.Subject, res, err)
	}
	sort.Strings(due)
	for _, sub := range due {
		_ = s.archiveDeparted(ctx, hb, sub, true)
	}
	return nil
}

func outcome(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// userHomes runs action for every gateway user id of the subject.
func (s *Server) userHomes(ctx context.Context, hb HomeBackend, subject, action string) (json.RawMessage, error) {
	results := map[string]json.RawMessage{}
	var firstErr error
	for _, id := range s.gatewayUserIDs(subject) {
		raw, err := hb.UserHomes(ctx, id, action)
		if err != nil {
			raw, _ = json.Marshal(map[string]string{"error": err.Error()})
			if firstErr == nil {
				firstErr = err
			}
		}
		results[id] = raw
	}
	out, _ := json.Marshal(results)
	return out, firstErr
}

// archiveDeparted archives the subject's homes now and records the result.
func (s *Server) archiveDeparted(ctx context.Context, hb HomeBackend, subject string, automatic bool) error {
	d := s.departed
	res, err := s.userHomes(ctx, hb, subject, "archive")
	d.store.mu.Lock()
	e := d.store.entries[subject]
	if e == nil {
		e = &DepartedEntry{Subject: subject, Reason: "admin", Since: d.now().UTC()}
		d.store.entries[subject] = e
	}
	e.Result = res
	if err != nil {
		e.Error = err.Error()
	} else {
		t := d.now().UTC()
		e.ArchivedAt, e.Error = &t, ""
	}
	serr := d.store.saveLocked()
	username := e.Username
	d.store.mu.Unlock()
	s.audit(systemPrincipal, s.findTool("manage_departed"), map[string]any{"action": "archive", "subject": subject, "username": username, "automatic": automatic}, outcome(err), err)
	log.Printf("departed users: archived %s (%s): %s (err %v)", username, subject, res, err)
	if err != nil {
		return err
	}
	return serr
}

func (s *Server) manageDeparted(ctx context.Context, p Principal, args map[string]any) (any, error) {
	d := s.departed
	if d == nil || s.dir == nil {
		return nil, errors.New("the departed-user cleanup is not configured on this server (MCP_DEPARTED_AFTER, user management)")
	}
	action := argString(args, "action")
	if action == "list" {
		return map[string]any{"departed": d.store.list(), "archive_after": d.after.String()}, nil
	}
	hb, err := s.homeBackend()
	if err != nil {
		return nil, err
	}
	u, err := s.findUser(ctx, argString(args, "username"))
	if err != nil {
		return nil, err
	}
	d.run.Lock()
	defer d.run.Unlock()
	switch action {
	case "archive":
		if u.Enabled {
			return nil, fmt.Errorf("%s is still enabled: disable the user in the identity provider first", u.Username)
		}
		if err := s.archiveDeparted(ctx, hb, u.ID, false); err != nil {
			return nil, err
		}
	case "restore":
		res, err := s.userHomes(ctx, hb, u.ID, "restore")
		if err != nil {
			return nil, err
		}
		d.store.mu.Lock()
		if e := d.store.entries[u.ID]; e != nil && e.ArchivedAt != nil {
			delete(d.store.entries, u.ID)
			_ = d.store.saveLocked()
		}
		d.store.mu.Unlock()
		return map[string]any{"username": u.Username, "restored": json.RawMessage(res)}, nil
	default:
		return nil, errors.New(`action must be "list", "archive" or "restore"`)
	}
	d.store.mu.Lock()
	defer d.store.mu.Unlock()
	return d.store.entries[u.ID], nil
}

func (s *Server) departedTools() []tool {
	return []tool{{
		name:        "manage_departed",
		description: "Departed users (disabled or deleted in the identity provider) and their archived homes (admin): \"list\" them, \"archive\" a disabled user's homes now (username), or \"restore\" a user's archived homes (username). Archiving and restoring require the termiplus-admin role and explicit human confirmation.",
		inputSchema: `{"type":"object","properties":{"action":{"type":"string","enum":["list","archive","restore"]},"username":{"type":"string"}},"required":["action"],"additionalProperties":true}`,
		destructive: true,
		gate: func(p Principal, args map[string]any) (bool, error) {
			if !p.HasRole(RoleAdmin) {
				return false, fmt.Errorf("departed users require the %s role", RoleAdmin)
			}
			switch argString(args, "action") {
			case "list":
				return false, nil
			case "archive", "restore":
				return true, nil
			}
			return false, errors.New(`action must be "list", "archive" or "restore"`)
		},
		handle: s.manageDeparted,
	}}
}
