package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDesktopPreview(t *testing.T) {
	var h *harness
	h = newHarness(t, withDesktop(&h))
	_, body := h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"desk"}`)
	var session sessionInfo
	_ = json.Unmarshal([]byte(body), &session)
	inst := h.g.mgr.streamTarget(session.ID, "alice")
	if inst == nil {
		t.Fatal("missing desktop")
	}
	var calls atomic.Int32
	var frame bytes.Buffer
	_ = png.Encode(&frame, image.NewRGBA(image.Rect(0, 0, 320, 200)))
	data := frame.Bytes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/execute" || r.Header.Get("Authorization") != "Bearer "+inst.key {
			t.Error("wrong execution target/auth")
		}
		var request map[string]string
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request["command"] != screenshotCommand {
			t.Error("non-fixed command")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "done", "exit_code": 0, "output": []map[string]string{{"type": "stdout", "data": base64.StdEncoding.EncodeToString(data)}}})
	}))
	defer server.Close()
	inst.addr = strings.TrimPrefix(server.URL, "http://")
	get := func(key, user string) int {
		code, _ := h.control(t, http.MethodGet, "preview/"+session.ID, key, user, "")
		return code
	}
	if code := get("wrong", "alice"); code != 401 {
		t.Fatalf("auth: %d", code)
	}
	if code := get(testControlKey, "bob"); code != 404 {
		t.Fatalf("owner: %d", code)
	}
	for i := 0; i < 2; i++ {
		if code := get(testControlKey, "alice"); code != 200 {
			t.Fatalf("preview: %d", code)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("cache calls: %d", calls.Load())
	}
	for _, bad := range [][]byte{[]byte("<svg/>"), bytes.Repeat([]byte{'x'}, 256<<10+1)} {
		inst.previewAt = inst.previewAt.Add(-60 * 1000000000)
		data = bad
		if code := get(testControlKey, "alice"); code != 502 {
			t.Fatalf("invalid bytes: %d", code)
		}
	}
	h.docker.mu.Lock()
	h.docker.ctrs[session.ID].running = false
	h.docker.mu.Unlock()
	before := calls.Load()
	if code := get(testControlKey, "alice"); code != 404 {
		t.Fatalf("offline: %d", code)
	}
	if calls.Load() != before {
		t.Fatal("offline desktop executed command")
	}

}
