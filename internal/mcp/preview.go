package mcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"termiplus/internal/preview"
)

type previewBackend interface {
	DesktopPreview(context.Context, Principal, string) (*http.Response, error)
}

// preview deliberately has no persistent-desktop fallback: it may never start
// a container or use the desktop sharing exception to the owner's entitlement.
func (u *UI) preview(w http.ResponseWriter, r *http.Request, p Principal, id string) {
	if err := u.srv.entitledSession(r.Context(), p, id); err != nil {
		uiError(w, http.StatusNotFound, "no such desktop")
		return
	}
	b, ok := u.srv.backend.(previewBackend)
	if !ok {
		uiError(w, http.StatusNotFound, "preview unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	resp, err := b.DesktopPreview(ctx, p, id)
	if err != nil {
		uiError(w, http.StatusBadGateway, "preview unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		uiError(w, http.StatusNotFound, "offline")
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, preview.MaxBytes+1))
	if resp.StatusCode != http.StatusOK || err != nil || preview.Validate(data) != nil {
		uiError(w, http.StatusBadGateway, "invalid preview")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (b *GatewayBackend) DesktopPreview(ctx context.Context, p Principal, id string) (*http.Response, error) {
	if id == "" || strings.ContainsAny(id, "/?#%") {
		return nil, fmt.Errorf("invalid session id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/control/v1/preview/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Control-Key", b.controlKey)
	req.Header.Set("X-User-Id", p.UserID)
	return b.client.Do(req)
}
