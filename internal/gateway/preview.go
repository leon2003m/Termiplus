package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"termiplus/internal/preview"
)

// This is a fixed command, never assembled from a user, policy, path or query.
// maim captures X as the existing desktop user; Pillow bounds the image before
// base64 transport through Open Terminal. timeout also bounds a detached command
// if the HTTP caller leaves. The container is untrusted despite these limits.
const screenshotCommand = `DISPLAY=:1 /usr/bin/timeout -k 1 4 /usr/bin/python3 -c 'import base64,io,subprocess,sys; from PIL import Image; p=subprocess.run(["/usr/bin/maim"],stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=3,check=True); im=Image.open(io.BytesIO(p.stdout)); im.thumbnail((320,320)); b=io.BytesIO(); im.convert("RGB").save(b,format="PNG"); data=b.getvalue(); sys.exit(1) if len(data)>262144 else print(base64.b64encode(data).decode())'`

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
	body, _ := json.Marshal(map[string]string{"command": screenshotCommand, "cwd": "/home/user"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+g.mgr.addrFor(inst.name, g.mgr.port)+"/execute?wait=4", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.mgr.containerKey(inst.name))
	req.Header.Set("Content-Type", "application/json")
	resp, err := previewClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	const maxReply = 2 * preview.MaxBytes
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil || len(raw) > maxReply || resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid screenshot response")
	}
	var result struct {
		Status    string `json:"status"`
		ExitCode  *int   `json:"exit_code"`
		Truncated bool   `json:"truncated"`
		Output    []struct {
			Type string `json:"type"`
			Data string `json:"data"`
		} `json:"output"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Status != "done" || result.ExitCode == nil || *result.ExitCode != 0 || result.Truncated {
		return nil, fmt.Errorf("screenshot command failed")
	}
	var text strings.Builder
	for _, out := range result.Output {
		if out.Type == "stdout" || out.Type == "output" {
			text.WriteString(out.Data)
		}
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text.String()))
	if err != nil {
		return nil, fmt.Errorf("screenshot encoding: %w", err)
	}
	if err := preview.Validate(data); err != nil {
		return nil, err
	}
	return data, nil
}
