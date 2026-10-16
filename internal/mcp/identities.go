package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"termiplus/internal/naming"
)

// IdentityStore remembers the stable mapping between an OIDC subject and an
// Open WebUI user id. The first mapping is learned only from the trusted native
// Open WebUI plugin (verified OIDC identity plus the deployment's plugin key).
// Once created, mappings are immutable through this API: changing accounts or
// reusing one Open WebUI id for a different OIDC subject requires an explicit
// administrative edit of the store. This prevents a later plugin call from
// silently moving an existing user's persistent home to another identity.
//
// The store is scoped to one configured OIDC issuer. If multiple issuers are
// ever supported, keys must become issuer+subject before enabling them.
type IdentityStore struct {
	path string

	mu       sync.Mutex
	subjects map[string]string // OIDC subject -> Open WebUI user id
	owners   map[string]string // Open WebUI user id -> OIDC subject
}

type identityFile struct {
	Subjects map[string]string `json:"subjects"`
}

// OpenIdentityStore loads path, or starts empty if it does not exist yet.
func OpenIdentityStore(path string) (*IdentityStore, error) {
	s := &IdentityStore{path: path, subjects: map[string]string{}, owners: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f identityFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("identity store %s: %w", path, err)
	}
	for sub, owui := range f.Subjects {
		if !naming.UserID.MatchString(sub) || !naming.UserID.MatchString(owui) {
			return nil, fmt.Errorf("identity store %s: invalid entry %q -> %q", path, sub, owui)
		}
		if prior, exists := s.owners[owui]; exists && prior != sub {
			return nil, fmt.Errorf("identity store %s: Open WebUI id %q is mapped to multiple OIDC subjects", path, owui)
		}
		s.subjects[sub] = owui
		s.owners[owui] = sub
	}
	return s, nil
}

// Lookup returns the Open WebUI user id recorded for an OIDC subject.
func (s *IdentityStore) Lookup(sub string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owui, ok := s.subjects[sub]
	return owui, ok
}

// ErrIdentityConflict means a Record call would change an existing binding:
// the subject is already bound to another Open WebUI user, or the Open WebUI
// user is already bound to another subject.
var ErrIdentityConflict = errors.New("identity already bound to a different account")

// Record creates a mapping if neither identity is already bound elsewhere.
// Repeating the same mapping is idempotent. A conflicting pair returns an
// error wrapping ErrIdentityConflict and changes nothing; any other error means
// the binding could not be stored. Rebinding requires stopping the MCP server
// and editing the identity file: the store is read only at startup and
// rewritten from memory, so a live edit would be overwritten.
func (s *IdentityStore) Record(sub, owui string) error {
	if !naming.UserID.MatchString(sub) || !naming.UserID.MatchString(owui) {
		return fmt.Errorf("invalid identity %q -> %q", sub, owui)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if old, exists := s.subjects[sub]; exists {
		if old == owui {
			return nil
		}
		return fmt.Errorf("subject is bound to Open WebUI user %s: %w", old, ErrIdentityConflict)
	}
	if oldSub, exists := s.owners[owui]; exists {
		return fmt.Errorf("user id is bound to subject %s: %w", oldSub, ErrIdentityConflict)
	}

	s.subjects[sub] = owui
	s.owners[owui] = sub
	if err := s.save(); err != nil {
		delete(s.subjects, sub)
		delete(s.owners, owui)
		return fmt.Errorf("persist identity binding: %w", err)
	}
	return nil
}

// save writes the file atomically. Caller holds mu.
func (s *IdentityStore) save() error {
	data, err := json.MarshalIndent(identityFile{Subjects: s.subjects}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, append(data, '\n'))
}

// writeFileAtomic replaces path with data (mode 0600) via a temp file and
// rename, so readers never see a partial file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
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
	// Flush before the rename: a binding lost in a crash would reopen that
	// user for first enrollment.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// Persist the rename itself.
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}
