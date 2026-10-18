package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"termiplus/internal/mcp"
)

// fakeApp is a "dev server" listening on a forwarded port: it echoes what
// reached it, redirects on /go and accepts WebSocket upgrades on /ws.
func fakeApp(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/go":
			http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
		case "/ws":
			conn, brw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\nhello")
			_ = brw.Flush()
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{
				"path": r.URL.Path, "query": r.URL.RawQuery, "host": r.Host,
				"cookie": r.Header.Get("Cookie"), "user": r.Header.Get("X-User-Id"),
				"key": r.Header.Get(controlKeyHeader), "fwduser": r.Header.Get("X-Forwarded-User"),
			})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withForwardApp(app *httptest.Server) func(*harnessOpts) {
	return func(o *harnessOpts) {
		o.gateway = func(c *Config) {
			c.ControlKey = testControlKey
			orig := c.AddrFor
			c.AddrFor = func(name string, port int) string {
				if port == 3000 {
					return strings.TrimPrefix(app.URL, "http://")
				}
				return orig(name, port)
			}
		}
	}
}

func (h *harness) forward(t *testing.T, user, path string, hdr map[string]string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.gw.URL+"/control/v1/forward/"+path, nil)
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

func TestForward(t *testing.T) {
	h := newHarness(t, withForwardApp(fakeApp(t)))
	_, b := h.control(t, http.MethodPost, "sessions", testControlKey, "alice", `{"workspace":"default"}`)
	var s sessionInfo
	_ = json.Unmarshal([]byte(b), &s)
	h.control(t, http.MethodPost, "sessions", testControlKey, "bob", `{"workspace":"default"}`)

	code, _, body := h.forward(t, "alice", s.ID+"/3000/api/x?q=1", map[string]string{
		"Cookie":           "_termiplus_fwd=secret; app=1; _oauth2_proxy_csrf=x",
		"X-Forwarded-User": "kc-sub",
	})
	if code != http.StatusOK {
		t.Fatalf("owner: %d %s", code, body)
	}
	var seen map[string]string
	_ = json.Unmarshal([]byte(body), &seen)
	want := map[string]string{"path": "/api/x", "query": "q=1", "host": "localhost:3000", "cookie": "app=1", "user": "", "key": "", "fwduser": ""}
	for k, v := range want {
		if seen[k] != v {
			t.Errorf("app saw %s=%q, want %q", k, seen[k], v)
		}
	}

	for name, c := range map[string]struct {
		user, path string
		want       int
	}{
		"another user's container": {"bob", s.ID + "/3000/", http.StatusNotFound},
		"unknown container":        {"alice", "ot-u-0000000000000000-default/3000/", http.StatusNotFound},
		"not a container":          {"alice", "gateway/3000/", http.StatusNotFound},
		"Open Terminal's port":     {"alice", s.ID + "/8000/", http.StatusForbidden},
		"desktop port":             {"alice", s.ID + "/6901/", http.StatusForbidden},
		"port 0":                   {"alice", s.ID + "/0/", http.StatusBadRequest},
		"port too big":             {"alice", s.ID + "/70000/", http.StatusBadRequest},
		"padded port":              {"alice", s.ID + "/03000/", http.StatusBadRequest},
		"no port":                  {"alice", s.ID, http.StatusBadRequest},
		"no user":                  {"", s.ID + "/3000/", http.StatusBadRequest},
	} {
		if code, _, body := h.forward(t, c.user, c.path, nil); code != c.want {
			t.Errorf("%s: want %d, got %d %s", name, c.want, code, body)
		}
	}

	// Without the control key nothing is forwarded.
	req, _ := http.NewRequest(http.MethodGet, h.gw.URL+"/control/v1/forward/"+s.ID+"/3000/", nil)
	req.Header.Set("X-User-Id", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without control key: %d", resp.StatusCode)
	}

	// Redirects stay under the public prefix.
	prefix := map[string]string{forwardPrefixHeader: "/" + s.ID + "/3000"}
	for to, want := range map[string]string{
		"/login":                         "/" + s.ID + "/3000/login",
		"http://localhost:3000/dash?x=1": "/" + s.ID + "/3000/dash?x=1",
		"https://example.com/":           "https://example.com/",
	} {
		_, hdr, _ := h.forward(t, "alice", s.ID+"/3000/go?to="+to, prefix)
		if got := hdr.Get("Location"); got != want {
			t.Errorf("redirect to %s: Location %q, want %q", to, got, want)
		}
	}

	// WebSockets pass.
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.gw.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	wreq, _ := http.NewRequest(http.MethodGet, h.gw.URL+"/control/v1/forward/"+s.ID+"/3000/ws", nil)
	for k, v := range map[string]string{controlKeyHeader: testControlKey, "X-User-Id": "alice",
		"Connection": "Upgrade", "Upgrade": "websocket"} {
		wreq.Header.Set(k, v)
	}
	if err := wreq.Write(conn); err != nil {
		t.Fatal(err)
	}
	wresp, err := http.ReadResponse(bufio.NewReader(conn), wreq)
	if err != nil {
		t.Fatal(err)
	}
	if wresp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("websocket: %d", wresp.StatusCode)
	}
}

