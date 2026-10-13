package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"

	"termiplus/internal/naming"
)

// IdentityStore remembers which Open WebUI user an OIDC subject belongs to.
// The MCP server learns the pair from Open WebUI plugin calls (verified token
// plus plugin key, see ProxyHeaderAuthenticator) and the management UI, where
// the browser sign-in only yields the subject, uses it to find the user's
// sessions. A subject that never came through the plugin is unknown. The file
// holds no secrets, only ids.
type IdentityStore struct {
	path string

	mu       sync.Mutex
	subjects map[string]string // OIDC subject -> Open WebUI user id
}

type identityFile struct {
	Subjects map[string]string `json:"subjects"`
}

// OpenIdentityStore loads path, or starts empty if it does not exist yet.
func OpenIdentityStore(path string) (*IdentityStore, error) {
	s := &IdentityStore{path: path, subjects: map[string]string{}}
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
		s.subjects[sub] = owui
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

// Record stores sub -> owui and persists it when it changed. The newest pair
// wins: a subject belongs to one Open WebUI user at a time.
func (s *IdentityStore) Record(sub, owui string) error {
	if !naming.UserID.MatchString(sub) || !naming.UserID.MatchString(owui) {
		return fmt.Errorf("invalid identity %q -> %q", sub, owui)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, had := s.subjects[sub]
	if had && old == owui {
		return nil
	}
	if had {
		log.Printf("identity: subject %s now belongs to Open WebUI user %s (was %s)", sub, owui, old)
	}
	s.subjects[sub] = owui
	if err := s.save(); err != nil {
		if had {
			s.subjects[sub] = old
		} else {
			delete(s.subjects, sub)
		}
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
