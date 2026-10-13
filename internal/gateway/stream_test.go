package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"termiplus/internal/mcp"
)

// fakeKasm stands in for KasmVNC: it requires basic auth "user" with the
// password the container was created with and echoes what reached it.
func fakeKasm(t *testing.T, password string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "user" || p != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="Websockify"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"path": r.URL.Path, "cookie": r.Header.Get("Cookie"), "user": r.Header.Get("X-User-Id"),
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withDesktop adds a "desk" desktop policy and serves each container's
// desktop port from a fakeKasm that knows the password from its create body.
func withDesktop(hp **harness) func(*harnessOpts) {
	return func(o *harnessOpts) {
		var mu sync.Mutex
		kasm := map[string]string{}
		o.gateway = func(c *Config) {
			c.ControlKey = testControlKey
			c.Policies["desk"] = Policy{Runtime: "runsc", MemoryMB: 1024, CPUs: 1, Pids: 256, Desktop: true}
			orig := c.AddrFor
			c.AddrFor = func(name string, port int) string {
				if port != desktopPort {
					return orig(name, port)
				}
				mu.Lock()
				defer mu.Unlock()
				if a, ok := kasm[name]; ok {
					return a
				}
				h := *hp
				h.docker.mu.Lock()
				pw := ""
				for _, e := range h.docker.ctrs[name].body["Env"].([]any) {
					if v, ok := strings.CutPrefix(e.(string), "OPEN_TERMINAL_DESKTOP_PASSWORD="); ok {
						pw = v
					}
				}
				h.docker.mu.Unlock()
				kasm[name] = strings.TrimPrefix(fakeKasm(h.docker.t, pw).URL, "http://")
				return kasm[name]
			}
		}
	}
}

func streamGet(t *testing.T, url, password string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if password != "" {
		req.SetBasicAuth("user", password)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestDesktopStream(t *testing.T) {
	var h *harness
	h = newHarness(t, withDesktop(&h))

	code, b := h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"desk"}`)
	if code != http.StatusCreated {
		t.Fatalf("create desk: %d %s", code, b)
	}
	var desk sessionInfo
	_ = json.Unmarshal([]byte(b), &desk)
	if desk.StreamPath != "/stream/"+desk.ID+"/" || len(desk.StreamPassword) != 64 {
		t.Fatalf("desktop handle missing: %+v", desk)
	}
	if desk.StreamPassword == h.g.mgr.containerKey(desk.ID) {
		t.Fatal("stream password equals the terminal key")
	}

	// A plain terminal gets neither a handle nor a desktop password.
	_, b = h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"default"}`)
	var plain sessionInfo
	_ = json.Unmarshal([]byte(b), &plain)
	if plain.StreamPath != "" || plain.StreamPassword != "" {
		t.Fatalf("terminal got a stream handle: %+v", plain)
	}
	h.docker.mu.Lock()
	env := toJSON(h.docker.ctrs[plain.ID].body["Env"])
	h.docker.mu.Unlock()
	if strings.Contains(env, "DESKTOP") {
		t.Fatalf("terminal got a desktop password: %s", env)
	}

	// Bob's desk gets a different password.
	_, b = h.control(t, http.MethodPost, "sessions", testControlKey, "bob", `{"workspace":"desk"}`)
	var bobDesk sessionInfo
	_ = json.Unmarshal([]byte(b), &bobDesk)
	if bobDesk.StreamPassword == desk.StreamPassword {
		t.Fatal("two containers share a stream password")
	}

	url := h.gw.URL + desk.StreamPath
	if code, _ := streamGet(t, url, "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no credentials: want 401 from KasmVNC, got %d", code)
	}
	if code, _ := streamGet(t, url, bobDesk.StreamPassword, nil); code != http.StatusUnauthorized {
		t.Fatalf("another container's password: want 401, got %d", code)
	}
	code, b = streamGet(t, url+"websockify", desk.StreamPassword,
		map[string]string{"Cookie": "_oauth2_proxy=secret", "X-User-Id": "mallory"})
	if code != http.StatusOK {
		t.Fatalf("owner: want 200, got %d %s", code, b)
	}
	var seen map[string]string
	_ = json.Unmarshal([]byte(b), &seen)
	if seen["path"] != "/websockify" || seen["cookie"] != "" || seen["user"] != "" {
		t.Fatalf("forwarded request: %v", seen)
	}

	if code, _ := streamGet(t, strings.TrimSuffix(url, "/"), "", nil); code != http.StatusMovedPermanently {
		t.Fatalf("missing slash: want 301, got %d", code)
	}
	for name, path := range map[string]string{
		"terminal container": "/stream/" + plain.ID + "/",
		"unknown container":  "/stream/ot-term-0000000000000000-desk/",
		"not a container":    "/stream/gateway/",
		"bare prefix":        "/stream/",
	} {
		if code, _ := streamGet(t, h.gw.URL+path, desk.StreamPassword, nil); code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", name, code)
		}
	}

	// Listing returns the same handle.
	_, b = h.control(t, http.MethodGet, "sessions", testControlKey, "alice", "")
	if !strings.Contains(b, desk.StreamPassword) || strings.Contains(b, bobDesk.StreamPassword) {
		t.Fatalf("listing: %s", b)
	}
}

