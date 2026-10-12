package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// protocolVersion is the MCP protocol revision this server speaks.
const protocolVersion = "2025-06-18"

// Server is the MCP control-plane server. It speaks JSON-RPC 2.0 over HTTP POST.
type Server struct {
	auth    Authenticator
	backend Backend
	version string
	tools   []tool
}

// NewServer builds the server with its fixed tool surface.
func NewServer(auth Authenticator, backend Backend, version string) *Server {
	s := &Server{auth: auth, backend: backend, version: version}
	s.tools = s.buildTools()
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
	if r.Method != http.MethodPost {
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
	p, err := s.auth.Authenticate(bearer(r))
	if err != nil {
		return nil, &rpcError{Code: codeUnauthenticated, Message: "unauthenticated"}
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
	t := s.findTool(params.Name)
	if t == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool " + params.Name}
	}

	needConfirm, gerr := t.gate(p, params.Arguments)
	if gerr != nil {
		return nil, &rpcError{Code: codeForbidden, Message: gerr.Error()}
	}
	if needConfirm && !confirmed(params.Arguments) {
		// The OWUI plugin must obtain an explicit human confirmation (a modal)
		// and re-call with "_confirmed": true. The server will not run a gated
		// action without it.
		return nil, &rpcError{
			Code:    codeConfirmRequired,
			Message: "human confirmation required",
			Data:    map[string]any{"tool": t.name, "summary": t.description},
		}
	}

	result, herr := t.handle(r.Context(), p, params.Arguments)
	if herr != nil {
		return &toolResult{Content: []contentBlock{{Type: "text", Text: herr.Error()}}, IsError: true}, nil
	}
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
				return s.backend.ListWorkspaces(ctx, p)
			},
		},
		{
			name:        "request_session",
			description: "Provision (or attach to) a sandbox session and return its access handle. A persistent session requires the termiplus-persist role and explicit human confirmation.",
			inputSchema: `{"type":"object","properties":{"workspace":{"type":"string"},"persistent":{"type":"boolean","default":false},"label":{"type":"string"}},"required":["workspace"],"additionalProperties":true}`,
			gate: func(p Principal, args map[string]any) (bool, error) {
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
			name:        "set_network_policy",
			description: "Set custom DNS entries and internal egress whitelist (admin). Security-sensitive: requires the termiplus-admin role and explicit human confirmation.",
			inputSchema: `{"type":"object","properties":{"policy":{"type":"object"}},"required":["policy"],"additionalProperties":true}`,
			destructive: true,
			gate: func(p Principal, _ map[string]any) (bool, error) {
				if !p.HasRole(RoleAdmin) {
					return false, fmt.Errorf("network policy requires the %s role", RoleAdmin)
				}
				return true, nil
			},
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
	}
}
