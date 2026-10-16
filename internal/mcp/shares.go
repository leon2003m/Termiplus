package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Shared desktops: the owner of a desktop session creates a link that lets
// other signed-in users watch it (view only: KasmVNC's "viewer" user, no
// keyboard or mouse) until it expires or is revoked. The link carries a
// random token; the store keeps only its hash. A share can be limited to one
// user of the identity provider. Viewers still sign in (desk-auth), so a
// leaked link alone is not enough.

const (
	shareDefaultTTL = time.Hour
	shareMaxTTL     = 24 * time.Hour
	shareMaxActive  = 10 // per owner
	sharePathPrefix = "/share/"
)

// Share is one active share link.
type Share struct {
	ID         string    `json:"share_id"` // public id: first 12 hex chars of the token hash
	Hash       string    `json:"-"`
	Owner      string    `json:"-"` // Open WebUI user id of the session owner
	Session    string    `json:"session_id"`
	Workspace  string    `json:"workspace,omitempty"`
	Created    time.Time `json:"created"`
	Expires    time.Time `json:"expires"`
	ForSubject string    `json:"-"`
	ForName    string    `json:"for_user,omitempty"`
}

type shareFileEntry struct {
	Hash       string    `json:"hash"`
	Owner      string    `json:"owner"`
	Session    string    `json:"session"`
	Workspace  string    `json:"workspace,omitempty"`
	Created    time.Time `json:"created"`
	Expires    time.Time `json:"expires"`
	ForSubject string    `json:"for_subject,omitempty"`
	ForName    string    `json:"for_name,omitempty"`
}

// ShareStore keeps share links, in a file when path is set.
type ShareStore struct {
	path string
	now  func() time.Time

	mu     sync.Mutex
	shares map[string]Share // by token hash
}

// OpenShareStore loads path, or starts empty. path "" keeps shares in memory.
func OpenShareStore(path string) (*ShareStore, error) {
	s := &ShareStore{path: path, now: time.Now, shares: map[string]Share{}}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []shareFileEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("share store %s: %w", path, err)
	}
	for _, e := range entries {
		s.shares[e.Hash] = Share{ID: e.Hash[:12], Hash: e.Hash, Owner: e.Owner, Session: e.Session, Workspace: e.Workspace,
			Created: e.Created, Expires: e.Expires, ForSubject: e.ForSubject, ForName: e.ForName}
	}
	return s, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// saveLocked drops expired shares and writes the file atomically.
func (s *ShareStore) saveLocked() error {
	now := s.now()
	entries := []shareFileEntry{}
	for h, sh := range s.shares {
		if !now.Before(sh.Expires) {
			delete(s.shares, h)
			continue
		}
		entries = append(entries, shareFileEntry{Hash: h, Owner: sh.Owner, Session: sh.Session, Workspace: sh.Workspace,
			Created: sh.Created, Expires: sh.Expires, ForSubject: sh.ForSubject, ForName: sh.ForName})
	}
	if s.path == "" {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Hash < entries[j].Hash })
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".shares-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// Create adds a share and returns it with the secret token for the link.
func (s *ShareStore) Create(owner, session, workspace string, ttl time.Duration, forSubject, forName string) (Share, string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return Share{}, "", err
	}
	token := hex.EncodeToString(buf[:])
	h := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	active := 0
	for _, sh := range s.shares {
		if sh.Owner == owner && now.Before(sh.Expires) {
			active++
		}
	}
	if active >= shareMaxActive {
		return Share{}, "", fmt.Errorf("you already have %d active share links: revoke one first", shareMaxActive)
	}
	sh := Share{ID: h[:12], Hash: h, Owner: owner, Session: session, Workspace: workspace,
		Created: now.UTC().Truncate(time.Second), Expires: now.Add(ttl).UTC().Truncate(time.Second), ForSubject: forSubject, ForName: forName}
	s.shares[h] = sh
	if err := s.saveLocked(); err != nil {
		delete(s.shares, h)
		return Share{}, "", err
	}
	return sh, token, nil
}

