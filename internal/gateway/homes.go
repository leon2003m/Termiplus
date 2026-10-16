package gateway

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"sort"
	"strings"
	"time"

	"termiplus/internal/dockerapi"
	"termiplus/internal/naming"
)

// Home archives: read a user's home volume as a tar, write one back, or
// delete the home. All file access goes through an archive helper container
// (naming.Archive) that mounts only that volume and is never started; the
// socket proxy allows archive calls on helpers only and refuses to start them.
// Workspace containers are never touched by an archive call, so a process in
// a running workspace cannot interfere with the copy.

const storageArchive = "archive" // LabelStorage of archive helpers

var errNoHome = errors.New("no such home")

// homeOf returns the home volume name part of a persistent policy.
func homeOf(policyName string, pol Policy) string {
	if pol.Home != "" {
		return pol.Home
	}
	return policyName
}

// policiesForHome returns the persistent policies sharing home, sorted.
func (m *manager) policiesForHome(home string) []string {
	var out []string
	for name, p := range m.policies {
		if !p.ephemeral() && homeOf(name, p) == home {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ownedVolume checks that the user's home volume exists and is theirs.
func (m *manager) ownedVolume(ctx context.Context, userID, home string) (string, error) {
	volume := naming.Volume(userID, home)
	v, err := m.docker.InspectVolume(ctx, volume)
	switch {
	case errors.Is(err, dockerapi.ErrNotFound):
		return "", errNoHome
	case err != nil:
		return "", fmt.Errorf("inspect volume: %w", err)
	case v.Labels[naming.LabelUser] != userID:
		return "", fmt.Errorf("volume %s belongs to another user", volume)
	}
	return volume, nil
}

// withHelper creates the archive helper for the user's existing home, runs fn
// with its name and removes the helper again.
func (m *manager) withHelper(ctx context.Context, userID, home string, fn func(helper string) error) error {
	volume, err := m.ownedVolume(ctx, userID, home)
	if err != nil {
		return err
	}
	pols := m.policiesForHome(home)
	if len(pols) == 0 {
		return fmt.Errorf("no persistent policy uses home %q", home)
	}
	pol := m.policies[pols[0]]
	helper := naming.Archive(userID, home)
	// One helper per home at a time: a second export or import of the same
	// home waits instead of replacing the helper under the first.
	l := m.lockFor(helper)
	l.Lock()
	defer l.Unlock()
	// A helper left over from an interrupted run is replaced.
	if err := m.docker.Remove(ctx, helper); err != nil {
		return fmt.Errorf("remove old helper: %w", err)
	}
	spec := map[string]any{
		"Image":  m.imageFor(pol),
		"Labels": map[string]string{naming.LabelUser: userID, naming.LabelPolicy: pols[0], naming.LabelStorage: storageArchive},
		"HostConfig": map[string]any{
			"Runtime":     pol.Runtime,
			"NetworkMode": m.network,
			"Memory":      pol.MemoryMB << 20,
			"NanoCpus":    int64(pol.CPUs * 1e9),
			"PidsLimit":   pol.Pids,
			"Mounts":      []map[string]any{{"Type": "volume", "Source": volume, "Target": naming.HomeDir}},
		},
	}
	if err := m.docker.Create(ctx, helper, spec); err != nil {
		return fmt.Errorf("create archive helper: %w", err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := m.docker.Remove(rctx, helper); err != nil {
			log.Printf("remove archive helper %s: %v", helper, err)
		}
	}()
	return fn(helper)
}

// exportHome writes the user's home as a tar to w. Entries start with
// "user/", as Docker names them. Running workspaces keep running; files they
// change during the copy may be caught half-written.
func (m *manager) exportHome(ctx context.Context, userID, home string, w io.Writer) error {
	return m.withHelper(ctx, userID, home, func(helper string) error {
		rc, err := m.docker.GetHomeArchive(ctx, helper)
		if err != nil {
			return err
		}
		defer rc.Close()
		_, err = io.Copy(w, rc)
		return err
	})
}

// lockHome takes the locks of every container of the user that uses home, so
// none of them is started while the home is rewritten, and stops the running
// ones. The returned function releases the locks.
func (m *manager) lockHome(ctx context.Context, userID, home string) (func(), error) {
	pols := m.policiesForHome(home)
	var held []func()
	unlock := func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i]()
		}
	}
	for _, p := range pols { // sorted: no lock-order inversion between callers
		name := naming.Container(userID, p)
		l := m.lockFor(name)
		l.Lock()
		held = append(held, l.Unlock)
		m.mu.Lock()
		delete(m.instances, name)
		m.mu.Unlock()
		if err := m.docker.Stop(ctx, name, 10); err != nil && !errors.Is(err, dockerapi.ErrNotFound) {
			unlock()
			return nil, fmt.Errorf("stop %s: %w", name, err)
		}
	}
	return unlock, nil
}

// importHome extracts a tar written by exportHome into the user's home,
// creating the home if needed. Files in the home that the tar does not
// contain stay. Workspaces using the home are stopped first and start again
// on their next use.
func (m *manager) importHome(ctx context.Context, userID, home string, r io.Reader) error {
	if len(m.policiesForHome(home)) == 0 {
		return fmt.Errorf("no persistent policy uses home %q", home)
	}
	volume := naming.Volume(userID, home)
	if _, err := m.ownedVolume(ctx, userID, home); errors.Is(err, errNoHome) {
		if err := m.docker.CreateVolume(ctx, volume, map[string]string{naming.LabelUser: userID, naming.LabelPolicy: m.policiesForHome(home)[0]}); err != nil {
			return fmt.Errorf("create volume: %w", err)
		}
	} else if err != nil {
		return err
	}
	unlock, err := m.lockHome(ctx, userID, home)
	if err != nil {
		return err
	}
	defer unlock()
	return m.withHelper(ctx, userID, home, func(helper string) error {
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(rerootHomeTar(r, pw)) }()
		defer pr.Close()
		return m.docker.PutHomeArchive(ctx, helper, pr)
	})
}

