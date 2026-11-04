package netpolicy

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
)

// Store holds the admin-set network policy and the paths it renders to. It is
// the single writer of all three files and serialises concurrent admin writes.
//
//   - policyPath  the source of truth, read back on restart
//   - dnsPath     the forwarder's managed records (DnsmasqHosts)
//   - egressPath  the firewall's allowed internal destinations (EgressRules)
//
// A write validates first, then renders every file to a temp sibling and
// renames it into place, so a reader (dnsmasq reload, firewall rebuild) never
// sees a half-written file and a rejected policy changes nothing on disk.
type Store struct {
	policyPath string
	dnsPath    string
	egressPath string
	extraDeny  []netip.Prefix

	mu      sync.RWMutex
	current Policy
}

// NewStore loads the persisted policy if policyPath exists (and re-validates
// it, so a hand-edited or stale file cannot weaken the deny-list) and renders
// the derived files from it. A missing policy file starts from an empty policy.
// extraDeny adds operator-specific never-allowed ranges to the built-in list.
func NewStore(policyPath, dnsPath, egressPath string, extraDeny ...netip.Prefix) (*Store, error) {
	if policyPath == "" || dnsPath == "" || egressPath == "" {
		return nil, fmt.Errorf("netpolicy store needs policy, dns and egress paths")
	}
	s := &Store{policyPath: policyPath, dnsPath: dnsPath, egressPath: egressPath, extraDeny: extraDeny}

	// Ensure the parent directories exist (e.g. the forwarder's hostsdir) so the
	// first render can write into them.
	for _, p := range []string{policyPath, dnsPath, egressPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", filepath.Dir(p), err)
		}
	}

	b, err := os.ReadFile(policyPath)
	switch {
	case os.IsNotExist(err):
		// Start empty and render empty files so the forwarder and firewall have
		// something consistent to read on a fresh deployment.
		if err := s.write(Policy{}); err != nil {
			return nil, err
		}
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read policy: %w", err)
	}

	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse policy %s: %w", policyPath, err)
	}
	if err := s.write(p); err != nil {
		return nil, fmt.Errorf("persisted policy is invalid: %w", err)
	}
	return s, nil
}

// Get returns a copy of the current policy.
func (s *Store) Get() Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Policy{
		DNS:    append([]DNSEntry{}, s.current.DNS...),
		Egress: append([]EgressRule{}, s.current.Egress...),
	}
}

// Put validates p and, only if it passes, atomically rewrites the policy file
// and the rendered forwarder and firewall files. On any error nothing changes.
func (s *Store) Put(p Policy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(p)
}

// write validates and persists under the caller's lock (or during construction,
// before the store is shared).
func (s *Store) write(p Policy) error {
	v, err := Validate(p, s.extraDeny...)
	if err != nil {
		return err
	}
	// Marshal the validated, normalised policy so the stored file always matches
	// what the derived files were rendered from.
	stored := Policy{DNS: v.DNS, Egress: v.Egress}
	if stored.DNS == nil {
		stored.DNS = []DNSEntry{}
	}
	if stored.Egress == nil {
		stored.Egress = []EgressRule{}
	}
	j, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	j = append(j, '\n')

	// Stage all three files before publishing any of them. If a rename fails,
	// already-published files are rolled back to their previous bytes.
	if err := writeFilesAtomic([]fileChange{
		{path: s.dnsPath, data: []byte(v.DnsmasqHosts())},
		{path: s.egressPath, data: []byte(v.EgressRules())},
		{path: s.policyPath, data: j},
	}); err != nil {
		return err
	}
	s.current = stored
	return nil
}

type fileChange struct {
	path string
	data []byte
}

type stagedFile struct {
	fileChange
	tmp       string
	old       []byte
	existed   bool
	published bool
}

// writeFilesAtomic stages every replacement first, then publishes the files.
// A publication failure triggers rollback of prior renames. Rollback failures
// are returned explicitly because mixed runtime policy must not be hidden.
func writeFilesAtomic(changes []fileChange) error {
	staged := make([]stagedFile, 0, len(changes))
	cleanup := func() {
		for _, f := range staged {
			if f.tmp != "" {
				_ = os.Remove(f.tmp)
			}
		}
	}
	for _, change := range changes {
		f := stagedFile{fileChange: change}
		old, err := os.ReadFile(change.path)
		switch {
		case err == nil:
			f.old, f.existed = old, true
		case os.IsNotExist(err):
		default:
			cleanup()
			return fmt.Errorf("read prior %s: %w", change.path, err)
		}
		tmp, err := os.CreateTemp(filepath.Dir(change.path), ".tmp-"+filepath.Base(change.path)+"-*")
		if err != nil {
			cleanup()
			return fmt.Errorf("stage %s: %w", change.path, err)
		}
		f.tmp = tmp.Name()
		staged = append(staged, f)
		if _, err := tmp.Write(change.data); err != nil {
			_ = tmp.Close()
			cleanup()
			return fmt.Errorf("stage %s: %w", change.path, err)
		}
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			cleanup()
			return fmt.Errorf("sync %s: %w", change.path, err)
		}
		if err := tmp.Close(); err != nil {
			cleanup()
			return fmt.Errorf("close %s: %w", change.path, err)
		}
		// 0644: the forwarder and firewall run as other users and must read these.
		if err := os.Chmod(f.tmp, 0o644); err != nil {
			cleanup()
			return fmt.Errorf("chmod %s: %w", change.path, err)
		}
	}
	for i := range staged {
		f := &staged[i]
		if err := os.Rename(f.tmp, f.path); err != nil {
			var rollbackErr error
			for j := i - 1; j >= 0; j-- {
				prev := staged[j]
				if !prev.published {
					continue
				}
				var e error
				if prev.existed {
					e = writeFileAtomic(prev.path, prev.old)
				} else {
					e = os.Remove(prev.path)
					if os.IsNotExist(e) {
						e = nil
					}
				}
				if e != nil {
					rollbackErr = fmt.Errorf("%v; rollback %s: %w", rollbackErr, prev.path, e)
				}
			}
			cleanup()
			if rollbackErr != nil {
				return fmt.Errorf("publish %s: %w (rollback incomplete: %v)", f.path, err, rollbackErr)
			}
			return fmt.Errorf("publish %s: %w (previous files restored)", f.path, err)
		}
		f.tmp = ""
		f.published = true
	}
	for _, f := range staged {
		if dir, err := os.Open(filepath.Dir(f.path)); err == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
	}
	cleanup()
	return nil
}

// writeFileAtomic writes data to a temp file in the same directory, fsyncs it,
// and renames it over path. The rename is atomic on the same filesystem, so a
// concurrent reader sees either the old or the new file, never a partial one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 0644: the forwarder and firewall run as other users and must read these.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
