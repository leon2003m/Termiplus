// Command mcp runs the termiplus control-plane MCP server (see docs/mcp.md).
//
// This is the skeleton: authentication is a dev-only static bearer token.
// Production OIDC/JWKS validation and the gateway-backed Backend are added in
// later spikes; until then the server uses an in-memory stub backend.
package main

import (
	"log"
	"net/http"
	"strings"
	"time"

	"termiplus/internal/config"
	"termiplus/internal/mcp"
)

var version = "dev"

func main() {
	config.HealthcheckIfRequested(config.Get("LISTEN_ADDR", ":8090"), "/healthz")

	addr := config.Get("LISTEN_ADDR", ":8090")
	gatewayBase := config.Get("GATEWAY_BASE", "http://gateway:8080")

	// Dev-only static auth until the OIDC/JWKS authenticator lands.
	tok := config.Secret("MCP_DEV_TOKEN", false)
	if tok == "" {
		log.Fatal("MCP_DEV_TOKEN is required (dev-only static auth; OIDC/JWKS not implemented yet)")
	}
	auth := mcp.NewStaticAuthenticator(map[string]mcp.Principal{
		tok: {UserID: config.Get("MCP_DEV_USER", "dev"), Roles: splitRoles(config.Get("MCP_DEV_ROLES", ""))},
	})

	srv := mcp.NewServer(auth, mcp.NewStubBackend(gatewayBase), version)
	log.Printf("termiplus mcp %s listening on %s (DEV static auth, stub backend; OIDC/gateway backend pending)", version, addr)
	s := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(s.ListenAndServe())
}

func splitRoles(s string) []string {
	var out []string
	for _, r := range strings.Split(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}
