package mcp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminToolsNeedRoleAndConfirmation(t *testing.T) {
	s := newTestServer()
	path := filepath.Join(t.TempDir(), "ent.json")
	if err := os.WriteFile(path, []byte(`{"default":"allow","workspaces":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithEntitlementsFile(path); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"get_network_policy", "get_entitlements", "set_entitlements", "set_network_policy"} {
		resp := toolCall(t, s, "persist-tok", tool, map[string]any{"_confirmed": true, "entitlements": map[string]any{}, "policy": map[string]any{}})
		if resp.Error == nil || resp.Error.Code != codeForbidden {
			t.Errorf("%s without admin: %+v", tool, resp.Error)
		}
	}
	set := map[string]any{"entitlements": map[string]any{"default": "deny", "workspaces": map[string]any{"desk": []any{"g"}}}}
	if resp := toolCall(t, s, "admin-tok", "set_entitlements", set); resp.Error == nil || resp.Error.Code != codeConfirmRequired {
		t.Fatalf("set_entitlements without confirmation: %+v", resp.Error)
	}
}

func TestSetEntitlementsPersistsAndApplies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ent.json")
	if err := os.WriteFile(path, []byte(`{"default":"allow","workspaces":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestServer()
	if _, err := s.WithEntitlementsFile(path); err != nil {
		t.Fatal(err)
	}
	var got struct{ Entitlements Entitlements }
	result(t, toolCall(t, s, "admin-tok", "get_entitlements", nil), &got)
	if got.Entitlements.Default != "allow" {
		t.Fatalf("initial explicit allow policy changed: %+v", got)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Invalid input is refused and changes nothing.
	bad := map[string]any{"_confirmed": true, "entitlements": map[string]any{"default": "open"}}
	if resp := toolCall(t, s, "admin-tok", "set_entitlements", bad); resp.Error != nil || !strings.Contains(string(resp.Result), "isError") {
		t.Fatalf("invalid entitlements: %+v %s", resp.Error, resp.Result)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("invalid entitlements changed the persisted file: before=%s after=%s", before, after)
	}

	set := map[string]any{"_confirmed": true, "entitlements": map[string]any{"default": "deny", "workspaces": map[string]any{"desk": []any{RolePersist}}}}
	result(t, toolCall(t, s, "admin-tok", "set_entitlements", set), &got)
	var ws []Workspace
	result(t, toolCall(t, s, "user-tok", "list_workspaces", nil), &ws)
	if len(ws) != 0 {
		t.Fatalf("deny default not applied: %+v", ws)
	}
	result(t, toolCall(t, s, "persist-tok", "list_workspaces", nil), &ws)
	if len(ws) != 1 || ws[0].Name != "desk" {
		t.Fatalf("desk entitlement not applied: %+v", ws)
	}

	// A restarted server reads what was saved.
	again := newTestServer()
	if _, err := again.WithEntitlementsFile(path); err != nil {
		t.Fatal(err)
	}
	if again.entitlements().Default != "deny" {
		t.Fatalf("not persisted: %+v", again.entitlements())
	}
}

func TestUIAdmin(t *testing.T) {
	f := newUIFixture(t)
	path := filepath.Join(t.TempDir(), "ent.json")
	if err := os.WriteFile(path, []byte(`{"default":"allow","workspaces":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ui.srv.WithEntitlementsFile(path); err != nil {
		t.Fatal(err)
	}
	ent := `{"default":"allow","workspaces":{"desk":["desk-users"]}}`
	if code, _, _ := f.do(t, http.MethodGet, "/ui/api/admin/entitlements", "kc-alice", "", "", nil); code != http.StatusForbidden {
		t.Fatalf("non-admin read: %d", code)
	}
	if code, _, _ := f.do(t, http.MethodPut, "/ui/api/admin/entitlements", "kc-alice", "", ent, nil); code != http.StatusForbidden {
		t.Fatalf("non-admin write: %d", code)
	}
	// An admin needs no Open WebUI link for admin pages.
	if code, body, _ := f.do(t, http.MethodPut, "/ui/api/admin/entitlements", "kc-root", RoleAdmin, ent, nil); code != http.StatusOK {
		t.Fatalf("admin write: %d %s", code, body)
	}
	if code, _, _ := f.do(t, http.MethodPut, "/ui/api/admin/entitlements", "kc-root", RoleAdmin, ent, map[string]string{"Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Fatalf("cross-site admin write: %d", code)
	}
	if code, _, _ := f.do(t, http.MethodPut, "/ui/api/admin/entitlements", "kc-root", RoleAdmin, `{"default":"maybe"}`, nil); code != http.StatusBadRequest {
		t.Fatalf("invalid entitlements: %d", code)
	}
	if _, body, _ := f.do(t, http.MethodGet, "/ui/api/workspaces", "kc-alice", "", "", nil); strings.Contains(body, `"desk"`) {
		t.Fatalf("saved entitlements not applied: %s", body)
	}
	pol := `{"dns_entries":[{"name":"db.internal","ip":"10.0.0.5"}],"egress_allow":[]}`
	if code, body, _ := f.do(t, http.MethodPut, "/ui/api/admin/netpolicy", "kc-root", RoleAdmin, pol, nil); code != http.StatusOK {
		t.Fatalf("netpolicy write: %d %s", code, body)
	}
	if _, body, _ := f.do(t, http.MethodGet, "/ui/api/admin/netpolicy", "kc-root", RoleAdmin, "", nil); !strings.Contains(body, "db.internal") {
		t.Fatalf("netpolicy read: %s", body)
	}
	if code, _, _ := f.do(t, http.MethodGet, "/ui/api/admin/other", "kc-root", RoleAdmin, "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown admin page: %d", code)
	}
}
