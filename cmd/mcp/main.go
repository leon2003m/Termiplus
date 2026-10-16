// Command mcp runs the termiplus control-plane MCP server (see docs/mcp.md).
//
// Sessions are provisioned through the gateway's /control/v1 API. Callers are
// authenticated by an OIDC proxy (oauth2-proxy) in front of the server
// (MCP_AUTH=proxy); termiplus never verifies tokens itself. MCP_AUTH=dev selects
// a static bearer token for local development.
package main

import (
	"context"
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
	case config.Get("MCP_STUB_BACKEND", "") == "1":
		// Asked for explicitly; wins over CONTROL_API_KEY so that tests can
		// combine it with the desktop stream proxy, which needs the key.
		log.Print("WARNING: using the in-memory stub backend (MCP_STUB_BACKEND=1); no real sessions are created")
		backend = mcp.NewStubBackend(publicURL)
	case controlKey != "":
		backend = mcp.NewGatewayBackend(gatewayURL, publicURL, controlKey, config.Secret("ADMIN_API_KEY", false)).
			WithStreamBase(config.Get("STREAM_PUBLIC_URL", "")).
			WithForwardBase(config.Get("FORWARD_PUBLIC_URL", ""))
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
		// With MCP_OWUI_PLUGIN_KEY, sessions belong to the Open WebUI user the
		// plugin names (same homes as that user's Open WebUI terminals).
		owuiKey := config.Secret("MCP_OWUI_PLUGIN_KEY", false)
		if owuiKey != "" && len(owuiKey) < 32 {
			log.Fatal("MCP_OWUI_PLUGIN_KEY must be at least 32 characters")
		}
		auth, authDesc = mcp.ProxyHeaderAuthenticator{OWUIKey: owuiKey}, "identity from the OIDC proxy, sessions keyed by the OIDC subject"
		if owuiKey != "" {
			authDesc = "identity from the OIDC proxy, sessions keyed by the Open WebUI user id"
		}
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
	entDesc := "all workspaces open to every signed-in user (no MCP_ENTITLEMENTS_FILE)"
	if f := config.Get("MCP_ENTITLEMENTS_FILE", ""); f != "" {
		if _, err := srv.WithEntitlementsFile(f); err != nil {
			log.Fatalf("MCP_ENTITLEMENTS_FILE: %v", err)
		}
		entDesc = "entitlements from " + f + " (admins can change them)"
	}

	// manage_user / manage_group: group membership in Keycloak, through the
	// admin API as a service account. Off unless MCP_KC_ADMIN_URL is set.
	if kc := config.Get("MCP_KC_ADMIN_URL", ""); kc != "" {
		dir, err := mcp.NewKeycloakDirectory(kc, config.Get("MCP_KC_REALM", "termiplus"),
			config.Get("MCP_KC_CLIENT_ID", "termiplus-mcp-admin"), config.Secret("MCP_KC_CLIENT_SECRET", true))
		if err != nil {
			log.Fatalf("MCP_KC_ADMIN_URL: %v", err)
		}
		srv.WithDirectory(dir, config.Get("MCP_KC_GROUP_PREFIX", "termiplus-"))
		entDesc += ", user management via " + kc
	}

	// Every state-changing tool call (chat and console) is recorded here and
	// admins read it with get_audit_log.
	if f := config.Get("MCP_AUDIT_FILE", ""); f != "" {
		a, err := mcp.OpenAuditLog(f, int64(config.Int("MCP_AUDIT_MAX_MB", 5))<<20)
		if err != nil {
			log.Fatal(err)
		}
		srv.WithAuditLog(a)
		entDesc += ", audit log " + f
	}

	// Download links and share links point at the console's public origin.
	srv.WithUIBase(config.Get("STREAM_PUBLIC_URL", ""))

	// Which Open WebUI user each OIDC subject belongs to, learned from plugin
	// calls; the management UI needs it.
	var ids *mcp.IdentityStore
	if f := config.Get("MCP_IDENTITY_FILE", ""); f != "" {
		var err error
		if ids, err = mcp.OpenIdentityStore(f); err != nil {
			log.Fatal(err)
		}
		srv.WithIdentities(ids)
	}

	// Departed users: archive the homes of users disabled or deleted in
	// Keycloak after MCP_DEPARTED_AFTER (off unless set).
	if after := config.Duration("MCP_DEPARTED_AFTER", 0); after > 0 {
		if config.Get("MCP_KC_ADMIN_URL", "") == "" || ids == nil {
			log.Fatal("MCP_DEPARTED_AFTER needs user management (MCP_KC_ADMIN_URL) and MCP_IDENTITY_FILE")
		}
		store, err := mcp.OpenDepartedStore(config.Get("MCP_DEPARTED_FILE", "/data/departed.json"))
		if err != nil {
			log.Fatal(err)
		}
		srv.WithDeparted(store, after)
		go srv.RunDeparted(context.Background(), config.Duration("MCP_DEPARTED_INTERVAL", 6*time.Hour))
		entDesc += ", departed users archived after " + after.String()
	}

	// The management UI trusts the sign-in proxy's identity headers, so it
	// only runs behind it (MCP_AUTH=proxy) on its own listener.
	if uiAddr := config.Get("UI_LISTEN_ADDR", ""); uiAddr != "" {
		if config.Get("MCP_AUTH", "") != "proxy" || ids == nil {
			log.Fatal("UI_LISTEN_ADDR needs MCP_AUTH=proxy and MCP_IDENTITY_FILE")
		}
		// View-only desktop share links (share_desktop).
		shares, err := mcp.OpenShareStore(config.Get("MCP_SHARES_FILE", ""))
		if err != nil {
			log.Fatal(err)
		}
		srv.WithShares(shares)
		handler := mcp.NewUI(srv, ids)
		// Desktops (/stream/) go through the gateway's control API.
		if controlKey := config.Secret("CONTROL_API_KEY", false); controlKey != "" {
			h, err := handler.WithStream(gatewayURL, controlKey)
			if err != nil {
				log.Fatal(err)
			}
			handler = h
		}
		ui := &http.Server{Addr: uiAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
		go func() { log.Fatal(ui.ListenAndServe()) }()
		log.Printf("management UI listening on %s", uiAddr)
	}

	// Port forwards: own listener behind their own sign-in proxy (fwd-auth).
	if fwdAddr := config.Get("FWD_LISTEN_ADDR", ""); fwdAddr != "" {
		controlKey := config.Secret("CONTROL_API_KEY", false)
		if config.Get("MCP_AUTH", "") != "proxy" || ids == nil || controlKey == "" {
			log.Fatal("FWD_LISTEN_ADDR needs MCP_AUTH=proxy, MCP_IDENTITY_FILE and CONTROL_API_KEY")
		}
		fwd, err := mcp.NewForwarder(ids, gatewayURL, controlKey)
		if err != nil {
			log.Fatal(err)
		}
		fs := &http.Server{Addr: fwdAddr, Handler: fwd, ReadHeaderTimeout: 10 * time.Second}
		go func() { log.Fatal(fs.ListenAndServe()) }()
		log.Printf("port forwards listening on %s", fwdAddr)
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
