// Command gateway routes Open WebUI terminal traffic to per-user containers.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
	"termiplus/internal/mqtt"
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
		BackupDir:    config.Get("BACKUP_DIR", ""),
		SnapshotKeep: int(config.Int("SNAPSHOT_KEEP", 5)),
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	g.Run(ctx)

	// Prometheus metrics on their own listener, off unless configured.
	if addr := config.Get("METRICS_LISTEN_ADDR", ""); addr != "" {
		ms := &http.Server{Addr: addr, Handler: g.MetricsHandler(), ReadHeaderTimeout: 10 * time.Second}
		go func() { log.Fatal(ms.ListenAndServe()) }()
		log.Printf("metrics on %s/metrics", addr)
	}
	// The same metrics to an MQTT broker (e.g. Home Assistant's), off unless
	// MQTT_URL is set.
	if mqttURL := config.Get("MQTT_URL", ""); mqttURL != "" {
		opts := mqtt.Options{
			URL:      mqttURL,
			ClientID: config.Get("MQTT_CLIENT_ID", "termiplus-gateway"),
			Username: config.Get("MQTT_USERNAME", ""),
			Password: config.Secret("MQTT_PASSWORD", false),
		}
		if ca := config.Get("MQTT_CA_FILE", ""); ca != "" {
			pem, err := os.ReadFile(ca)
			if err != nil {
				log.Fatalf("MQTT_CA_FILE: %v", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				log.Fatal("MQTT_CA_FILE: no certificates found")
			}
			opts.TLS = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		discovery := config.Get("MQTT_DISCOVERY_PREFIX", "homeassistant")
		if discovery == "off" {
			discovery = ""
		}
		go g.RunMQTT(ctx, gateway.MQTTConfig{
			Options:         opts,
			Prefix:          config.Get("MQTT_TOPIC_PREFIX", "termiplus"),
			DiscoveryPrefix: discovery,
			Interval:        config.Duration("MQTT_INTERVAL", 30*time.Second),
		})
	}

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
