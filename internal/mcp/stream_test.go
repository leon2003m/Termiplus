//go:build !noshare

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type screenWriteObserver struct {
	http.ResponseWriter
	started chan struct{}
	once    sync.Once
}

func (w *screenWriteObserver) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return w.ResponseWriter.Write(p)
}

func (w *screenWriteObserver) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func screenFixture(t *testing.T) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 400, 400))
	x := uint32(1)
	for i := 0; i < len(im.Pix); i += 4 {
		for j := 0; j < 3; j++ {
			x ^= x << 13
			x ^= x >> 17
			x ^= x << 5
			im.Pix[i+j] = byte(x)
		}
		im.Pix[i+3] = 255
	}
	var data bytes.Buffer
	if err := png.Encode(&data, im); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestScreenWriteEndsOnRevocationAndDeadline(t *testing.T) {
	data := screenFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(controlKeyHeaderMCP) != "obviously-fake-control-key" {
			t.Error("wrong control key")
		}
		_, _ = w.Write(data)
	}))
	defer upstream.Close()
	for _, cause := range []string{"revocation", "deadline", "expiry", "write-timeout"} {
		t.Run(cause, func(t *testing.T) {
			shares, err := OpenShareStore("")
			if err != nil {
				t.Fatal(err)
			}
			sh, token, err := shares.Create("dummy-owner", "dummy-session", "dummy-workspace", time.Hour, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if cause == "expiry" {
				shares.mu.Lock()
				sh.Expires = time.Now().Add(2 * time.Second)
				shares.shares[sh.Hash] = sh
				shares.mu.Unlock()
			}
			handler, err := NewDesktopHandler(&Server{shares: shares}, nil, upstream.URL, "obviously-fake-control-key")
			if err != nil {
				t.Fatal(err)
			}
			started, done := make(chan struct{}), make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				if cause == "deadline" {
					ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
					defer cancel()
					r = r.WithContext(ctx)
				}
				handler.ServeHTTP(&screenWriteObserver{ResponseWriter: w, started: started}, r)
			}))
			server.Config.ConnState = func(c net.Conn, state http.ConnState) {
				if tcp, ok := c.(*net.TCPConn); ok && state == http.StateNew {
					_ = tcp.SetWriteBuffer(1024)
				}
			}
			server.Start()
			defer server.Close()
			conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.(*net.TCPConn).SetReadBuffer(1024)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprintf(conn, "GET /share/%s/screen.png HTTP/1.1\r\nHost: example.invalid\r\nX-Forwarded-User: dummy-viewer\r\nConnection: close\r\n\r\n", token); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			status, err := reader.ReadString('\n')
			if err != nil || !strings.Contains(status, "200 OK") {
				t.Fatalf("response %q: %v", status, err)
			}
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if line == "\r\n" {
					break
				}
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("handler did not start writing")
			}
			select {
			case <-done:
				t.Fatal("fixture did not hold a blocked downstream write")
			case <-time.After(50 * time.Millisecond):
			}
			if cause == "revocation" {
				if err := shares.Revoke(sh.Owner, sh.ID); err != nil {
					t.Fatal(err)
				}
				if _, ok := shares.Lookup(token); ok {
					t.Fatal("revoked share still valid")
				}
			}
			select {
			case <-done: // peer remains open and does not read the PNG
			case <-time.After(3 * time.Second):
				t.Fatal("screen write outlived " + cause)
			}
		})
	}
}

