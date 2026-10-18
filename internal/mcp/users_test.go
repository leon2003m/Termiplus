package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeKeycloak serves the token endpoint and the admin API calls the
// KeycloakDirectory makes, and rejects admin calls without the issued token.
type fakeKeycloak struct {
	mu      sync.Mutex
	groups  []DirGroup
	users   []DirUser
	member  map[string]map[string]bool // group id -> user id
	tokens  int
	writes  []string
	badCred bool
	// listFails makes the full user listing fail.
	listFails bool
}

func newFakeKeycloak() *fakeKeycloak {
	return &fakeKeycloak{
		groups: []DirGroup{
			{ID: "g-users", Name: "termiplus-users", Path: "/termiplus-users"},
			{ID: "g-persist", Name: "termiplus-persist", Path: "/termiplus-persist"},
			{ID: "g-admin", Name: "termiplus-admin", Path: "/termiplus-admin"},
			{ID: "g-realm", Name: "realm-admins", Path: "/realm-admins"},
		},
		users: []DirUser{
			{ID: "u-carol", Username: "carol", Enabled: true},
			{ID: "u-dave", Username: "dave", Email: "dave@example.com", Enabled: true},
			{ID: "u-erin", Username: "erin", Enabled: false},
		},
		member: map[string]map[string]bool{"g-admin": {"u-carol": true}, "g-users": {}, "g-persist": {}, "g-realm": {}},
	}
}

const fakeKCToken = "kc-service-token"

