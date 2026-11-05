package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
		if r.URL.Path != "/desktop/screenshot" || r.Header.Get("Authorization") != "Bearer "+h.g.mgr.containerKey(inst.name) {
			t.Error("wrong execution target/auth")
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	orig := h.g.mgr.addrFor
	h.g.mgr.addrFor = func(name string, port int) string {
		if name == inst.name {
			return strings.TrimPrefix(server.URL, "http://")
		}
		return orig(name, port)
	}
	// Adopted instances have no stored terminal address or key. Capture must
	// derive both without calling ensure or starting the container.
	inst.addr, inst.key = "", ""
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
		inst.previewAt = inst.previewAt.Add(-time.Minute)
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

// A workspace controls both response headers and body timing. Neither wait may
// reserve the gateway-wide decoder or make its owner's other requests exclusive.
func TestSlowWorkspaceDoesNotBlockOtherPreviews(t *testing.T) {
	for _, route := range []string{"screen", "preview"} {
		for _, phase := range []string{"headers", "body"} {
			t.Run(route+"/"+phase, func(t *testing.T) {
				var h *harness
				h = newHarness(t, withDesktop(&h))
				sessions := make(map[string]string)
				for _, user := range []string{"alice", "bob"} {
					code, body := h.control(t, http.MethodPost, "sessions", testControlKey, user, `{"workspace":"desk"}`)
					var session sessionInfo
					if code != 201 || json.Unmarshal([]byte(body), &session) != nil {
						t.Fatalf("create: %d %s", code, body)
					}
					sessions[user] = session.ID
				}
				var frame bytes.Buffer
				if err := png.Encode(&frame, image.NewRGBA(image.Rect(0, 0, 320, 200))); err != nil {
					t.Fatal(err)
				}
				var slow atomic.Bool
				started := make(chan struct{}, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if slow.Load() && r.Header.Get("X-User-Id") == "alice" {
						if phase == "body" {
							w.WriteHeader(http.StatusOK)
							w.(http.Flusher).Flush()
						}
						started <- struct{}{}
						select {
						case <-time.After(2 * time.Second):
						case <-r.Context().Done():
							return
						}
					}
					_, _ = w.Write(frame.Bytes())
				}))
				defer upstream.Close()
				h.g.mgr.addrFor = func(string, int) string { return strings.TrimPrefix(upstream.URL, "http://") }
				type result struct {
					code int
					err  error
				}
				get := func(path, user string, timeout time.Duration) result {
					req, err := http.NewRequest(http.MethodGet, h.gw.URL+"/control/v1/"+path+"/"+sessions[user], nil)
					if err != nil {
						return result{err: err}
					}
					req.Header.Set(controlKeyHeader, testControlKey)
					req.Header.Set("X-User-Id", user)
					resp, err := (&http.Client{Timeout: timeout}).Do(req)
					if err != nil {
						return result{err: err}
					}
					defer resp.Body.Close()
					_, err = io.Copy(io.Discard, resp.Body)
					return result{resp.StatusCode, err}
				}
				if route == "screen" {
					if r := get("preview", "alice", time.Second); r.err != nil || r.code != 200 {
						t.Fatalf("prime: %+v", r)
					}
				}
				slow.Store(true)
				done := make(chan result, 1)
				go func() { done <- get(route, "alice", 5*time.Second) }()
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("slow workspace was not fetched")
				}
				for i := 0; i < 2; i++ { // uncached and then cached for the other user
					if r := get("preview", "bob", time.Second); r.err != nil || r.code != 200 {
						t.Fatalf("slow %s blocked bob: %+v", phase, r)
					}
				}
				if route == "screen" {
					if r := get("preview", "alice", time.Second); r.err != nil || r.code != 200 {
						t.Fatalf("own share blocked cached thumbnail: %+v", r)
					}
				}
				select {
				case r := <-done:
					if r.err != nil || r.code != 200 {
						t.Fatalf("slow request: %+v", r)
					}
				case <-time.After(4 * time.Second):
					t.Fatal("slow request did not finish")
				}
			})
		}
	}
}

func TestWorkspaceFetchDeadlineCoversHeadersAndBody(t *testing.T) {
	for _, route := range []string{"screen", "preview"} {
		for _, phase := range []string{"headers", "body"} {
			t.Run(route+"/"+phase, func(t *testing.T) {
				var h *harness
				h = newHarness(t, withDesktop(&h))
				_, body := h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"desk"}`)
				var session sessionInfo
				if err := json.Unmarshal([]byte(body), &session); err != nil {
					t.Fatal(err)
				}
				cancelled := make(chan struct{})
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if phase == "body" {
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
					}
					<-r.Context().Done()
					close(cancelled)
				}))
				defer upstream.Close()
				h.g.mgr.addrFor = func(string, int) string { return strings.TrimPrefix(upstream.URL, "http://") }
				start := time.Now()
				code, _ := h.control(t, http.MethodGet, route+"/"+session.ID, testControlKey, "alice", "")
				if elapsed := time.Since(start); code != http.StatusGatewayTimeout || elapsed > 4*time.Second {
					t.Fatalf("unbounded workspace %s: status %d after %v", phase, code, elapsed)
				}
				select {
				case <-cancelled:
				case <-time.After(time.Second):
					t.Fatal("workspace fetch was not cancelled")
				}
			})
		}
	}
}

type previewDeadlineFailure struct {
	*httptest.ResponseRecorder
	calls atomic.Int32
}

func (w *previewDeadlineFailure) SetWriteDeadline(time.Time) error {
	if w.calls.Add(1) == 2 {
		return fmt.Errorf("dummy socket deadline failure")
	}
	return nil
}

func TestImageWriteStartFailureIsNotSuccess(t *testing.T) {
	var h *harness
	h = newHarness(t, withDesktop(&h))
	_, body := h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"desk"}`)
	var session sessionInfo
	if err := json.Unmarshal([]byte(body), &session); err != nil {
		t.Fatal(err)
	}
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(imageData.Bytes()) }))
	defer upstream.Close()
	h.g.mgr.addrFor = func(string, int) string { return strings.TrimPrefix(upstream.URL, "http://") }
	for _, route := range []string{"screen", "preview"} {
		t.Run(route, func(t *testing.T) {
			w := &previewDeadlineFailure{ResponseRecorder: httptest.NewRecorder()}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if route == "screen" {
				h.g.serveScreen(w, r, "alice", session.ID)
			} else {
				h.g.servePreview(w, r, "alice", session.ID)
			}
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("write-start failure became an empty success: %d", w.Code)
			}
		})
	}
}
