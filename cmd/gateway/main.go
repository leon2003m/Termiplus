// Command gateway routes Open WebUI terminal traffic to per-user containers.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"termiplus/internal/config"
	"termiplus/internal/dockerapi"
	"termiplus/internal/gateway"
	"termiplus/internal/netpolicy"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	config.HealthcheckIfRequested(config.Get("LISTEN_ADDR", ":8080"), "/healthz")

	policies := map[string]gateway.Policy{
		"default": {Runtime: "runsc", MemoryMB: 2048, CPUs: 2, Pids: 512},
	}
	if f := os.Getenv("POLICIES_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			log.Fatalf("POLICIES_FILE: %v", err)
		}
		policies = map[string]gateway.Policy{}
		if err := json.Unmarshal(b, &policies); err != nil {
			log.Fatalf("POLICIES_FILE: %v", err)
		}
	}

	// The admin network-policy API is on only when ADMIN_API_KEY is set. The
	// store persists the policy and renders the forwarder and firewall files to
	// a shared volume (defaults live under /data/netpolicy).
	adminKey := config.Secret("ADMIN_API_KEY", false)
	var store *netpolicy.Store
	if adminKey != "" {
		var err error
		store, err = netpolicy.NewStore(
			config.Get("NETPOLICY_FILE", "/data/netpolicy/policy.json"),
			config.Get("NETPOLICY_DNSMASQ_FILE", "/data/netpolicy/dnsmasq.hosts"),
			config.Get("NETPOLICY_EGRESS_FILE", "/data/netpolicy/egress.rules"),
		)
		if err != nil {
			log.Fatalf("netpolicy store: %v", err)
		}
	}

	// The MCP control API (/control/v1) is on only when CONTROL_API_KEY is set.
	g, err := gateway.New(gateway.Config{
		APIKey:       config.Secret("GATEWAY_API_KEY", true),
		KeySecret:    config.Secret("KEY_SECRET", true),
		Image:        config.Must("TERMINAL_IMAGE"),
		Network:      config.Get("SANDBOX_NETWORK", "ot-sandbox"),
		Port:         int(config.Int("TERMINAL_PORT", 8000)),
		Policies:     policies,
		StartWait:    config.Duration("START_TIMEOUT", 90*time.Second),
		IdleTimeout:  config.Duration("IDLE_TIMEOUT", 30*time.Minute),
		MaxRunning:   int(config.Int("MAX_RUNNING", 50)),
		Docker:       dockerapi.New(config.Get("DOCKER_API", "http://sockproxy:2375"), config.Secret("PROXY_TOKEN", false)),
		AdminKey:     adminKey,
		NetPolicy:    store,
		ControlKey:   config.Secret("CONTROL_API_KEY", false),
		ForwarderDNS: config.Get("ALLOWED_DNS", ""),
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	g.Run(ctx)

	srv := &http.Server{
		Addr:              config.Get("LISTEN_ADDR", ":8080"),
		Handler:           g,
		ReadHeaderTimeout: 15 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	log.Printf("termiplus gateway %s listening on %s with %d policies", version, srv.Addr, len(policies))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
