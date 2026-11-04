package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestNewRoleMap(t *testing.T) {
	m, err := NewRoleMap([]string{"ki-nutzer", "/ki-team"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Persist, []string{"ki-nutzer", "ki-team"}) || !reflect.DeepEqual(m.Admin, []string{RoleAdmin}) {
		t.Fatalf("unexpected map: %+v", m)
	}
	for _, bad := range []string{"", "/", "role:", " x", "a\nb"} {
		if _, err := NewRoleMap(nil, []string{bad}); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestRoleMapApply(t *testing.T) {
	m, _ := NewRoleMap([]string{"ki-nutzer"}, []string{"ki-admin"})
	cases := []struct {
		groups []string
		want   []string
	}{
		{[]string{"ki-nutzer"}, []string{"ki-nutzer", RolePersist}},
		{[]string{"/ki-nutzer"}, []string{"ki-nutzer", RolePersist}},
		{[]string{"role:ki-nutzer"}, []string{"ki-nutzer", RolePersist}},
		{[]string{"role:/ki-admin", "ki-admin"}, []string{"ki-admin", RoleAdmin}},
		// Only one prefix of each kind is stripped.
		{[]string{"//ki-nutzer", "role:role:ki-nutzer"}, []string{"/ki-nutzer", "role:ki-nutzer"}},
		// Subgroups and look-alikes do not match.
		{[]string{"/org/ki-nutzer", "ki-nutzer-alt"}, []string{"org/ki-nutzer", "ki-nutzer-alt"}},
		// The default names count only when they are listed.
		{[]string{RolePersist, "/" + RoleAdmin}, []string{}},
		{nil, []string{}},
	}
	for _, c := range cases {
		if got := m.apply(c.groups); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.groups, got, c.want)
		}
	}

	// Unset lists keep the default names, now also as "/name" and "role:name".
	d, _ := NewRoleMap(nil, nil)
	if got := d.apply([]string{"/" + RolePersist, "role:" + RoleAdmin}); !reflect.DeepEqual(got, []string{RolePersist, RoleAdmin}) {
		t.Errorf("defaults: got %q", got)
	}

	// No RoleMap: groups exactly as forwarded.
	var none *RoleMap
	in := []string{"/" + RolePersist, "role:x"}
	if got := none.apply(in); !reflect.DeepEqual(got, in) {
		t.Errorf("nil map: got %q", got)
	}
}

func TestRoleMapIsAdminGroup(t *testing.T) {
	var none *RoleMap
	if !none.isAdminGroup(RoleAdmin) || none.isAdminGroup("/"+RoleAdmin) {
		t.Fatal("nil map must match the default name exactly")
	}
	m, _ := NewRoleMap(nil, []string{"ki-admin"})
	if !m.isAdminGroup("ki-admin") || !m.isAdminGroup("/ki-admin") || m.isAdminGroup(RoleAdmin) {
		t.Fatal("configured admin groups")
	}
}

// proxyToolCall calls an MCP tool as the sign-in proxy would forward it.
func proxyToolCall(t *testing.T, s *Server, groups, tool string, args map[string]any) rpcResp {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	req.Header.Set(proxyUserHeader, "alice")
	if groups != "" {
		req.Header.Set(proxyGroupsHeader, groups)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var resp rpcResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestRoleMapMCPEndpoint(t *testing.T) {
	m, _ := NewRoleMap([]string{"ki-nutzer"}, []string{"ki-admin"})
	s := NewServer(ProxyHeaderAuthenticator{Roles: m}, NewStubBackend("https://gw.example"), "test").WithRoles(m)
	persist := map[string]any{"workspace": "desk", "persistent": true, "_confirmed": true}
	for _, g := range []string{"ki-nutzer", "/ki-nutzer", "role:ki-nutzer", "other,/ki-nutzer"} {
		var sess Session
		result(t, proxyToolCall(t, s, g, "request_session", persist), &sess)
		if !sess.Persistent {
			t.Fatalf("%q: not persistent", g)
		}
	}
	for _, g := range []string{"", "other", RolePersist, "/org/ki-nutzer"} {
		if resp := proxyToolCall(t, s, g, "request_session", persist); resp.Error == nil || resp.Error.Code != codeForbidden {
			t.Fatalf("%q: want forbidden, got %+v", g, resp.Error)
		}
	}
	for _, g := range []string{"ki-admin", "/ki-admin", "role:ki-admin"} {
		if resp := proxyToolCall(t, s, g, "get_network_policy", nil); resp.Error != nil {
			t.Fatalf("%q: admin refused: %+v", g, resp.Error)
		}
	}
	if resp := proxyToolCall(t, s, "ki-nutzer,"+RoleAdmin, "get_network_policy", nil); resp.Error == nil {
		t.Fatal("unlisted default admin group accepted")
	}
}

func TestRoleMapDefaultsUnchanged(t *testing.T) {
	s := NewServer(ProxyHeaderAuthenticator{}, NewStubBackend("https://gw.example"), "test")
	persist := map[string]any{"workspace": "desk", "persistent": true, "_confirmed": true}
	if resp := proxyToolCall(t, s, RolePersist, "request_session", persist); resp.Error != nil {
		t.Fatalf("default role refused: %+v", resp.Error)
	}
	for _, g := range []string{"/" + RolePersist, "role:" + RolePersist} {
		if resp := proxyToolCall(t, s, g, "request_session", persist); resp.Error == nil {
			t.Fatalf("%q accepted without a RoleMap", g)
		}
	}
}

func TestRoleMapUI(t *testing.T) {
	m, _ := NewRoleMap([]string{"ki-nutzer"}, []string{"ki-admin"})
	f := newUIFixture(t)
	f.ui.srv.WithRoles(m)
	persist := `{"workspace":"desk","persistent":true}`
	for _, g := range []string{"ki-nutzer", "/ki-nutzer", "role:ki-nutzer"} {
		if code, body, _ := f.do(t, http.MethodPost, "/ui/api/sessions", "kc-alice", g, persist, nil); code != http.StatusOK {
			t.Fatalf("%q: %d %s", g, code, body)
		}
	}
	for _, g := range []string{"other", RolePersist} {
		if code, _, _ := f.do(t, http.MethodPost, "/ui/api/sessions", "kc-alice", g, persist, nil); code != http.StatusForbidden {
			t.Fatalf("%q: want forbidden, got %d", g, code)
		}
	}
	// Entitlements see normalised names.
	ent, _ := ParseEntitlements([]byte(`{"default":"deny","workspaces":{"desk":["desk-users"]}}`))
	f.ui.srv.WithEntitlements(ent)
	if code, body, _ := f.do(t, http.MethodPost, "/ui/api/sessions", "kc-alice", "/desk-users,role:ki-nutzer", persist, nil); code != http.StatusOK {
		t.Fatalf("entitled by group path: %d %s", code, body)
	}
}
