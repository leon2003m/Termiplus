// Command mcp runs the termiplus control-plane MCP server (see docs/mcp.md).
//
// Sessions are provisioned through the gateway's /control/v1 API. Callers are
// authenticated by an OIDC proxy (oauth2-proxy) in front of the server
// (MCP_AUTH=proxy); termiplus never verifies tokens itself. MCP_AUTH=dev selects
// a static bearer token for local development.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
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

	var auth mcp.Authenticator
	authDesc := ""
	switch mode := config.Get("MCP_AUTH", ""); mode {
	case "proxy":
		if config.Secret("MCP_DEV_TOKEN", false) != "" {
			log.Fatal("MCP_DEV_TOKEN must not be set with MCP_AUTH=proxy")
		}
		auth, authDesc = mcp.ProxyHeaderAuthenticator{}, "identity from the OIDC proxy"
	case "dev":
		tok := config.Secret("MCP_DEV_TOKEN", true)
		log.Print("WARNING: MCP_AUTH=dev - every caller with MCP_DEV_TOKEN acts as MCP_DEV_USER; never use this in production")
		auth = mcp.NewStaticAuthenticator(map[string]mcp.Principal{
			tok: {UserID: config.Get("MCP_DEV_USER", "dev"), Roles: splitRoles(config.Get("MCP_DEV_ROLES", ""))},
		})
		authDesc = "DEV static token"
	default:
		log.Fatalf("MCP_AUTH must be \"proxy\" (behind oauth2-proxy) or \"dev\", got %q", mode)
	}

	srv := mcp.NewServer(auth, backend, version)
	entDesc := "all workspaces open to every signed-in user"
	if f := config.Get("MCP_ENTITLEMENTS_FILE", ""); f != "" {
		data, err := os.ReadFile(f)
		if err != nil {
			log.Fatalf("MCP_ENTITLEMENTS_FILE: %v", err)
		}
		ent, err := mcp.ParseEntitlements(data)
		if err != nil {
			log.Fatal(err)
		}
		srv.WithEntitlements(ent)
		entDesc = fmt.Sprintf("entitlements from %s (%d workspaces, default %s)", f, len(ent.Workspaces), ent.Default)
	}
	log.Printf("termiplus mcp %s listening on %s, gateway %s, auth: %s, %s", version, addr, gatewayURL, authDesc, entDesc)
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
