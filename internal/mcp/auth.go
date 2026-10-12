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

// ProxyHeaderAuthenticator trusts the identity an OIDC proxy forwards. termiplus
// deliberately contains no token verification of its own: oauth2-proxy
// validates the bearer JWT (signature via the issuer's JWKS, iss, aud, exp) and
// forwards the subject and groups. This is only safe when the MCP server is
// reachable exclusively through that proxy (deploy/docker-compose.mcp.yml puts
// it on an internal network shared only with the proxy and the gateway).
type ProxyHeaderAuthenticator struct{}

// Authenticate implements Authenticator.
func (ProxyHeaderAuthenticator) Authenticate(r *http.Request) (Principal, error) {
	users := r.Header.Values(proxyUserHeader)
	if len(users) != 1 || !naming.UserID.MatchString(users[0]) {
		return Principal{}, ErrUnauthenticated
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
	return Principal{UserID: users[0], Roles: roles}, nil
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
