package gateway

import (
	"slices"
	"strings"
	"testing"

	"termiplus/internal/naming"
)

func (h *harness) env(t *testing.T, name string) []string {
	t.Helper()
	h.docker.mu.Lock()
	defer h.docker.mu.Unlock()
	c := h.docker.ctrs[name]
	if c == nil {
		t.Fatalf("no container %s", name)
	}
	var out []string
	for _, e := range c.body["Env"].([]any) {
		out = append(out, e.(string))
	}
	return out
}

func TestPolicyLocaleKeyboardRecordEnv(t *testing.T) {
	h := newHarness(t, func(o *harnessOpts) {
		o.gateway = func(c *Config) {
			c.Policies["default"] = Policy{Runtime: "runsc", MemoryMB: 1, CPUs: 1, Pids: 1, Locale: "de_DE.UTF-8"}
			c.Policies["desk"] = Policy{Runtime: "runsc", MemoryMB: 1, CPUs: 1, Pids: 1, Desktop: true,
				Locale: "de_DE.UTF-8", Keyboard: "de", Record: true}
		}
	})
	if code, _ := h.get(t, "/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatalf("start: %d", code)
	}
	env := h.env(t, naming.Container("alice", "default"))
	if !slices.Contains(env, "OPEN_TERMINAL_LOCALE=de_DE.UTF-8") {
		t.Errorf("locale missing: %v", env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "OPEN_TERMINAL_KEYBOARD=") || strings.HasPrefix(e, "OPEN_TERMINAL_RECORD=") {
			t.Errorf("terminal policy got %s", e)
		}
	}
	h.get(t, "/p/desk/files/cwd", "alice", testAPIKey)
	env = h.env(t, naming.Container("alice", "desk"))
	for _, want := range []string{"OPEN_TERMINAL_LOCALE=de_DE.UTF-8", "OPEN_TERMINAL_KEYBOARD=de", "OPEN_TERMINAL_RECORD=1"} {
		if !slices.Contains(env, want) {
			t.Errorf("desk: %s missing: %v", want, env)
		}
	}
}

func TestPolicyLocaleKeyboardRecordValidated(t *testing.T) {
	bad := []Policy{
		{Locale: "de_DE"},
		{Locale: "de_DE.UTF-8\nX=1"},
		{Locale: "de_DE.UTF-8 x"},
		{Locale: "../../etc"},
		{Keyboard: "de", Desktop: true, Locale: "C"},
		{Keyboard: "de;reboot", Desktop: true},
		{Keyboard: "DE", Desktop: true},
		{Keyboard: "de"},
		{Record: true},
	}
	for _, p := range bad {
		p.Runtime, p.MemoryMB, p.CPUs, p.Pids = "runsc", 1, 1, 1
		cfg := Config{APIKey: testAPIKey, KeySecret: testKeySecret, Policies: map[string]Policy{"default": p}}
		if _, err := New(cfg); err == nil {
			t.Errorf("%+v: accepted", p)
		}
	}
	for _, p := range []Policy{{Locale: "C.UTF-8"}, {Locale: "en_US.UTF-8"}, {Desktop: true, Keyboard: "us", Record: true}} {
		p.Runtime, p.MemoryMB, p.CPUs, p.Pids = "runsc", 1, 1, 1
		cfg := Config{APIKey: testAPIKey, KeySecret: testKeySecret, Policies: map[string]Policy{"default": p}}
		if _, err := New(cfg); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
}
