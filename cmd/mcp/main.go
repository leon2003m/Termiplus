// Command mcp runs the termiplus control-plane MCP server (see docs/mcp.md).
//
// Sessions are provisioned through the gateway's /control/v1 API. Caller
// authentication is still a dev-only static bearer token; OIDC/JWKS
// validation replaces it in a later spike.
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
	gatewayURL := config.Get("GATEWAY_URL", "http://gateway:8080")
	publicURL := config.Get("GATEWAY_PUBLIC_URL", gatewayURL)

	// The real backend drives the gateway's /control/v1 API with CONTROL_API_KEY.
	// The in-memory stub is only for local development and must be asked for.
	var backend mcp.Backend
	switch controlKey := config.Secret("CONTROL_API_KEY", false); {
	case controlKey != "":
		backend = mcp.NewGatewayBackend(gatewayURL, publicURL, controlKey, config.Secret("ADMIN_API_KEY", false))
	case config.Get("MCP_STUB_BACKEND", "") == "1":
		log.Print("WARNING: using the in-memory stub backend (MCP_STUB_BACKEND=1); no real sessions are created")
		backend = mcp.NewStubBackend(publicURL)
	default:
		log.Fatal("CONTROL_API_KEY is required (or MCP_STUB_BACKEND=1 for local development)")
	}

	// Dev-only static auth until the OIDC/JWKS authenticator lands.
	tok := config.Secret("MCP_DEV_TOKEN", false)
	if tok == "" {
		log.Fatal("MCP_DEV_TOKEN is required (dev-only static auth; OIDC/JWKS not implemented yet)")
	}
	auth := mcp.NewStaticAuthenticator(map[string]mcp.Principal{
		tok: {UserID: config.Get("MCP_DEV_USER", "dev"), Roles: splitRoles(config.Get("MCP_DEV_ROLES", ""))},
	})

	srv := mcp.NewServer(auth, backend, version)
	log.Printf("termiplus mcp %s listening on %s, gateway %s (DEV static auth; OIDC pending)", version, addr, gatewayURL)
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
