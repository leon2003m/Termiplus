package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseEntitlementsRejectsUnsafeConfig(t *testing.T) {
	for name, in := range map[string]string{
		"no default":      `{"workspaces":{"desk":["g"]}}`,
		"unknown default": `{"default":"open","workspaces":{}}`,
		"unknown field":   `{"default":"deny","workspace":{"desk":["g"]}}`,
		"empty groups":    `{"default":"deny","workspaces":{"desk":[]}}`,
		"empty group":     `{"default":"deny","workspaces":{"desk":[""]}}`,
		"padded group":    `{"default":"deny","workspaces":{"desk":[" g"]}}`,
		"empty workspace": `{"default":"deny","workspaces":{"":["g"]}}`,
		"trailing data":   `{"default":"deny"} {}`,
		"not json":        `default=deny`,
	} {
		if _, err := ParseEntitlements([]byte(in)); err == nil {
			t.Errorf("%s: accepted %s", name, in)
		}
	}
}

func TestEntitlementsAllowed(t *testing.T) {
	deny, err := ParseEntitlements([]byte(`{"default":"deny","workspaces":{"desk":["team-a","team-b"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	allow, err := ParseEntitlements([]byte(`{"default":"allow","workspaces":{"gpu":["gpu-users"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	none := Principal{UserID: "u"}
	teamB := Principal{UserID: "u", Roles: []string{"team-b"}}
	admin := Principal{UserID: "u", Roles: []string{RoleAdmin}}
	for _, c := range []struct {
		e    *Entitlements
		p    Principal
		ws   string
		want bool
	}{
		{nil, none, "anything", true},
		{deny, none, "desk", false},
		{deny, teamB, "desk", true},
		{deny, teamB, "default", false}, // unlisted follows default deny
		{deny, admin, "desk", false},    // admin is not an implicit entitlement
		{allow, none, "default", true},
		{allow, none, "gpu", false},
		{allow, Principal{Roles: []string{"gpu-users"}}, "gpu", true},
	} {
		if got := c.e.Allowed(c.p, c.ws); got != c.want {
			t.Errorf("Allowed(%v, %q) = %v, want %v", c.p.Roles, c.ws, got, c.want)
		}
	}
}

func TestServerEnforcesEntitlements(t *testing.T) {
	ent, err := ParseEntitlements([]byte(`{"default":"allow","workspaces":{"desk":["termiplus-persist"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer().WithEntitlements(ent)

	names := func(token string) []string {
		var ws []Workspace
		result(t, toolCall(t, s, token, "list_workspaces", nil), &ws)
		var out []string
		for _, w := range ws {
			out = append(out, w.Name)
		}
		return out
	}
	if got, _ := json.Marshal(names("user-tok")); string(got) != `["default"]` {
		t.Errorf("alice sees %s, want only default", got)
	}
	if got, _ := json.Marshal(names("persist-tok")); string(got) != `["default","desk"]` {
		t.Errorf("bob sees %s, want default and desk", got)
	}

	// Not entitled: forbidden before any confirmation is asked for, even when
	// the caller claims to have confirmed.
	resp := toolCall(t, s, "user-tok", "request_session", map[string]any{"workspace": "desk", "persistent": true, "_confirmed": true})
	if resp.Error == nil || resp.Error.Code != codeForbidden {
		t.Fatalf("alice got %+v, want forbidden", resp.Error)
	}
	resp = toolCall(t, s, "persist-tok", "request_session", map[string]any{"workspace": "desk", "persistent": true})
	if resp.Error == nil || resp.Error.Code != codeConfirmRequired {
		t.Fatalf("bob got %+v, want confirmation required", resp.Error)
	}
}

func TestEntitlementsFileFailsClosedAndRejectsUnknownWorkspace(t *testing.T) {
	s := NewServer(NewStaticAuthenticator(nil), NewStubBackend("https://gw.example"), "test")
	if _, err := s.WithEntitlementsFile(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing entitlement file was accepted")
	}
	path := filepath.Join(t.TempDir(), "entitlements.json")
	if err := os.WriteFile(path, []byte(`{"default":"deny","workspaces":{"deskk":["team-a"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithEntitlementsFile(path); err == nil {
		t.Fatal("unknown workspace typo was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"default":"deny","workspaces":{"desk":["team-a"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithEntitlementsFile(path); err != nil {
		t.Fatalf("valid entitlement file rejected: %v", err)
	}
}

func TestRevokedWorkspaceHidesExistingSessions(t *testing.T) {
	b := NewStubBackend("https://gw.example")
	s := NewServer(NewStaticAuthenticator(map[string]Principal{
		"user-tok": {UserID: "alice"},
	}), b, "test")
	created := toolCall(t, s, "user-tok", "request_session", map[string]any{"workspace": "default"})
	var session Session
	result(t, created, &session)

	deny, err := ParseEntitlements([]byte(`{"default":"deny","workspaces":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	s.WithEntitlements(deny)

	var sessions []Session
	result(t, toolCall(t, s, "user-tok", "list_sessions", nil), &sessions)
	if len(sessions) != 0 {
		t.Fatalf("revoked session remains visible: %+v", sessions)
	}
	if err := s.entitledSession(t.Context(), Principal{UserID: "alice"}, session.ID); err == nil {
		t.Fatal("revoked session remains usable")
	}
}
