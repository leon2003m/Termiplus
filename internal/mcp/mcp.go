// Package mcp implements the termiplus control-plane MCP server: a
// provisioning interface (list/request/destroy sandbox sessions, admin
// network policy) spoken over JSON-RPC 2.0 / HTTP. See docs/mcp.md.
//
// It is a provisioning plane only - it deliberately exposes no file or exec
// tools, because an agent works natively inside its session. Every consequential
// tool is gated twice: by RBAC (roles from the caller's verified identity) and
// by an explicit human confirmation obtained in Open WebUI (the server enforces
// that a gated call carries the confirmation flag the plugin sets after the
// user approves the modal).
package mcp

import (
	"context"
	"errors"
	"net/http"
)

// Principal is the authenticated caller, resolved from a bearer token by an
// Authenticator. Roles drive RBAC entitlement.
type Principal struct {
	UserID string
	Roles  []string
}

// HasRole reports whether the principal carries role r.
func (p Principal) HasRole(r string) bool {
	for _, have := range p.Roles {
		if have == r {
			return true
		}
	}
	return false
}

// Authenticator resolves the caller of a request to a Principal. termiplus does
// not verify tokens itself: in production an OIDC proxy (oauth2-proxy) in front
// of the server validates the bearer JWT and ProxyHeaderAuthenticator reads the
// identity it forwards. A static token implementation exists for development
// and tests.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, error)
}

// ErrUnauthenticated is returned by an Authenticator when the token is missing
// or invalid.
var ErrUnauthenticated = errors.New("unauthenticated")

// Workspace is a launchable profile/image the caller may request.
type Workspace struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Persistent reports whether this workspace can be launched with a
	// persistent home (still subject to the caller's entitlement).
	Persistent bool `json:"persistent"`
}

// Session is a provisioned sandbox and the handle used to reach it.
type Session struct {
	ID          string `json:"session_id"`
	Workspace   string `json:"workspace"`
	UserID      string `json:"-"`
	Persistent  bool   `json:"persistent"`
	Label       string `json:"label,omitempty"`
	Running     bool   `json:"running"`
	TerminalURL string `json:"terminal_url,omitempty"`
	// TerminalSession must be sent as X-Session-Id to reach an ephemeral
	// session through TerminalURL; empty for persistent sessions.
	TerminalSession string `json:"terminal_session,omitempty"`
	StreamURL       string `json:"stream_url,omitempty"`
}

// ErrPersistenceMismatch is returned when a request's persistent flag does not
// match the workspace. Persistence is a property of the workspace, so a caller
// can never obtain a persistent session without asking for one explicitly -
// which is what triggers the RBAC + confirmation gate.
var ErrPersistenceMismatch = errors.New("persistent flag does not match the workspace")

// Backend performs the actual control-plane operations: GatewayBackend drives
// the gateway's /control/v1 API, StubBackend is in-memory for development.
type Backend interface {
	ListWorkspaces(ctx context.Context, p Principal) ([]Workspace, error)
	RequestSession(ctx context.Context, p Principal, workspace string, persistent bool, label string) (Session, error)
	ListSessions(ctx context.Context, p Principal) ([]Session, error)
	DestroySession(ctx context.Context, p Principal, id string) error
	SetNetworkPolicy(ctx context.Context, p Principal, policy []byte) error
}

// Role names used for RBAC entitlement (claims from the OIDC token).
const (
	RolePersist = "termiplus-persist" // may create persistent sessions
	RoleAdmin   = "termiplus-admin"   // may use admin tools (network policy, users)
)
