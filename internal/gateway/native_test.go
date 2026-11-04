package gateway

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
	"strings"
	"testing"
	"time"

	"termiplus/internal/naming"
)

func nativeHarness(t *testing.T) *harness {
	h := newHarness(t)
	pol := h.g.cfg.Policies["default"]
	pol.Storage = storageEphemeral
	h.g.cfg.Policies["default"] = pol
	return h
}

func nativeDial(t *testing.T, endpoint, path, user, chat, key string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	addr := strings.TrimPrefix(endpoint, "http://")
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	wsKey := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\nX-User-Id: %s\r\nX-Session-Id: %s\r\nAuthorization: Bearer %s\r\n\r\n", path, addr, wsKey, user, chat, key)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn, br, resp.StatusCode
}

func TestNativeFirstFrameSockets(t *testing.T) {
	h := nativeHarness(t)
	for _, path := range []string{"/desktop/stream", "/desktop/audio", "/events", "/execute", "/execute/proc/status", "/api/terminals/user"} {
		for _, user := range []string{"alice", "bob"} {
			conn, br, code := nativeDial(t, h.gw.URL, path, user, "", "ignored-header")
			if code != 101 {
				t.Fatalf("%s: %d", path, code)
			}
			raw, _ := json.Marshal(map[string]any{"type": "auth", "token": testAPIKey, "chat_id": "saved-chat"})
			if err := writeMaskedText(conn, raw); err != nil {
				t.Fatal(err)
			}
			if err := writeMaskedText(conn, []byte("native-payload")); err != nil {
				t.Fatal(err)
			}
			op, payload := readServerFrame(t, br)
			if op != 2 || string(payload) != user+":native-payload" {
				t.Fatalf("%s mixed tenant or frame: %q", path, payload)
			}
			conn.Close()
		}
	}
	h.docker.mu.Lock()
	defer h.docker.mu.Unlock()
	if h.docker.ctrs[naming.Ephemeral("alice", "saved-chat", "default")] == nil {
		t.Fatal("auth chat_id did not select native HTTP chat's sandbox")
	}
}

func TestNativeSocketChatValidationBeforeProvision(t *testing.T) {
	for _, chat := range []any{17, "../bad", "other-chat"} {
		t.Run(fmt.Sprint(chat), func(t *testing.T) {
			h := nativeHarness(t)
			conn, br, _ := nativeDial(t, h.gw.URL, "/desktop/stream", "alice", "header-chat", "")
			raw, _ := json.Marshal(map[string]any{"type": "auth", "token": testAPIKey, "chat_id": chat})
			_ = writeMaskedText(conn, raw)
			op, payload := readServerFrame(t, br)
			if op != 8 || binary.BigEndian.Uint16(payload[:2]) != 4001 {
				t.Fatalf("invalid context accepted: %d %q", op, payload)
			}
			h.docker.mu.Lock()
			defer h.docker.mu.Unlock()
			if h.docker.creates != 0 {
				t.Fatal("invalid chat spawned a sandbox")
			}
		})
	}
}

func TestNativeBrowserHeaderAuthAndNoInjectedFrame(t *testing.T) {
	h := nativeHarness(t)
	path := "/browser/view/session/capability/stream"
	for _, key := range []string{"", "wrong"} {
		_, _, code := nativeDial(t, h.gw.URL, path, "alice", "chat1", key)
		if code != 401 {
			t.Fatalf("browser upgrade accepted wrong key: %d", code)
		}
	}
	if h.docker.creates != 0 {
		t.Fatal("unauthorized browser created container")
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		user := r.Header.Get("X-User-Id")
		name := naming.Ephemeral(user, "chat1", "default")
		if r.Header.Get("Authorization") != "Bearer "+h.g.mgr.containerKey(name) {
			t.Error("container key not translated")
		}
		if r.Header.Get("Cookie") != "" {
			t.Error("browser cookie forwarded")
		}
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", acceptKey(r.Header.Get("Sec-WebSocket-Key")))
		_ = brw.Flush()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		frame, err := readClientTextFrame(brw.Reader, 4096)
		if err != nil {
			return
		}
		if string(frame) != `{"type":"frames"}` {
			t.Errorf("auth injected into browser data: %q", frame)
			return
		}
		payload := []byte(user + ":JPEG")
		_, _ = conn.Write(append([]byte{0x82, byte(len(payload))}, payload...))
	}))
	defer up.Close()
	h.g.mgr.addrFor = func(string, int) string { return strings.TrimPrefix(up.URL, "http://") }
	for _, user := range []string{"alice", "bob"} {
		conn, br, code := nativeDial(t, h.gw.URL, path, user, "chat1", testAPIKey)
		if code != 101 {
			t.Fatal(code)
		}
		_ = writeMaskedText(conn, []byte(`{"type":"frames"}`))
		op, payload := readServerFrame(t, br)
		if op != 2 || string(payload) != user+":JPEG" {
			t.Fatalf("wrong browser tenant or frame: %q", payload)
		}
		conn.Close()
	}
}

func TestNativeHTTPRoutesKeepIdentityAndContext(t *testing.T) {
	h := nativeHarness(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		user := r.Header.Get("X-User-Id")
		chat := r.Header.Get("X-Session-Id")
		name := naming.Ephemeral(user, chat, "default")
		if r.Header.Get("Authorization") != "Bearer "+h.g.mgr.containerKey(name) || r.Header.Get("Cookie") != "" {
			t.Error("upstream credentials not isolated")
		}
		fmt.Fprintf(w, "%s:%s:%s", user, chat, r.URL.RequestURI())
	}))
	defer up.Close()
	h.g.mgr.addrFor = func(string, int) string { return strings.TrimPrefix(up.URL, "http://") }
	for _, path := range []string{"/desktop", "/desktop/screenshot", "/browser/sessions", "/browser/view/session/cap/frame/https/example.test/", "/files/list", "/git/diff", "/ports", "/proxy/3000/index.html"} {
		for _, user := range []string{"alice", "bob"} {
			req, _ := http.NewRequest("GET", h.gw.URL+path, nil)
			req.Header.Set("Authorization", "Bearer "+testAPIKey)
			req.Header.Set("X-User-Id", user)
			req.Header.Set("X-Session-Id", "chat1")
			req.Header.Set("Cookie", "credential=secret")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || string(body) != user+":chat1:"+path {
				t.Fatalf("%s context lost: %d %q", path, resp.StatusCode, body)
			}
		}
	}
}
