package gateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"termiplus/internal/naming"
)

// A terminal that answers the WebSocket upgrade with an endless status line
// must not make the gateway buffer it (security audit run-1, ws.go).
func TestDialUpstreamBoundsHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		for {
			l, err := br.ReadString('\n')
			if err != nil || l == "\r\n" {
				break
			}
		}
		_, _ = io.WriteString(c, "HTTP/1.1 101 ")
		chunk := []byte(strings.Repeat("A", 64<<10))
		for i := 0; i < 64; i++ { // 4 MiB, never a line end
			if _, err := c.Write(chunk); err != nil {
				return
			}
		}
	}()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, _, err = dialUpstream(ln.Addr().String(), "/api/terminals/x", "", http.Header{})
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("oversized handshake accepted")
	}
	if len(err.Error()) > 200 {
		t.Fatalf("error carries upstream bytes: %d", len(err.Error()))
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 2<<20 {
		t.Fatalf("handshake allocated %d bytes", grew)
	}
}

// The /p/ terminal proxy refuses oversized response headers from a container.
func TestTerminalProxyBoundsResponseHeaders(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.get(t, "/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatal("start failed")
	}
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Pad", strings.Repeat("a", maxUpstreamHeaderBytes+1024))
		w.WriteHeader(200)
	}))
	defer big.Close()
	name := naming.Container("alice", "default")
	h.g.mgr.mu.Lock()
	h.g.mgr.instances[name].addr = strings.TrimPrefix(big.URL, "http://")
	h.g.mgr.mu.Unlock()
	if code, _ := h.get(t, "/files/cwd", "alice", testAPIKey); code != http.StatusBadGateway {
		t.Fatalf("oversized upstream headers: got %d", code)
	}
}

func countRunning(h *harness, prefix string) int {
	h.docker.mu.Lock()
	defer h.docker.mu.Unlock()
	n := 0
	for name, c := range h.docker.ctrs {
		if c.running && strings.HasPrefix(name, prefix) {
			n++
		}
	}
	return n
}

// Concurrent starts must respect MaxRunning (3) and MaxPerUser (2) like
// sequential ones do (security audit run-1, lifecycle.go).
func TestConcurrentStartsRespectCaps(t *testing.T) {
	h := newHarness(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.getChat(t, "/p/scratch/files/cwd", "alice", fmt.Sprintf("chat%02d", i), testAPIKey)
		}(i)
	}
	wg.Wait()
	if n := countRunning(h, naming.EphemeralPrefix+naming.UserHash(naming.Ephemeral("alice", "x", "scratch"))); n > 2 {
		t.Fatalf("alice runs %d sandboxes, cap is 2", n)
	}

	h = newHarness(t)
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _ := h.get(t, "/files/cwd", fmt.Sprintf("user%02d", i), testAPIKey)
			codes <- code
		}(i)
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == 200 {
			ok++
		} else if c != http.StatusServiceUnavailable {
			t.Errorf("unexpected status %d", c)
		}
	}
	if n := countRunning(h, naming.ContainerPrefix); n > 3 || ok > 3 || ok == 0 {
		t.Fatalf("running %d, ok %d; MaxRunning is 3", n, ok)
	}
}

// A container's /health that redirects must not make the gateway request the
// redirect target, and does not count as healthy (security audit run 2).
func TestHealthChecksDoNotFollowRedirects(t *testing.T) {
	marker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("health check followed a redirect to %s", r.URL)
	}))
	defer marker.Close()
	term := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, marker.URL+"/internal", http.StatusFound)
	}))
	defer term.Close()

	m := &manager{startWait: 700 * time.Millisecond}
	inst := &instance{name: "t", addr: strings.TrimPrefix(term.URL, "http://")}
	if m.probe(context.Background(), inst) {
		t.Fatal("probe: a redirect counted as healthy")
	}
	if err := m.waitReady(context.Background(), inst); err == nil {
		t.Fatal("waitReady: a redirect counted as ready")
	}
}
