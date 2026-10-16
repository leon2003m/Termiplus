package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newAuditedServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.log")
	a, err := OpenAuditLog(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	return newTestServer().WithAuditLog(a), path
}

func auditEntries(t *testing.T, s *Server, args map[string]any) []AuditEntry {
	t.Helper()
	var out struct{ Entries []AuditEntry }
	result(t, toolCall(t, s, "admin-tok", "get_audit_log", args), &out)
	return out.Entries
}

func TestAuditRecordsChangesNotReads(t *testing.T) {
	s, path := newAuditedServer(t)
	toolCall(t, s, "user-tok", "list_workspaces", nil)
	toolCall(t, s, "user-tok", "list_sessions", nil)
	// waiting for confirmation: nothing happened yet
	toolCall(t, s, "user-tok", "destroy_session", map[string]any{"session_id": "x"})
	var sess struct {
		SessionID string `json:"session_id"`
	}
	result(t, toolCall(t, s, "user-tok", "request_session", map[string]any{"workspace": "default"}), &sess)
	toolCall(t, s, "user-tok", "destroy_session", map[string]any{"session_id": "nope", "_confirmed": true})
	// refused: alice is not an admin
	toolCall(t, s, "user-tok", "set_entitlements", map[string]any{"entitlements": map[string]any{}, "_confirmed": true})
	toolCall(t, s, "admin-tok", "get_audit_log", nil) // reads are not recorded

	got := auditEntries(t, s, nil)
	want := []string{
		"set_entitlements denied alice",
		"destroy_session error alice",
		"request_session ok alice",
	}
	if len(got) != len(want) {
		t.Fatalf("want %d entries, got %+v", len(want), got)
	}
	for i, e := range got {
		if s := e.Tool + " " + e.Outcome + " " + e.Actor; s != want[i] {
			t.Errorf("entry %d: %q, want %q", i, s, want[i])
		}
		if e.Time.IsZero() || time.Since(e.Time) > time.Minute {
			t.Errorf("entry %d: bad time %v", i, e.Time)
		}
		if _, ok := e.Args["_confirmed"]; ok {
			t.Errorf("entry %d: control arguments must not be recorded: %v", i, e.Args)
		}
	}
	if got[0].Error == "" || got[2].Args["workspace"] != "default" {
		t.Errorf("details missing: %+v", got)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("audit file must be private: %v %v", st, err)
	}
}

func TestAuditManageUserShowIsNotRecorded(t *testing.T) {
	s, _ := newAuditedServer(t)
	s.dir = nil
	toolCall(t, s, "admin-tok", "manage_user", map[string]any{"action": "show", "username": "dave"})
	toolCall(t, s, "admin-tok", "manage_user", map[string]any{"action": "add_to_group", "username": "dave", "group": "termiplus-users", "_confirmed": true})
	got := auditEntries(t, s, nil)
	if len(got) != 1 || got[0].Args["action"] != "add_to_group" || got[0].Outcome != "error" {
		t.Fatalf("got %+v", got)
	}
}

func TestAuditLogIsAdminOnlyAndFilters(t *testing.T) {
	s, _ := newAuditedServer(t)
	for _, tok := range []string{"user-tok", "persist-tok"} {
		if resp := toolCall(t, s, tok, "get_audit_log", nil); resp.Error == nil || resp.Error.Code != codeForbidden {
			t.Fatalf("%s: want forbidden, got %+v", tok, resp.Error)
		}
	}
	for i := 0; i < 3; i++ {
		toolCall(t, s, "user-tok", "request_session", map[string]any{"workspace": "default"})
	}
	toolCall(t, s, "persist-tok", "request_session", map[string]any{"workspace": "default"})

	// The two denied get_audit_log calls are not recorded (read-only tool).
	if got := auditEntries(t, s, nil); len(got) != 4 || got[0].Actor != "bob" {
		t.Fatalf("all: %+v", got)
	}
	if got := auditEntries(t, s, map[string]any{"actor": "alice", "limit": 2}); len(got) != 2 || got[0].Actor != "alice" {
		t.Fatalf("actor+limit: %+v", got)
	}
	if got := auditEntries(t, s, map[string]any{"tool": "set_network_policy"}); len(got) != 0 {
		t.Fatalf("tool filter: %+v", got)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if got := auditEntries(t, s, map[string]any{"since": future}); len(got) != 0 {
		t.Fatalf("since: %+v", got)
	}
	for _, bad := range []map[string]any{{"limit": 0}, {"limit": 501}, {"since": "yesterday"}} {
		if text, isErr := toolText(t, toolCall(t, s, "admin-tok", "get_audit_log", bad)); !isErr {
			t.Errorf("%v accepted: %s", bad, text)
		}
	}
}

func TestAuditWithoutFile(t *testing.T) {
	s := newTestServer()
	toolCall(t, s, "user-tok", "request_session", map[string]any{"workspace": "default"})
	text, isErr := toolText(t, toolCall(t, s, "admin-tok", "get_audit_log", nil))
	if !isErr || !strings.Contains(text, "not configured") {
		t.Fatalf("got %q", text)
	}
}

func TestAuditRotatesAndReadsAcrossFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	a, err := OpenAuditLog(path, 600)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if err := a.Append(AuditEntry{Time: time.Now(), Actor: "u" + string(rune('a'+i)), Tool: "request_session", Outcome: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{path, path + ".1"} {
		if st, err := os.Stat(p); err != nil || st.Size() > 600 {
			t.Fatalf("%s: %v %v", p, st, err)
		}
	}
	got, err := a.Read(AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 4 || got[0].Actor != "ul" {
		t.Fatalf("newest first across both files: %+v", got)
	}
	// A torn line (crash mid-write) does not break reading.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	_, _ = f.WriteString(`{"time":"2026-`)
	f.Close()
	if _, err := a.Read(AuditFilter{}); err != nil {
		t.Fatal(err)
	}
}

func TestAuditArgsAreBounded(t *testing.T) {
	big := map[string]any{"policy": strings.Repeat("x", 10_000), "_confirmed": true}
	got := auditArgs(big)
	if s, _ := got["truncated"].(string); len(s) != auditMaxArgs {
		t.Fatalf("got %d bytes", len(s))
	}
	if auditArgs(map[string]any{"_confirmed": true}) != nil {
		t.Fatal("only control args: nothing to record")
	}
}