func TestRewriteLocation(t *testing.T) {
	const p = "/ot-u-1/3000"
	for in, want := range map[string]string{
		"/":                          p + "/",
		"/a/b?c=d":                   p + "/a/b?c=d",
		p + "/already":               p + "/already",
		"http://localhost:3000":      p + "/",
		"https://localhost:3000/x":   p + "/x",
		"http://localhost:30001/x":   "http://localhost:30001/x",
		"//evil.example/x":           "//evil.example/x",
		"relative/path":              "relative/path",
		"https://other.example/path": "https://other.example/path",
	} {
		if got := rewriteLocation(in, "localhost:3000", p); got != want {
			t.Errorf("rewriteLocation(%q) = %q, want %q", in, got, want)
		}
	}
}

// Full chain: browser identity (as fwd-auth forwards it) -> MCP forwarder ->
// gateway -> the user's app.
func TestForwardThroughMCP(t *testing.T) {
	h := newHarness(t, withForwardApp(fakeApp(t)))
	_, b := h.control(t, http.MethodPost, "sessions", testControlKey, "owui-alice", `{"workspace":"default"}`)
	var s sessionInfo
	_ = json.Unmarshal([]byte(b), &s)
	_, b = h.control(t, http.MethodPost, "sessions", testControlKey, "owui-bob", `{"workspace":"default"}`)
	var bob sessionInfo
	_ = json.Unmarshal([]byte(b), &bob)

	ids, err := mcp.OpenIdentityStore(filepath.Join(t.TempDir(), "ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = ids.Record("kc-alice", "owui-alice")
	fwd, err := mcp.NewForwarder(ids, h.gw.URL, testControlKey)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(fwd)
	t.Cleanup(front.Close)

	get := func(sub, path string, hdr map[string]string) (int, http.Header, string) {
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
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(body)
	}

	code, _, body := get("kc-alice", "/"+s.ID+"/3000/page?x=1", map[string]string{
		"X-User-Id": "owui-bob", "Cookie": "_termiplus_fwd=secret; app=1"})
	if code != http.StatusOK || !strings.Contains(body, `"path":"/page"`) || !strings.Contains(body, `"cookie":"app=1"`) {
		t.Fatalf("alice's own port: %d %s", code, body)
	}
	if code, _, _ := get("kc-alice", "/"+bob.ID+"/3000/", nil); code != http.StatusNotFound {
		t.Fatalf("bob's session for alice: %d", code)
	}
	if code, _, _ := get("kc-carol", "/"+s.ID+"/3000/", nil); code != http.StatusForbidden {
		t.Fatalf("unlinked user: %d", code)
	}
	if code, _, _ := get("", "/"+s.ID+"/3000/", nil); code != http.StatusUnauthorized {
		t.Fatalf("not signed in: %d", code)
	}
	if code, hdr, _ := get("kc-alice", "/"+s.ID+"/3000", nil); code != http.StatusFound || hdr.Get("Location") != "/"+s.ID+"/3000/" {
		t.Fatalf("missing slash: %d %q", code, hdr.Get("Location"))
	}
	if _, hdr, _ := get("kc-alice", "/"+s.ID+"/3000/go?to=/login", nil); hdr.Get("Location") != "/"+s.ID+"/3000/login" {
		t.Fatalf("redirect not kept under the prefix: %q", hdr.Get("Location"))
	}
	if code, _, _ := get("kc-alice", "/"+s.ID+"/abc/", nil); code != http.StatusBadRequest {
		t.Fatalf("bad port: %d", code)
	}
	// Only container names are sessions: the trailing-slash redirect must
	// never point to another site (security audit run 1).
	for _, p := range []string{"/%5Cevil.example/80", "/%2F%2Fevil.example/80", "/evil.example/80", "/./80"} {
		if code, hdr, _ := get("kc-alice", p, nil); code != http.StatusNotFound || hdr.Get("Location") != "" {
			t.Errorf("%s: %d %q", p, code, hdr.Get("Location"))
		}
	}
}
