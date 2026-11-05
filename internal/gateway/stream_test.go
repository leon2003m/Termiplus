package gateway

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"termiplus/internal/naming"
)

func withDesktop(_ **harness) func(*harnessOpts) {
	return func(o *harnessOpts) {
		o.gateway = func(c *Config) {
			c.ControlKey = testControlKey
			c.Policies["desk"] = Policy{Runtime: "runsc", MemoryMB: 1024, CPUs: 1, Pids: 256, Desktop: true}
		}
	}
}

func nativeScreen(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 1280, 800))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestNativeDesktopPolicyAndOwnership(t *testing.T) {
	var h *harness
	h = newHarness(t, withDesktop(&h))
	_, body := h.control(t, "POST", "sessions", testControlKey, "alice", `{"workspace":"desk"}`)
	var s sessionInfo
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Desktop || strings.Contains(body, "stream") || strings.Contains(body, "password") {
		t.Fatalf("native handle: %s", body)
	}
	h.docker.mu.Lock()
	env, _ := json.Marshal(h.docker.ctrs[s.ID].body["Env"])
	h.docker.mu.Unlock()
	if !strings.Contains(string(env), "OPEN_TERMINAL_ENABLE_DESKTOP=true") || !strings.Contains(string(env), "OPEN_TERMINAL_ENABLE_BROWSER=true") || strings.Contains(string(env), "PASSWORD") {
		t.Fatalf("native env: %s", env)
	}
	frame := nativeScreen(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/desktop/screenshot" || r.Header.Get("Authorization") != "Bearer "+h.g.mgr.containerKey(s.ID) || r.Header.Get("X-User-Id") != "alice" {
			t.Error("wrong native target or identity")
		}
		_, _ = w.Write(frame)
	}))
	defer up.Close()
	original := h.g.mgr.addrFor
	h.g.mgr.addrFor = func(name string, port int) string {
		if name == s.ID {
			return strings.TrimPrefix(up.URL, "http://")
		}
		return original(name, port)
	}
	for _, tc := range []struct {
		user, key, path string
		want            int
	}{
		{"alice", testControlKey, "screen/" + s.ID, 200},
		{"bob", testControlKey, "screen/" + s.ID, 404},
		{"alice", "wrong", "screen/" + s.ID, 401},
		{"alice", testControlKey, "stream/" + s.ID + "/", 404},
		{"alice", testControlKey, "forward/" + s.ID + "/3000/", 404},
	} {
		if code, _ := h.control(t, "GET", tc.path, tc.key, tc.user, ""); code != tc.want {
			t.Errorf("%s as %s: %d", tc.path, tc.user, code)
		}
	}
	if code, _ := h.control(t, "POST", "screen/"+s.ID, testControlKey, "alice", ""); code != 405 {
		t.Fatalf("input accepted: %d", code)
	}
	if code, _ := h.get(t, "/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatal("plain session failed")
	}
	h.docker.mu.Lock()
	plainEnv, _ := json.Marshal(h.docker.ctrs[naming.Container("alice", "default")].body["Env"])
	h.docker.mu.Unlock()
	if !strings.Contains(string(plainEnv), "OPEN_TERMINAL_ENABLE_DESKTOP=false") {
		t.Fatal("desktop enabled on plain policy")
	}
}

func TestNativeDiscoveryKeepsFeaturesSeparate(t *testing.T) {
	p := map[string]Policy{"default": {Runtime: "runsc"}, "scratch": {Runtime: "runsc", Storage: "ephemeral"}, "desk": {Runtime: "runsc", Desktop: true}, "custom": {Runtime: "runsc", Image: "custom:1"}, "offline": {Runtime: "runsc", Egress: "offline"}}
	got := specPolicies(p)
	if got["scratch"] != "default" || got["desk"] != "desk" || got["custom"] != "custom" || got["offline"] != "offline" {
		t.Fatalf("feature discovery conflated: %v", got)
	}
}
