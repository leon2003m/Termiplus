package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"termiplus/internal/mcp"
	"termiplus/internal/naming"
)

// Full chain: MCP server -> gateway /control/v1 -> socket proxy -> fake dockerd.
// Proves the MCP gate holds against the real gateway: no persistent container
// is ever created without the role AND an explicit confirmation.

type mcpChain struct {
	h   *harness
	srv *mcp.Server
}

func newMCPChain(t *testing.T) *mcpChain {
	t.Helper()
	h := newHarness(t, withControl)
	auth := mcp.NewStaticAuthenticator(map[string]mcp.Principal{
		"alice-tok": {UserID: "alice"},
		"bob-tok":   {UserID: "bob", Roles: []string{mcp.RolePersist}},
	})
	backend := mcp.NewGatewayBackend(h.gw.URL, "https://terminals.example", testControlKey, "")
	return &mcpChain{h: h, srv: mcp.NewServer(auth, backend, "test")}
}

type mcpOut struct {
	Result *struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *mcpChain) call(t *testing.T, token, tool string, args map[string]any) mcpOut {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	c.srv.ServeHTTP(w, req)
	var out mcpOut
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	return out
}

func (c *mcpChain) ok(t *testing.T, token, tool string, args map[string]any, v any) {
	t.Helper()
	out := c.call(t, token, tool, args)
	if out.Error != nil {
		t.Fatalf("%s: rpc error %+v", tool, *out.Error)
	}
	if out.Result.IsError {
		t.Fatalf("%s: tool error %s", tool, out.Result.Content[0].Text)
	}
	if v != nil {
		if err := json.Unmarshal([]byte(out.Result.Content[0].Text), v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMCPEphemeralSessionEndToEnd(t *testing.T) {
	c := newMCPChain(t)
	var s mcp.Session
	c.ok(t, "alice-tok", "request_session", map[string]any{"workspace": "scratch"}, &s)
	if s.Persistent || s.TerminalSession == "" {
		t.Fatalf("unexpected ephemeral handle: %+v", s)
	}
	if s.TerminalURL != "https://terminals.example/p/scratch/" {
		t.Fatalf("terminal URL should use the public base: %q", s.TerminalURL)
	}
	if c.h.ctr(naming.Ephemeral("alice", s.TerminalSession, "scratch")) == nil {
		t.Fatal("ephemeral container was not created")
	}

	var list []mcp.Session
	c.ok(t, "alice-tok", "list_sessions", nil, &list)
	if len(list) != 1 || list[0].ID != s.ID {
		t.Fatalf("alice should see her session: %+v", list)
	}
	c.ok(t, "bob-tok", "list_sessions", nil, &list)
	if len(list) != 0 {
		t.Fatalf("bob must not see alice's sessions: %+v", list)
	}
}

func TestMCPPersistentGateHoldsEndToEnd(t *testing.T) {
	c := newMCPChain(t)
	persistent := naming.Container("alice", "default")
	none := func(step string) {
		t.Helper()
		if c.h.ctr(persistent) != nil || c.h.ctr(naming.Container("bob", "default")) != nil {
			t.Fatalf("%s: a persistent container was created", step)
		}
	}

	// Asking for a persistent workspace without saying so cannot sneak past the gate.
	out := c.call(t, "alice-tok", "request_session", map[string]any{"workspace": "default"})
	if out.Error != nil || !out.Result.IsError || !strings.Contains(out.Result.Content[0].Text, "persistent") {
		t.Fatalf("implicit persistent request should fail as a mismatch, got %+v", out)
	}
	none("implicit")

	// No role.
	out = c.call(t, "alice-tok", "request_session", map[string]any{"workspace": "default", "persistent": true, "_confirmed": true})
	if out.Error == nil || out.Error.Code != -32003 {
		t.Fatalf("want forbidden without role, got %+v", out)
	}
	none("no role")

	// Role but no human confirmation.
	out = c.call(t, "bob-tok", "request_session", map[string]any{"workspace": "default", "persistent": true})
	if out.Error == nil || out.Error.Code != -32010 {
		t.Fatalf("want confirmation required, got %+v", out)
	}
	none("unconfirmed")

	// Role and confirmation: created.
	var s mcp.Session
	c.ok(t, "bob-tok", "request_session", map[string]any{"workspace": "default", "persistent": true, "_confirmed": true}, &s)
	if s.ID != naming.Container("bob", "default") || !s.Persistent || c.h.ctr(s.ID) == nil {
		t.Fatalf("persistent session not created as expected: %+v", s)
	}

	// Destroy also requires confirmation, then removes the container.
	out = c.call(t, "bob-tok", "destroy_session", map[string]any{"session_id": s.ID})
	if out.Error == nil || out.Error.Code != -32010 {
		t.Fatalf("destroy without confirmation should be refused, got %+v", out)
	}
	if c.h.ctr(s.ID) == nil {
		t.Fatal("container removed without confirmation")
	}
	// Another user cannot destroy it even with confirmation.
	out = c.call(t, "alice-tok", "destroy_session", map[string]any{"session_id": s.ID, "_confirmed": true})
	if out.Error != nil || !out.Result.IsError {
		t.Fatalf("alice destroying bob's session should fail, got %+v", out)
	}
	if c.h.ctr(s.ID) == nil {
		t.Fatal("alice removed bob's container")
	}
	c.ok(t, "bob-tok", "destroy_session", map[string]any{"session_id": s.ID, "_confirmed": true}, nil)
	if c.h.ctr(s.ID) != nil {
		t.Fatal("container still present after confirmed destroy")
	}
}

func TestMCPNetworkPolicyNotConfigured(t *testing.T) {
	c := newMCPChain(t)
	// No admin role: forbidden before anything else.
	out := c.call(t, "bob-tok", "set_network_policy", map[string]any{"policy": map[string]any{}, "_confirmed": true})
	if out.Error == nil || out.Error.Code != -32003 {
		t.Fatalf("want forbidden, got %+v", out)
	}
}

// Admin tools through the real gateway admin API: set, then read back.
func TestMCPNetworkPolicyThroughGateway(t *testing.T) {
	opt, _ := withAdmin(t)
	h := newHarness(t, opt)
	auth := mcp.NewStaticAuthenticator(map[string]mcp.Principal{"root-tok": {UserID: "root", Roles: []string{mcp.RoleAdmin}}})
	c := &mcpChain{h: h, srv: mcp.NewServer(auth, mcp.NewGatewayBackend(h.gw.URL, "", "", testAdminKey), "test")}

	pol := map[string]any{"dns_entries": []any{map[string]any{"name": "db.internal", "ip": "10.0.0.5"}}, "egress_allow": []any{}}
	if out := c.call(t, "root-tok", "set_network_policy", map[string]any{"policy": pol, "_confirmed": true}); out.Error != nil || out.Result.IsError {
		t.Fatalf("set: %+v", out)
	}
	out := c.call(t, "root-tok", "get_network_policy", nil)
	if out.Error != nil || out.Result.IsError || !strings.Contains(out.Result.Content[0].Text, "db.internal") {
		t.Fatalf("get: %+v", out)
	}
}
