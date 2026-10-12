package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
)

// StubBackend is an in-memory Backend for the skeleton: it tracks sessions per
// user and fabricates a terminal URL from gatewayBase, without touching Docker
// or the gateway. The gateway-backed Backend replaces it in a later spike.
type StubBackend struct {
	gatewayBase string
	workspaces  []Workspace

	mu       sync.Mutex
	sessions map[string]Session // by id
}

// NewStubBackend returns a stub with a fixed set of workspaces. gatewayBase is
// the external base URL used to build a session's terminal handle.
func NewStubBackend(gatewayBase string) *StubBackend {
	return &StubBackend{
		gatewayBase: gatewayBase,
		workspaces: []Workspace{
			{Name: "default", Description: "ephemeral shell", Persistent: false},
			{Name: "desk", Description: "persistent workspace", Persistent: true},
		},
		sessions: map[string]Session{},
	}
}

func (b *StubBackend) ListWorkspaces(_ context.Context, _ Principal) ([]Workspace, error) {
	out := make([]Workspace, len(b.workspaces))
	copy(out, b.workspaces)
	return out, nil
}

func (b *StubBackend) RequestSession(_ context.Context, p Principal, workspace string, persistent bool, label string) (Session, error) {
	var ws *Workspace
	for i := range b.workspaces {
		if b.workspaces[i].Name == workspace {
			ws = &b.workspaces[i]
			break
		}
	}
	if ws == nil {
		return Session{}, fmt.Errorf("unknown workspace %q", workspace)
	}
	if persistent && !ws.Persistent {
		return Session{}, fmt.Errorf("workspace %q does not support persistence", workspace)
	}

	id, err := randomID()
	if err != nil {
		return Session{}, err
	}
	s := Session{
		ID:          id,
		Workspace:   workspace,
		UserID:      p.UserID,
		Persistent:  persistent,
		Label:       label,
		TerminalURL: fmt.Sprintf("%s/p/%s/", b.gatewayBase, workspace),
	}
	b.mu.Lock()
	b.sessions[id] = s
	b.mu.Unlock()
	return s, nil
}

func (b *StubBackend) ListSessions(_ context.Context, p Principal) ([]Session, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Session
	for _, s := range b.sessions {
		if s.UserID == p.UserID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (b *StubBackend) DestroySession(_ context.Context, p Principal, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[id]
	if !ok || s.UserID != p.UserID {
		return fmt.Errorf("no such session %q", id)
	}
	delete(b.sessions, id)
	return nil
}

func (b *StubBackend) SetNetworkPolicy(_ context.Context, _ Principal, _ []byte) error {
	// Wired to the gateway admin API in a later spike.
	return nil
}

func randomID() (string, error) {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
