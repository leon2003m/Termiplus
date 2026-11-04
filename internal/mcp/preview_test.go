package mcp

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"testing"
)

type previewStub struct {
	*StubBackend
	calls int
	data  []byte
}

func (b *previewStub) DesktopPreview(_ context.Context, _ Principal, _ string) (*http.Response, error) {
	b.calls++
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b.data))}, nil
}
func TestPreviewOwnerAndEntitlement(t *testing.T) {
	f := newUIFixture(t)
	var imageBytes bytes.Buffer
	_ = png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 320, 200)))
	b := &previewStub{StubBackend: f.backend, data: imageBytes.Bytes()}
	f.ui.srv.backend = b
	b.sessions["desktop"] = Session{ID: "desktop", UserID: "owui-alice", Workspace: "desk", Running: true, StreamURL: "https://desk.example/stream/desktop/"}
	for _, sub := range []string{"", "kc-bob"} {
		code, _, _ := f.do(t, http.MethodGet, "/ui/api/desktops/desktop/preview", sub, "", "", nil)
		if code != 401 && code != 404 {
			t.Fatalf("non-owner: %d", code)
		}
	}
	if b.calls != 0 {
		t.Fatal("non-owner reached backend")
	}
	code, _, h := f.do(t, http.MethodGet, "/ui/api/desktops/desktop/preview", "kc-alice", "", "", nil)
	if code != 200 || h.Get("Content-Type") != "image/png" || h.Get("Cache-Control") != "private, max-age=30" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("preview: %d %v", code, h)
	}
	ent, err := ParseEntitlements([]byte(`{"default":"deny","workspaces":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	f.ui.srv.WithEntitlements(ent)
	before := b.calls
	if code, _, _ := f.do(t, http.MethodGet, "/ui/api/desktops/desktop/preview", "kc-alice", "", "", nil); code != 404 {
		t.Fatalf("revoked entitlement: %d", code)
	}
	if b.calls != before {
		t.Fatal("revoked entitlement reached preview backend")
	}
	allow, _ := ParseEntitlements([]byte(`{"default":"allow","workspaces":{}}`))
	f.ui.srv.WithEntitlements(allow)
	b.data = []byte("<svg/>")
	if code, _, _ := f.do(t, http.MethodGet, "/ui/api/desktops/desktop/preview", "kc-alice", "", "", nil); code != 502 {
		t.Fatalf("bad bytes: %d", code)
	}
}
