package mcp

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"termiplus/internal/naming"
)

// Headers set by the OIDC proxy (oauth2-proxy with --pass-user-headers, the
// default). The proxy strips client-supplied copies before forwarding.
const (
	proxyUserHeader   = "X-Forwarded-User"
	proxyGroupsHeader = "X-Forwarded-Groups"
)

// Headers set by the Open WebUI plugin (integrations/openwebui) when homes are
// keyed by the Open WebUI user id. oauth2-proxy passes them through unchanged,
// so they are only trusted together with the plugin key.
const (
	owuiKeyHeader  = "X-Termiplus-Owui-Key"
	owuiUserHeader = "X-Termiplus-Owui-User"
)

// ProxyHeaderAuthenticator trusts the identity an OIDC proxy forwards. termiplus
// deliberately contains no token verification of its own: oauth2-proxy
// validates the bearer JWT (signature via the issuer's JWKS, iss, aud, exp) and
// forwards the subject and groups. This is only safe when the MCP server is
// reachable exclusively through that proxy (deploy/docker-compose.mcp.yml puts
// it on an internal network shared only with the proxy and the gateway).
//
// With OWUIKey set, sessions belong to the Open WebUI user instead of the OIDC
// subject, so they share homes with that user's Open WebUI terminals. The
// caller must then also present the plugin key and the Open WebUI user id; the
// token (still verified by the proxy) proves a signed-in user and carries the
// groups. Trust is the same as the gateway's: whoever holds the Open WebUI
// side's key may name the user, exactly as Open WebUI does towards the gateway
// with X-User-Id.
type ProxyHeaderAuthenticator struct {
	OWUIKey string
}

// Authenticate implements Authenticator.
func (a ProxyHeaderAuthenticator) Authenticate(r *http.Request) (Principal, error) {
	users := r.Header.Values(proxyUserHeader)
	if len(users) != 1 || !naming.UserID.MatchString(users[0]) {
		return Principal{}, ErrUnauthenticated
	}
	userID, subject := users[0], ""
	if a.OWUIKey != "" {
		keys := r.Header.Values(owuiKeyHeader)
		owui := r.Header.Values(owuiUserHeader)
		if len(keys) != 1 || subtle.ConstantTimeCompare([]byte(keys[0]), []byte(a.OWUIKey)) != 1 ||
			len(owui) != 1 || !naming.UserID.MatchString(owui[0]) {
			return Principal{}, ErrUnauthenticated
		}
		userID, subject = owui[0], users[0]
	}
	var roles []string
	seen := map[string]bool{}
	for _, v := range r.Header.Values(proxyGroupsHeader) {
		for _, g := range strings.Split(v, ",") {
			if g = strings.TrimSpace(g); g != "" && !seen[g] {
				seen[g] = true
				roles = append(roles, g)
			}
		}
	}
	return Principal{UserID: userID, Roles: roles, Subject: subject}, nil
}

// StaticAuthenticator maps fixed bearer tokens to principals. It exists for
// development and tests only. Tokens are compared in constant time.
type StaticAuthenticator struct {
	tokens map[string]Principal
}

// NewStaticAuthenticator builds an authenticator from token -> principal.
func NewStaticAuthenticator(tokens map[string]Principal) *StaticAuthenticator {
	m := make(map[string]Principal, len(tokens))
	for k, v := range tokens {
		m[k] = v
	}
	return &StaticAuthenticator{tokens: m}
}

// Authenticate implements Authenticator.
func (a *StaticAuthenticator) Authenticate(r *http.Request) (Principal, error) {
	if tok := bearer(r); tok != "" {
		for want, p := range a.tokens {
			if subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1 {
				return p, nil
			}
		}
	}
	return Principal{}, ErrUnauthenticated
}
