//go:build noshare

package mcp

import (
	"errors"
	"net/http"
)

const sharingCompiled = false

// DesktopHandler does not exist in a noshare build.
type DesktopHandler struct{}

// NewDesktopHandler always fails: the /share handler is compiled out.
func NewDesktopHandler(*Server, *IdentityStore, string, string) (*DesktopHandler, error) {
	return nil, errors.New("screen sharing is compiled out (noshare build)")
}

func (*DesktopHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNotFound)
}
