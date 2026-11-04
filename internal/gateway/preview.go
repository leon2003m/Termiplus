package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"termiplus/internal/preview"
)

var previewClient = &http.Client{
	Transport:     newUpstreamTransport(5 * time.Second),
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (g *Gateway) servePreview(w http.ResponseWriter, r *http.Request, user, name string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	inst := g.mgr.streamTarget(name, user)
	if inst == nil {
		httpError(w, 404, "offline")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// Inspect even on cache hits: a stopped desktop must never return a stale frame.
	ct, err := g.mgr.docker.Inspect(ctx, name)
	if err != nil || !ct.State.Running {
		httpError(w, 404, "offline")
		return
	}
	for !inst.previewMu.TryLock() {
		select {
		case <-ctx.Done():
			httpError(w, 504, "preview timed out")
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer inst.previewMu.Unlock()
	if time.Since(inst.previewAt) >= 30*time.Second {
		data, err := g.capturePreview(ctx, inst)
		if err != nil {
			httpError(w, 502, "preview unavailable")
			return
		}
		inst.previewData, inst.previewAt = data, time.Now()
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(inst.previewData)
}

func (g *Gateway) capturePreview(ctx context.Context, inst *instance) ([]byte, error) {
	data, err := g.captureScreen(ctx, inst)
	if err != nil {
		return nil, err
	}
	return preview.Thumbnail(data)
}

// captureScreen uses the same native desktop identity as the owner's Open WebUI
// connection. Never follow redirects from the untrusted workspace.
func (g *Gateway) captureScreen(ctx context.Context, inst *instance) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+g.mgr.addrFor(inst.name, g.mgr.port)+"/desktop/screenshot", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.mgr.containerKey(inst.name))
	req.Header.Set("X-User-Id", inst.userID)
	resp, err := previewClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, preview.MaxScreenBytes+1))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("invalid screenshot response")
	}
	return preview.Screen(data)
}

// serveScreen is the only share capability: a bounded image, never arbitrary
// paths, input, clipboard, audio, an upstream HTML page or a VNC connection.
func (g *Gateway) serveScreen(w http.ResponseWriter, r *http.Request, user, name string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	inst := g.mgr.streamTarget(name, user)
	if inst == nil {
		httpError(w, 404, "offline")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ct, err := g.mgr.docker.Inspect(ctx, name)
	if err != nil || !ct.State.Running {
		httpError(w, 404, "offline")
		return
	}
	if !g.mgr.tryTouch(inst, maxProxiedConns) {
		httpError(w, 429, "too many screen requests")
		return
	}
	defer g.mgr.touch(inst, -1)
	data, err := g.captureScreen(ctx, inst)
	if err != nil {
		httpError(w, 502, "screen unavailable")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
