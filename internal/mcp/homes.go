package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Workspace resets, home downloads and snapshots (tools reset_workspace and
// manage_snapshots, console downloads), and the gateway calls behind them and
// behind archiving departed users. Only backends that implement HomeBackend
// (the gateway backend) offer them.

// HomeBackend is the optional part of a Backend that manages homes.
type HomeBackend interface {
	// ResetSession recreates one of the user's sessions from a fresh
	// container (a persistent one keeps its home).
	ResetSession(ctx context.Context, p Principal, id string) (json.RawMessage, error)
	// Snapshots runs action (list, create, restore, delete) on the snapshots
	// of the home of a persistent workspace.
	Snapshots(ctx context.Context, p Principal, workspace, action, id string) (json.RawMessage, error)
	// HomeDownload streams the current home (id "") or a snapshot.
	HomeDownload(ctx context.Context, p Principal, workspace, id string) (*http.Response, error)
	// UserHomes archives, restores or lists the archived homes of userID.
	UserHomes(ctx context.Context, userID, action string) (json.RawMessage, error)
}

var snapshotIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z(-[0-9]{3})?$`)

func pathSeg(s string) (string, error) {
	if s == "" || strings.ContainsAny(s, "/?#%\\") || s == "." || s == ".." {
		return "", fmt.Errorf("invalid name %q", s)
	}
	return url.PathEscape(s), nil
}

func (b *GatewayBackend) raw(ctx context.Context, slow bool, method, path, userID string) (json.RawMessage, error) {
	if slow {
		ctx2, cancel := context.WithTimeout(ctx, time.Hour)
		defer cancel()
		ctx = ctx2
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+"/control/v1/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Control-Key", b.controlKey)
	req.Header.Set("X-User-Id", userID)
	var out json.RawMessage
	client := b.client
	if slow {
		client = &http.Client{Transport: b.client.Transport}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", err)
	}
	defer resp.Body.Close()
	if err := decodeGateway(req, resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *GatewayBackend) ResetSession(ctx context.Context, p Principal, id string) (json.RawMessage, error) {
	seg, err := pathSeg(id)
	if err != nil {
		return nil, err
	}
	return b.raw(ctx, false, http.MethodPost, "sessions/"+seg+"/reset", p.UserID)
}

func (b *GatewayBackend) Snapshots(ctx context.Context, p Principal, workspace, action, id string) (json.RawMessage, error) {
	ws, err := pathSeg(workspace)
	if err != nil {
		return nil, err
	}
	base := "workspaces/" + ws + "/home/snapshots"
	if action != "list" && action != "create" && !snapshotIDPattern.MatchString(id) {
		return nil, errors.New("snapshot_id must be an id from the snapshot list, e.g. 20261016T221500Z")
	}
	switch action {
	case "list":
		return b.raw(ctx, false, http.MethodGet, base, p.UserID)
	case "create":
		return b.raw(ctx, true, http.MethodPost, base, p.UserID)
	case "restore":
		return b.raw(ctx, true, http.MethodPost, base+"/"+id+"/restore", p.UserID)
	case "delete":
		if _, err := b.raw(ctx, false, http.MethodDelete, base+"/"+id, p.UserID); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"deleted":` + fmt.Sprintf("%q", id) + `}`), nil
	}
	return nil, errors.New(`action must be "list", "create", "restore" or "delete"`)
}

