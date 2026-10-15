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

// Record creates a mapping if neither identity is already bound elsewhere.
// Repeating the same mapping is idempotent. Rebinding requires an explicit
// administrative edit of the identity file; normal plugin requests cannot
// transfer ownership of an existing user's home.
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
		return fmt.Errorf("OIDC subject is already linked to a different Open WebUI user; administrator intervention required")
	}
	if oldSub, exists := s.owners[owui]; exists && oldSub != sub {
		return fmt.Errorf("Open WebUI user is already linked to a different OIDC subject; administrator intervention required")
	}

	s.subjects[sub] = owui
	s.owners[owui] = sub
	if err := s.save(); err != nil {
		delete(s.subjects, sub)
		delete(s.owners, owui)
		return err
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
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
