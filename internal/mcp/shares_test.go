package mcp

import (
	"context"
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

// Revoking a share ends the connections viewers have open through it, and a
// relay started after the revoke never runs (security audit run 1).
func TestShareRevokeCancelsOpenRelays(t *testing.T) {
	st, err := OpenShareStore("")
	if err != nil {
		t.Fatal(err)
	}
	sh, _, err := st.Create("alice", "ot-u-x", "desk", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx1, done1 := st.relayContext(context.Background(), sh)
	defer done1()
	ctx2, done2 := st.relayContext(context.Background(), sh)
	done2() // a finished request is forgotten
	if ctx1.Err() != nil || ctx2.Err() == nil {
		t.Fatal("relay contexts before the revoke")
	}
	if err := st.Revoke("alice", sh.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx1.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("open relay survived the revoke")
	}
	ctx3, done3 := st.relayContext(context.Background(), sh)
	defer done3()
	if ctx3.Err() == nil {
		t.Fatal("relay through a revoked share")
	}
	st.mu.Lock()
	n := len(st.relays)
	st.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d relay sets left", n)
	}
}

// A relay through a share ends when the share expires.
func TestShareRelayEndsAtExpiry(t *testing.T) {
	st, _ := OpenShareStore("")
	sh, _, err := st.Create("alice", "ot-u-x", "desk", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	sh.Expires = time.Now().Add(100 * time.Millisecond)
	ctx, done := st.relayContext(context.Background(), sh)
	defer done()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("relay outlived the share")
	}
}
