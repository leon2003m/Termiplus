package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
)

// schemaLimit bounds the schema body that is decoded. Open Terminal 0.16.0
// (image/Dockerfile OPEN_TERMINAL_VERSION) serves a schema of about 24 KiB
// (40 KiB pretty-printed), measured from the terminal image on 2026-11-10;
// the desktop operations add a few KiB. 512 KiB keeps a margin of over 10x.
// Larger bodies pass through unmodified.
const schemaLimit = 512 << 10

// schemaSlot admits one decode/re-encode at a time across the gateway. It is
// never held across network I/O.
var schemaSlot = make(chan struct{}, 1)

// schemaDecodeHook, when set by tests, runs while the slot is held.
var schemaDecodeHook func()

// describeDesktopActions fills upstream's missing conditional field descriptions.
// The endpoint, payload and validation stay native; models otherwise confuse
// click/move coordinates with the optional drag destination and repeatedly get 422.
// Open Terminal 0.16's Linux Desktop fixes its screenshot to 1280x800; the image
// smoke test checks that size. Make the pixel scale explicit for vision models
// that otherwise supply normalized 0..1000 points and repeatedly get HTTP 400.
func describeDesktopActions(response *http.Response) error {
	if response.StatusCode != http.StatusOK || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") {
		return nil
	}
	// The body is attacker-controlled (a user's container can replace the Open
	// Terminal server). Two limits, so one hostile instance can only hurt itself:
	// a per-instance slot (keyed by the upstream address) is taken before the
	// body is read, so an instance has at most one buffer and its waiters hold
	// none; a trickling upstream stalls only its own /openapi.json. The
	// gateway-wide schemaSlot then covers only the CPU-bound decode of the fully
	// buffered body. A waiter gives up with its request, which the proxy
	// ErrorHandler turns into 502 (passing it through unannotated would hand
	// models a schema without the coordinate guidance, silently).
	ctx := context.Background()
	host := ""
	if response.Request != nil {
		ctx = response.Request.Context()
		if response.Request.URL != nil {
			host = response.Request.URL.Host
		}
	}
	release, err := acquireInstanceSlot(ctx, host)
	if err != nil {
		return fmt.Errorf("native schema annotation: %w", err)
	}
	handedOver := false
	defer func() {
		if !handedOver {
			release()
		}
	}()
	body, err := io.ReadAll(io.LimitReader(response.Body, schemaLimit+1))
	if err != nil {
		return fmt.Errorf("read native schema: %w", err)
	}
	if len(body) > schemaLimit {
		// The buffered prefix is replayed to the client, so keep only the
		// instance slot until it has been handed over (or the body is closed).
		upstream := response.Body
		response.Body = &schemaBody{Reader: &schemaPrefix{prefix: bytes.NewReader(body), rest: upstream, release: release}, Closer: closeFunc(func() error { release(); return upstream.Close() })}
		handedOver = true
		return nil
	}
	_ = response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(body))
	select {
	case schemaSlot <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("native schema annotation: %w", ctx.Err())
	}
	defer func() { <-schemaSlot }()
	if schemaDecodeHook != nil {
		schemaDecodeHook()
	}
	var document map[string]any
	if json.Unmarshal(body, &document) != nil {
		return nil
	}
	object := func(value any) map[string]any {
		result, _ := value.(map[string]any)
		return result
	}
	components := object(document["components"])
	schemas := object(components["schemas"])
	action := object(schemas["open_terminal__desktop__Action"])
	if action == nil {
		action = object(schemas["Action"])
	}
	fields := object(action["properties"])
	if fields == nil {
		return nil
	}
	for field, description := range map[string]string{
		"action": "click, double_click, move and drag REQUIRE both x and y. Drag ALSO requires to_x and to_y. Type requires text; key requires key.",
		"x":      "REQUIRED for click, double_click, move and drag: raw horizontal PIXEL coordinate, 0..1279 on this 1280x800 Linux desktop. Convert a normalized 0..1000 point u to round(u*1280/1000). For move this is the destination; for drag this is the start.",
		"y":      "REQUIRED for click, double_click, move and drag: raw vertical PIXEL coordinate, 0..799 on this 1280x800 Linux desktop. Convert a normalized 0..1000 point v to round(v*800/1000). The bottom panel is near y=775, NOT y=960. For move this is the destination; for drag this is the start.",
		"to_x":   "Drag destination X ONLY. For click, double_click or move use x instead; to_x does not provide their required coordinate.",
		"to_y":   "Drag destination Y ONLY. For click, double_click or move use y instead; to_y does not provide their required coordinate.",
		"text":   "Required when action is type: literal text to paste.",
	} {
		if property := object(fields[field]); property != nil {
			property["description"] = description
		}
	}
	paths := object(document["paths"])
	coordinates := "This Linux desktop returns 1280x800 screenshots. Use RAW PIXELS: x=0..1279, y=0..799, NOT normalized 0..1000 points. Convert normalized (u,v) to (round(u*1280/1000),round(v*800/1000)); for example (500,960) becomes pixel (640,768). Recalculate after an out-of-bounds error; do not repeat the failed coordinates."
	if operation := object(object(paths["/desktop/screenshot"])["get"]); operation != nil {
		original, _ := operation["description"].(string)
		operation["description"] = coordinates + "\n\n" + original
	}
	desktop := object(paths["/desktop/action"])
	if operation := object(desktop["post"]); operation != nil {
		original, _ := operation["description"].(string)
		operation["description"] = coordinates + "\nCoordinate fields: click, double_click and move require x AND y (not to_x/to_y). Example move: {\"actions\":[{\"action\":\"move\",\"x\":20,\"y\":20}]}. Example click: {\"actions\":[{\"action\":\"click\",\"x\":500,\"y\":400}]}. Drag requires x/y for the start AND to_x/to_y for the destination. Coordinates refer to the latest screenshot.\n\n" + original
	}
	updated, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode native schema: %w", err)
	}
	response.Body = io.NopCloser(bytes.NewReader(updated))
	response.ContentLength = int64(len(updated))
	response.Header.Set("Content-Length", strconv.Itoa(len(updated)))
	response.Header.Del("ETag")
	response.Header.Del("Content-MD5")
	return nil
}

