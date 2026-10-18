package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestProxyHeaderAuthenticator(t *testing.T) {
	cases := []struct {
		name      string
		users     []string
		groups    []string
		wantErr   bool
		wantUser  string
		wantRoles []string
	}{
		{name: "no identity", wantErr: true},
		{name: "empty user", users: []string{""}, wantErr: true},
		{name: "two user headers", users: []string{"alice", "mallory"}, wantErr: true},
		{name: "invalid user id", users: []string{"alice bob"}, wantErr: true},
		{name: "user without groups", users: []string{"d165efb0-00c3-4578-b636-2e4192c3b269"},
			wantUser: "d165efb0-00c3-4578-b636-2e4192c3b269"},
		{name: "comma-separated groups", users: []string{"alice"}, groups: []string{"termiplus-admin,termiplus-persist"},
			wantUser: "alice", wantRoles: []string{RoleAdmin, RolePersist}},
		{name: "repeated and padded groups", users: []string{"alice"}, groups: []string{" termiplus-persist ", "termiplus-persist,,"},
			wantUser: "alice", wantRoles: []string{RolePersist}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			for _, u := range c.users {
				r.Header.Add(proxyUserHeader, u)
			}
			for _, g := range c.groups {
				r.Header.Add(proxyGroupsHeader, g)
			}
			p, err := ProxyHeaderAuthenticator{}.Authenticate(r)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", p)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.UserID != c.wantUser || !reflect.DeepEqual(p.Roles, c.wantRoles) {
				t.Fatalf("got %+v, want user %q roles %v", p, c.wantUser, c.wantRoles)
			}
		})
	}
}

// A bearer token alone is meaningless to the proxy authenticator: identity
// comes only from the proxy's headers.
func TestProxyAuthIgnoresBearer(t *testing.T) {
	s := NewServer(ProxyHeaderAuthenticator{}, NewStubBackend("https://gw.example"), "test")
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "list_sessions"}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer anything")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var resp rpcResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != codeUnauthenticated {
		t.Fatalf("want unauthenticated, got %s", w.Body.String())
	}
}

func TestProxyHeaderAuthenticatorOWUIUser(t *testing.T) {
	const key = "owui-plugin-key-0123456789abcdef0123"
	a := ProxyHeaderAuthenticator{OWUIKey: key}
	req := func(h map[string][]string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set(proxyUserHeader, "kc-sub-1")
		r.Header.Set(proxyGroupsHeader, RolePersist)
		for k, vs := range h {
			for _, v := range vs {
				r.Header.Add(k, v)
			}
		}
		return r
	}
	for name, h := range map[string]map[string][]string{
		"no plugin headers": {},
		"user without key":  {owuiUserHeader: {"owui-1"}},
		"key without user":  {owuiKeyHeader: {key}},
		"wrong key":         {owuiKeyHeader: {key + "x"}, owuiUserHeader: {"owui-1"}},
		"two keys":          {owuiKeyHeader: {"x", key}, owuiUserHeader: {"owui-1"}},
		"two users":         {owuiKeyHeader: {key}, owuiUserHeader: {"owui-1", "owui-2"}},
		"invalid user":      {owuiKeyHeader: {key}, owuiUserHeader: {"../x"}},
	} {
		if p, err := a.Authenticate(req(h)); err == nil {
			t.Errorf("%s: accepted as %+v", name, p)
		}
	}

	p, err := a.Authenticate(req(map[string][]string{owuiKeyHeader: {key}, owuiUserHeader: {"owui-1"}}))
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "owui-1" || !reflect.DeepEqual(p.Roles, []string{RolePersist}) {
		t.Fatalf("got %+v, want owui-1 with the token's groups", p)
	}

	// Without a verified token (no proxy identity) the plugin headers are useless.
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(owuiKeyHeader, key)
	r.Header.Set(owuiUserHeader, "owui-1")
	if _, err := a.Authenticate(r); err == nil {
		t.Fatal("plugin headers without proxy identity were accepted")
	}
}