// Lookup returns the active share for a link token.
func (s *ShareStore) Lookup(token string) (Share, bool) {
	if len(token) != 64 {
		return Share{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sh, ok := s.shares[hashToken(token)]
	if !ok || !s.now().Before(sh.Expires) {
		return Share{}, false
	}
	return sh, true
}

// List returns the owner's active shares, newest first.
func (s *ShareStore) List(owner string) []Share {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := []Share{}
	for _, sh := range s.shares {
		if sh.Owner == owner && now.Before(sh.Expires) {
			out = append(out, sh)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created.After(out[j].Created) || out[i].Created.Equal(out[j].Created) && out[i].ID < out[j].ID
	})
	return out
}

// Revoke ends one of the owner's shares.
func (s *ShareStore) Revoke(owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, sh := range s.shares {
		if sh.Owner == owner && sh.ID == id {
			delete(s.shares, h)
			return s.saveLocked()
		}
	}
	return fmt.Errorf("no share %q", id)
}

// WithShares enables share_desktop and the /share/ links.
func (s *Server) WithShares(st *ShareStore) *Server {
	s.shares = st
	return s
}

func (s *Server) shareDesktop(ctx context.Context, p Principal, args map[string]any) (any, error) {
	if s.shares == nil || s.uiBase == "" {
		return nil, errors.New("desktop sharing is not configured on this server (needs the desktop overlay)")
	}
	switch argString(args, "action") {
	case "list":
		return map[string]any{"shares": s.shares.List(p.UserID)}, nil
	case "revoke":
		if err := s.shares.Revoke(p.UserID, argString(args, "share_id")); err != nil {
			return nil, err
		}
		return map[string]any{"revoked": argString(args, "share_id")}, nil
	case "create":
	default:
		return nil, errors.New(`action must be "create", "list" or "revoke"`)
	}
	id := argString(args, "session_id")
	sessions, err := s.backend.ListSessions(ctx, p)
	if err != nil {
		return nil, err
	}
	var sess *Session
	for i := range sessions {
		if sessions[i].ID == id {
			sess = &sessions[i]
		}
	}
	if sess == nil {
		return nil, fmt.Errorf("no session %q of yours", id)
	}
	if sess.StreamURL == "" {
		return nil, fmt.Errorf("session %q has no desktop to share", id)
	}
	ttl := shareDefaultTTL
	if m, ok := args["minutes"].(float64); ok {
		ttl = time.Duration(m) * time.Minute
		if ttl < 5*time.Minute || ttl > shareMaxTTL {
			return nil, errors.New("minutes must be between 5 and 1440")
		}
	}
	forSubject, forName := "", argString(args, "for_user")
	if forName != "" {
		if s.dir == nil {
			return nil, errors.New("sharing with one named user needs user management (Keycloak) configured; leave for_user empty to share with any signed-in user who has the link")
		}
		u, err := s.findUser(ctx, forName)
		if err != nil {
			return nil, err
		}
		forSubject, forName = u.ID, u.Username
	}
	sh, token, err := s.shares.Create(p.UserID, sess.ID, sess.Workspace, ttl, forSubject, forName)
	if err != nil {
		return nil, err
	}
	who := "anyone who can sign in to termiplus and has the link"
	if forName != "" {
		who = "only " + forName + " (after signing in)"
	}
	return map[string]any{
		"share_id": sh.ID,
		"url":      s.uiBase + sharePathPrefix + token + "/",
		"expires":  sh.Expires,
		"who":      who,
		"note":     "view only: viewers see the screen (and clipboard contents) but cannot type or click; revoke with share_desktop action revoke",
	}, nil
}

func (s *Server) shareGate(p Principal, args map[string]any) (bool, error) {
	switch argString(args, "action") {
	case "create":
		return true, nil // shows your screen to others: a human confirms
	case "list", "revoke":
		return false, nil
	}
	return false, errors.New(`action must be "create", "list" or "revoke"`)
}

func (s *Server) shareTools() []tool {
	return []tool{{
		name:        "share_desktop",
		description: "Let other people watch one of your desktop sessions (view only, no keyboard or mouse) through a link that expires: action \"create\" (session_id, optional minutes 5-1440, default 60, optional for_user to allow only that username), \"list\" your active links, or \"revoke\" one (share_id). Creating a link requires explicit human confirmation.",
		inputSchema: `{"type":"object","properties":{"action":{"type":"string","enum":["create","list","revoke"]},"session_id":{"type":"string"},"minutes":{"type":"integer","minimum":5,"maximum":1440},"for_user":{"type":"string"},"share_id":{"type":"string"}},"required":["action"],"additionalProperties":true}`,
		destructive: true,
		gate:        s.shareGate,
		handle:      s.shareDesktop,
	}}
}