type schemaBody struct {
	io.Reader
	io.Closer
}

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

// schemaPrefix replays a buffered prefix, then the rest of the body, and
// releases the slot as soon as the prefix is drained.
type schemaPrefix struct {
	prefix  *bytes.Reader
	rest    io.Reader
	release func()
}

func (p *schemaPrefix) Read(b []byte) (int, error) {
	if p.prefix.Len() > 0 {
		n, _ := p.prefix.Read(b)
		if p.prefix.Len() == 0 {
			p.release()
		}
		return n, nil
	}
	return p.rest.Read(b)
}

type instanceSlot struct {
	slot chan struct{}
	refs int
}

var (
	instanceSlotsMu sync.Mutex
	instanceSlots   = map[string]*instanceSlot{}
)

// acquireInstanceSlot admits one schema buffer per upstream address. Entries
// are refcounted and removed when unused. The returned release is idempotent.
func acquireInstanceSlot(ctx context.Context, host string) (func(), error) {
	instanceSlotsMu.Lock()
	entry := instanceSlots[host]
	if entry == nil {
		entry = &instanceSlot{slot: make(chan struct{}, 1)}
		instanceSlots[host] = entry
	}
	entry.refs++
	instanceSlotsMu.Unlock()
	drop := func() {
		instanceSlotsMu.Lock()
		if entry.refs--; entry.refs == 0 {
			delete(instanceSlots, host)
		}
		instanceSlotsMu.Unlock()
	}
	select {
	case entry.slot <- struct{}{}:
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-entry.slot
			drop()
		})
	}, nil
}
