package gateway

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"termiplus/internal/naming"
)

const testForwardURL = "https://fwd.example.test"

func withForwardURL(u string) func(*harnessOpts) {
	return func(o *harnessOpts) {
		o.gateway = func(c *Config) { c.ForwardPublicURL = u }
	}
}

// otProxy sends a request as Open WebUI would and does not follow redirects.
func (h *harness) otProxy(t *testing.T, method, path, user, chat string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest(method, h.gw.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	req.Header.Set("X-User-Id", user)
	if chat != "" {
		req.Header.Set("X-Session-Id", chat)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

func TestOTProxyRedirectsToForward(t *testing.T) {
	h := newHarness(t, withForwardURL(testForwardURL+"/"))
	if code, _ := h.get(t, "/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatalf("start: %d", code)
	}
	alice := naming.Container("alice", "default")

	code, loc, _ := h.otProxy(t, http.MethodGet, "/proxy/3000/app/a%20b?x=1&y=2", "alice", "")
	if want := testForwardURL + "/" + alice + "/3000/app/a%20b?x=1&y=2"; code != http.StatusTemporaryRedirect || loc != want {
		t.Fatalf("got %d %q, want 307 %q", code, loc, want)
	}
	code, loc, _ = h.otProxy(t, http.MethodGet, "/p/default/proxy/5173/", "alice", "")
	if want := testForwardURL + "/" + alice + "/5173/"; code != http.StatusTemporaryRedirect || loc != want {
		t.Fatalf("got %d %q, want 307 %q", code, loc, want)
	}

	cases := []struct {
		method, path, user string
		want               int
	}{
		{"GET", "/proxy/8000/", "alice", http.StatusForbidden},   // Open Terminal's API
		{"GET", "/proxy/6901/", "alice", http.StatusForbidden},   // desktop
		{"GET", "/proxy/abc/", "alice", http.StatusBadRequest},   // not a port
		{"GET", "/proxy/03000/", "alice", http.StatusBadRequest}, // not canonical
		{"GET", "/proxy/0/", "alice", http.StatusBadRequest},
		{"GET", "/proxy/65536/", "alice", http.StatusBadRequest},
		{"GET", "/proxy", "alice", http.StatusBadRequest},
		{"POST", "/proxy/3000/", "alice", http.StatusNotFound}, // no redirect for bodies
		{"PUT", "/proxy/3000/", "alice", http.StatusNotFound},
		{"GET", "/proxy/3000/", "bob", http.StatusNotFound}, // no running session
	}
	for _, c := range cases {
		code, loc, _ := h.otProxy(t, c.method, c.path, c.user, "")
		if code != c.want || loc != "" {
			t.Errorf("%s %s as %s: got %d %q, want %d", c.method, c.path, c.user, code, loc, c.want)
		}
	}
	if h.ctr(naming.Container("bob", "default")) != nil {
		t.Fatal("a port link must never start a session")
	}
}

func TestOTProxyEphemeralUsesChatSandbox(t *testing.T) {
	h := newHarness(t, withForwardURL(testForwardURL))
	if code, b := h.getChat(t, "/p/scratch/files/cwd", "alice", "chat-1", testAPIKey); code != 200 {
		t.Fatalf("start: %d %s", code, b)
	}
	code, loc, _ := h.otProxy(t, http.MethodGet, "/p/scratch/proxy/3000/", "alice", "chat-1")
	if want := testForwardURL + "/" + naming.Ephemeral("alice", "chat-1", "scratch") + "/3000/"; code != http.StatusTemporaryRedirect || loc != want {
		t.Fatalf("got %d %q, want 307 %q", code, loc, want)
	}
	// Another chat has no sandbox yet: nothing to forward to, nothing started.
	if code, _, _ := h.otProxy(t, http.MethodGet, "/p/scratch/proxy/3000/", "alice", "chat-2"); code != http.StatusNotFound {
		t.Fatalf("other chat: got %d, want 404", code)
	}
	if h.ctr(naming.Ephemeral("alice", "chat-2", "scratch")) != nil {
		t.Fatal("a port link must never start a sandbox")
	}
}

func TestOTProxyRefusedWithoutForwards(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.get(t, "/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatalf("start: %d", code)
	}
	for _, p := range []string{"/proxy/3000/", "/proxy/3000/x?y=1", "/p/default/proxy/8000/health", "/proxy"} {
		code, loc, body := h.otProxy(t, http.MethodGet, p, "alice", "")
		if code != http.StatusNotFound || loc != "" || !strings.Contains(body, "port forwards") {
			t.Errorf("%s: got %d %q %q, want 404 from the gateway", p, code, loc, body)
		}
	}
}

func TestOTProxyWebSocketRefused(t *testing.T) {
	h := newHarness(t, withForwardURL(testForwardURL))
	h.get(t, "/files/cwd", "alice", testAPIKey)
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.gw.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /proxy/3000/ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nX-User-Id: alice\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("got %d, want 404", resp.StatusCode)
	}
}

func TestOTProxyPortsListStillPassesThrough(t *testing.T) {
	h := newHarness(t, withForwardURL(testForwardURL))
	h.get(t, "/files/cwd", "alice", testAPIKey)
	// The fake terminal has no /ports handler: a 404 from it, not the gateway's.
	code, loc, body := h.otProxy(t, http.MethodGet, "/ports", "alice", "")
	if code != http.StatusNotFound || loc != "" || strings.Contains(body, "port forwards") {
		t.Fatalf("got %d %q %q, want the terminal's own answer", code, loc, body)
	}
}

func TestForwardPublicURLValidated(t *testing.T) {
	for _, u := range []string{"fwd.example.test", "ftp://fwd.example.test", "https://", "https://u:p@fwd.example.test",
		"https://fwd.example.test/?a=1", "https://fwd.example.test/#x", "javascript:alert(1)"} {
		cfg := Config{APIKey: testAPIKey, KeySecret: testKeySecret, Policies: map[string]Policy{
			"default": {Runtime: "runsc", MemoryMB: 1, CPUs: 1, Pids: 1}}, ForwardPublicURL: u}
		if _, err := New(cfg); err == nil {
			t.Errorf("%q: accepted", u)
		}
	}
}
