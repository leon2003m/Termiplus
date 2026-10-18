package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityStorePersists(t *testing.T) {
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
	if err := s.Record("sub-1", "owui-2"); err != nil { // newest pair wins
		t.Fatal(err)
	}
	if err := s.Record("bad id", "owui-1"); err == nil {
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
	if got, ok := again.Lookup("sub-1"); !ok || got != "owui-2" {
		t.Fatalf("after reload: %q %v", got, ok)
	}
}

func TestIdentityStoreRejectsBadFile(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"not json":   "nope",
		"bad entry":  `{"subjects":{"a b":"owui"}}`,
		"bad target": `{"subjects":{"sub":"../x"}}`,
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

// Plugin calls (verified token + plugin key) teach the server the pair.
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
	// A rejected call records nothing.
	req[owuiKeyHeader] = "wrong"
	req[owuiUserHeader] = "owui-evil"
	_ = callWithHeaders(t, s, req, "list_sessions")
	if got, _ := ids.Lookup("kc-sub"); got != "owui-7" {
		t.Fatalf("rejected call changed the mapping to %q", got)
	}
}
