package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"termiplus/internal/naming"
)

const testControlKey = "mcp-control-key-distinct-0123456789abcd"

func withControl(o *harnessOpts) {
	o.gateway = func(c *Config) { c.ControlKey = testControlKey }
}

func (h *harness) control(t *testing.T, method, path, key, user, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, h.gw.URL+"/control/v1/"+path, r)
	if key != "" {
		req.Header.Set(controlKeyHeader, key)
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestControlDisabledReturns404(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.control(t, http.MethodGet, "workspaces", testControlKey, "", ""); code != http.StatusNotFound {
		t.Fatalf("control API with feature off: want 404, got %d", code)
	}
}

func TestControlAuth(t *testing.T) {
	h := newHarness(t, withControl)
	for _, c := range []struct {
		name, key string
		want      int
	}{
		{"no key", "", http.StatusUnauthorized},
		{"wrong key", "nope-nope-nope-nope-nope-nope-nope-nope", http.StatusUnauthorized},
		{"connection key is not the control key", testAPIKey, http.StatusUnauthorized},
		{"control key", testControlKey, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			if code, b := h.control(t, http.MethodGet, "workspaces", c.key, "", ""); code != c.want {
				t.Fatalf("want %d, got %d %s", c.want, code, b)
			}
		})
	}
	// The connection key on the normal bearer header must not open it either.
	req, _ := http.NewRequest(http.MethodGet, h.gw.URL+"/control/v1/workspaces", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bearer connection key opened the control API: %d", resp.StatusCode)
	}
}

func TestControlWorkspaces(t *testing.T) {
	h := newHarness(t, withControl)
	_, b := h.control(t, http.MethodGet, "workspaces", testControlKey, "", "")
	var ws []workspaceInfo
	if err := json.Unmarshal([]byte(b), &ws); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, w := range ws {
		got[w.Name] = w.Persistent
	}
	if p, ok := got["default"]; !ok || !p {
		t.Fatalf("default should be listed as persistent: %v", ws)
	}
	if p, ok := got["scratch"]; !ok || p {
		t.Fatalf("scratch should be listed as ephemeral: %v", ws)
	}
}

func TestControlSessionLifecycle(t *testing.T) {
	h := newHarness(t, withControl)

	// Persistent session.
	code, b := h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"default"}`)
	if code != http.StatusCreated {
		t.Fatalf("create persistent: %d %s", code, b)
	}
	var pers sessionInfo
	_ = json.Unmarshal([]byte(b), &pers)
	if pers.ID != naming.Container("alice", "default") || !pers.Persistent || pers.TerminalSession != "" {
		t.Fatalf("unexpected persistent session: %+v", pers)
	}

	// Ephemeral session gets its own session id.
	code, b = h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"scratch"}`)
	if code != http.StatusCreated {
		t.Fatalf("create ephemeral: %d %s", code, b)
	}
	var eph sessionInfo
	_ = json.Unmarshal([]byte(b), &eph)
	if eph.Persistent || eph.TerminalSession == "" || eph.ID != naming.Ephemeral("alice", eph.TerminalSession, "scratch") {
		t.Fatalf("unexpected ephemeral session: %+v", eph)
	}

	// Listing is per user.
	_, b = h.control(t, http.MethodGet, "sessions", testControlKey, "alice", "")
	var mine []sessionInfo
	_ = json.Unmarshal([]byte(b), &mine)
	if len(mine) != 2 {
		t.Fatalf("alice should have 2 sessions, got %s", b)
	}
	for _, s := range mine {
		want := ""
		if s.ID == eph.ID {
			want = eph.TerminalSession // listed ephemerals keep a usable handle
		}
		if s.TerminalSession != want {
			t.Fatalf("session %s: terminal_session %q, want %q", s.ID, s.TerminalSession, want)
		}
	}

	// An Open WebUI chat sandbox is listed, but its chat id is never echoed back.
	if code, b := h.getChat(t, "/p/scratch/files/cwd", "alice", "secret-chat-id", testAPIKey); code != 200 {
		t.Fatalf("chat sandbox: %d %s", code, b)
	}
	_, b = h.control(t, http.MethodGet, "sessions", testControlKey, "alice", "")
	if strings.Contains(b, "secret-chat-id") {
		t.Fatalf("chat id leaked through the control API: %s", b)
	}
	if err := json.Unmarshal([]byte(b), &mine); err != nil || len(mine) != 3 {
		t.Fatalf("alice should now have 3 sessions, got %s", b)
	}
	_, b = h.control(t, http.MethodGet, "sessions", testControlKey, "bob", "")
	if strings.TrimSpace(b) != "[]" {
		t.Fatalf("bob should see no sessions, got %s", b)
	}

	// Bob cannot remove alice's session, and learns nothing about it.
	if code, _ := h.control(t, http.MethodDelete, "sessions/"+pers.ID, testControlKey, "bob", ""); code != http.StatusNotFound {
		t.Fatalf("bob deleting alice's session: want 404, got %d", code)
	}
	if h.ctr(pers.ID) == nil {
		t.Fatal("alice's container was removed by bob")
	}

	// Alice can.
	if code, b := h.control(t, http.MethodDelete, "sessions/"+pers.ID, testControlKey, "alice", ""); code != http.StatusNoContent {
		t.Fatalf("alice delete: %d %s", code, b)
	}
	if h.ctr(pers.ID) != nil {
		t.Fatal("container still present after delete")
	}
	// The persistent home volume survives.
	h.docker.mu.Lock()
	_, vol := h.docker.volumes[naming.Volume("alice", "default")]
	h.docker.mu.Unlock()
	if !vol {
		t.Fatal("home volume must survive session removal")
	}
}

func TestControlRejectsBadRequests(t *testing.T) {
	h := newHarness(t, withControl)
	cases := []struct {
		name, method, path, user, body string
		want                           int
	}{
		{"no user", http.MethodGet, "sessions", "", "", http.StatusBadRequest},
		{"spec user", http.MethodGet, "sessions", specUser, "", http.StatusBadRequest},
		{"unknown workspace", http.MethodPost, "sessions", "alice", `{"workspace":"nope"}`, http.StatusNotFound},
		{"unknown field", http.MethodPost, "sessions", "alice", `{"workspace":"default","privileged":true}`, http.StatusBadRequest},
		{"foreign name", http.MethodDelete, "sessions/traefik", "alice", "", http.StatusNotFound},
		{"wrong method", http.MethodPut, "sessions", "alice", "", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, b := h.control(t, c.method, c.path, testControlKey, c.user, c.body); code != c.want {
				t.Fatalf("want %d, got %d %s", c.want, code, b)
			}
		})
	}
}

func TestControlKeyMustBeDistinct(t *testing.T) {
	base := Config{
		APIKey: testAPIKey, KeySecret: testKeySecret,
		Policies: map[string]Policy{"default": {Runtime: "runsc", MemoryMB: 1, CPUs: 1, Pids: 1}},
	}
	for _, key := range []string{"short", testAPIKey} {
		cfg := base
		cfg.Policies = map[string]Policy{"default": {Runtime: "runsc", MemoryMB: 1, CPUs: 1, Pids: 1}}
		cfg.ControlKey = key
		if _, err := New(cfg); err == nil {
			t.Fatalf("control key %q should be rejected", key)
		}
	}
}