func (b *GatewayBackend) HomeDownload(ctx context.Context, p Principal, workspace, id string) (*http.Response, error) {
	ws, err := pathSeg(workspace)
	if err != nil {
		return nil, err
	}
	path := "workspaces/" + ws + "/home/archive"
	if id != "" {
		if !snapshotIDPattern.MatchString(id) {
			return nil, errors.New("invalid snapshot id")
		}
		path = "workspaces/" + ws + "/home/snapshots/" + id
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/control/v1/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Control-Key", b.controlKey)
	req.Header.Set("X-User-Id", p.UserID)
	resp, err := (&http.Client{Transport: b.client.Transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, decodeGateway(req, resp, nil)
	}
	return resp, nil
}

func (b *GatewayBackend) UserHomes(ctx context.Context, userID, action string) (json.RawMessage, error) {
	switch action {
	case "archive":
		return b.raw(ctx, true, http.MethodPost, "user/archive", userID)
	case "restore":
		return b.raw(ctx, true, http.MethodPost, "user/restore", userID)
	case "list":
		return b.raw(ctx, false, http.MethodGet, "user/archives", userID)
	}
	return nil, fmt.Errorf("unknown action %q", action)
}

// homeBackend returns the backend's HomeBackend side, if it has one.
func (s *Server) homeBackend() (HomeBackend, error) {
	hb, ok := s.backend.(HomeBackend)
	if !ok {
		return nil, errors.New("workspace resets and snapshots are not available with this backend")
	}
	return hb, nil
}

// WithUIBase sets the browser-facing base URL of the management console
// (STREAM_PUBLIC_URL), used for download links.
func (s *Server) WithUIBase(u string) *Server {
	s.uiBase = strings.TrimSuffix(u, "/")
	return s
}

// WithDesktopBase sets the browser-facing base URL of the desktops' own
// origin (STREAM_PUBLIC_URL), used for share links.
func (s *Server) WithDesktopBase(u string) *Server {
	s.desktopBase = strings.TrimSuffix(u, "/")
	return s
}

// downloadURL is the console link that downloads a home or a snapshot.
func (s *Server) downloadURL(workspace, id string) string {
	if s.uiBase == "" {
		return ""
	}
	u := s.uiBase + "/ui/api/workspaces/" + url.PathEscape(workspace) + "/home/"
	if id == "" {
		return u + "archive"
	}
	return u + "snapshots/" + id
}

// entitledPersistent checks that p may use the persistent workspace ws.
func (s *Server) entitledWorkspace(ctx context.Context, p Principal, ws string) error {
	if !s.entitlements().Allowed(p, ws) {
		return fmt.Errorf("you are not entitled to the workspace %q", ws)
	}
	all, err := s.backend.ListWorkspaces(ctx, p)
	if err != nil {
		return err
	}
	for _, w := range all {
		if w.Name == ws {
			if !w.Persistent {
				return fmt.Errorf("%q is a temporary workspace: it has no home to keep", ws)
			}
			return nil
		}
	}
	return fmt.Errorf("unknown workspace %q", ws)
}

func (s *Server) manageSnapshots(ctx context.Context, p Principal, args map[string]any) (any, error) {
	hb, err := s.homeBackend()
	if err != nil {
		return nil, err
	}
	ws := argString(args, "workspace")
	if err := s.entitledWorkspace(ctx, p, ws); err != nil {
		return nil, err
	}
	action := argString(args, "action")
	raw, err := hb.Snapshots(ctx, p, ws, action, argString(args, "snapshot_id"))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("unexpected answer from the gateway")
	}
	switch action {
	case "list":
		if list, ok := out["snapshots"].([]any); ok {
			for _, x := range list {
				if m, ok := x.(map[string]any); ok {
					if id, _ := m["id"].(string); id != "" && s.uiBase != "" {
						m["download_url"] = s.downloadURL(ws, id)
					}
				}
			}
		}
		if s.uiBase != "" {
			out["current_home_download_url"] = s.downloadURL(ws, "")
		}
	case "create":
		if id, _ := out["id"].(string); id != "" && s.uiBase != "" {
			out["download_url"] = s.downloadURL(ws, id)
		}
	case "restore":
		out["note"] = "the workspace's containers were stopped; files from the snapshot were written back (files created since then stay); the next use starts it again"
	}
	return out, nil
}

// snapshotsGate: the user's own workspace; restoring and deleting need a
// human confirmation.
func (s *Server) snapshotsGate(p Principal, args map[string]any) (bool, error) {
	if ws := argString(args, "workspace"); !s.entitlements().Allowed(p, ws) {
		return false, fmt.Errorf("you are not entitled to the workspace %q", ws)
	}
	switch argString(args, "action") {
	case "list", "create":
		return false, nil
	case "restore", "delete":
		return true, nil
	}
	return false, errors.New(`action must be "list", "create", "restore" or "delete"`)
}

func (s *Server) homeTools() []tool {
	return []tool{
		{
			name:        "reset_workspace",
			description: "Start one of your sessions over from a fresh container: a persistent workspace keeps everything in /home/user but loses what was installed elsewhere; a temporary sandbox is removed. Destructive: requires explicit human confirmation.",
			inputSchema: `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"],"additionalProperties":true}`,
			destructive: true,
			gate:        func(Principal, map[string]any) (bool, error) { return true, nil },
			handle: func(ctx context.Context, p Principal, args map[string]any) (any, error) {
				hb, err := s.homeBackend()
				if err != nil {
					return nil, err
				}
				id := argString(args, "session_id")
				if err := s.entitledSession(ctx, p, id); err != nil {
					return nil, err
				}
				raw, err := hb.ResetSession(ctx, p, id)
				if err != nil {
					return nil, err
				}
				var out map[string]any
				if err := json.Unmarshal(raw, &out); err != nil {
					return nil, errors.New("unexpected answer from the gateway")
				}
				return out, nil
			},
		},
		{
			name:        "manage_snapshots",
			description: "Snapshots of the home (/home/user) of one of your persistent workspaces: list them (with download links), create one now, restore one (writes its files back; stops the workspace first) or delete one. Restore and delete require explicit human confirmation. A few snapshots per home are kept; the oldest go first.",
			inputSchema: `{"type":"object","properties":{"action":{"type":"string","enum":["list","create","restore","delete"]},"workspace":{"type":"string"},"snapshot_id":{"type":"string"}},"required":["action","workspace"],"additionalProperties":true}`,
			destructive: true,
			gate:        s.snapshotsGate,
			confirmIf:   s.snapshotWouldPrune,
			handle:      s.manageSnapshots,
		},
	}
}

// snapshotWouldPrune: a create that pushes out the oldest snapshot deletes
// it, so it needs the same human confirmation as delete (security audit
// run 1). When the count cannot be read, ask rather than risk it.
func (s *Server) snapshotWouldPrune(ctx context.Context, p Principal, args map[string]any) bool {
	if argString(args, "action") != "create" {
		return false
	}
	hb, err := s.homeBackend()
	if err != nil {
		return false // the handler reports that create is unavailable
	}
	raw, err := hb.Snapshots(ctx, p, argString(args, "workspace"), "list", "")
	if err != nil {
		return true
	}
	var list struct {
		Snapshots []json.RawMessage `json:"snapshots"`
		Keep      int               `json:"keep"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Keep <= 0 {
		return true
	}
	return len(list.Snapshots) >= list.Keep
}
