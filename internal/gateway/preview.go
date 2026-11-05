package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"termiplus/internal/preview"
)

var screenRequests = preview.NewScreenBudget(8, 4)

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
	response, err := preview.BoundResponse(ctx, w)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer response.Close()
	work, ok := screenRequests.Begin(user)
	if !ok {
		w.Header().Set("Retry-After", "1")
		httpError(w, http.StatusTooManyRequests, "screen busy")
		return
	}
	defer work.Close()
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
	if time.Since(inst.previewAt) >= 30*time.Second {
		data, err := g.capturePreview(ctx, inst, work)
		if err != nil {
			inst.previewMu.Unlock()
			if errors.Is(err, context.DeadlineExceeded) {
				httpError(w, 504, "preview fetch timed out")
			} else {
				httpError(w, 502, "preview unavailable")
			}
			return
		}
		inst.previewData, inst.previewAt = data, time.Now()
	}
	data := inst.previewData
	inst.previewMu.Unlock()
	if err := response.StartWrite(len(data)); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (g *Gateway) capturePreview(ctx context.Context, inst *instance, work *preview.ScreenRequest) ([]byte, error) {
	return g.captureScreen(ctx, inst, work, true)
}

// captureScreen uses the same native desktop identity as the owner's Open WebUI
// connection. Never follow redirects from the untrusted workspace.
func (g *Gateway) captureScreen(ctx context.Context, inst *instance, work *preview.ScreenRequest, thumbnail bool) ([]byte, error) {
	fetchCtx, cancelFetch := context.WithTimeout(ctx, preview.FetchTimeout)
	defer cancelFetch()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, "http://"+g.mgr.addrFor(inst.name, g.mgr.port)+"/desktop/screenshot", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.mgr.containerKey(inst.name))
	req.Header.Set("X-User-Id", inst.userID)
	resp, err := previewClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch screenshot: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("invalid screenshot response")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, preview.MaxScreenBytes+1))
	if err != nil {
		if fetchCtx.Err() != nil {
			return nil, fmt.Errorf("fetch screenshot body: %w", fetchCtx.Err())
		}
		return nil, fmt.Errorf("read screenshot body: %w", err)
	}
	_ = resp.Body.Close()
	cancelFetch()
	if !work.BeginImage(ctx) {
		return nil, fmt.Errorf("image processing interrupted")
	}
	defer work.ImageReady()
	if thumbnail {
		return preview.Thumbnail(data)
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
	response, err := preview.BoundResponse(ctx, w)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer response.Close()
	work, ok := screenRequests.Begin(user)
	if !ok {
		w.Header().Set("Retry-After", "1")
		httpError(w, http.StatusTooManyRequests, "screen busy")
		return
	}
	defer work.Close()
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
	data, err := g.captureScreen(ctx, inst, work, false)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			httpError(w, 504, "screen fetch timed out")
		} else {
			httpError(w, 502, "screen unavailable")
		}
		return
	}
	if err := response.StartWrite(len(data)); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
