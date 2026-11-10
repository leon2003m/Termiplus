package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDesktopDescriptionsPreserveNativeContract(t *testing.T) {
	for _, name := range []string{"Action", "open_terminal__desktop__Action"} {
		t.Run(name, func(t *testing.T) {
			original := `{"components":{"schemas":{"` + name + `":{"properties":{"x":{"type":"integer","minimum":0},"to_x":{"type":"integer"}},"required":["action"],"additionalProperties":false}}},"paths":{"/desktop/action":{"post":{"description":"Native behavior","security":[{"HTTPBearer":[]}]}}}}`
			r := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": {"old"}}, Body: io.NopCloser(strings.NewReader(original))}
			if err := describeDesktopActions(r); err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(r.Body)
			for _, wanted := range []string{`"minimum":0`, `"required":["action"]`, `"additionalProperties":false`, `"security":[{"HTTPBearer":[]}]`, "Drag destination X ONLY", "Native behavior", "move require x AND y"} {
				if !strings.Contains(string(body), wanted) {
					t.Errorf("missing native contract or model guidance %q", wanted)
				}
			}
			if r.ContentLength != int64(len(body)) || r.Header.Get("ETag") != "" {
				t.Fatal("stale response metadata")
			}
		})
	}
}

func TestDesktopDescriptionsPassThroughOtherBodies(t *testing.T) {
	for _, original := range []string{`{"detail":"not a schema"}`, `not JSON`, strings.Repeat("x", schemaLimit+10)} {
		r := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(original))}
		if err := describeDesktopActions(r); err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if string(body) != original {
			t.Fatal("unrelated or oversized response changed")
		}
	}
}

func schemaResponse(ctx context.Context, body string) *http.Response {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://x/openapi.json", nil)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req, Body: io.NopCloser(strings.NewReader(body))}
}

func TestSchemaLimitBoundary(t *testing.T) {
	decoded := 0
	schemaDecodeHook = func() { decoded++ }
	defer func() { schemaDecodeHook = nil }()
	pad := func(n int) string { // valid JSON of exactly n bytes
		prefix, suffix := `{"a":"`, `"}`
		return prefix + strings.Repeat("x", n-len(prefix)-len(suffix)) + suffix
	}
	over := pad(schemaLimit + 1)
	r := schemaResponse(context.Background(), over)
	if err := describeDesktopActions(r); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r.Body)
	if string(got) != over || decoded != 0 {
		t.Fatalf("oversized body altered or decoded (%d)", decoded)
	}
	r = schemaResponse(context.Background(), pad(schemaLimit))
	if err := describeDesktopActions(r); err != nil {
		t.Fatal(err)
	}
	if decoded != 1 {
		t.Fatalf("body at the limit not decoded (%d)", decoded)
	}
}

func TestSchemaDecodeIsSerialized(t *testing.T) {
	var running, peak, total atomic.Int32
	schemaDecodeHook = func() {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		total.Add(1)
		time.Sleep(2 * time.Millisecond)
		running.Add(-1)
	}
	defer func() { schemaDecodeHook = nil }()
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := describeDesktopActions(schemaResponse(context.Background(), `{"a":1}`)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if total.Load() != 40 || peak.Load() != 1 {
		t.Fatalf("decodes=%d peak concurrency=%d", total.Load(), peak.Load())
	}
}

func TestSchemaWaiterHonoursCancellation(t *testing.T) {
	hold, err := acquireInstanceSlot(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- describeDesktopActions(schemaResponse(ctx, `{"a":1}`)) }()
	select {
	case err := <-done:
		t.Fatalf("returned while slot was held: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not return after cancel")
	}
}

type recordingBody struct {
	io.Reader
	reads *atomic.Int32
}

func (b recordingBody) Read(p []byte) (int, error) { b.reads.Add(1); return b.Reader.Read(p) }
func (b recordingBody) Close() error               { return nil }

func TestSchemaWaiterReadsNoBodyWhileSlotHeld(t *testing.T) {
	hold, err := acquireInstanceSlot(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	var reads atomic.Int32
	r := schemaResponse(context.Background(), "")
	r.Body = recordingBody{Reader: strings.NewReader(`{"a":1}`), reads: &reads}
	done := make(chan error, 1)
	go func() { done <- describeDesktopActions(r) }()
	time.Sleep(30 * time.Millisecond)
	if reads.Load() != 0 {
		t.Fatal("waiting request read its body before getting the slot")
	}
	hold()
	if err := <-done; err != nil || reads.Load() == 0 {
		t.Fatalf("err=%v reads=%d", err, reads.Load())
	}
}

func TestSchemaOversizeHoldsSlotUntilPrefixDrained(t *testing.T) {
	over := strings.Repeat("x", schemaLimit+10)
	r := schemaResponse(context.Background(), over)
	if err := describeDesktopActions(r); err != nil {
		t.Fatal(err)
	}
	if len(schemaSlot) != 0 {
		t.Fatal("global slot must not be held by the prefix replay")
	}
	instanceSlotsMu.Lock()
	held := len(instanceSlots)
	instanceSlotsMu.Unlock()
	if held != 1 {
		t.Fatal("instance slot not held while the buffered prefix is pending")
	}
	got, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if string(got) != over {
		t.Fatalf("altered=%v", string(got) != over)
	}
	r = schemaResponse(context.Background(), over)
	if err := describeDesktopActions(r); err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	_ = r.Body.Close()
	instanceSlotsMu.Lock()
	defer instanceSlotsMu.Unlock()
	if len(schemaSlot) != 0 || len(instanceSlots) != 0 {
		t.Fatal("slot leaked after close")
	}
}

type blockingBody struct{ unblock chan struct{} }

func (b blockingBody) Read(p []byte) (int, error) { <-b.unblock; return 0, io.EOF }
func (b blockingBody) Close() error               { return nil }

func TestSchemaStalledInstanceDoesNotBlockOthers(t *testing.T) {
	block := blockingBody{unblock: make(chan struct{})}
	a := schemaResponse(context.Background(), "")
	a.Request.URL.Host = "instance-a:8000"
	a.Body = block
	doneA := make(chan error, 1)
	go func() { doneA <- describeDesktopActions(a) }()
	time.Sleep(20 * time.Millisecond)
	b := schemaResponse(context.Background(), `{"a":1}`)
	b.Request.URL.Host = "instance-b:8000"
	doneB := make(chan error, 1)
	go func() { doneB <- describeDesktopActions(b) }()
	select {
	case err := <-doneB:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("instance B blocked by stalled instance A")
	}
	close(block.unblock)
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
	instanceSlotsMu.Lock()
	defer instanceSlotsMu.Unlock()
	if len(instanceSlots) != 0 {
		t.Fatal("instance slot entries leaked")
	}
}

// A drained prefix is dropped before the instance slot is released, so a
// stalled oversize body does not pin its 512 KiB buffer (review P1).
func TestSchemaPrefixDroppedOnRelease(t *testing.T) {
	released := false
	p := &schemaPrefix{prefix: bytes.NewReader([]byte("abc")), rest: strings.NewReader("rest"), release: func() { released = true }}
	buf := make([]byte, 8)
	if n, _ := p.Read(buf); n != 3 || !released || p.prefix != nil {
		t.Fatalf("n=%d released=%v prefix kept=%v", n, released, p.prefix != nil)
	}
	if n, _ := p.Read(buf); string(buf[:n]) != "rest" {
		t.Fatalf("rest = %q", buf[:n])
	}
}
