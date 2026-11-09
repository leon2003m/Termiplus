package mcp

import (
	"strings"
	"testing"
)

// The panel learns from the overview whether the deployment allows the
// factory reset (ALLOW_HOME_REMOVE=1) and hides the action otherwise.
func TestOverviewReportsHomeRemove(t *testing.T) {
	auth := NewStaticAuthenticator(map[string]Principal{"tok": {UserID: "alice", Roles: []string{RolePersist}}})
	for _, on := range []bool{false, true} {
		s := NewServer(auth, NewStubDevBackend("https://gw.example"), "test").WithHomeRemove(on)
		var state map[string]any
		result(t, toolCall(t, s, "tok", appToolName, nil), &state)
		if v, ok := state["home_remove"].(bool); !ok || v != on {
			t.Fatalf("home_remove = %v, want %v", state["home_remove"], on)
		}
	}
}

func TestPanelHidesWipeWhenDisabled(t *testing.T) {
	html := appHTML
	want := `if (s.persistent && state.home_remove !== false) r.appendChild(dangerAction("Reset (wipe home)"`
	if !strings.Contains(html, want) {
		t.Fatal("the panel must offer the wipe only when state.home_remove is not false")
	}
}
