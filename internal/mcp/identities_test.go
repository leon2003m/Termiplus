package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityStorePersistsAndIsImmutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities.json")
	s, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup("sub-1"); ok {
		t.Fatal("empty store knows sub-1")
	}
	if err := s.Record("sub-1", "owui-1"); err != nil {
		t.Fatal(err)
	}
	// Repeating the exact same pair is idempotent.
	if err := s.Record("sub-1", "owui-1"); err != nil {
		t.Fatal(err)
	}
	// A subject cannot be silently rebound to another Open WebUI account.
	if err := s.Record("sub-1", "owui-2"); err == nil {
		t.Fatal("accepted rebind of an existing OIDC subject")
	}
	// Nor can another subject claim the existing Open WebUI account.
	if err := s.Record("sub-2", "owui-1"); err == nil {
		t.Fatal("accepted a second OIDC subject for the same Open WebUI user")
	}
	if err := s.Record("bad id", "owui-3"); err == nil {
		t.Fatal("invalid subject accepted")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", st.Mode().Perm())
	}
	again, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := again.Lookup("sub-1"); !ok || got != "owui-1" {
		t.Fatalf("after reload: %q %v", got, ok)
	}
	if _, ok := again.Lookup("sub-2"); ok {
		t.Fatal("failed rebind unexpectedly persisted")
	}
}

func TestIdentityStoreRejectsBadFile(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"not json":       "nope",
		"bad entry":      `{"subjects":{"a b":"owui"}}`,
		"bad target":     `{"subjects":{"sub":"../x"}}`,
		"duplicate owner": `{"subjects":{"sub-1":"owui-1","sub-2":"owui-1"}}`,
	} {
		path := filepath.Join(dir, "f.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenIdentityStore(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Plugin calls (verified token + plugin key) may establish a mapping once.
func TestServerRecordsIdentityFromPlugin(t *testing.T) {
	ids, err := OpenIdentityStore(filepath.Join(t.TempDir(), "ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	const key = "owui-plugin-key-0123456789abcdef0123"
	s := NewServer(ProxyHeaderAuthenticator{OWUIKey: key}, NewStubBackend("https://gw.example"), "test").WithIdentities(ids)
	req := map[string]string{proxyUserHeader: "kc-sub", owuiKeyHeader: key, owuiUserHeader: "owui-7"}
	if resp := callWithHeaders(t, s, req, "list_sessions"); resp.Error != nil {
		t.Fatalf("plugin call: %+v", resp.Error)
	}
	if got, ok := ids.Lookup("kc-sub"); !ok || got != "owui-7" {
		t.Fatalf("not recorded: %q %v", got, ok)
	}

	// A valid token and the shared plugin key are not enough to reassign a
	// previously bound subject to a different Open WebUI id.
	req[owuiUserHeader] = "owui-evil"
	resp := callWithHeaders(t, s, req, "list_sessions")
	if resp.Error == nil {
		t.Fatal("rebind attempt should fail closed")
	}
	if got, _ := ids.Lookup("kc-sub"); got != "owui-7" {
		t.Fatalf("rebind attempt changed mapping to %q", got)
	}

	// A rejected call with an invalid plugin key cannot establish or change it.
	req[owuiKeyHeader] = "wrong"
	req[owuiUserHeader] = "owui-evil"
	_ = callWithHeaders(t, s, req, "list_sessions")
	if got, _ := ids.Lookup("kc-sub"); got != "owui-7" {
		t.Fatalf("rejected call changed mapping to %q", got)
	}
}


// callWithHeaders sends one MCP tools/call request with explicit proxy/plugin
// headers and returns the decoded JSON-RPC response.
func callWithHeaders(t *testing.T, s *Server, headers map[string]string, toolName string) rpcResp {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": toolName, "arguments": map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var resp rpcResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (%s)", err, w.Body.String())
	}
	return resp
}
