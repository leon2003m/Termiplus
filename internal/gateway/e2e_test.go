package gateway

// End-to-end test of the full chain without Docker:
//   test client -> gateway -> sockproxy (real) -> fake dockerd (unix socket)
//                         \-> fake Open Terminal instances (one per container)

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"termiplus/internal/dockerapi"
	"termiplus/internal/naming"
	"termiplus/internal/sockproxy"
)

const (
	testAPIKey    = "owui-to-gateway-key-0123456789abcdef"
	testKeySecret = "per-container-key-secret-0123456789ab"
	testImage     = "registry.local/ot-terminal@sha256:test"
)

// --- fake Open Terminal -------------------------------------------------------

func fakeTerminal(t *testing.T, apiKey string) *httptest.Server {
	mux := http.NewServeMux()
	auth := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+apiKey }
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/files/cwd", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"user": r.Header.Get("X-User-Id"), "key": apiKey[:8]})
	})
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"openapi":"3.1.0"}`)
	})
	mux.HandleFunc("/api/terminals/", func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", acceptKey(r.Header.Get("Sec-WebSocket-Key")))
		brw.Flush()
		p, err := readClientTextFrame(brw.Reader, 4096)
		if err != nil {
			return
		}
		var msg map[string]string
		_ = json.Unmarshal(p, &msg)
		if msg["token"] != apiKey || r.Header.Get("X-User-Id") == "" {
			writeClose(conn, 4001, "Invalid API key")
			return
		}
		// echo frames back unmasked, prefixed with the user id
		for {
			data, err := readAnyClientFrame(brw.Reader)
			if err != nil {
				return
			}
			out := []byte(r.Header.Get("X-User-Id") + ":" + string(data))
			conn.Write(append([]byte{0x82, byte(len(out))}, out...))
		}
	})
	return httptest.NewServer(mux)
}

func readAnyClientFrame(r io.Reader) ([]byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	n := int(h[1] & 0x7f)
	var mask [4]byte
	if _, err := io.ReadFull(r, mask[:]); err != nil {
		return nil, err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	for i := range b {
		b[i] ^= mask[i%4]
	}
	return b, nil
}

// --- fake dockerd -------------------------------------------------------------

type fakeDocker struct {
	t       *testing.T
	mu      sync.Mutex
	ctrs    map[string]*fakeCtr          // by name
	volumes map[string]map[string]string // name -> labels
	creates int
	addrs   map[string]string
	seen    []string // raw create bodies received from the proxy
}

type fakeCtr struct {
	id      string
	name    string
	body    map[string]any
	running bool
	srv     *httptest.Server
}

func (d *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := r.URL.Path
	switch {
	case p == "/volumes/create":
		var v map[string]any
		_ = json.NewDecoder(r.Body).Decode(&v)
		labels := map[string]string{}
		if l, ok := v["Labels"].(map[string]any); ok {
			for k, val := range l {
				labels[k], _ = val.(string)
			}
		}
		d.volumes[v["Name"].(string)] = labels
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "{}")
	case strings.HasPrefix(p, "/volumes/"):
		name := strings.TrimPrefix(p, "/volumes/")
		labels, ok := d.volumes[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"no such volume"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Name": name, "Labels": labels})
	case p == "/containers/create":
		raw, _ := io.ReadAll(r.Body)
		d.seen = append(d.seen, string(raw))
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		name := r.URL.Query().Get("name")
		d.creates++
		id := fmt.Sprintf("%064x", d.creates)
		d.ctrs[name] = &fakeCtr{id: id, name: name, body: b}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"Id":%q}`, id)
	case p == "/containers/json":
		var out []map[string]any
		for _, c := range d.ctrs {
			st := "exited"
			if c.running {
				st = "running"
			}
			out = append(out, map[string]any{"Id": c.id, "Names": []string{"/" + c.name}, "State": st, "Labels": c.body["Labels"]})
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(p, "/containers/"):
		parts := strings.Split(strings.TrimPrefix(p, "/containers/"), "/")
		c := d.find(parts[0])
		if c == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"No such container"}`)
			return
		}
		switch {
		case len(parts) == 2 && parts[1] == "json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": c.id, "Name": "/" + c.name,
				"State":  map[string]any{"Running": c.running},
				"Config": map[string]any{"Image": c.body["Image"], "Labels": c.body["Labels"]},
			})
		case len(parts) == 2 && parts[1] == "start":
			if !c.running {
				key := strings.TrimPrefix(c.body["Env"].([]any)[0].(string), "OPEN_TERMINAL_API_KEY=")
				c.srv = fakeTerminal(d.t, key)
				d.addrs[c.name] = strings.TrimPrefix(c.srv.URL, "http://")
				c.running = true
			}
			w.WriteHeader(http.StatusNoContent)
		case len(parts) == 2 && parts[1] == "stop":
			if c.running {
				c.srv.Close()
				c.running = false
			}
			if hc, _ := c.body["HostConfig"].(map[string]any); hc["AutoRemove"] == true {
				delete(d.ctrs, c.name)
			}
			w.WriteHeader(http.StatusNoContent)
		case len(parts) == 1 && r.Method == http.MethodDelete:
			if c.running {
				c.srv.Close()
			}
			delete(d.ctrs, c.name)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		d.t.Errorf("fake dockerd got unexpected %s %s", r.Method, p)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (d *fakeDocker) find(ref string) *fakeCtr {
	if c, ok := d.ctrs[ref]; ok {
		return c
	}
	for _, c := range d.ctrs {
		if c.id == ref {
			return c
		}
	}
	return nil
}

// --- harness ------------------------------------------------------------------

type harness struct {
	gw     *httptest.Server
	g      *Gateway
	docker *fakeDocker
	proxy  *httptest.Server
}

// harnessOpts lets a test tweak the socket-proxy rules and gateway config
// before they are constructed (e.g. to enable the DNS forwarder).
type harnessOpts struct {
	rules   func(*sockproxy.Rules)
	gateway func(*Config)
}

func newHarness(t *testing.T, opts ...func(*harnessOpts)) *harness {
	var o harnessOpts
	for _, fn := range opts {
		fn(&o)
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fd := &fakeDocker{t: t, ctrs: map[string]*fakeCtr{}, volumes: map[string]map[string]string{}, addrs: map[string]string{}}
	dsrv := &http.Server{Handler: fd}
	go dsrv.Serve(l)
	t.Cleanup(func() {
		dsrv.Close()
		os.Remove(sock)
		fd.mu.Lock()
		for _, c := range fd.ctrs {
			if c.running {
				c.srv.Close()
			}
		}
		fd.mu.Unlock()
	})

	rules := sockproxy.Rules{
		Image: testImage, Runtimes: map[string]bool{"runsc": true, "sysbox-runc": true},
		Network: "ot-sandbox", MaxMemory: 8 << 30, MaxNanoCPUs: 4e9, MaxPids: 2048,
	}
	if o.rules != nil {
		o.rules(&rules)
	}
	proxy := httptest.NewServer(sockproxy.New(rules, sock, "proxy-token"))
	t.Cleanup(proxy.Close)

	cfg := Config{
		APIKey: testAPIKey, KeySecret: testKeySecret, Image: testImage, Network: "ot-sandbox", Port: 8000,
		Policies: map[string]Policy{
			"default": {Runtime: "runsc", MemoryMB: 1024, CPUs: 1, Pids: 256},
			"sysbox":  {Runtime: "sysbox-runc", MemoryMB: 2048, CPUs: 2, Pids: 512, Home: "default"},
			"scratch": {Runtime: "runsc", MemoryMB: 1024, CPUs: 1, Pids: 256, Storage: "ephemeral", IdleTimeout: "10m", MaxPerUser: 2},
		},
		StartWait: 5 * time.Second, IdleTimeout: time.Hour, MaxRunning: 3,
		Docker: dockerapi.New(proxy.URL, "proxy-token"),
		AddrFor: func(name string, _ int) string {
			fd.mu.Lock()
			defer fd.mu.Unlock()
			return fd.addrs[name]
		},
	}
	if o.gateway != nil {
		o.gateway(&cfg)
	}
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(g)
	t.Cleanup(gw.Close)
	return &harness{gw: gw, g: g, docker: fd, proxy: proxy}
}

func (h *harness) get(t *testing.T, path, user, key string) (int, string) {
	return h.getChat(t, path, user, "", key)
}

func (h *harness) getChat(t *testing.T, path, user, chat, key string) (int, string) {
	req, _ := http.NewRequest(http.MethodGet, h.gw.URL+path, nil)
	if chat != "" {
		req.Header.Set("X-Session-Id", chat)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
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

// --- tests --------------------------------------------------------------------

func TestPerUserRouting(t *testing.T) {
	h := newHarness(t)

	code, body := h.get(t, "/files/cwd", "alice", testAPIKey)
	if code != 200 || !strings.Contains(body, `"user":"alice"`) {
		t.Fatalf("alice: %d %s", code, body)
	}
	code, body2 := h.get(t, "/files/cwd", "bob", testAPIKey)
	if code != 200 || !strings.Contains(body2, `"user":"bob"`) {
		t.Fatalf("bob: %d %s", code, body2)
	}
	// different containers => different per-container keys
	if body[strings.Index(body, `"key"`):] == body2[strings.Index(body2, `"key"`):] {
		t.Fatal("alice and bob share a container key")
	}
	// second request for alice reuses the container
	h.get(t, "/files/cwd", "alice", testAPIKey)
	if h.docker.creates != 2 {
		t.Fatalf("expected 2 containers, got %d", h.docker.creates)
	}
	// the body the daemon received was rebuilt by the proxy
	for _, raw := range h.docker.seen {
		if !strings.Contains(raw, `"Privileged":false`) || !strings.Contains(raw, `"Runtime":"runsc"`) {
			t.Fatalf("unexpected create body: %s", raw)
		}
	}
}

func TestAuthAndInputChecks(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.get(t, "/files/cwd", "alice", ""); code != 401 {
		t.Fatalf("no key: %d", code)
	}
	if code, _ := h.get(t, "/files/cwd", "alice", "wrong-key-wrong-key-wrong-key-wrong"); code != 401 {
		t.Fatalf("wrong key: %d", code)
	}
	if code, _ := h.get(t, "/files/cwd", "", testAPIKey); code != 400 {
		t.Fatalf("no user: %d", code)
	}
	if code, _ := h.get(t, "/files/cwd", "a b/c", testAPIKey); code != 400 {
		t.Fatalf("bad user: %d", code)
	}
	if code, _ := h.get(t, "/files/cwd", specUser, testAPIKey); code != 400 {
		t.Fatalf("spec identity via header must be refused: %d", code)
	}
	if code, _ := h.get(t, "/p/nope/files/cwd", "alice", testAPIKey); code != 404 {
		t.Fatalf("unknown policy: %d", code)
	}
	if code, _ := h.get(t, "/api/v1/policies", "", testAPIKey); code != 404 {
		t.Fatalf("policies api must not exist: %d", code)
	}
	if h.docker.creates != 0 {
		t.Fatalf("rejected requests created %d containers", h.docker.creates)
	}
}

func TestUserlessSpecRequests(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.get(t, "/health", "", testAPIKey); code != 200 {
		t.Fatalf("health: %d", code)
	}
	if h.docker.creates != 0 {
		t.Fatal("health must not spawn a container")
	}
	code, body := h.get(t, "/openapi.json", "", testAPIKey)
	if code != 200 || !strings.Contains(body, "openapi") {
		t.Fatalf("openapi: %d %s", code, body)
	}
	// user-less requests to anything else are refused
	if code, _ := h.get(t, "/files/cwd", "", testAPIKey); code != 400 {
		t.Fatalf("user-less files: %d", code)
	}
}

func TestRejectsNonCanonicalPath(t *testing.T) {
	h := newHarness(t)
	// ".." and "//" must be refused before routing, so a terminal that
	// normalizes them cannot be reached at the blocked /api/v1 admin path.
	for _, p := range []string{
		"/x/../api/v1/policies",
		"/p/default/x/../api/v1/x",
		"/files//cwd",
		"/..",
	} {
		if code, _ := h.get(t, p, "alice", testAPIKey); code != 400 {
			t.Fatalf("%s: got %d, want 400", p, code)
		}
	}
	// a single trailing slash is still allowed (not a 400 from the guard)
	if code, _ := h.get(t, "/files/cwd/", "alice", testAPIKey); code == 400 {
		t.Fatal("legitimate trailing slash was rejected")
	}
}

func TestSpecContainerSharedPerRuntime(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/openapi.json", "/p/scratch/openapi.json", "/p/sysbox/openapi.json"} {
		if code, body := h.get(t, path, "", testAPIKey); code != 200 {
			t.Fatalf("%s: %d %s", path, code, body)
		}
	}
	// default and scratch both use runsc: one shared spec container in the
	// preferred "default" policy, no ephemeral spec sandbox. sysbox keeps its own.
	if h.ctr(naming.Container(specUser, "default")) == nil || h.ctr(naming.Container(specUser, "sysbox")) == nil {
		t.Fatal("expected spec containers for default (runsc) and sysbox")
	}
	if h.ctr(naming.Ephemeral(specUser, "spec", "scratch")) != nil {
		t.Fatal("scratch must reuse the runsc spec container")
	}
	if h.docker.creates != 2 {
		t.Fatalf("want 2 spec containers, got %d creates", h.docker.creates)
	}
}

func TestSpecPolicies(t *testing.T) {
	got := specPolicies(map[string]Policy{
		"default": {Runtime: "runsc"},
		"scratch": {Runtime: "runsc", Storage: storageEphemeral},
		"zz":      {Runtime: "sysbox-runc", Storage: storageEphemeral},
		"desk":    {Runtime: "sysbox-runc", Storage: storagePersistent},
	})
	want := map[string]string{"default": "default", "scratch": "default", "zz": "desk", "desk": "desk"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("spec policy for %s: got %q want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func TestPolicySharesHome(t *testing.T) {
	h := newHarness(t)
	if code, b := h.get(t, "/p/sysbox/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatalf("sysbox policy: %d %s", code, b)
	}
	c := h.docker.ctrs[naming.Container("alice", "sysbox")]
	if c == nil {
		t.Fatal("sysbox container missing")
	}
	hc := c.body["HostConfig"].(map[string]any)
	m := hc["Mounts"].([]any)[0].(map[string]any)
	if hc["Runtime"] != "sysbox-runc" || m["Source"] != naming.Volume("alice", "default") {
		t.Fatalf("unexpected sysbox container: %v", hc)
	}
}

// A persistent volume whose name matches but whose owner label is a different
// user (e.g. a 64-bit user-hash collision) must not be reused. Mirrors the
// container ownership check. Regression test for #16.
func TestPersistentVolumeOwnerMismatchRefused(t *testing.T) {
	h := newHarness(t)
	vol := naming.Volume("alice", "default")
	h.docker.mu.Lock()
	h.docker.volumes[vol] = map[string]string{naming.LabelUser: "mallory"}
	h.docker.mu.Unlock()

	if code, _ := h.get(t, "/p/sysbox/files/cwd", "alice", testAPIKey); code == 200 {
		t.Fatalf("expected refusal reusing another user's volume, got 200")
	}

	h.docker.mu.Lock()
	owner := h.docker.volumes[vol][naming.LabelUser]
	_, created := h.docker.ctrs[naming.Container("alice", "sysbox")]
	h.docker.mu.Unlock()
	if owner != "mallory" {
		t.Fatalf("foreign volume owner changed to %q", owner)
	}
	if created {
		t.Fatal("container was created despite volume ownership mismatch")
	}
}

func TestCapacityLimit(t *testing.T) {
	h := newHarness(t)
	for _, u := range []string{"u1", "u2", "u3"} {
		if code, b := h.get(t, "/files/cwd", u, testAPIKey); code != 200 {
			t.Fatalf("%s: %d %s", u, code, b)
		}
	}
	if code, _ := h.get(t, "/files/cwd", "u4", testAPIKey); code != 503 {
		t.Fatalf("expected 503 at capacity, got %d", code)
	}
}

func TestIdleReapAndRestart(t *testing.T) {
	h := newHarness(t)
	h.get(t, "/files/cwd", "alice", testAPIKey)
	h.g.mgr.mu.Lock()
	for _, inst := range h.g.mgr.instances {
		inst.idle = time.Nanosecond
	}
	h.g.mgr.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	h.g.mgr.reap(t.Context())
	if c := h.docker.ctrs[naming.Container("alice", "default")]; c == nil || c.running {
		t.Fatal("container should exist and be stopped")
	}
	if code, b := h.get(t, "/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatalf("restart after reap: %d %s", code, b)
	}
	if h.docker.creates != 1 {
		t.Fatalf("restart must reuse the container, creates=%d", h.docker.creates)
	}
}

func TestConfigChangeRecreates(t *testing.T) {
	h := newHarness(t)
	h.get(t, "/files/cwd", "alice", testAPIKey)
	h.g.mgr.invalidate(naming.Container("alice", "default"))
	h.g.cfg.Policies["default"] = Policy{Runtime: "runsc", MemoryMB: 1536, CPUs: 1, Pids: 256}
	if code, b := h.get(t, "/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if h.docker.creates != 2 {
		t.Fatalf("config change should recreate, creates=%d", h.docker.creates)
	}
}

// --- websocket ----------------------------------------------------------------

func wsDial(t *testing.T, gwURL, path, user string) (net.Conn, *bufio.Reader) {
	addr := strings.TrimPrefix(gwURL, "http://")
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	fmt.Fprintf(c, "GET %s?user_id=%s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\nX-User-Id: %s\r\nX-Session-Id: chat1\r\n\r\n", path, user, addr, key, user)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		t.Fatalf("handshake failed: %v %v", err, resp)
	}
	return c, br
}

func readServerFrame(t *testing.T, br *bufio.Reader) (byte, []byte) {
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, h[1]&0x7f)
	if _, err := io.ReadFull(br, b); err != nil {
		t.Fatal(err)
	}
	return h[0] & 0x0f, b
}

func TestWebSocketAuthRewrite(t *testing.T) {
	h := newHarness(t)
	c, br := wsDial(t, h.gw.URL, "/api/terminals/sess1", "alice")
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))

	auth, _ := json.Marshal(map[string]string{"type": "auth", "token": testAPIKey})
	if err := writeMaskedText(c, auth); err != nil {
		t.Fatal(err)
	}
	// a binary keystroke frame, masked
	payload := []byte("ls\n")
	mask := []byte{1, 2, 3, 4}
	frame := []byte{0x82, 0x80 | byte(len(payload))}
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	c.Write(frame)

	op, got := readServerFrame(t, br)
	if op != 0x2 || string(got) != "alice:ls\n" {
		t.Fatalf("echo: op=%d %q", op, got)
	}
}

func TestWebSocketBadToken(t *testing.T) {
	h := newHarness(t)
	c, br := wsDial(t, h.gw.URL, "/api/terminals/sess1", "alice")
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	auth, _ := json.Marshal(map[string]string{"type": "auth", "token": "nope"})
	writeMaskedText(c, auth)
	op, p := readServerFrame(t, br)
	if op != 0x8 || binary.BigEndian.Uint16(p[:2]) != 4001 {
		t.Fatalf("expected close 4001, got op=%d %q", op, p)
	}
	if h.docker.creates != 0 {
		t.Fatal("unauthenticated websocket must not spawn a container")
	}
}

func TestProxyBlocksDirectAbuse(t *testing.T) {
	h := newHarness(t)
	h.get(t, "/files/cwd", "alice", testAPIKey) // one managed container exists
	// an unmanaged container the attacker would like to touch
	h.docker.mu.Lock()
	h.docker.ctrs["traefik"] = &fakeCtr{id: fmt.Sprintf("%064x", 999), name: "traefik", body: map[string]any{"Labels": map[string]any{}}}
	h.docker.mu.Unlock()

	do := func(method, path, body string, token string) int {
		req, _ := http.NewRequest(method, h.proxy.URL+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("X-Proxy-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	alice := naming.Container("alice", "default")
	cases := []struct {
		method, path, body, token string
		want                      int
	}{
		{"GET", "/containers/json", "", "", 401},
		{"POST", "/containers/create?name=" + naming.Container("eve", "default"), `{"Image":"alpine","HostConfig":{"Privileged":true}}`, "proxy-token", 403},
		{"POST", "/v1.47/containers/" + alice + "/exec", `{"Cmd":["sh"],"Privileged":true}`, "proxy-token", 403},
		{"POST", "/containers/" + fmt.Sprintf("%064x", 999) + "/stop", "", "proxy-token", 403},
		{"GET", "/containers/traefik/json", "", "proxy-token", 403},
		{"POST", "/images/create?fromImage=alpine", "", "proxy-token", 403},
		{"POST", "/build", "", "proxy-token", 403},
		{"DELETE", "/containers/" + alice + "?v=1", "", "proxy-token", 403},
		{"POST", "/volumes/create", `{"Name":"` + naming.Volume("eve", "x") + `","DriverOpts":{"o":"bind","device":"/"}}`, "proxy-token", 403},
		{"GET", "/containers/" + alice + "/json", "", "proxy-token", 200},
	}
	for _, c := range cases {
		if got := do(c.method, c.path, c.body, c.token); got != c.want {
			t.Errorf("%s %s: got %d want %d", c.method, c.path, got, c.want)
		}
	}
}

// --- storage profiles ---------------------------------------------------------

func (h *harness) ctr(name string) *fakeCtr {
	h.docker.mu.Lock()
	defer h.docker.mu.Unlock()
	return h.docker.ctrs[name]
}

func TestEphemeralPerChat(t *testing.T) {
	h := newHarness(t)
	for _, chat := range []string{"chat-1", "chat-2", "chat-1"} {
		if code, b := h.getChat(t, "/p/scratch/files/cwd", "alice", chat, testAPIKey); code != 200 {
			t.Fatalf("%s: %d %s", chat, code, b)
		}
	}
	if h.docker.creates != 2 {
		t.Fatalf("expected one sandbox per chat (2), got %d", h.docker.creates)
	}
	c := h.ctr(naming.Ephemeral("alice", "chat-1", "scratch"))
	if c == nil {
		t.Fatal("sandbox for chat-1 missing")
	}
	hc := c.body["HostConfig"].(map[string]any)
	if hc["AutoRemove"] != true || hc["Mounts"] != nil {
		t.Fatalf("ephemeral sandbox must auto-remove and mount nothing: %v", hc)
	}
	if c.body["Labels"].(map[string]any)[naming.LabelStorage] != "ephemeral" {
		t.Fatal("storage label missing")
	}
	// the persistent policy is untouched by chats: one container regardless
	h.getChat(t, "/files/cwd", "alice", "chat-1", testAPIKey)
	h.getChat(t, "/files/cwd", "alice", "chat-2", testAPIKey)
	if h.docker.creates != 3 {
		t.Fatalf("persistent policy must ignore the chat, creates=%d", h.docker.creates)
	}
}

func TestEphemeralDeletedPersistentStoppedOnIdle(t *testing.T) {
	h := newHarness(t)
	h.getChat(t, "/p/scratch/files/cwd", "alice", "chat-1", testAPIKey)
	h.get(t, "/files/cwd", "alice", testAPIKey)
	h.g.mgr.mu.Lock()
	for _, inst := range h.g.mgr.instances {
		inst.idle = time.Nanosecond
	}
	h.g.mgr.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	h.g.mgr.reap(t.Context())
	if h.ctr(naming.Ephemeral("alice", "chat-1", "scratch")) != nil {
		t.Fatal("idle sandbox should be deleted")
	}
	if c := h.ctr(naming.Container("alice", "default")); c == nil || c.running {
		t.Fatal("idle persistent container should be stopped, not deleted")
	}
	// coming back to the same chat gives a fresh sandbox
	if code, _ := h.getChat(t, "/p/scratch/files/cwd", "alice", "chat-1", testAPIKey); code != 200 {
		t.Fatal("fresh sandbox not created")
	}
}

func TestEphemeralPerUserCap(t *testing.T) {
	h := newHarness(t)
	h.g.mgr.maxRunning = 0
	h.getChat(t, "/p/scratch/files/cwd", "alice", "c1", testAPIKey)
	time.Sleep(2 * time.Millisecond)
	h.getChat(t, "/p/scratch/files/cwd", "alice", "c2", testAPIKey)
	// bob's sandboxes do not count against alice
	h.getChat(t, "/p/scratch/files/cwd", "bob", "c1", testAPIKey)
	// third chat for alice: the least recently used (c1) is deleted
	if code, b := h.getChat(t, "/p/scratch/files/cwd", "alice", "c3", testAPIKey); code != 200 {
		t.Fatalf("c3: %d %s", code, b)
	}
	if h.ctr(naming.Ephemeral("alice", "c1", "scratch")) != nil {
		t.Fatal("LRU sandbox c1 should have been deleted")
	}
	if h.ctr(naming.Ephemeral("alice", "c2", "scratch")) == nil || h.ctr(naming.Ephemeral("bob", "c1", "scratch")) == nil {
		t.Fatal("wrong sandbox deleted")
	}
	// if all of alice's sandboxes have an open terminal, a new one is refused
	h.g.mgr.mu.Lock()
	for _, inst := range h.g.mgr.instances {
		inst.conns = 1
	}
	h.g.mgr.mu.Unlock()
	if code, _ := h.getChat(t, "/p/scratch/files/cwd", "alice", "c4", testAPIKey); code != 429 {
		t.Fatalf("expected 429 when every sandbox is in use, got %d", code)
	}
}

func TestRemovedContainerIsRecreated(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.get(t, "/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatal("first request failed")
	}
	// remove the container behind the gateway's back
	name := naming.Container("alice", "default")
	h.docker.mu.Lock()
	c := h.docker.ctrs[name]
	c.srv.Close()
	delete(h.docker.ctrs, name)
	h.docker.mu.Unlock()
	// make the cached instance look idle so the next request probes it
	h.g.mgr.mu.Lock()
	h.g.mgr.instances[name].lastUsed = time.Now().Add(-time.Minute)
	h.g.mgr.mu.Unlock()

	if code, b := h.get(t, "/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatalf("expected transparent recreate, got %d %s", code, b)
	}
	if h.docker.creates != 2 {
		t.Fatalf("container should have been recreated, creates=%d", h.docker.creates)
	}
}
