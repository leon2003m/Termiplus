package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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
	if err := s.Record("sub-1", "owui-2"); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("rebind of an existing OIDC subject: got %v, want ErrIdentityConflict", err)
	}
	// Nor can another subject claim the existing Open WebUI account.
	if err := s.Record("sub-2", "owui-1"); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("second OIDC subject for the same Open WebUI user: got %v, want ErrIdentityConflict", err)
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
		"not json":        "nope",
		"bad entry":       `{"subjects":{"a b":"owui"}}`,
		"bad target":      `{"subjects":{"sub":"../x"}}`,
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
	key := testPluginKey
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
	if resp.Error == nil || resp.Error.Code != codeForbidden {
		t.Fatalf("rebind attempt: got %+v, want forbidden", resp.Error)
	}
	if got, _ := ids.Lookup("kc-sub"); got != "owui-7" {
		t.Fatalf("rebind attempt changed mapping to %q", got)
	}

	// A rejected call with an invalid plugin key cannot establish or change it.
	req[owuiKeyHeader] = "wrong"
	req[owuiUserHeader] = "owui-evil"
	if resp := callWithHeaders(t, s, req, "list_sessions"); resp.Error == nil || resp.Error.Code != codeUnauthenticated {
		t.Fatalf("wrong plugin key: got %+v, want unauthenticated", resp.Error)
	}
	if got, _ := ids.Lookup("kc-sub"); got != "owui-7" {
		t.Fatalf("rejected call changed mapping to %q", got)
	}
}

const testPluginKey = "owui-plugin-key-0123456789abcdef0123"

// pluginCall sends one tools/call with arguments as the Open WebUI plugin does
// behind the OIDC proxy: verified subject, plugin key, Open WebUI user id.
func pluginCall(t *testing.T, s *Server, subject, owui, tool string, args map[string]any) rpcResp {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	req.Header.Set(proxyUserHeader, subject)
	req.Header.Set(owuiKeyHeader, testPluginKey)
	req.Header.Set(owuiUserHeader, owui)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var resp rpcResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (%s)", err, w.Body.String())
	}
	return resp
}

// A second subject holding a valid token and the plugin key cannot claim an
// Open WebUI user that is already bound, and the tool it asked for never runs.
func TestServerRejectsSecondSubjectBeforeToolRuns(t *testing.T) {
	ids, err := OpenIdentityStore(filepath.Join(t.TempDir(), "ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	b := NewStubBackend("https://gw.example")
	s := NewServer(ProxyHeaderAuthenticator{OWUIKey: testPluginKey}, b, "test").WithIdentities(ids)

	if resp := pluginCall(t, s, "kc-alice", "owui-alice", "list_sessions", map[string]any{}); resp.Error != nil {
		t.Fatalf("enrollment: %+v", resp.Error)
	}
	resp := pluginCall(t, s, "kc-mallory", "owui-alice", "request_session", map[string]any{"workspace": "default"})
	if resp.Error == nil || resp.Error.Code != codeForbidden {
		t.Fatalf("claim of a bound user: got %+v, want forbidden", resp.Error)
	}
	sessions, err := b.ListSessions(context.Background(), Principal{UserID: "owui-alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("rejected call still created %d session(s) for owui-alice", len(sessions))
	}
	if _, ok := ids.Lookup("kc-mallory"); ok {
		t.Fatal("rejected subject was recorded")
	}
	// The bound pair keeps working.
	if resp := pluginCall(t, s, "kc-alice", "owui-alice", "request_session", map[string]any{"workspace": "default"}); resp.Error != nil {
		t.Fatalf("bound user: %+v", resp.Error)
	}
}

// If the binding cannot be stored, the call fails closed with an internal
// error (not a misleading conflict), no tool runs and nothing is remembered.
func TestIdentityStoreFailureFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "ids.json")
	ids, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ids.Record("sub-1", "owui-1"); err == nil || errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("store failure: got %v, want a non-conflict error", err)
	}
	if _, ok := ids.Lookup("sub-1"); ok {
		t.Fatal("failed save left the binding in memory")
	}

	b := NewStubBackend("https://gw.example")
	s := NewServer(ProxyHeaderAuthenticator{OWUIKey: testPluginKey}, b, "test").WithIdentities(ids)
	resp := pluginCall(t, s, "kc-alice", "owui-alice", "request_session", map[string]any{"workspace": "default"})
	if resp.Error == nil || resp.Error.Code != codeInternal {
		t.Fatalf("store failure: got %+v, want internal error", resp.Error)
	}
	sessions, err := b.ListSessions(context.Background(), Principal{UserID: "owui-alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("tool ran despite the store failure: %d session(s)", len(sessions))
	}
}

// Concurrent first enrollments for one Open WebUI user: exactly one subject
// wins, in memory and on disk.
func TestIdentityStoreConcurrentClaims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ids.json")
	ids, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatal(err)
	}
	const n = 32
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = ids.Record(fmt.Sprintf("sub-%d", i), "owui-shared")
		}()
	}
	wg.Wait()
	winner := ""
	for i, err := range errs {
		switch {
		case err == nil:
			if winner != "" {
				t.Fatalf("both %s and sub-%d were bound to owui-shared", winner, i)
			}
			winner = fmt.Sprintf("sub-%d", i)
		case !errors.Is(err, ErrIdentityConflict):
			t.Fatalf("sub-%d: unexpected error %v", i, err)
		}
	}
	if winner == "" {
		t.Fatal("no subject was bound")
	}
	again, err := OpenIdentityStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		sub := fmt.Sprintf("sub-%d", i)
		if _, ok := again.Lookup(sub); ok != (sub == winner) {
			t.Fatalf("on disk: %s bound=%v, winner %s", sub, ok, winner)
		}
	}
}