func (f *fakeKeycloak) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/realms/termiplus/protocol/openid-connect/token" {
		id, secret, ok := r.BasicAuth()
		_ = r.ParseForm()
		if !ok || id != "termiplus-mcp-admin" || secret != "kc-secret" || r.PostForm.Get("grant_type") != "client_credentials" || f.badCred {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.tokens++
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fakeKCToken, "expires_in": 300})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeKCToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/admin/realms/termiplus/")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	parts := strings.Split(rest, "/")
	switch {
	case r.Method == http.MethodGet && rest == "groups":
		_ = json.NewEncoder(w).Encode(f.groups)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "groups" && parts[2] == "members":
		out := []DirUser{}
		for _, u := range f.users {
			if f.member[parts[1]][u.ID] {
				out = append(out, u)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodGet && rest == "users" && r.URL.Query().Get("exact") == "":
		// full listing, paged by first/max
		first, _ := strconv.Atoi(r.URL.Query().Get("first"))
		max, _ := strconv.Atoi(r.URL.Query().Get("max"))
		if f.listFails {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		out := []DirUser{}
		for i := first; i < len(f.users) && i < first+max; i++ {
			out = append(out, f.users[i])
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodGet && rest == "users":
		if r.URL.Query().Get("exact") != "true" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		out := []DirUser{}
		for _, u := range f.users {
			if strings.EqualFold(u.Username, r.URL.Query().Get("username")) {
				out = append(out, u)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "users" && parts[2] == "groups":
		out := []DirGroup{}
		for _, g := range f.groups {
			if f.member[g.ID][parts[1]] {
				out = append(out, g)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case (r.Method == http.MethodPut || r.Method == http.MethodDelete) && len(parts) == 4 && parts[0] == "users" && parts[2] == "groups":
		f.writes = append(f.writes, r.Method+" "+parts[1]+" "+parts[3])
		if r.Method == http.MethodPut {
			f.member[parts[3]][parts[1]] = true
		} else {
			delete(f.member[parts[3]], parts[1])
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newUserAdminServer(t *testing.T) (*Server, *fakeKeycloak) {
	t.Helper()
	kc := newFakeKeycloak()
	srv := httptest.NewServer(kc)
	t.Cleanup(srv.Close)
	dir, err := NewKeycloakDirectory(srv.URL, "termiplus", "termiplus-mcp-admin", "kc-secret")
	if err != nil {
		t.Fatal(err)
	}
	return newTestServer().WithDirectory(dir, ""), kc
}

// toolText returns a tool call's text, failing on rpc errors.
func toolText(t *testing.T, resp rpcResp) (string, bool) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	var tr toolResult
	if err := json.Unmarshal(resp.Result, &tr); err != nil {
		t.Fatal(err)
	}
	return tr.Content[0].Text, tr.IsError
}

func TestManageToolsAdminOnly(t *testing.T) {
	s, kc := newUserAdminServer(t)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"manage_group", map[string]any{"action": "list"}},
		{"manage_user", map[string]any{"action": "show", "username": "dave"}},
		{"manage_user", map[string]any{"action": "add_to_group", "username": "dave", "group": "termiplus-admin", "_confirmed": true}},
	} {
		for _, tok := range []string{"user-tok", "persist-tok"} {
			if resp := toolCall(t, s, tok, c.tool, c.args); resp.Error == nil || resp.Error.Code != codeForbidden {
				t.Errorf("%s %v as %s: want forbidden, got %+v", c.tool, c.args, tok, resp.Error)
			}
		}
	}
	if len(kc.writes) != 0 || kc.tokens != 0 {
		t.Fatalf("non-admins must not reach the directory: writes=%v tokens=%d", kc.writes, kc.tokens)
	}
}

func TestManageGroupListsOnlyPrefixedGroups(t *testing.T) {
	s, _ := newUserAdminServer(t)
	var out struct{ Groups []string }
	result(t, toolCall(t, s, "admin-tok", "manage_group", map[string]any{"action": "list"}), &out)
	if strings.Join(out.Groups, ",") != "termiplus-admin,termiplus-persist,termiplus-users" {
		t.Fatalf("groups: %v", out.Groups)
	}
	text, isErr := toolText(t, toolCall(t, s, "admin-tok", "manage_group", map[string]any{"action": "members", "group": "realm-admins"}))
	if !isErr || !strings.Contains(text, "not managed") {
		t.Fatalf("foreign group must be refused: %s", text)
	}
	var m struct {
		Members []struct{ Username string }
	}
	result(t, toolCall(t, s, "admin-tok", "manage_group", map[string]any{"action": "members", "group": "termiplus-admin"}), &m)
	if len(m.Members) != 1 || m.Members[0].Username != "carol" {
		t.Fatalf("members: %+v", m.Members)
	}
}

func TestManageUserChangesNeedConfirmation(t *testing.T) {
	s, kc := newUserAdminServer(t)
	add := map[string]any{"action": "add_to_group", "username": "dave", "group": "termiplus-persist"}
	if resp := toolCall(t, s, "admin-tok", "manage_user", add); resp.Error == nil || resp.Error.Code != codeConfirmRequired {
		t.Fatalf("want confirmation required, got %+v", resp.Error)
	}
	if len(kc.writes) != 0 {
		t.Fatalf("nothing may change before confirmation: %v", kc.writes)
	}
	// show needs no confirmation
	var shown struct{ Groups []string }
	result(t, toolCall(t, s, "admin-tok", "manage_user", map[string]any{"action": "show", "username": "dave"}), &shown)
	if len(shown.Groups) != 0 {
		t.Fatalf("dave starts without groups: %v", shown.Groups)
	}

	add["_confirmed"] = true
	var res struct{ Changed bool }
	result(t, toolCall(t, s, "admin-tok", "manage_user", add), &res)
	if !res.Changed || !kc.member["g-persist"]["u-dave"] {
		t.Fatal("dave should now be in termiplus-persist")
	}
	result(t, toolCall(t, s, "admin-tok", "manage_user", add), &res)
	if res.Changed || len(kc.writes) != 1 {
		t.Fatalf("adding again must be a no-op: %v", kc.writes)
	}
	result(t, toolCall(t, s, "admin-tok", "manage_user", map[string]any{"action": "remove_from_group", "username": "DAVE", "group": "termiplus-persist", "_confirmed": true}), &res)
	if !res.Changed || kc.member["g-persist"]["u-dave"] {
		t.Fatal("dave should be out of termiplus-persist again")
	}
}

func TestManageUserRefusals(t *testing.T) {
	s, kc := newUserAdminServer(t)
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "add_to_group", "username": "dave", "group": "realm-admins"}, "not managed"},
		{map[string]any{"action": "add_to_group", "username": "dave", "group": "termiplus-nope"}, "no group"},
		{map[string]any{"action": "add_to_group", "username": "nobody", "group": "termiplus-users"}, "no user"},
		{map[string]any{"action": "add_to_group", "username": "a/../b", "group": "termiplus-users"}, "invalid username"},
		// carol is the only enabled admin; erin (disabled) does not count
		{map[string]any{"action": "remove_from_group", "username": "carol", "group": "termiplus-admin"}, "last enabled member"},
	}
	kc.member["g-admin"]["u-erin"] = true
	for _, c := range cases {
		c.args["_confirmed"] = true
		text, isErr := toolText(t, toolCall(t, s, "admin-tok", "manage_user", c.args))
		if !isErr || !strings.Contains(text, c.want) {
			t.Errorf("%v: want error containing %q, got %q", c.args, c.want, text)
		}
	}
	if resp := toolCall(t, s, "admin-tok", "manage_user", map[string]any{"action": "delete", "username": "dave"}); resp.Error == nil {
		t.Error("unknown action must be refused")
	}
	if len(kc.writes) != 0 {
		t.Fatalf("refused calls must not write: %v", kc.writes)
	}
	// With a second enabled admin, carol can step down.
	kc.member["g-admin"]["u-dave"] = true
	var res struct{ Changed bool }
	result(t, toolCall(t, s, "admin-tok", "manage_user", map[string]any{"action": "remove_from_group", "username": "carol", "group": "termiplus-admin", "_confirmed": true}), &res)
	if !res.Changed {
		t.Fatal("removal with another admin left should work")
	}
}

func TestManageToolsWithoutDirectory(t *testing.T) {
	s := newTestServer()
	text, isErr := toolText(t, toolCall(t, s, "admin-tok", "manage_group", map[string]any{"action": "list"}))
	if !isErr || !strings.Contains(text, "not configured") {
		t.Fatalf("got %q", text)
	}
}

func TestKeycloakTokenIsCachedAndErrorsSurface(t *testing.T) {
	s, kc := newUserAdminServer(t)
	for i := 0; i < 3; i++ {
		toolCall(t, s, "admin-tok", "manage_group", map[string]any{"action": "list"})
	}
	if kc.tokens != 1 {
		t.Fatalf("service token should be cached, fetched %d times", kc.tokens)
	}
	s2, kc2 := newUserAdminServer(t)
	kc2.badCred = true
	text, isErr := toolText(t, toolCall(t, s2, "admin-tok", "manage_group", map[string]any{"action": "list"}))
	if !isErr || !strings.Contains(text, "keycloak token: status 401") || strings.Contains(text, "kc-secret") {
		t.Fatalf("got %q", text)
	}
}

func TestNewKeycloakDirectoryValidates(t *testing.T) {
	for _, base := range []string{"", "keycloak:8080", "file:///etc", "http://"} {
		if _, err := NewKeycloakDirectory(base, "termiplus", "c", "s"); err == nil {
			t.Errorf("%q accepted", base)
		}
	}
	if _, err := NewKeycloakDirectory("http://keycloak:8080", "termiplus", "c", ""); err == nil {
		t.Error("empty secret accepted")
	}
}
