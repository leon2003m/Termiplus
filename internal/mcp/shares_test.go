package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShareStoreExpiryLimitsAndHashing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shares.json")
	st, err := OpenShareStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	sh, token, err := st.Create("alice", "ot-u-x", "desk", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 || sh.Hash != hashToken(token) || sh.ID != sh.Hash[:12] {
		t.Fatalf("share %+v token %q", sh, token)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), token) || !strings.Contains(string(raw), sh.Hash) {
		t.Fatal("the file must hold the hash, never the token")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", st.Mode())
	}
	if got, ok := st.Lookup(token); !ok || got.Owner != "alice" {
		t.Fatal("lookup failed")
	}
	for _, bad := range []string{"", token[:63], token + "0", strings.ToUpper(token), sh.Hash} {
		if _, ok := st.Lookup(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	// Reloaded from disk.
	st2, err := OpenShareStore(path)
	if err != nil {
		t.Fatal(err)
	}
	st2.now = st.now
	if _, ok := st2.Lookup(token); !ok {
		t.Fatal("not persisted")
	}
	// Expiry.
	now = now.Add(time.Hour)
	if _, ok := st.Lookup(token); ok {
		t.Fatal("expired share still valid")
	}
	if len(st.List("alice")) != 0 {
		t.Fatal("expired share listed")
	}
	// Per-owner limit counts active shares only.
	for i := 0; i < shareMaxActive; i++ {
		if _, _, err := st.Create("alice", "ot-u-x", "desk", time.Hour, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.Create("alice", "ot-u-x", "desk", time.Hour, "", ""); err == nil {
		t.Fatal("limit not enforced")
	}
	if _, _, err := st.Create("bob", "ot-u-y", "desk", time.Hour, "", ""); err != nil {
		t.Fatalf("limit is per owner: %v", err)
	}
	// Revoke only one's own.
	id := st.List("alice")[0].ID
	if err := st.Revoke("bob", id); err == nil {
		t.Fatal("bob revoked alice's share")
	}
	if err := st.Revoke("alice", id); err != nil {
		t.Fatal(err)
	}
}
