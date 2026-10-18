package gateway

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"

	"termiplus/internal/dockerapi"
	"termiplus/internal/naming"
)

// Control-API operations on a user's workspaces and homes (alpha, driven by
// the MCP server): reset a workspace, download a home, snapshots of a home,
// and archiving / restoring all homes of a departed user. They build on the
// home archive engine in homes.go and the backup store in backups.go.

var (
	errNoBackups     = errors.New("backups are not configured on this gateway (no BACKUP_DIR)")
	errNotPersistent = errors.New("temporary workspaces have no home")
)

// homeNames returns the homes used by persistent policies, sorted.
func (m *manager) homeNames() []string {
	seen := map[string]bool{}
	for name, p := range m.policies {
		if !p.ephemeral() {
			seen[homeOf(name, p)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// workspaceHome returns the home of a persistent workspace (policy).
func (g *Gateway) workspaceHome(ws string) (string, error) {
	pol, ok := g.cfg.Policies[ws]
	switch {
	case !ok:
		return "", errUnknownWorkspace
	case pol.ephemeral():
		return "", errNotPersistent
	}
	return homeOf(ws, pol), nil
}

var errUnknownWorkspace = errors.New("unknown workspace")

// reset deletes one of the user's containers so its workspace starts from a
// fresh container: a persistent workspace keeps its home and is started again
// right away, an ephemeral sandbox is just gone.
func (g *Gateway) reset(ctx context.Context, userID, name string) (*sessionInfo, error) {
	if !naming.IsManagedContainer(name) {
		return nil, errNoSession
	}
	ct, err := g.mgr.docker.Inspect(ctx, name)
	switch {
	case errors.Is(err, dockerapi.ErrNotFound):
		return nil, errNoSession
	case err != nil:
		return nil, err
	case ct.Config.Labels[naming.LabelUser] != userID:
		return nil, errNoSession
	}
	ws := ct.Config.Labels[naming.LabelPolicy]
	if err := g.mgr.remove(ctx, userID, name); err != nil {
		return nil, err
	}
	pol, ok := g.cfg.Policies[ws]
	if !ok || pol.ephemeral() {
		return nil, nil
	}
	inst, err := g.mgr.ensure(ctx, userID, "", ws, pol)
	if err != nil {
		return nil, err
	}
	s := g.mgr.withStream(sessionInfo{ID: inst.name, Workspace: ws, Persistent: true, Running: true, TerminalPath: "/p/" + ws + "/"}, pol)
	return &s, nil
}

func (g *Gateway) snapshot(ctx context.Context, userID, ws string) (Backup, error) {
	if g.backups == nil {
		return Backup{}, errNoBackups
	}
	home, err := g.workspaceHome(ws)
	if err != nil {
		return Backup{}, err
	}
	if _, err := g.mgr.ownedVolume(ctx, userID, home); err != nil {
		return Backup{}, err
	}
	return g.backups.create(kindSnapshot, naming.Volume(userID, home), func(w io.Writer) error {
		return g.mgr.exportHome(ctx, userID, home, w)
	})
}

// restoreBackup extracts one stored backup into the user's home.
func (g *Gateway) restoreBackup(ctx context.Context, userID, home, kind, id string) error {
	f, err := g.backups.open(kind, naming.Volume(userID, home), id)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("backup %s: %w", id, err)
	}
	return g.mgr.importHome(ctx, userID, home, zr)
}

// ArchivedHome is the outcome for one home in archiveUser / restoreUser.
type ArchivedHome struct {
	Home    string `json:"home"`
	Backup  string `json:"backup,omitempty"`
	Removed bool   `json:"removed"` // the home volume was deleted
	Note    string `json:"note,omitempty"`
}

// archiveUser stores every home of the user as an archived backup, then
// deletes all their containers and, where the socket proxy allows it
// (ALLOW_HOME_REMOVE=1), the homes. A home whose backup fails is left alone.
func (g *Gateway) archiveUser(ctx context.Context, userID string) ([]ArchivedHome, error) {
	if g.backups == nil {
		return nil, errNoBackups
	}
	out := []ArchivedHome{}
	for _, home := range g.mgr.homeNames() {
		if _, err := g.mgr.ownedVolume(ctx, userID, home); errors.Is(err, errNoHome) {
			continue
		} else if err != nil {
			return out, err
		}
		b, err := g.backups.create(kindArchived, naming.Volume(userID, home), func(w io.Writer) error {
			return g.mgr.exportHome(ctx, userID, home, w)
		})
		if err != nil {
			return out, fmt.Errorf("archive home %s: %w", home, err)
		}
		res := ArchivedHome{Home: home, Backup: b.ID}
		if err := g.mgr.removeHome(ctx, userID, home); err != nil {
			res.Note = "archived, but the home was kept: " + err.Error()
			log.Printf("archive %q/%q: %q", userID, home, err)
		} else {
			res.Removed = true
		}
		out = append(out, res)
	}
	// Ephemeral sandboxes and anything else of the user.
	list, err := g.mgr.listUser(ctx, userID)
	if err != nil {
		return out, err
	}
	for _, s := range list {
		if err := g.mgr.remove(ctx, userID, s.ID); err != nil && !errors.Is(err, errNoSession) {
			return out, err
		}
	}
	return out, nil
}

// RestoredHome is the outcome for one home in restoreUser.
type RestoredHome struct {
	Home   string `json:"home"`
	Backup string `json:"backup"`
}

// restoreUser puts the newest archived backup of each home back.
func (g *Gateway) restoreUser(ctx context.Context, userID string) ([]RestoredHome, error) {
	if g.backups == nil {
		return nil, errNoBackups
	}
	out := []RestoredHome{}
	for _, home := range g.mgr.homeNames() {
		list, err := g.backups.list(kindArchived, naming.Volume(userID, home))
		if err != nil {
			return out, err
		}
		if len(list) == 0 {
			continue
		}
		if err := g.restoreBackup(ctx, userID, home, kindArchived, list[0].ID); err != nil {
			return out, fmt.Errorf("restore home %s: %w", home, err)
		}
		out = append(out, RestoredHome{Home: home, Backup: list[0].ID})
	}
	return out, nil
}

// serveHomeControl handles the control-API paths below workspaces/<ws>/home/:
//
//	GET    archive                    the current home as a tar
//	GET    snapshots                  list
//	POST   snapshots                  create
//	GET    snapshots/<id>             download (.tar.gz)
//	POST   snapshots/<id>/restore     restore (stops the workspaces of the home)
//	DELETE snapshots/<id>             delete
func (g *Gateway) serveHomeControl(w http.ResponseWriter, r *http.Request, userID, ws, rest string) {
	home, err := g.workspaceHome(ws)
	if err != nil {
		httpError(w, http.StatusNotFound, err.Error())
		return
	}
	volume := naming.Volume(userID, home)
	parts := strings.Split(rest, "/")
	switch {
	case rest == "archive" && r.Method == http.MethodGet:
		g.exportHome(w, r, userID, home)
	case rest == "archive":
		methodNotAllowed(w, "GET")
	case g.backups == nil:
		httpError(w, http.StatusNotFound, errNoBackups.Error())
	case rest == "snapshots" && r.Method == http.MethodGet:
		list, err := g.backups.list(kindSnapshot, volume)
		if err != nil {
			httpError(w, http.StatusInternalServerError, "could not list snapshots")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"home": home, "snapshots": list, "keep": g.backups.keep})
	case rest == "snapshots" && r.Method == http.MethodPost:
		b, err := g.snapshot(r.Context(), userID, ws)
		switch {
		case errors.Is(err, errNoHome):
			httpError(w, http.StatusNotFound, "this workspace has no home yet: start it once")
		case errors.Is(err, errExportTooLarge):
			log.Printf("snapshot %q/%q: %q", userID, home, err)
			httpError(w, http.StatusRequestEntityTooLarge, "the home is larger than the export limit (EXPORT_MAX_BYTES); large sparse files count at their full size")
		case err != nil:
			log.Printf("snapshot %q/%q: %q", userID, home, err)
			httpError(w, http.StatusBadGateway, "could not take the snapshot")
		default:
			log.Printf("snapshot %q of %q/%q", b.ID, userID, home)
			writeJSON(w, http.StatusCreated, b)
		}
	case rest == "snapshots":
		methodNotAllowed(w, "GET, POST")
	case len(parts) == 2 && parts[0] == "snapshots" && r.Method == http.MethodGet:
		f, err := g.backups.open(kindSnapshot, volume, parts[1])
		if err != nil {
			httpError(w, http.StatusNotFound, "no such snapshot")
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="home-%s-%s.tar.gz"`, home, parts[1]))
		_, _ = io.Copy(w, f)
	case len(parts) == 2 && parts[0] == "snapshots" && r.Method == http.MethodDelete:
		if err := g.backups.remove(kindSnapshot, volume, parts[1]); err != nil {
			httpError(w, http.StatusNotFound, "no such snapshot")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 3 && parts[0] == "snapshots" && parts[2] == "restore" && r.Method == http.MethodPost:
		err := g.restoreBackup(r.Context(), userID, home, kindSnapshot, parts[1])
		switch {
		case errors.Is(err, errNoBackup):
			httpError(w, http.StatusNotFound, "no such snapshot")
		case err != nil:
			log.Printf("restore %q of %q/%q: %q", parts[1], userID, home, err)
			httpError(w, http.StatusBadGateway, "could not restore the snapshot")
		default:
			log.Printf("restored snapshot %q of %q/%q", parts[1], userID, home)
			writeJSON(w, http.StatusOK, map[string]any{"restored": parts[1], "home": home})
		}
	default:
		httpError(w, http.StatusNotFound, "not found")
	}
}
