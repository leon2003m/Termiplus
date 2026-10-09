// Command sockproxy exposes a filtered subset of the Docker API over TCP.
package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"termiplus/internal/config"
	"termiplus/internal/sockproxy"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	config.HealthcheckIfRequested(config.Get("LISTEN_ADDR", ":2375"), "/healthz")

	rules := sockproxy.Rules{
		Image:       config.Must("TERMINAL_IMAGE"),
		Runtimes:    map[string]bool{},
		Network:     config.Get("SANDBOX_NETWORK", "ot-sandbox"),
		MaxMemory:   config.Int("MAX_MEMORY_MB", 8192) << 20,
		MaxNanoCPUs: config.Int("MAX_CPUS", 4) * 1e9,
		MaxPids:     config.Int("MAX_PIDS", 2048),
	}
	for _, rt := range strings.Split(config.Get("ALLOWED_RUNTIMES", "runsc,sysbox-runc"), ",") {
		if rt = strings.TrimSpace(rt); rt != "" {
			if rt == "runc" && os.Getenv("ALLOW_RUNC_INSECURE") != "1" {
				log.Fatal("runc gives containers real root on the host kernel; set ALLOW_RUNC_INSECURE=1 only for local testing")
			}
			rules.Runtimes[rt] = true
		}
	}

	p := sockproxy.New(rules, config.Get("DOCKER_SOCKET", "/var/run/docker.sock"), config.Secret("PROXY_TOKEN", false))
	addr := config.Get("LISTEN_ADDR", ":2375")
	log.Printf("termiplus sockproxy %s listening on %s, image=%s runtimes=%v network=%s", version, addr, rules.Image, keys(rules.Runtimes), rules.Network)
	srv := &http.Server{Addr: addr, Handler: p, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