func TestStreamSameOrigin(t *testing.T) {
	for _, c := range []struct {
		host    string
		origins []string
		want    bool
	}{
		{"desk.example", []string{"https://desk.example"}, true},
		{"desk.example:8443", []string{"https://DESK.example:8443"}, true},
		{"desk.example", nil, false},
		{"desk.example", []string{"null"}, false},
		{"desk.example", []string{"https://evil.example"}, false},
		{"desk.example", []string{"https://desk.example.evil.example"}, false},
		{"desk.example", []string{"https://desk.example:8443"}, false},
		{"desk.example", []string{"https://desk.example", "https://desk.example"}, false},
		{"desk.example", []string{"file://desk.example"}, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://"+c.host+"/stream/x/websockify", nil)
		for _, o := range c.origins {
			r.Header.Add("Origin", o)
		}
		if got := sameOrigin(r); got != c.want {
			t.Errorf("host %q origins %v: got %v, want %v", c.host, c.origins, got, c.want)
		}
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Full chain: the MCP handle for a desktop workspace opens that desktop.
func TestDesktopStreamThroughMCP(t *testing.T) {
	var h *harness
	h = newHarness(t, withDesktop(&h))
	auth := mcp.NewStaticAuthenticator(map[string]mcp.Principal{"alice-tok": {UserID: "alice", Roles: []string{mcp.RolePersist}}})
	backend := mcp.NewGatewayBackend(h.gw.URL, "https://terminals.example", testControlKey, "").WithStreamBase(h.gw.URL)
	c := &mcpChain{h: h, srv: mcp.NewServer(auth, backend, "test")}

	out := c.call(t, "alice-tok", "list_workspaces", nil)
	if !strings.Contains(out.Result.Content[0].Text, `"name":"desk","description":"runtime runsc","persistent":true,"desktop":true`) {
		t.Fatalf("workspaces: %s", out.Result.Content[0].Text)
	}
	out = c.call(t, "alice-tok", "request_session", map[string]any{"workspace": "desk", "persistent": true, "_confirmed": true})
	if out.Error != nil || out.Result.IsError {
		t.Fatalf("request desk: %+v", out)
	}
	var s mcp.Session
	if err := json.Unmarshal([]byte(out.Result.Content[0].Text), &s); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.StreamURL, h.gw.URL+"/stream/") || s.StreamPassword == "" {
		t.Fatalf("no stream handle: %+v", s)
	}
	if code, b := streamGet(t, s.StreamURL, s.StreamPassword, nil); code != http.StatusOK {
		t.Fatalf("open desktop: %d %s", code, b)
	}
}

// TestDesktopStreamRealKasmVNC runs the stream route against a real desktop
// container: TERMIPLUS_KASM_ADDR=host:port of its 6901 and
// TERMIPLUS_KASM_PASSWORD its OPEN_TERMINAL_DESKTOP_PASSWORD. Skipped
// otherwise. It checks basic auth and the WebSocket upgrade (KasmVNC's origin
// check) through the gateway.
func TestDesktopStreamRealKasmVNC(t *testing.T) {
	addr, pw := os.Getenv("TERMIPLUS_KASM_ADDR"), os.Getenv("TERMIPLUS_KASM_PASSWORD")
	if addr == "" || pw == "" {
		t.Skip("set TERMIPLUS_KASM_ADDR and TERMIPLUS_KASM_PASSWORD")
	}
	h := newHarness(t, func(o *harnessOpts) {
		o.gateway = func(c *Config) {
			c.ControlKey = testControlKey
			c.Policies["desk"] = Policy{Runtime: "runsc", MemoryMB: 1024, CPUs: 1, Pids: 256, Desktop: true}
			orig := c.AddrFor
			c.AddrFor = func(name string, port int) string {
				if port == desktopPort {
					return addr
				}
				return orig(name, port)
			}
		}
	})
	_, b := h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"desk"}`)
	var s sessionInfo
	_ = json.Unmarshal([]byte(b), &s)
	url := h.gw.URL + s.StreamPath

	if code, _ := streamGet(t, url, "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no credentials: want 401, got %d", code)
	}
	if code, _ := streamGet(t, url, pw, nil); code != http.StatusOK {
		t.Fatalf("with password: want 200, got %d", code)
	}
	upgrade := func(origin string) int {
		conn, err := net.Dial("tcp", strings.TrimPrefix(h.gw.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		req, _ := http.NewRequest(http.MethodGet, url+"websockify", nil)
		req.SetBasicAuth("user", pw)
		for k, v := range map[string]string{
			"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13",
			"Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==", "Sec-WebSocket-Protocol": "binary",
		} {
			req.Header.Set(k, v)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := req.Write(conn); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	if code := upgrade(h.gw.URL); code != http.StatusSwitchingProtocols {
		t.Fatalf("same-origin WebSocket: want 101, got %d", code)
	}
	for _, origin := range []string{"https://evil.example", "", "null"} {
		if code := upgrade(origin); code != http.StatusForbidden {
			t.Fatalf("WebSocket with Origin %q: want 403, got %d", origin, code)
		}
	}
}
