package mcp

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"sort"
)

// MCP Apps (io.modelcontextprotocol/ui): one model-visible entry tool,
// open_workspaces, renders an inline management panel. The panel's buttons call
// the existing management tools through the host; those tools are listed as
// app-only so a host that supports MCP Apps hides them from the model.
//
// App-only visibility is a host convention, not proof that a human pressed a
// button: every call still runs through callTool's allowlist, the RBAC gate,
// entitlement checks and the confirmation rule. Administration, snapshot
// restore/delete and pruning stay unreachable over MCP regardless of metadata.
const (
	appExtension   = "io.modelcontextprotocol/ui"
	appResourceURI = "ui://termiplus/workspaces"
	appMIMEType    = "text/html;profile=mcp-app"
	appToolName    = "open_workspaces"
	maxPanelHomes  = 8
)

//go:embed app/workspaces.html
var appHTML string

// appResource is the single UI resource. The panel is self-contained: no
// remote scripts, styles, frames or network access (empty CSP domain lists).
func appResource() map[string]any {
	return map[string]any{
		"uri":         appResourceURI,
		"name":        "termiplus workspaces",
		"description": "Manage your termiplus sessions, home backups and screen-share links.",
		"mimeType":    appMIMEType,
	}
}

func appResourceContents() map[string]any {
	return map[string]any{
		"uri":      appResourceURI,
		"mimeType": appMIMEType,
		"text":     appHTML,
		"_meta": map[string]any{"ui": map[string]any{
			"csp":           map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}, "frameDomains": []string{}, "baseUriDomains": []string{}},
			"prefersBorder": true,
		}},
	}
}

// appOnly lists the management tools the panel drives. They are also the
// existing plugin's tools, so they keep working over tools/call; only their
// tools/list metadata changes. Like modelTool this is an upper bound; a tool
// missing from s.tools (share_desktop with sharing disabled) is never listed.
func appOnly(name string) bool {
	switch name {
	case "list_workspaces", "list_sessions", "request_session", "destroy_session", "reset_workspace", "wipe_home", "manage_snapshots", "share_desktop":
		return true
	}
	return false
}

func toolMeta(name string) map[string]any {
	switch {
	case name == appToolName:
		return map[string]any{"ui": map[string]any{"resourceUri": appResourceURI, "visibility": []string{"model", "app"}}}
	case appOnly(name):
		return map[string]any{"ui": map[string]any{"visibility": []string{"app"}}}
	}
	return nil
}

// handleResources serves resources/list and resources/read. Reading requires
// an authenticated caller even though the panel holds no secret, so the
// endpoint reveals nothing to anonymous clients.
func (s *Server) handleResources(r *http.Request, method string, raw json.RawMessage) (any, *rpcError) {
	if _, err := s.auth.Authenticate(r); err != nil {
		return nil, &rpcError{Code: codeUnauthenticated, Message: "unauthenticated"}
	}
	switch method {
	case "resources/list":
		return map[string]any{"resources": []any{appResource()}}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, nil
	}
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.URI != appResourceURI {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown resource"}
	}
	return map[string]any{"contents": []any{appResourceContents()}}, nil
}

// appTool returns the read-only entry tool. It gathers the panel's initial
// state in one call; a failing section is reported in "errors" instead of
// failing the whole panel. It never provisions a workspace.
func (s *Server) appTool() tool {
	return tool{
		name:        appToolName,
		description: "Open the termiplus workspace manager: your workspaces, sessions, home backups and screen-share links in one panel. Read-only; changes are made by the user in the panel.",
		inputSchema: `{"type":"object","properties":{},"additionalProperties":false}`,
		readOnly:    true,
		gate:        func(Principal, map[string]any) (bool, error) { return false, nil },
		handle:      s.workspaceOverview,
	}
}

func (s *Server) workspaceOverview(ctx context.Context, p Principal, _ map[string]any) (any, error) {
	return s.overview(ctx, p), nil
}

// panelResult is what the panel's action tools return: the freshly rebuilt
// panel state plus a short human-readable note, so the panel re-renders from
// the action's own response in one round trip. extra is the tool's original
// result: its fields are kept (never overriding a panel field) and its own
// note, if any, follows ours, so callers without MCP App support lose nothing.
func (s *Server) panelResult(ctx context.Context, p Principal, note string, extra any) (any, error) {
	out := s.overview(ctx, p)
	if m, ok := extra.(map[string]any); ok {
		for k, v := range m {
			if k == "note" {
				if t, ok := v.(string); ok && t != "" {
					note += " " + t
				}
				continue
			}
			if _, taken := out[k]; !taken {
				out[k] = v
			}
		}
	}
	out["note"] = note
	return out, nil
}

// sessionName is the label a user knows a session by, for notes. Best effort:
// it falls back to the workspace name, then the id.
func (s *Server) sessionName(ctx context.Context, p Principal, id string) string {
	if sessions, err := s.backend.ListSessions(ctx, p); err == nil {
		for _, sess := range sessions {
			if sess.ID == id {
				if sess.Label != "" {
					return sess.Label
				}
				return sess.Workspace
			}
		}
	}
	return id
}

func (s *Server) overview(ctx context.Context, p Principal) map[string]any {
	out := map[string]any{"workspaces": []Workspace{}, "sessions": []Session{}, "backups": map[string]any{}, "shares": []any{}, "sharing": !s.noSharing, "home_remove": s.homeRemove}
	// Section errors are generic: backend messages can name internal URLs.
	errs := map[string]string{}
	if all, err := s.backend.ListWorkspaces(ctx, p); err != nil {
		errs["workspaces"] = "unavailable"
	} else {
		out["workspaces"] = s.entitlements().filter(p, all)
	}
	sessions, err := s.backend.ListSessions(ctx, p)
	if err != nil {
		errs["sessions"] = "unavailable"
	} else {
		sessions = s.filterSessions(p, sessions)
		out["sessions"] = sessions
	}
	if _, herr := s.homeBackend(); herr == nil {
		seen := map[string]bool{}
		var homes []string
		for _, sess := range sessions {
			if sess.Persistent && !seen[sess.Workspace] {
				seen[sess.Workspace] = true
				homes = append(homes, sess.Workspace)
			}
		}
		sort.Strings(homes)
		backups := map[string]any{}
		// Bounded fan-out: one snapshot listing per persistent home, at most
		// maxPanelHomes, so opening the panel cannot multiply gateway calls.
		if len(homes) > maxPanelHomes {
			homes = homes[:maxPanelHomes]
		}
		for _, ws := range homes {
			if s.entitledWorkspace(ctx, p, ws) != nil {
				continue
			}
			res, berr := s.manageSnapshots(ctx, p, map[string]any{"action": "list", "workspace": ws})
			if berr != nil {
				errs["backups:"+ws] = "unavailable"
				continue
			}
			backups[ws] = res
		}
		out["backups"] = backups
	}
	if s.shares != nil && s.desktopBase != "" {
		out["shares"] = s.shares.List(p.UserID)
	}
	if len(errs) > 0 {
		out["errors"] = errs
	}
	return out
}
