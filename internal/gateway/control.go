package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"termiplus/internal/dockerapi"
	"termiplus/internal/naming"
)

// The control API is the provisioning surface the termiplus MCP server drives
// (docs/mcp.md): list workspaces, and list/create/destroy one user's sessions.
// It is gated by a dedicated control key on its own header - never the
// connection key Open WebUI holds - so only the MCP server, which verifies the
// caller's OIDC identity first, may act on behalf of an arbitrary X-User-Id.
// Without a configured key the whole surface returns 404.

const controlKeyHeader = "X-Control-Key"

const controlPrefix = "/control/v1/"

const maxControlBody = 16 << 10

// controlSessionPrefix marks ephemeral session ids minted by the control API,
// as opposed to Open WebUI chat ids, which are never echoed back.
const controlSessionPrefix = "mcp-"

type workspaceInfo struct {
	Name       string `json:"name"`
	Runtime    string `json:"runtime"`
	Persistent bool   `json:"persistent"`
	Desktop    bool   `json:"desktop,omitempty"`
}

type sessionInfo struct {
	ID         string `json:"session_id"` // container name
	Workspace  string `json:"workspace"`
	Persistent bool   `json:"persistent"`
	Running    bool   `json:"running"`
	// TerminalPath is the gateway path that reaches the session; for an
	// ephemeral session TerminalSession must be sent as X-Session-Id.
	TerminalPath    string `json:"terminal_path,omitempty"`
	TerminalSession string `json:"terminal_session,omitempty"`
	// StreamPath and StreamPassword reach the desktop of a "desktop" policy
	// (KasmVNC basic auth, user "user"). Only the session's owner gets them.
	StreamPath     string `json:"stream_path,omitempty"`
	StreamPassword string `json:"stream_password,omitempty"`
}

// withStream fills the desktop stream handle for desktop policies.
func (m *manager) withStream(s sessionInfo, pol Policy) sessionInfo {
	if pol.Desktop {
		s.StreamPath = streamPrefix + s.ID + "/"
		s.StreamPassword = m.desktopPassword(s.ID)
	}
	return s
}

var (
	errNoSession = errors.New("no such session")
)

// serveControl handles /control/v1/*. It returns true if it handled the request.
func (g *Gateway) serveControl(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != strings.TrimSuffix(controlPrefix, "/") && !strings.HasPrefix(r.URL.Path, controlPrefix) {
		return false
	}
	if g.cfg.ControlKey == "" {
		httpError(w, http.StatusNotFound, "not found")
		return true
	}
	if !keyMatches(r.Header.Values(controlKeyHeader), g.cfg.ControlKey) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return true
	}
	rest := strings.TrimPrefix(r.URL.Path, controlPrefix)

	if rest == "workspaces" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, "GET")
			return true
		}
		writeJSON(w, http.StatusOK, g.workspaces())
		return true
	}

	// Everything below acts for one user.
	userID := r.Header.Get("X-User-Id")
	if !naming.UserID.MatchString(userID) || userID == specUser {
		httpError(w, http.StatusBadRequest, "valid X-User-Id required")
		return true
	}

	switch {
	case strings.HasPrefix(rest, forwardPrefix):
		g.serveForward(w, r, userID, strings.TrimPrefix(rest, forwardPrefix))
	case rest == "sessions" && r.Method == http.MethodGet:
		list, err := g.mgr.listUser(r.Context(), userID)
		if err != nil {
			httpError(w, http.StatusBadGateway, "could not list sessions")
			return true
		}
		writeJSON(w, http.StatusOK, list)
	case rest == "sessions" && r.Method == http.MethodPost:
		g.createSession(w, r, userID)
	case rest == "sessions":
		methodNotAllowed(w, "GET, POST")
	case strings.HasPrefix(rest, "sessions/") && r.Method == http.MethodDelete:
		name := strings.TrimPrefix(rest, "sessions/")
		switch err := g.mgr.remove(r.Context(), userID, name); {
		case errors.Is(err, errNoSession):
			httpError(w, http.StatusNotFound, "no such session")
		case err != nil:
			httpError(w, http.StatusBadGateway, "could not remove session")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	case strings.HasPrefix(rest, "sessions/"):
		methodNotAllowed(w, "DELETE")
	default:
		httpError(w, http.StatusNotFound, "not found")
	}
	return true
}

