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
)

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

	g, err := gateway.New(gateway.Config{
		APIKey:      config.Secret("GATEWAY_API_KEY", true),
		KeySecret:   config.Secret("KEY_SECRET", true),
		Image:       config.Must("TERMINAL_IMAGE"),
		Network:     config.Get("SANDBOX_NETWORK", "ot-sandbox"),
		Port:        int(config.Int("TERMINAL_PORT", 8000)),
		Policies:    policies,
		StartWait:   config.Duration("START_TIMEOUT", 90*time.Second),
		IdleTimeout: config.Duration("IDLE_TIMEOUT", 30*time.Minute),
		MaxRunning:  int(config.Int("MAX_RUNNING", 50)),
		Docker:      dockerapi.New(config.Get("DOCKER_API", "http://sockproxy:2375"), config.Secret("PROXY_TOKEN", false)),
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
	log.Printf("gateway listening on %s with %d policies", srv.Addr, len(policies))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
