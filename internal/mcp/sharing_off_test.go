package mcp

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func sharingTestServer(t *testing.T, disabled bool) *Server {
	t.Helper()
	auth := NewStaticAuthenticator(map[string]Principal{"tok": {UserID: "alice", Roles: []string{RolePersist}}})
	s := NewServer(auth, NewStubDevBackend("https://gw.example"), "test")
	if disabled {
		s.DisableSharing()
	}
	if err := ConfigureStubServer(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func listedTools(t *testing.T, s *Server) map[string]bool {
	t.Helper()
	var out struct {
		Tools []toolSpec `json:"tools"`
	}
	if err := json.Unmarshal(call(t, s, "tok", "tools/list", nil).Result, &out); err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, tl := range out.Tools {
		m[tl.Name] = true
	}
	return m
}

func TestSharingDisabledUnregistersShareTool(t *testing.T) {
	s := sharingTestServer(t, true)
	if s.SharingEnabled() || s.shares != nil {
		t.Fatalf("sharing must be off with no store: enabled=%v shares=%v", s.SharingEnabled(), s.shares)
	}
	if listedTools(t, s)["share_desktop"] {
		t.Fatal("share_desktop listed with sharing disabled")
	}
	if s.findTool("share_desktop") != nil {
		t.Fatal("share_desktop still registered")
	}
	resp := toolCall(t, s, "tok", "share_desktop", map[string]any{"action": "list"})
	if resp.Error == nil || !strings.Contains(strings.ToLower(resp.Error.Message), "unknown") {
		t.Fatalf("share_desktop should be an unknown tool: %+v", resp)
	}
	s.WithShares(&ShareStore{})
	if s.shares != nil {
		t.Fatal("WithShares must be a no-op when sharing is disabled")
	}
}

func TestSharingDisabledOverviewHasNoShares(t *testing.T) {
	s := sharingTestServer(t, true)
	var state map[string]any
	result(t, toolCall(t, s, "tok", appToolName, nil), &state)
	if v, ok := state["sharing"].(bool); !ok || v {
		t.Fatalf("sharing flag = %v", state["sharing"])
	}
	if sh, _ := state["shares"].([]any); len(sh) != 0 {
		t.Fatalf("shares present: %v", state["shares"])
	}
}

func TestSharingDisabledConsoleRoutes404(t *testing.T) {
	ids, err := OpenIdentityStore(filepath.Join(t.TempDir(), "ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = ids.Record("kc-alice", "owui-alice")
	srv := NewServer(ProxyHeaderAuthenticator{}, NewStubBackend("https://gw.example"), "test").WithIdentities(ids)
	srv.DisableSharing()
	f := uiFixture{ui: NewUI(srv, ids)}
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodGet, "/ui/api/shares", ""},
		{http.MethodPost, "/ui/api/shares", `{"session_id":"x"}`},
		{http.MethodDelete, "/ui/api/shares/abc", ""},
		{http.MethodGet, "/ui/api/desktops/x/people", ""},
	} {
		if code, _, _ := f.do(t, rt.method, rt.path, "kc-alice", "", rt.body, nil); code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", rt.method, rt.path, code)
		}
	}
	// Control: the enabled server does not 404 the list route. Only meaningful
	// when sharing is compiled in; a -tags noshare build has no enabled server.
	if sharingCompiled {
		srv2 := NewServer(ProxyHeaderAuthenticator{}, NewStubBackend("https://gw.example"), "test").WithIdentities(ids)
		f2 := uiFixture{ui: NewUI(srv2, ids)}
		if code, _, _ := f2.do(t, http.MethodGet, "/ui/api/shares", "kc-alice", "", "", nil); code == http.StatusNotFound {
			t.Error("enabled server 404s the share list route")
		}
	}
}

func TestSharingEnabledByDefault(t *testing.T) {
	if !sharingCompiled {
		t.Skip("sharing is compiled out (-tags noshare)")
	}
	s := sharingTestServer(t, false)
	if !s.SharingEnabled() || !listedTools(t, s)["share_desktop"] {
		t.Fatal("share_desktop must be listed by default")
	}
	var state map[string]any
	result(t, toolCall(t, s, "tok", appToolName, nil), &state)
	if v, _ := state["sharing"].(bool); !v {
		t.Fatalf("sharing flag = %v", state["sharing"])
	}
}
