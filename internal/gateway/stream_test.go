package gateway

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"termiplus/internal/mcp"
)

// fakeKasm stands in for KasmVNC: it requires basic auth "user" with the
// password the container was created with (or "viewer" with the view-only
// password) and echoes what reached it.
func fakeKasm(t *testing.T, password string, viewPassword ...string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		okUser := ok && u == "user" && p == password
		okViewer := ok && u == "viewer" && len(viewPassword) == 1 && viewPassword[0] != "" && p == viewPassword[0]
		if !okUser && !okViewer {
			w.Header().Set("WWW-Authenticate", `Basic realm="Websockify"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"path": r.URL.Path, "cookie": r.Header.Get("Cookie"), "user": r.Header.Get("X-User-Id"), "as": u,
			"view": r.Header.Get("X-Stream-View"),
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
				pw, vpw := "", ""
				for _, e := range h.docker.ctrs[name].body["Env"].([]any) {
					if v, ok := strings.CutPrefix(e.(string), "OPEN_TERMINAL_DESKTOP_PASSWORD="); ok {
						pw = v
					}
					if v, ok := strings.CutPrefix(e.(string), "OPEN_TERMINAL_DESKTOP_VIEW_PASSWORD="); ok {
						vpw = v
					}
				}
				h.docker.mu.Unlock()
				kasm[name] = strings.TrimPrefix(fakeKasm(h.docker.t, pw, vpw).URL, "http://")
				return kasm[name]
			}
		}
	}
}

// streamGet calls the gateway's control stream route as the MCP server does.
func streamGet(t *testing.T, base, path, user string, hdr map[string]string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/control/v1/stream/"+path, nil)
	req.Header.Set(controlKeyHeader, testControlKey)
	if user != "" {
		req.Header.Set("X-User-Id", user)
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
	return resp.StatusCode, resp.Header, string(b)
}

func TestDesktopStreamViewMode(t *testing.T) {
	var h *harness
	h = newHarness(t, withDesktop(&h))
	_, b := h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"desk"}`)
	var s sessionInfo
	_ = json.Unmarshal([]byte(b), &s)
	_, _, body := streamGet(t, h.gw.URL, s.ID+"/app.js", "alice", map[string]string{"X-Stream-View": "1"})
	if !strings.Contains(body, `"as":"viewer"`) || !strings.Contains(body, `"view":""`) {
		t.Fatalf("view mode: %s (and the header must not reach KasmVNC)", body)
	}
	_, _, body = streamGet(t, h.gw.URL, s.ID+"/app.js", "alice", nil)
	if !strings.Contains(body, `"as":"user"`) {
		t.Fatalf("owner mode: %s", body)
	}
}

func TestDesktopStream(t *testing.T) {
	var h *harness
	h = newHarness(t, withDesktop(&h))

	code, b := h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"desk"}`)
	if code != http.StatusCreated {
		t.Fatalf("create desk: %d %s", code, b)
	}
	if strings.Contains(b, "password") {
		t.Fatalf("session hands out a password: %s", b)
	}
	var desk sessionInfo
	_ = json.Unmarshal([]byte(b), &desk)
	if desk.StreamPath != "/stream/"+desk.ID+"/" {
		t.Fatalf("desktop handle missing: %+v", desk)
	}
	if h.g.mgr.desktopPassword(desk.ID) == h.g.mgr.containerKey(desk.ID) {
		t.Fatal("desktop password equals the terminal key")
	}

	// A plain terminal gets neither a handle nor a desktop password.
	_, b = h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"default"}`)
	var plain sessionInfo
	_ = json.Unmarshal([]byte(b), &plain)
	if plain.StreamPath != "" {
		t.Fatalf("terminal got a stream handle: %+v", plain)
	}
	h.docker.mu.Lock()
	env := toJSON(h.docker.ctrs[plain.ID].body["Env"])
	h.docker.mu.Unlock()
	if strings.Contains(env, "DESKTOP") {
		t.Fatalf("terminal got a desktop password: %s", env)
	}
	_, b = h.control(t, http.MethodPost, "sessions", testControlKey, "bob", `{"workspace":"desk"}`)
	var bobDesk sessionInfo
	_ = json.Unmarshal([]byte(b), &bobDesk)
	if h.g.mgr.desktopPassword(bobDesk.ID) == h.g.mgr.desktopPassword(desk.ID) {
		t.Fatal("two containers share a desktop password")
	}

	// The owner gets in without any password of their own: the gateway fills
	// in KasmVNC's (fakeKasm rejects anything else), replacing whatever the
	// client sent, and strips cookies and identity headers.
	code, _, b = streamGet(t, h.gw.URL, desk.ID+"/websockify?x=1", "alice", map[string]string{
		"Authorization": "Basic dXNlcjp3cm9uZw==", "Cookie": "_termiplus_desk=secret",
		"X-Forwarded-User": "kc-alice"})
	if code != http.StatusOK {
		t.Fatalf("owner: want 200, got %d %s", code, b)
	}
	var seen map[string]string
	_ = json.Unmarshal([]byte(b), &seen)
	if seen["path"] != "/websockify" || seen["cookie"] != "" || seen["user"] != "" {
		t.Fatalf("forwarded request: %v", seen)
	}

	for name, c := range map[string]struct{ path, user string }{
		"another user's desktop": {desk.ID + "/", "bob"},
		"terminal container":     {plain.ID + "/", "alice"},
		"unknown container":      {"ot-u-0000000000000000-desk/", "alice"},
		"not a container":        {"gateway/", "alice"},
		"bare prefix":            {"", "alice"},
	} {
		if code, _, _ := streamGet(t, h.gw.URL, c.path, c.user, nil); code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", name, code)
		}
	}
	if code, _, _ := streamGet(t, h.gw.URL, desk.ID+"/", "", nil); code != http.StatusBadRequest {
		t.Errorf("no user: want 400, got %d", code)
	}
	// Without the control key, and on the old public path, nothing is served.
	for _, u := range []string{h.gw.URL + "/control/v1/stream/" + desk.ID + "/", h.gw.URL + desk.StreamPath} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		req.Header.Set("X-User-Id", "alice")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || strings.Contains(string(body), "websockify") {
			t.Errorf("%s without control key: %d %s", u, resp.StatusCode, body)
		}
	}

	// Listing returns the handle and no password.
	_, b = h.control(t, http.MethodGet, "sessions", testControlKey, "alice", "")
	if !strings.Contains(b, desk.StreamPath) || strings.Contains(b, "password") {
		t.Fatalf("listing: %s", b)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Full chain: browser identity (as desk-auth forwards it) -> MCP UI listener
// -> gateway -> the user's desktop, with no second password.
func TestDesktopStreamThroughMCP(t *testing.T) {
	var h *harness
	h = newHarness(t, withDesktop(&h))
	auth := mcp.NewStaticAuthenticator(map[string]mcp.Principal{"alice-tok": {UserID: "alice", Roles: []string{mcp.RolePersist}}})
	backend := mcp.NewGatewayBackend(h.gw.URL, "https://terminals.example", testControlKey, "").WithStreamBase("https://desk.example")
	srv := mcp.NewServer(auth, backend, "test")
	c := &mcpChain{h: h, srv: srv}

	out := c.call(t, "alice-tok", "list_workspaces", nil)
	if !strings.Contains(out.Result.Content[0].Text, `"name":"desk","description":"runtime runsc","persistent":true,"desktop":true`) {
		t.Fatalf("workspaces: %s", out.Result.Content[0].Text)
	}
	out = c.call(t, "alice-tok", "request_session", map[string]any{"workspace": "desk", "persistent": true, "_confirmed": true})
	if out.Error != nil || out.Result.IsError {
		t.Fatalf("request desk: %+v", out)
	}
	text := out.Result.Content[0].Text
	var s mcp.Session
	if err := json.Unmarshal([]byte(text), &s); err != nil {
		t.Fatal(err)
	}
	if s.StreamURL != "https://desk.example/stream/"+s.ID+"/" || strings.Contains(text, "password") {
		t.Fatalf("stream handle: %s", text)
	}
	_, b := h.control(t, http.MethodPost, "sessions", testControlKey, "bob", `{"workspace":"desk"}`)
	var bob sessionInfo
	_ = json.Unmarshal([]byte(b), &bob)

	ids, err := mcp.OpenIdentityStore(filepath.Join(t.TempDir(), "ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = ids.Record("kc-alice", "alice")
	ui, err := mcp.NewUI(srv, ids).WithStream(h.gw.URL, testControlKey)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(ui)
	t.Cleanup(front.Close)
	get := func(sub, path string, hdr map[string]string) (int, http.Header) {
		req, _ := http.NewRequest(http.MethodGet, front.URL+path, nil)
		if sub != "" {
			req.Header.Set("X-Forwarded-User", sub)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, resp.Header
	}
	path := "/stream/" + s.ID + "/"
	// The page gets KasmVNC's WebSocket path, which defaults to /websockify.
	wantLoc := path + "?path=stream%2F" + s.ID + "%2Fwebsockify"
	if code, hdr := get("kc-alice", path, nil); code != http.StatusFound || hdr.Get("Location") != wantLoc {
		t.Fatalf("desktop page without path: %d %q, want %q", code, hdr.Get("Location"), wantLoc)
	}
	if code, hdr := get("kc-alice", wantLoc, map[string]string{"X-User-Id": "bob"}); code != http.StatusOK || hdr.Get("Www-Authenticate") != "" {
		t.Fatalf("alice's desktop: %d %v", code, hdr)
	}
	if code, _ := get("kc-alice", "/stream/"+bob.ID+"/app.js", nil); code != http.StatusNotFound {
		t.Fatalf("bob's desktop for alice: %d", code)
	}
	// A desktop that does not answer (here: someone else's, which looks the
	// same as a stopped one) gets the start page; assets keep the error.
	bobPage := "/stream/" + bob.ID + "/?path=x"
	if code, hdr := get("kc-alice", bobPage, nil); code != http.StatusNotFound ||
		!strings.HasPrefix(hdr.Get("Content-Type"), "text/html") || !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("start page: %d %v", code, hdr)
	}
	if _, hdr := get("kc-alice", "/stream/"+bob.ID+"/app.js", nil); strings.HasPrefix(hdr.Get("Content-Type"), "text/html") {
		t.Fatalf("asset got the start page: %v", hdr)
	}
	if code, _ := get("kc-carol", wantLoc, nil); code != http.StatusForbidden {
		t.Fatalf("unlinked user: %d", code)
	}
	if code, _ := get("", wantLoc, nil); code != http.StatusUnauthorized {
		t.Fatalf("not signed in: %d", code)
	}
	if code, hdr := get("kc-alice", strings.TrimSuffix(path, "/"), nil); code != http.StatusFound || hdr.Get("Location") != path {
		t.Fatalf("missing slash: %d %q", code, hdr.Get("Location"))
	}
	// The start page looks the desktop up: running, gone but restartable
	// (persistent), or someone else's (unknown).
	desk := func(id string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, front.URL+"/ui/api/desktops/"+id, nil)
		req.Header.Set("X-Forwarded-User", "kc-alice")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, b := desk(s.ID); code != http.StatusOK || !strings.Contains(b, `"running":true`) {
		t.Fatalf("own running desktop: %d %s", code, b)
	}
	if code, b := desk(bob.ID); code != http.StatusNotFound {
		t.Fatalf("bob's desktop for alice: %d %s", code, b)
	}
	// WebSockets only from the desktop's own origin.
	ws := map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"}
	for _, origin := range []string{"", "null", "https://evil.example"} {
		hdr := map[string]string{"Origin": origin}
		for k, v := range ws {
			hdr[k] = v
		}
		if code, _ := get("kc-alice", path+"websockify", hdr); code != http.StatusForbidden {
			t.Errorf("WebSocket from Origin %q: want 403, got %d", origin, code)
		}
	}
	if code, _ := h.control(t, http.MethodDelete, "sessions/"+s.ID, testControlKey, "alice", ""); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("remove alice's desktop: %d", code)
	}
	if code, b := desk(s.ID); code != http.StatusOK || !strings.Contains(b, `"running":false`) || !strings.Contains(b, `"workspace":"desk"`) {
		t.Fatalf("removed persistent desktop: %d %s", code, b)
	}
}

// TestDesktopStreamRealKasmVNC runs the stream route against a real desktop
// container: TERMIPLUS_KASM_ADDR=host:port of its 6901 and
// TERMIPLUS_KASM_PASSWORD its OPEN_TERMINAL_DESKTOP_PASSWORD. Skipped
// otherwise. It checks that the gateway's basic auth and the WebSocket upgrade
// get through to KasmVNC.
func TestDesktopStreamRealKasmVNC(t *testing.T) {
	addr, pw := os.Getenv("TERMIPLUS_KASM_ADDR"), os.Getenv("TERMIPLUS_KASM_PASSWORD")
	if addr == "" || pw == "" {
		t.Skip("set TERMIPLUS_KASM_ADDR and TERMIPLUS_KASM_PASSWORD")
	}
	// Between gateway and KasmVNC: require the password the gateway derived
	// for the session, then swap in the real container's.
	var want atomic.Value
	want.Store("")
	kasm := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(&url.URL{Scheme: "http", Host: addr})
		pr.Out.Host = addr
		if pr.In.Header.Get("Authorization") == want.Load().(string) {
			pr.Out.SetBasicAuth("user", pw)
		} else {
			pr.Out.Header.Del("Authorization")
		}
	}}
	mid := httptest.NewServer(kasm)
	t.Cleanup(mid.Close)
	h := newHarness(t, func(o *harnessOpts) {
		o.gateway = func(c *Config) {
			c.ControlKey = testControlKey
			c.Policies["desk"] = Policy{Runtime: "runsc", MemoryMB: 1024, CPUs: 1, Pids: 256, Desktop: true}
			orig := c.AddrFor
			c.AddrFor = func(name string, port int) string {
				if port == desktopPort {
					return strings.TrimPrefix(mid.URL, "http://")
				}
				return orig(name, port)
			}
		}
	})
	_, b := h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"desk"}`)
	var s sessionInfo
	_ = json.Unmarshal([]byte(b), &s)
	want.Store("Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+h.g.mgr.desktopPassword(s.ID))))
	target := h.gw.URL + "/control/v1/stream/" + s.ID + "/"

	if code, _, _ := streamGet(t, h.gw.URL, s.ID+"/", "alice", nil); code != http.StatusOK {
		t.Fatalf("owner: want 200, got %d", code)
	}
	upgrade := func(origin string) int {
		conn, err := net.Dial("tcp", strings.TrimPrefix(h.gw.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		req, _ := http.NewRequest(http.MethodGet, target+"websockify", nil)
		for k, v := range map[string]string{
			controlKeyHeader: testControlKey, "X-User-Id": "alice",
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
	// KasmVNC wants an Origin but accepts any; the MCP server checks it.
	if code := upgrade("https://desk.example"); code != http.StatusSwitchingProtocols {
		t.Fatalf("WebSocket: want 101, got %d", code)
	}
}
