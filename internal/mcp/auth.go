package mcp

import "crypto/subtle"

// StaticAuthenticator maps fixed bearer tokens to principals. It exists for
// development and tests only; production uses an OIDC/JWKS authenticator (added
// in a later spike). Tokens are compared in constant time.
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
func (a *StaticAuthenticator) Authenticate(bearer string) (Principal, error) {
	if bearer != "" {
		for tok, p := range a.tokens {
			if subtle.ConstantTimeCompare([]byte(bearer), []byte(tok)) == 1 {
				return p, nil
			}
		}
	}
	return Principal{}, ErrUnauthenticated
}
