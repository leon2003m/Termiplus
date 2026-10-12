package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer() *Server {
	auth := NewStaticAuthenticator(map[string]Principal{
		"user-tok":    {UserID: "alice"},
		"persist-tok": {UserID: "bob", Roles: []string{RolePersist}},
		"admin-tok":   {UserID: "carol", Roles: []string{RoleAdmin}},
	})
	return NewServer(auth, NewStubBackend("https://gw.example"), "test")
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func call(t *testing.T, s *Server, token, method string, params any) rpcResp {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var resp rpcResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (%s)", err, w.Body.String())
	}
	return resp
}

func toolCall(t *testing.T, s *Server, token, name string, args map[string]any) rpcResp {
	t.Helper()
	return call(t, s, token, "tools/call", map[string]any{"name": name, "arguments": args})
}

// result decodes a successful tool call's JSON payload into v.
func result(t *testing.T, resp rpcResp, v any) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	var tr toolResult
	if err := json.Unmarshal(resp.Result, &tr); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if tr.IsError {
		t.Fatalf("tool returned error: %s", tr.Content[0].Text)
	}
	if err := json.Unmarshal([]byte(tr.Content[0].Text), v); err != nil {
		t.Fatalf("decode tool payload: %v (%s)", err, tr.Content[0].Text)
	}
}

func TestInitialize(t *testing.T) {
	s := newTestServer()
	resp := call(t, s, "", "initialize", nil)
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	var out struct {
		ProtocolVersion string                `json:"protocolVersion"`
		ServerInfo      struct{ Name string } `json:"serverInfo"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatal(err)
	}
	if out.ProtocolVersion != protocolVersion || out.ServerInfo.Name != "termiplus" {
		t.Fatalf("unexpected initialize result: %s", resp.Result)
	}
}

func TestToolsListNoFsOrExec(t *testing.T) {
	s := newTestServer()
	resp := call(t, s, "", "tools/list", nil)
	var out struct {
		Tools []toolSpec `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range out.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"list_workspaces", "request_session", "list_sessions", "destroy_session", "set_network_policy"} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}
	for _, forbidden := range []string{"read_file", "write_file", "list_dir", "exec"} {
		if names[forbidden] {
			t.Errorf("control plane must not expose %q", forbidden)
		}
	}
}

func TestCallRequiresAuth(t *testing.T) {
	s := newTestServer()
	resp := toolCall(t, s, "", "list_workspaces", nil)
	if resp.Error == nil || resp.Error.Code != codeUnauthenticated {
		t.Fatalf("want unauthenticated, got %+v", resp.Error)
	}
	resp = toolCall(t, s, "wrong-tok", "list_workspaces", nil)
	if resp.Error == nil || resp.Error.Code != codeUnauthenticated {
		t.Fatalf("want unauthenticated for bad token, got %+v", resp.Error)
	}
}

func TestListWorkspaces(t *testing.T) {
	s := newTestServer()
	var ws []Workspace
	result(t, toolCall(t, s, "user-tok", "list_workspaces", nil), &ws)
	if len(ws) == 0 {
		t.Fatal("expected workspaces")
	}
}

func TestEphemeralSessionIsAutonomous(t *testing.T) {
	s := newTestServer()
	var sess Session
	result(t, toolCall(t, s, "user-tok", "request_session", map[string]any{"workspace": "default"}), &sess)
	if sess.ID == "" || sess.Persistent || sess.TerminalURL == "" {
		t.Fatalf("unexpected session: %+v", sess)
	}
}

func TestPersistentSessionNeedsRole(t *testing.T) {
	s := newTestServer()
	resp := toolCall(t, s, "user-tok", "request_session", map[string]any{"workspace": "desk", "persistent": true})
	if resp.Error == nil || resp.Error.Code != codeForbidden {
		t.Fatalf("want forbidden without role, got %+v", resp.Error)
	}
}

func TestPersistentSessionNeedsConfirmation(t *testing.T) {
	s := newTestServer()
	// Entitled (persist role) but no confirmation -> must be refused.
	resp := toolCall(t, s, "persist-tok", "request_session", map[string]any{"workspace": "desk", "persistent": true})
	if resp.Error == nil || resp.Error.Code != codeConfirmRequired {
		t.Fatalf("want confirmation-required, got %+v", resp.Error)
	}
	// With confirmation it succeeds.
	var sess Session
	result(t, toolCall(t, s, "persist-tok", "request_session", map[string]any{"workspace": "desk", "persistent": true, "_confirmed": true}), &sess)
	if !sess.Persistent || sess.UserID != "" { // UserID is json:"-"
		t.Fatalf("unexpected persistent session: %+v", sess)
	}
}

func TestDestroyNeedsConfirmation(t *testing.T) {
	s := newTestServer()
	var sess Session
	result(t, toolCall(t, s, "user-tok", "request_session", map[string]any{"workspace": "default"}), &sess)

	resp := toolCall(t, s, "user-tok", "destroy_session", map[string]any{"session_id": sess.ID})
	if resp.Error == nil || resp.Error.Code != codeConfirmRequired {
		t.Fatalf("destroy without confirmation should be refused, got %+v", resp.Error)
	}
	var out map[string]any
	result(t, toolCall(t, s, "user-tok", "destroy_session", map[string]any{"session_id": sess.ID, "_confirmed": true}), &out)
	if out["destroyed"] != sess.ID {
		t.Fatalf("expected destroyed %s, got %v", sess.ID, out)
	}
}

func TestSessionsAreScopedToUser(t *testing.T) {
	s := newTestServer()
	var mine Session
	result(t, toolCall(t, s, "user-tok", "request_session", map[string]any{"workspace": "default"}), &mine)

	var bobs []Session
	result(t, toolCall(t, s, "persist-tok", "list_sessions", nil), &bobs)
	if len(bobs) != 0 {
		t.Fatalf("bob should not see alice's sessions, got %d", len(bobs))
	}
	var alices []Session
	result(t, toolCall(t, s, "user-tok", "list_sessions", nil), &alices)
	if len(alices) != 1 || alices[0].ID != mine.ID {
		t.Fatalf("alice should see her one session, got %+v", alices)
	}
}

func TestNetworkPolicyNeedsAdmin(t *testing.T) {
	s := newTestServer()
	resp := toolCall(t, s, "user-tok", "set_network_policy", map[string]any{"policy": map[string]any{}})
	if resp.Error == nil || resp.Error.Code != codeForbidden {
		t.Fatalf("want forbidden without admin, got %+v", resp.Error)
	}
	resp = toolCall(t, s, "admin-tok", "set_network_policy", map[string]any{"policy": map[string]any{}})
	if resp.Error == nil || resp.Error.Code != codeConfirmRequired {
		t.Fatalf("admin still needs confirmation, got %+v", resp.Error)
	}
	var out map[string]any
	result(t, toolCall(t, s, "admin-tok", "set_network_policy", map[string]any{"policy": map[string]any{}, "_confirmed": true}), &out)
	if out["applied"] != true {
		t.Fatalf("expected applied, got %v", out)
	}
}
