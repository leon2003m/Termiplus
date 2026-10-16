package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

// protocolVersion is the MCP protocol revision this server speaks.
const protocolVersion = "2025-06-18"

// Server is the MCP control-plane server. It speaks JSON-RPC 2.0 over HTTP POST.
type Server struct {
	auth    Authenticator
	backend Backend
	version string
	tools   []tool
	ent     atomic.Pointer[Entitlements]
	entPath string // where set_entitlements persists; "" makes them read-only
	ids     *IdentityStore
	// dir backs manage_user / manage_group; nil leaves them unconfigured.
	dir         Directory
	groupPrefix string
	auditLog    *AuditLog // nil: calls are not recorded (WithAuditLog)
	uiBase      string    // browser-facing console base URL, for download links
	shares      *ShareStore
	departed    *departed
}

// NewServer builds the server with its fixed tool surface.
func NewServer(auth Authenticator, backend Backend, version string) *Server {
	s := &Server{auth: auth, backend: backend, version: version}
	s.tools = s.buildTools()
	for _, extra := range [][]tool{s.homeTools(), s.shareTools(), s.departedTools()} {
		s.tools = append(s.tools, extra...)
	}
	return s
}

// WithEntitlements restricts which workspaces callers may see and launch.
// Without it every workspace is open to every authenticated caller.
func (s *Server) WithEntitlements(e *Entitlements) *Server {
	s.ent.Store(e)
	return s
}

// WithEntitlementsFile loads the entitlements from path and lets admins change
// them with set_entitlements, which rewrites the file. A missing file means
// every workspace is open until an admin sets entitlements.
func (s *Server) WithEntitlementsFile(path string) (*Server, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.ent.Store(nil)
	case err != nil:
		return nil, err
	default:
		e, err := ParseEntitlements(data)
		if err != nil {
			return nil, err
		}
		s.ent.Store(e)
	}
	s.entPath = path
	return s, nil
}

func (s *Server) entitlements() *Entitlements { return s.ent.Load() }

// setEntitlements validates raw like the file, persists it atomically and
// only then applies it.
func (s *Server) setEntitlements(raw []byte) (*Entitlements, error) {
	if s.entPath == "" {
		return nil, errors.New("entitlements are not editable on this server (no MCP_ENTITLEMENTS_FILE)")
	}
	e, err := ParseEntitlements(raw)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(s.entPath, append(e.marshal(), '\n')); err != nil {
		return nil, fmt.Errorf("save entitlements: %w", err)
	}
	s.ent.Store(e)
	return e, nil
}

// WithIdentities makes the server remember which Open WebUI user each OIDC
// subject belongs to, for the management UI.
func (s *Server) WithIdentities(ids *IdentityStore) *Server {
	s.ids = ids
	return s
}

// tool is one MCP tool: its schema, UI hints, the RBAC/confirmation gate and the
// handler. gate reports whether the call is allowed for this principal+args and
// whether it needs explicit human confirmation; handle does the work.
type tool struct {
	name        string
	description string
	inputSchema string
	readOnly    bool
	destructive bool
	gate        func(p Principal, args map[string]any) (needConfirm bool, err error)
	handle      func(ctx context.Context, p Principal, args map[string]any) (any, error)
}

// --- JSON-RPC 2.0 wire types -------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// JSON-RPC / MCP error codes.
const (
	codeParse           = -32700
	codeInvalidRequest  = -32600
	codeMethodNotFound  = -32601
	codeInvalidParams   = -32602
	codeInternal        = -32603
	codeUnauthenticated = -32001
	codeForbidden       = -32003
	codeConfirmRequired = -32010
)

// --- MCP result types --------------------------------------------------------

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type toolAnnotations struct {
	Title                string `json:"title,omitempty"`
	ReadOnlyHint         bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint      bool   `json:"destructiveHint,omitempty"`
	RequiresConfirmation bool   `json:"requiresConfirmation,omitempty"` // non-standard hint for the OWUI plugin
}

type toolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations toolAnnotations `json:"annotations"`
}

// ServeHTTP dispatches a single JSON-RPC request per POST.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		s.write(w, &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeInvalidRequest, Message: "POST required"}})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.write(w, &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParse, Message: "read body"}})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.write(w, &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParse, Message: "invalid JSON"}})
		return
	}
	if req.JSONRPC != "2.0" {
		s.write(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeInvalidRequest, Message: "jsonrpc must be 2.0"}})
		return
	}

	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = s.initialize()
	case "tools/list":
		resp.Result = map[string]any{"tools": s.listTools()}
	case "tools/call":
		result, rerr := s.callTool(r, req.Params)
		if rerr != nil {
			resp.Error = rerr
		} else {
			resp.Result = result
		}
	case "notifications/initialized", "ping":
		resp.Result = map[string]any{}
	default:
		resp.Error = &rpcError{Code: codeMethodNotFound, Message: "unknown method " + req.Method}
	}
	s.write(w, resp)
}

func (s *Server) initialize() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "termiplus", "version": s.version},
	}
}

func (s *Server) listTools() []toolSpec {
	out := make([]toolSpec, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, toolSpec{
			Name:        t.name,
			Description: t.description,
			InputSchema: json.RawMessage(t.inputSchema),
			Annotations: toolAnnotations{
				ReadOnlyHint:         t.readOnly,
				DestructiveHint:      t.destructive,
				RequiresConfirmation: t.destructive,
			},
		})
	}
	return out
}

func (s *Server) callTool(r *http.Request, raw json.RawMessage) (*toolResult, *rpcError) {
	p, err := s.auth.Authenticate(r)
	if err != nil {
		return nil, &rpcError{Code: codeUnauthenticated, Message: "unauthenticated"}
	}
	if s.ids != nil && p.Subject != "" {
		if err := s.ids.Record(p.Subject, p.UserID); err != nil {
			log.Printf("identity: record %s: %v", p.Subject, err)
		}
	}
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params"}
	}
	if params.Arguments == nil {
		params.Arguments = map[string]any{}
	}
	return s.invoke(r.Context(), p, params.Name, params.Arguments)
}

// invoke runs one tool for an authenticated principal through the RBAC,
// entitlement and confirmation gates.
func (s *Server) invoke(ctx context.Context, p Principal, name string, args map[string]any) (*toolResult, *rpcError) {
	t := s.findTool(name)
	if t == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool " + name}
	}

	needConfirm, gerr := t.gate(p, args)
	if gerr != nil {
		s.audit(p, t, args, "denied", gerr)
		return nil, &rpcError{Code: codeForbidden, Message: gerr.Error()}
	}
	if needConfirm && !confirmed(args) {
		// The OWUI plugin must obtain an explicit human confirmation (a modal)
		// and re-call with "_confirmed": true. The server will not run a gated
		// action without it.
		return nil, &rpcError{
			Code:    codeConfirmRequired,
			Message: "human confirmation required",
			Data:    map[string]any{"tool": t.name, "summary": t.description},
		}
	}

	result, herr := t.handle(ctx, p, args)
	if herr != nil {
		s.audit(p, t, args, "error", herr)
		return &toolResult{Content: []contentBlock{{Type: "text", Text: herr.Error()}}, IsError: true}, nil
	}
	s.audit(p, t, args, "ok", nil)
	js, err := json.Marshal(result)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: "marshal result"}
	}
	return &toolResult{Content: []contentBlock{{Type: "text", Text: string(js)}}}, nil
}

func (s *Server) findTool(name string) *tool {
	for i := range s.tools {
		if s.tools[i].name == name {
			return &s.tools[i]
		}
	}
	return nil
}