// removeHome deletes all of the user's containers that use home and then the
// home volume itself. The socket proxy must allow it (ALLOW_HOME_REMOVE=1).
func (m *manager) removeHome(ctx context.Context, userID, home string) error {
	volume, err := m.ownedVolume(ctx, userID, home)
	if errors.Is(err, errNoHome) {
		return nil
	}
	if err != nil {
		return err
	}
	unlock, err := m.lockHome(ctx, userID, home)
	if err != nil {
		return err
	}
	defer unlock()
	for _, p := range m.policiesForHome(home) {
		if err := m.docker.Remove(ctx, naming.Container(userID, p)); err != nil {
			return err
		}
	}
	// Wait for a running export of this home (same lock order as importHome:
	// container locks, then the helper's).
	helper := naming.Archive(userID, home)
	hl := m.lockFor(helper)
	hl.Lock()
	defer hl.Unlock()
	if err := m.docker.Remove(ctx, helper); err != nil {
		return err
	}
	return m.docker.RemoveVolume(ctx, volume)
}

// rerootHomeTar turns Docker's "user/..." entries into entries relative to
// the home directory itself (what PUT ...?path=/home/user expects) and drops
// the entry for the directory itself. Anything outside "user/" is refused.
func rerootHomeTar(in io.Reader, out io.Writer) error {
	tr := tar.NewReader(in)
	tw := tar.NewWriter(out)
	strip := func(name string) (string, bool) {
		rest, ok := strings.CutPrefix(strings.TrimPrefix(name, "./"), "user/")
		rest = strings.TrimSuffix(rest, "/")
		clean := path.Clean(rest) == rest && rest != ".." && !strings.HasPrefix(rest, "../") && !strings.HasPrefix(rest, "/")
		return rest, ok && rest != "" && clean
	}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return tw.Close()
		}
		if err != nil {
			return fmt.Errorf("archive: %w", err)
		}
		if n := strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/"); n == "user" {
			continue
		}
		name, ok := strip(h.Name)
		if !ok {
			return fmt.Errorf("archive entry %q is not below user/", h.Name)
		}
		h.Name = name
		if h.Typeflag == tar.TypeLink {
			if h.Linkname, ok = strip(h.Linkname); !ok {
				return fmt.Errorf("archive hard link %q points outside user/", name)
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
}
