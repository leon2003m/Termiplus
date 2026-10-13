package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GatewayBackend implements Backend against the termiplus gateway's
// /control/v1 API (and, for network policy, its /admin API). The MCP server is
// the only holder of the control key and the only component that sets
// X-User-Id on these calls, after authenticating the caller.
type GatewayBackend struct {
	base       string // internal gateway URL, e.g. http://gateway:8080
	publicBase string // externally reachable gateway URL for access handles
	streamBase string // browser-facing URL of the desktop stream (sign-in proxy)
	controlKey string
	adminKey   string // optional; empty disables set_network_policy
	client     *http.Client
}

// NewGatewayBackend returns a backend talking to the gateway at base. publicBase
// is used to build the terminal URLs handed back to callers (defaults to base).
func NewGatewayBackend(base, publicBase, controlKey, adminKey string) *GatewayBackend {
	if publicBase == "" {
		publicBase = base
	}
	return &GatewayBackend{
		base:       strings.TrimSuffix(base, "/"),
		publicBase: strings.TrimSuffix(publicBase, "/"),
		streamBase: strings.TrimSuffix(publicBase, "/"),
		controlKey: controlKey,
		adminKey:   adminKey,
		// Creating a session waits for the terminal to become ready.
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// WithStreamBase sets the browser-facing base URL for desktop stream handles
// (the desktop sign-in proxy). Defaults to the public gateway URL.
func (b *GatewayBackend) WithStreamBase(u string) *GatewayBackend {
	if u != "" {
		b.streamBase = strings.TrimSuffix(u, "/")
	}
	return b
}

// gwWorkspace / gwSession mirror the gateway control API's JSON.
type gwWorkspace struct {
	Name       string `json:"name"`
	Runtime    string `json:"runtime"`
	Persistent bool   `json:"persistent"`
	Desktop    bool   `json:"desktop"`
}

type gwSession struct {
	ID              string `json:"session_id"`
	Workspace       string `json:"workspace"`
	Persistent      bool   `json:"persistent"`
	Running         bool   `json:"running"`
	TerminalPath    string `json:"terminal_path"`
	TerminalSession string `json:"terminal_session"`
	StreamPath      string `json:"stream_path"`
	StreamPassword  string `json:"stream_password"`
}

func (b *GatewayBackend) ListWorkspaces(ctx context.Context, _ Principal) ([]Workspace, error) {
	var ws []gwWorkspace
	if err := b.do(ctx, http.MethodGet, "workspaces", "", nil, &ws); err != nil {
		return nil, err
	}
	out := make([]Workspace, 0, len(ws))
	for _, w := range ws {
		out = append(out, Workspace{Name: w.Name, Description: "runtime " + w.Runtime, Persistent: w.Persistent, Desktop: w.Desktop})
	}
	return out, nil
}

func (b *GatewayBackend) RequestSession(ctx context.Context, p Principal, workspace string, persistent bool, label string) (Session, error) {
	ws, err := b.ListWorkspaces(ctx, p)
	if err != nil {
		return Session{}, err
	}
	found := false
	for _, w := range ws {
		if w.Name == workspace {
			found = true
			if w.Persistent != persistent {
				return Session{}, fmt.Errorf("%w: workspace %q has persistent=%t", ErrPersistenceMismatch, workspace, w.Persistent)
			}
		}
	}
	if !found {
		return Session{}, fmt.Errorf("unknown workspace %q", workspace)
	}
	var s gwSession
	if err := b.do(ctx, http.MethodPost, "sessions", p.UserID, map[string]string{"workspace": workspace}, &s); err != nil {
		return Session{}, err
	}
	out := b.session(s, p.UserID)
	out.Label = label
	return out, nil
}

func (b *GatewayBackend) ListSessions(ctx context.Context, p Principal) ([]Session, error) {
	var list []gwSession
	if err := b.do(ctx, http.MethodGet, "sessions", p.UserID, nil, &list); err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(list))
	for _, s := range list {
		out = append(out, b.session(s, p.UserID))
	}
	return out, nil
}

func (b *GatewayBackend) DestroySession(ctx context.Context, p Principal, id string) error {
	if id == "" || strings.ContainsAny(id, "/?#%") {
		return fmt.Errorf("invalid session id %q", id)
	}
	return b.do(ctx, http.MethodDelete, "sessions/"+url.PathEscape(id), p.UserID, nil, nil)
}

func (b *GatewayBackend) SetNetworkPolicy(ctx context.Context, _ Principal, policy []byte) error {
	if b.adminKey == "" {
		return errors.New("network policy administration is not configured on this server")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, b.base+"/admin/netpolicy", bytes.NewReader(policy))
	if err != nil {
		return err
	}
	req.Header.Set("X-Admin-Key", b.adminKey)
	req.Header.Set("Content-Type", "application/json")
	return b.send(req, nil)
}

func (b *GatewayBackend) session(s gwSession, userID string) Session {
	out := Session{
		ID:              s.ID,
		Workspace:       s.Workspace,
		UserID:          userID,
		Persistent:      s.Persistent,
		Running:         s.Running,
		TerminalURL:     b.publicBase + s.TerminalPath,
		TerminalSession: s.TerminalSession,
	}
	if s.StreamPath != "" {
		out.StreamURL = b.streamBase + s.StreamPath
		out.StreamPassword = s.StreamPassword
	}
	return out
}

// do calls /control/v1/<path> as userID (empty for user-less endpoints).
func (b *GatewayBackend) do(ctx context.Context, method, path, userID string, in, out any) error {
	var body io.Reader
	if in != nil {
		js, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(js)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+"/control/v1/"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Control-Key", b.controlKey)
	if userID != "" {
		req.Header.Set("X-User-Id", userID)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return b.send(req, out)
}

func (b *GatewayBackend) send(req *http.Request, out any) error {
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Detail string `json:"detail"` // the gateway's httpError shape
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Detail != "" {
			msg = e.Detail
		}
		return fmt.Errorf("gateway %s: %d %s", req.Method, resp.StatusCode, msg)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
