package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Development stub URLs. They use the reserved .invalid TLD, so a stub link
// can never resolve to a real host.
const (
	StubDesktopBase  = "https://desktop.stub.invalid"
	StubConsoleBase  = "https://console.stub.invalid"
	stubSnapshotKeep = 5
)

// StubDevBackend is the in-memory backend for MCP_STUB_BACKEND=1. On top of
// StubBackend it implements HomeBackend (resets and per-workspace snapshots)
// and seeds each user's first view with a temporary terminal and a persistent
// desktop, so every panel control has something to act on. It is for local
// development and browser tests only; the gateway backend never uses it.
type StubDevBackend struct {
	*StubBackend

	mu     sync.Mutex
	seeded map[string]bool
	snaps  map[string][]map[string]any // by user + "\x00" + workspace
	seq    int
}

// NewStubDevBackend wraps a fresh StubBackend.
func NewStubDevBackend(gatewayBase string) *StubDevBackend {
	return &StubDevBackend{StubBackend: NewStubBackend(gatewayBase), seeded: map[string]bool{}, snaps: map[string][]map[string]any{}}
}

// ConfigureStubServer makes sharing and snapshot links available on srv, with
// in-memory share storage. Existing bases (from the environment) win.
func ConfigureStubServer(srv *Server) error {
	st, err := OpenShareStore("")
	if err != nil {
		return err
	}
	srv.WithShares(st)
	if srv.desktopBase == "" {
		srv.WithDesktopBase(StubDesktopBase)
	}
	if srv.uiBase == "" {
		srv.WithUIBase(StubConsoleBase)
	}
	return nil
}

func (b *StubDevBackend) seed(ctx context.Context, p Principal) {
	b.mu.Lock()
	done := b.seeded[p.UserID]
	b.seeded[p.UserID] = true
	b.mu.Unlock()
	if done {
		return
	}
	_, _ = b.StubBackend.RequestSession(ctx, p, "desk", true, "My desktop")
	_, _ = b.StubBackend.RequestSession(ctx, p, "default", false, "Scratch term")
}

func (b *StubDevBackend) ListSessions(ctx context.Context, p Principal) ([]Session, error) {
	b.seed(ctx, p)
	return b.StubBackend.ListSessions(ctx, p)
}

func (b *StubDevBackend) RequestSession(ctx context.Context, p Principal, workspace string, persistent bool, label string) (Session, error) {
	b.seed(ctx, p)
	return b.StubBackend.RequestSession(ctx, p, workspace, persistent, label)
}

func (b *StubDevBackend) DestroySession(ctx context.Context, p Principal, id string) error {
	b.seed(ctx, p)
	return b.StubBackend.DestroySession(ctx, p, id)
}

// ResetSession marks the session running again; the stub has no container.
func (b *StubDevBackend) ResetSession(ctx context.Context, p Principal, id string) (json.RawMessage, error) {
	b.seed(ctx, p)
	b.StubBackend.mu.Lock()
	defer b.StubBackend.mu.Unlock()
	s, ok := b.StubBackend.sessions[id]
	if !ok || s.UserID != p.UserID {
		return nil, fmt.Errorf("no such session %q", id)
	}
	s.Running = true
	s.StartedAt = time.Now().UTC().Format(time.RFC3339)
	b.StubBackend.sessions[id] = s
	return json.Marshal(map[string]any{"reset": id})
}

// Snapshots implements list and create (with prune when at the limit); restore
// and delete are not offered to the panel or to models.
func (b *StubDevBackend) Snapshots(_ context.Context, p Principal, workspace, action, id string, prune bool) (json.RawMessage, error) {
	key := p.UserID + "\x00" + workspace
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.snaps[key]
	switch action {
	case "list":
		out := make([]map[string]any, len(list))
		copy(out, list)
		sort.Slice(out, func(i, j int) bool { return out[i]["id"].(string) > out[j]["id"].(string) })
		return json.Marshal(map[string]any{"snapshots": out, "keep": stubSnapshotKeep})
	case "create":
		if len(list) >= stubSnapshotKeep {
			if !prune {
				return nil, fmt.Errorf("%d snapshots already exist: free space in the console first", stubSnapshotKeep)
			}
			list = list[1:]
		}
		b.seq++
		now := time.Now().UTC()
		snap := map[string]any{
			"id":      fmt.Sprintf("%s-%03d", now.Format("20060102T150405Z"), b.seq%1000),
			"created": now.Format(time.RFC3339),
			"size":    int64(5<<20 + b.seq*1048576),
		}
		b.snaps[key] = append(list, snap)
		return json.Marshal(snap)
	}
	return nil, errors.New("the stub backend supports only list and create")
}

func (b *StubDevBackend) HomeDownload(context.Context, Principal, string, string) (*http.Response, error) {
	return nil, errors.New("downloads are not available with the stub backend")
}

func (b *StubDevBackend) UserHomes(context.Context, string, string) (json.RawMessage, error) {
	return nil, errors.New("user homes are not available with the stub backend")
}