func (s *Server) write(w http.ResponseWriter, resp *rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func confirmed(args map[string]any) bool {
	v, ok := args["_confirmed"].(bool)
	return ok && v
}

func argString(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func argBool(args map[string]any, key string) bool {
	b, _ := args[key].(bool)
	return b
}

func (s *Server) buildTools() []tool {
	return []tool{
		{
			name:        "list_workspaces",
			description: "List the workspaces (profiles) you may launch.",
			inputSchema: `{"type":"object","properties":{},"additionalProperties":false}`,
			readOnly:    true,
			gate:        func(Principal, map[string]any) (bool, error) { return false, nil },
			handle: func(ctx context.Context, p Principal, _ map[string]any) (any, error) {
				all, err := s.backend.ListWorkspaces(ctx, p)
				if err != nil {
					return nil, err
				}
				return s.entitlements().filter(p, all), nil
			},
		},
		{
			name:        "request_session",
			description: "Provision (or attach to) a sandbox session and return its access handle. A persistent session requires the termiplus-persist role and explicit human confirmation.",
			inputSchema: `{"type":"object","properties":{"workspace":{"type":"string"},"persistent":{"type":"boolean","default":false},"label":{"type":"string"}},"required":["workspace"],"additionalProperties":true}`,
			gate: func(p Principal, args map[string]any) (bool, error) {
				if ws := argString(args, "workspace"); !s.entitlements().Allowed(p, ws) {
					return false, fmt.Errorf("you are not entitled to the workspace %q", ws)
				}
				if argBool(args, "persistent") {
					if !p.HasRole(RolePersist) {
						return false, fmt.Errorf("persistent sessions require the %s role", RolePersist)
					}
					return true, nil // entitled, but still needs human confirmation
				}
				return false, nil
			},
			handle: func(ctx context.Context, p Principal, args map[string]any) (any, error) {
				ws := argString(args, "workspace")
				if ws == "" {
					return nil, fmt.Errorf("workspace is required")
				}
				return s.backend.RequestSession(ctx, p, ws, argBool(args, "persistent"), argString(args, "label"))
			},
		},
		{
			name:        "list_sessions",
			description: "List your active sessions.",
			inputSchema: `{"type":"object","properties":{},"additionalProperties":false}`,
			readOnly:    true,
			gate:        func(Principal, map[string]any) (bool, error) { return false, nil },
			handle: func(ctx context.Context, p Principal, _ map[string]any) (any, error) {
				return s.backend.ListSessions(ctx, p)
			},
		},
		{
			name:        "destroy_session",
			description: "Tear down one of your sessions. Destructive: requires explicit human confirmation.",
			inputSchema: `{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"],"additionalProperties":true}`,
			destructive: true,
			gate:        func(Principal, map[string]any) (bool, error) { return true, nil },
			handle: func(ctx context.Context, p Principal, args map[string]any) (any, error) {
				id := argString(args, "session_id")
				if id == "" {
					return nil, fmt.Errorf("session_id is required")
				}
				if err := s.backend.DestroySession(ctx, p, id); err != nil {
					return nil, err
				}
				return map[string]any{"destroyed": id}, nil
			},
		},
		{
			name:        "get_network_policy",
			description: "Show the custom DNS entries and internal egress whitelist (admin).",
			inputSchema: `{"type":"object","properties":{},"additionalProperties":false}`,
			readOnly:    true,
			gate:        adminOnly("network policy", false),
			handle: func(ctx context.Context, p Principal, _ map[string]any) (any, error) {
				return s.backend.GetNetworkPolicy(ctx, p)
			},
		},
		{
			name:        "get_entitlements",
			description: "Show which groups may use which workspace (admin).",
			inputSchema: `{"type":"object","properties":{},"additionalProperties":false}`,
			readOnly:    true,
			gate:        adminOnly("entitlements", false),
			handle: func(context.Context, Principal, map[string]any) (any, error) {
				e := s.entitlements()
				return map[string]any{"entitlements": json.RawMessage(e.marshal()), "editable": s.entPath != ""}, nil
			},
		},
		{
			name:        "set_entitlements",
			description: "Replace which groups may use which workspace (admin). Security-sensitive: requires the termiplus-admin role and explicit human confirmation.",
			inputSchema: `{"type":"object","properties":{"entitlements":{"type":"object"}},"required":["entitlements"],"additionalProperties":true}`,
			destructive: true,
			gate:        adminOnly("entitlements", true),
			handle: func(_ context.Context, _ Principal, args map[string]any) (any, error) {
				raw, err := json.Marshal(args["entitlements"])
				if err != nil {
					return nil, err
				}
				e, err := s.setEntitlements(raw)
				if err != nil {
					return nil, err
				}
				return map[string]any{"entitlements": json.RawMessage(e.marshal())}, nil
			},
		},
		{
			name:        "set_network_policy",
			description: "Set custom DNS entries and internal egress whitelist (admin). Security-sensitive: requires the termiplus-admin role and explicit human confirmation.",
			inputSchema: `{"type":"object","properties":{"policy":{"type":"object"}},"required":["policy"],"additionalProperties":true}`,
			destructive: true,
			gate:        adminOnly("network policy", true),
			handle: func(ctx context.Context, p Principal, args map[string]any) (any, error) {
				pol, err := json.Marshal(args["policy"])
				if err != nil {
					return nil, err
				}
				if err := s.backend.SetNetworkPolicy(ctx, p, pol); err != nil {
					return nil, err
				}
				return map[string]any{"applied": true}, nil
			},
		},
		{
			name:        "manage_group",
			description: "List the termiplus groups (action \"list\") or the members of one (action \"members\", group) (admin).",
			inputSchema: `{"type":"object","properties":{"action":{"type":"string","enum":["list","members"]},"group":{"type":"string"}},"required":["action"],"additionalProperties":false}`,
			readOnly:    true,
			gate:        adminOnly("user management", false),
			handle: func(ctx context.Context, _ Principal, args map[string]any) (any, error) {
				return s.manageGroup(ctx, args)
			},
		},
		{
			name:        "manage_user",
			description: "Show a user's termiplus groups (action \"show\", username) or add/remove them to/from one (actions \"add_to_group\" / \"remove_from_group\", username, group) (admin). Changes are security-sensitive: they require the termiplus-admin role and explicit human confirmation. Users themselves are created in the identity provider.",
			inputSchema: `{"type":"object","properties":{"action":{"type":"string","enum":["show","add_to_group","remove_from_group"]},"username":{"type":"string"},"group":{"type":"string"}},"required":["action","username"],"additionalProperties":true}`,
			destructive: true,
			gate:        manageUserGate,
			handle: func(ctx context.Context, p Principal, args map[string]any) (any, error) {
				return s.manageUser(ctx, p, args)
			},
		},
		{
			name:        "get_audit_log",
			description: "Show the newest recorded state-changing calls (sessions, entitlements, network policy, group changes): who, what, when and the outcome, newest first (admin). Optional filters: actor (user id), tool, since (RFC 3339 time), limit (1-500, default 50).",
			inputSchema: `{"type":"object","properties":{"actor":{"type":"string"},"tool":{"type":"string"},"since":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":500}},"additionalProperties":false}`,
			readOnly:    true,
			gate:        adminOnly("the audit log", false),
			handle: func(_ context.Context, _ Principal, args map[string]any) (any, error) {
				return s.getAuditLog(args)
			},
		},
	}
}

// adminOnly gates a tool on the admin role; confirm marks it as needing an
// explicit human confirmation.
func adminOnly(what string, confirm bool) func(Principal, map[string]any) (bool, error) {
	return func(p Principal, _ map[string]any) (bool, error) {
		if !p.HasRole(RoleAdmin) {
			return false, fmt.Errorf("%s requires the %s role", what, RoleAdmin)
		}
		return confirm, nil
	}
}