func TestScreenAdmissionRejectsBeforeFetchAndReleasesOnError(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "slow") {
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write(pngData.Bytes())
	}))
	defer upstream.Close()
	shares, err := OpenShareStore("")
	if err != nil {
		t.Fatal(err)
	}
	tokens := make(map[string]string)
	for _, owner := range []string{"a", "b", "c", "d", "e"} {
		_, token, err := shares.Create(owner, "slow", "dummy", time.Hour, "", "")
		if err != nil {
			t.Fatal(err)
		}
		tokens[owner] = token
	}
	_, fast, err := shares.Create("e", "fast", "dummy", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewDesktopHandler(&Server{shares: shares}, nil, upstream.URL, "obviously-fake-control-key")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	defer unblock() // release pending handlers before either server closes on failure
	get := func(token, viewer string) (int, error) {
		req, err := http.NewRequest(http.MethodGet, server.URL+"/share/"+token+"/screen.png", nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("X-Forwarded-User", viewer)
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		_, err = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, err
	}
	pending := make(chan int, 4)
	start := func(owner, viewer string) {
		go func() {
			code, _ := get(tokens[owner], viewer)
			pending <- code
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("admitted request did not reach upstream")
		}
	}
	start("a", "viewer-a")
	for _, pair := range [][2]string{{tokens["b"], "viewer-a"}} {
		if code, err := get(pair[0], pair[1]); err != nil || code != http.StatusTooManyRequests {
			t.Fatalf("duplicate principal admitted: %d, %v", code, err)
		}
	}
	start("a", "viewer-b") // two viewers may share an owner
	if code, err := get(tokens["a"], "viewer-e"); err != nil || code != http.StatusTooManyRequests {
		t.Fatalf("third viewer consumed the owner's reserved capacity: %d, %v", code, err)
	}
	if code, err := get(fast, "viewer-e"); err != nil || code != http.StatusOK {
		t.Fatalf("one slow owner blocked another owner's share: %d, %v", code, err)
	}
	start("c", "viewer-c")
	start("d", "viewer-d")
	if code, err := get(fast, "viewer-e"); err != nil || code != http.StatusTooManyRequests {
		t.Fatalf("global capacity exceeded: %d, %v", code, err)
	}
	if calls.Load() != 5 {
		t.Fatal("rejected request reached the image upstream")
	}
	unblock()
	for range 4 {
		select {
		case code := <-pending:
			if code != http.StatusBadGateway {
				t.Fatalf("upstream failure: %d", code)
			}
		case <-time.After(time.Second):
			t.Fatal("failed request did not release")
		}
	}
	if code, err := get(fast, "viewer-e"); err != nil || code != http.StatusOK {
		t.Fatalf("capacity not reusable after errors: %d, %v", code, err)
	}
}

func TestTwoSlowShareViewersLeaveCapacityForThird(t *testing.T) {
	data := screenFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) }))
	defer upstream.Close()
	shares, err := OpenShareStore("")
	if err != nil {
		t.Fatal(err)
	}
	var tokens []string
	for _, owner := range []string{"a", "b", "c"} {
		_, token, err := shares.Create(owner, "dummy-session", "dummy-workspace", time.Hour, "", "")
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	handler, err := NewDesktopHandler(&Server{shares: shares}, nil, upstream.URL, "obviously-fake-control-key")
	if err != nil {
		t.Fatal(err)
	}
	started, done := make([]chan struct{}, 2), make([]chan struct{}, 2)
	for i := range started {
		started[i], done[i] = make(chan struct{}), make(chan struct{})
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := range started {
			if strings.Contains(r.URL.Path, tokens[i]) {
				defer close(done[i])
				handler.ServeHTTP(&screenWriteObserver{ResponseWriter: w, started: started[i]}, r)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	server.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if tcp, ok := c.(*net.TCPConn); ok && state == http.StateNew {
			_ = tcp.SetWriteBuffer(1024)
		}
	}
	server.Start()
	defer server.Close()
	for i, viewer := range []string{"b", "a"} { // a shares with b, b shares with a
		conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.(*net.TCPConn).SetReadBuffer(1024)
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := fmt.Fprintf(conn, "GET /share/%s/screen.png HTTP/1.1\r\nHost: example.invalid\r\nX-Forwarded-User: %s\r\nConnection: close\r\n\r\n", tokens[i], viewer); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(conn)
		status, err := reader.ReadString('\n')
		if err != nil || !strings.Contains(status, "200 OK") {
			t.Fatalf("response %q: %v", status, err)
		}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if line == "\r\n" {
				break
			}
		}
		select {
		case <-started[i]:
		case <-time.After(time.Second):
			t.Fatal("write did not start")
		}
		select {
		case <-done[i]:
			t.Fatal("fixture did not block a downstream write")
		case <-time.After(50 * time.Millisecond):
		}
	}
	req, err := http.NewRequest(http.MethodGet, server.URL+"/share/"+tokens[2]+"/screen.png", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-User", "c")
	resp, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(body, data) {
		t.Fatalf("two stopped readers blocked third viewer: %d, %v", resp.StatusCode, err)
	}
	for _, ch := range done {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal("small image write retained its slot beyond the short deadline")
		}
	}
}

// Socket deadline support remains available at admission but can fail when the
// normalized image is ready. Record the handler's chosen status explicitly.
type screenDeadlineRecorder struct {
	*httptest.ResponseRecorder
	calls     atomic.Int32
	failWrite bool
}

func (w *screenDeadlineRecorder) SetWriteDeadline(time.Time) error {
	if w.calls.Add(1) == 2 && w.failWrite {
		return fmt.Errorf("dummy socket deadline failure")
	}
	return nil
}

func TestScreenFailurePathsNeverReturnEmptySuccess(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(imageData.Bytes()) }))
	defer upstream.Close()
	shares, err := OpenShareStore("")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := shares.Create("dummy-owner", "dummy-session", "dummy", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewDesktopHandler(&Server{shares: shares}, nil, upstream.URL, "obviously-fake-control-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, cause := range []string{"decoder-timeout", "write-start"} {
		t.Run(cause, func(t *testing.T) {
			timeout := time.Second
			if cause == "decoder-timeout" {
				timeout = 250 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			want := http.StatusServiceUnavailable
			if cause == "decoder-timeout" {
				work, ok := shareScreens.Begin("dummy-busy-decoder")
				if !ok || !work.BeginImage(context.Background()) {
					t.Fatal("cannot occupy decoder")
				}
				defer work.Close()
				want = http.StatusGatewayTimeout
			}
			req := httptest.NewRequest(http.MethodGet, "/share/"+token+"/screen.png", nil).WithContext(ctx)
			req.Header.Set("X-Forwarded-User", "dummy-viewer")
			w := &screenDeadlineRecorder{ResponseRecorder: httptest.NewRecorder(), failWrite: cause == "write-start"}
			handler.ServeHTTP(w, req)
			if w.Code != want {
				t.Fatalf("failure became an empty success: got %d, want %d", w.Code, want)
			}
		})
	}
}
