package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func callWithHeaders(t *testing.T, s *Server, hdr map[string]string, tool string) rpcResp {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": map[string]any{}}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var resp rpcResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

type uiFixture struct {
	ui      *UI
	backend *StubBackend
}

func newUIFixture(t *testing.T) uiFixture {
	ids, err := OpenIdentityStore(filepath.Join(t.TempDir(), "ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = ids.Record("kc-alice", "owui-alice")
	_ = ids.Record("kc-bob", "owui-bob")
	b := NewStubBackend("https://gw.example")
	srv := NewServer(ProxyHeaderAuthenticator{}, b, "test").WithIdentities(ids)
	return uiFixture{ui: NewUI(srv, ids), backend: b}
}

// do sends a browser-like request as subject sub (empty: not signed in).
func (f uiFixture) do(t *testing.T, method, path, sub, groups, body string, hdr map[string]string) (int, string, http.Header) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://desk.example"+path, r)
	if sub != "" {
		req.Header.Set(proxyUserHeader, sub)
	}
	if groups != "" {
		req.Header.Set(proxyGroupsHeader, groups)
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", "http://desk.example")
		req.Header.Set(uiCSRFHeader, "1")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	f.ui.ServeHTTP(w, req)
	return w.Code, w.Body.String(), w.Header()
}

func TestUIStaticAndHeaders(t *testing.T) {
	f := newUIFixture(t)
	code, body, h := f.do(t, http.MethodGet, "/ui/", "", "", "", nil)
	if code != http.StatusOK || !strings.Contains(body, "app.js") {
		t.Fatalf("index: %d %s", code, body)
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP: %q", csp)
	}
	for _, f2 := range []string{"/ui/app.js", "/ui/desktop.js"} {
		if code, _, _ := f.do(t, http.MethodGet, f2, "", "", "", nil); code != http.StatusOK {
			t.Fatalf("%s: %d", f2, code)
		}
	}
	if code, _, _ := f.do(t, http.MethodGet, "/ui/app.js", "", "", "", nil); code != http.StatusOK {
		t.Fatalf("app.js: %d", code)
	}
	if code, _, _ := f.do(t, http.MethodGet, "/other", "", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("outside /ui/: %d", code)
	}
}

func TestUIIdentity(t *testing.T) {
	f := newUIFixture(t)
	if code, _, _ := f.do(t, http.MethodGet, "/ui/api/sessions", "", "", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("not signed in: %d", code)
	}
	code, body, _ := f.do(t, http.MethodGet, "/ui/api/me", "kc-carol", "", "", nil)
	if code != http.StatusOK || !strings.Contains(body, `"linked":false`) {
		t.Fatalf("unlinked me: %d %s", code, body)
	}
	if code, _, _ := f.do(t, http.MethodGet, "/ui/api/sessions", "kc-carol", "", "", nil); code != http.StatusConflict {
		t.Fatalf("unlinked sessions: %d", code)
	}

	// Alice starts a session; Bob does not see it, and cannot remove it.
	code, body, _ = f.do(t, http.MethodPost, "/ui/api/sessions", "kc-alice", "", `{"workspace":"default"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("start: %d %s", code, body)
	}
	var s Session
	_ = json.Unmarshal([]byte(body), &s)
	if _, body, _ := f.do(t, http.MethodGet, "/ui/api/sessions", "kc-bob", "", "", nil); strings.Contains(body, s.ID) {
		t.Fatalf("bob sees alice's session: %s", body)
	}
	if code, _, _ := f.do(t, http.MethodDelete, "/ui/api/sessions/"+s.ID, "kc-bob", "", "", nil); code == http.StatusOK {
		t.Fatal("bob removed alice's session")
	}
	if _, body, _ := f.do(t, http.MethodGet, "/ui/api/sessions", "kc-alice", "", "", nil); !strings.Contains(body, s.ID) {
		t.Fatalf("alice lost her session: %s", body)
	}
	if code, body, _ := f.do(t, http.MethodDelete, "/ui/api/sessions/"+s.ID, "kc-alice", "", "", nil); code != http.StatusOK {
		t.Fatalf("alice remove: %d %s", code, body)
	}
}

func TestUIRBACAndEntitlements(t *testing.T) {
	f := newUIFixture(t)
	persist := `{"workspace":"desk","persistent":true}`
	if code, _, _ := f.do(t, http.MethodPost, "/ui/api/sessions", "kc-alice", "", persist, nil); code != http.StatusForbidden {
		t.Fatalf("persistent without role: %d", code)
	}
	if code, body, _ := f.do(t, http.MethodPost, "/ui/api/sessions", "kc-alice", RolePersist, persist, nil); code != http.StatusOK {
		t.Fatalf("persistent with role: %d %s", code, body)
	}
	ent, _ := ParseEntitlements([]byte(`{"default":"allow","workspaces":{"desk":["desk-users"]}}`))
	f.ui.srv.WithEntitlements(ent)
	if _, body, _ := f.do(t, http.MethodGet, "/ui/api/workspaces", "kc-alice", RolePersist, "", nil); strings.Contains(body, `"desk"`) {
		t.Fatalf("not entitled but listed: %s", body)
	}
	if code, _, _ := f.do(t, http.MethodPost, "/ui/api/sessions", "kc-alice", RolePersist, persist, nil); code != http.StatusForbidden {
		t.Fatalf("not entitled: %d", code)
	}
}

func TestUIRefusesCrossSiteWrites(t *testing.T) {
	f := newUIFixture(t)
	body := `{"workspace":"default"}`
	for name, hdr := range map[string]map[string]string{
		"no CSRF header":   {uiCSRFHeader: ""},
		"wrong CSRF value": {uiCSRFHeader: "yes"},
		"no Origin":        {"Origin": ""},
		"foreign Origin":   {"Origin": "https://evil.example"},
		"null Origin":      {"Origin": "null"},
	} {
		if code, _, _ := f.do(t, http.MethodPost, "/ui/api/sessions", "kc-alice", "", body, hdr); code != http.StatusForbidden {
			t.Errorf("%s: want 403, got %d", name, code)
		}
	}
	if _, list, _ := f.do(t, http.MethodGet, "/ui/api/sessions", "kc-alice", "", "", nil); list != "[]" {
		t.Fatalf("a refused request created a session: %s", list)
	}
	if code, _, _ := f.do(t, http.MethodPost, "/ui/api/sessions", "kc-alice", "", `{"workspace":"default","_confirmed":true}`, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d", code)
	}
}

func TestUIAuditLog(t *testing.T) {
	f := newUIFixture(t)
	a, err := OpenAuditLog(filepath.Join(t.TempDir(), "audit.log"), 0)
	if err != nil {
		t.Fatal(err)
	}
	f.ui.srv.WithAuditLog(a)
	if code, body, _ := f.do(t, http.MethodPost, "/ui/api/sessions", "kc-alice", "", `{"workspace":"default"}`, nil); code != http.StatusOK {
		t.Fatalf("start: %d %s", code, body)
	}
	if code, _, _ := f.do(t, http.MethodGet, "/ui/api/admin/audit", "kc-alice", "", "", nil); code != http.StatusForbidden {
		t.Fatalf("non-admin read the audit log: %d", code)
	}
	code, body, _ := f.do(t, http.MethodGet, "/ui/api/admin/audit", "kc-bob", RoleAdmin, "", nil)
	if code != http.StatusOK || !strings.Contains(body, `"actor":"owui-alice"`) || !strings.Contains(body, `"tool":"request_session"`) {
		t.Fatalf("admin read: %d %s", code, body)
	}
	if code, _, _ := f.do(t, http.MethodPut, "/ui/api/admin/audit", "kc-bob", RoleAdmin, `{}`, nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("the audit log must not be writable: %d", code)
	}
}