func (g *Gateway) workspaces() []workspaceInfo {
	out := make([]workspaceInfo, 0, len(g.cfg.Policies))
	for name, p := range g.cfg.Policies {
		out = append(out, workspaceInfo{Name: name, Runtime: p.Runtime, Persistent: !p.ephemeral(), Desktop: p.Desktop})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (g *Gateway) createSession(w http.ResponseWriter, r *http.Request, userID string) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxControlBody+1))
	if err != nil || len(data) > maxControlBody {
		httpError(w, http.StatusBadRequest, "could not read body")
		return
	}
	var req struct {
		Workspace string `json:"workspace"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return
	}
	pol, ok := g.cfg.Policies[req.Workspace]
	if !ok {
		httpError(w, http.StatusNotFound, "unknown workspace")
		return
	}
	// Ephemeral sessions are keyed by a session id (Open WebUI uses the chat);
	// a control-plane request gets a fresh random one.
	sessionID := ""
	if pol.ephemeral() {
		var buf [12]byte
		if _, err := rand.Read(buf[:]); err != nil {
			httpError(w, http.StatusInternalServerError, "could not create session id")
			return
		}
		sessionID = controlSessionPrefix + hex.EncodeToString(buf[:])
	}
	inst, err := g.mgr.ensure(r.Context(), userID, sessionID, req.Workspace, pol)
	if err != nil {
		g.ensureError(w, userID, req.Workspace, err)
		return
	}
	writeJSON(w, http.StatusCreated, g.mgr.withStream(sessionInfo{
		ID:              inst.name,
		Workspace:       req.Workspace,
		Persistent:      !pol.ephemeral(),
		Running:         true,
		TerminalPath:    "/p/" + req.Workspace + "/",
		TerminalSession: sessionID,
	}, pol))
}

// listUser returns the user's terminal containers, running or stopped.
func (m *manager) listUser(ctx context.Context, userID string) ([]sessionInfo, error) {
	list, err := m.docker.List(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	known := map[string]string{} // container -> control-API session id
	for name, inst := range m.instances {
		if inst.controlSession != "" {
			known[name] = inst.controlSession
		}
	}
	m.mu.Unlock()
	out := []sessionInfo{}
	for _, c := range list {
		if len(c.Names) == 0 || c.Labels[naming.LabelUser] != userID {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		if !naming.IsManagedContainer(name) {
			continue
		}
		policy := c.Labels[naming.LabelPolicy]
		out = append(out, m.withStream(sessionInfo{
			ID:              name,
			Workspace:       policy,
			Persistent:      c.Labels[naming.LabelStorage] != storageEphemeral,
			Running:         c.State == "running",
			TerminalPath:    "/p/" + policy + "/",
			TerminalSession: known[name],
		}, m.policies[policy]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// remove deletes one of the user's terminal containers. A persistent home
// volume is kept (the socket proxy never removes volumes). A container that
// does not exist or belongs to someone else is reported the same way, so the
// API does not reveal other users' sessions.
func (m *manager) remove(ctx context.Context, userID, name string) error {
	if !naming.IsManagedContainer(name) {
		return errNoSession
	}
	ct, err := m.docker.Inspect(ctx, name)
	switch {
	case errors.Is(err, dockerapi.ErrNotFound):
		return errNoSession
	case err != nil:
		return err
	}
	if ct.Config.Labels[naming.LabelUser] != userID {
		return errNoSession
	}
	l := m.lockFor(name)
	l.Lock()
	defer l.Unlock()
	m.mu.Lock()
	delete(m.instances, name)
	m.mu.Unlock()
	return m.docker.Remove(ctx, name)
}

// keyMatches requires exactly one header value equal to want (constant time).
func keyMatches(vals []string, want string) bool {
	return len(vals) == 1 && subtle.ConstantTimeCompare([]byte(vals[0]), []byte(want)) == 1
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	httpError(w, http.StatusMethodNotAllowed, "method not allowed")
}
