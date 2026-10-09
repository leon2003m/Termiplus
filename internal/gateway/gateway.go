// Package gateway sits between Open WebUI and per-user Open Terminal
// containers. Open WebUI is configured with one admin "Open Terminal"
// connection (auth type Bearer) pointing here; every request it sends carries
// X-User-Id, which the gateway uses to route to that user's own container.
package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"termiplus/internal/dockerapi"
	"termiplus/internal/naming"
)

type Config struct {
	APIKey      string // what Open WebUI sends as Bearer / WebSocket auth token
	KeySecret   string // HMAC secret for per-container keys
	Image       string
	Network     string
	Port        int
	Policies    map[string]Policy
	StartWait   time.Duration
	IdleTimeout time.Duration
	MaxRunning  int
	Docker      *dockerapi.Client

	// AddrFor maps a container name to host:port. Defaults to Docker DNS on
	// the sandbox network. Tests override it.
	AddrFor func(name string, port int) string
}

type Gateway struct {
	cfg Config
	mgr *manager
}

// The user-less "spec" identity serves Open WebUI's connection checks and
// tool-spec loading, which are sent without X-User-Id. It only ever answers
// the read-only paths in specPaths.
const specUser = "termiplus-spec"

var specPaths = map[string]bool{"/openapi.json": true, "/api/config": true, "/system": true}

func New(cfg Config) (*Gateway, error) {
	if len(cfg.APIKey) < 32 || len(cfg.KeySecret) < 32 {
		return nil, errors.New("API key and key secret must be at least 32 characters")
	}
	if _, ok := cfg.Policies["default"]; !ok {
		return nil, errors.New(`a policy named "default" is required`)
	}
	for name, p := range cfg.Policies {
		if !naming.PolicyName.MatchString(name) {
			return nil, fmt.Errorf("invalid policy name %q", name)
		}
		if p.Home != "" && !naming.PolicyName.MatchString(p.Home) {
			return nil, fmt.Errorf("policy %s: invalid home %q", name, p.Home)
		}
		if p.Runtime == "" || p.MemoryMB <= 0 || p.CPUs <= 0 || p.Pids <= 0 {
			return nil, fmt.Errorf("policy %s: runtime, memory_mb, cpus and pids are required", name)
		}
		switch p.Storage {
		case "":
			p.Storage = storagePersistent
		case storagePersistent, storageEphemeral:
		default:
			return nil, fmt.Errorf("policy %s: storage must be %q or %q", name, storagePersistent, storageEphemeral)
		}
		if p.IdleTimeout != "" {
			d, err := time.ParseDuration(p.IdleTimeout)
			if err != nil || d < time.Minute {
				return nil, fmt.Errorf("policy %s: idle_timeout must be a duration of at least 1m", name)
			}
			p.idle = d
		}
		if p.MaxPerUser < 0 {
			return nil, fmt.Errorf("policy %s: max_per_user must be positive", name)
		}
		cfg.Policies[name] = p
	}
	if cfg.AddrFor == nil {
		cfg.AddrFor = func(name string, port int) string { return net.JoinHostPort(name, strconv.Itoa(port)) }
	}
	m := &manager{
		docker:     cfg.Docker,
		image:      cfg.Image,
		network:    cfg.Network,
		port:       cfg.Port,
		keySecret:  []byte(cfg.KeySecret),
		startWait:  cfg.StartWait,
		idle:       cfg.IdleTimeout,
		maxRunning: cfg.MaxRunning,
		policies:   cfg.Policies,
		addrFor:    cfg.AddrFor,
		instances:  map[string]*instance{},
		locks:      map[string]*sync.Mutex{},
	}
	return &Gateway{cfg: cfg, mgr: m}, nil
}

// Run starts background work (adopting running containers, idle reaper).
func (g *Gateway) Run(ctx context.Context) {
	g.mgr.adopt(ctx)
	go g.mgr.runReaper(ctx, time.Minute)
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if r.URL.RawPath != "" && strings.Contains(strings.ToLower(r.URL.RawPath), "%2f") {
		httpError(w, http.StatusBadRequest, "encoded slashes not allowed")
		return
	}

	policyName, rest, ok := splitPolicy(r.URL.Path)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown policy")
		return
	}
	pol, ok := g.cfg.Policies[policyName]
	if !ok {
		httpError(w, http.StatusNotFound, "unknown policy")
		return
	}

	ws := isWebSocket(r)
	// WebSocket auth happens in the first message (Open WebUI protocol);
	// everything else must carry the bearer key.
	if !ws && !g.checkBearer(r) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// We are a plain terminal from Open WebUI's point of view, not an
	// orchestrator, so the policies admin API does not exist here.
	if strings.HasPrefix(rest, "/api/v1/") {
		httpError(w, http.StatusNotFound, "not found")
		return
	}

	userID := r.Header.Get("X-User-Id")
	if userID == "" {
		switch {
		case !ws && r.Method == http.MethodGet && rest == "/health":
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		case !ws && r.Method == http.MethodGet && specPaths[rest]:
			userID = specUser
		default:
			httpError(w, http.StatusBadRequest, "X-User-Id header required")
			return
		}
	}
	if !naming.UserID.MatchString(userID) || (userID == specUser && r.Header.Get("X-User-Id") != "") {
		httpError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	// For ephemeral policies the chat decides which sandbox is used: every
	// chat gets its own fresh container. Persistent policies ignore it.
	sessionID := r.Header.Get("X-Session-Id")
	switch {
	case userID == specUser:
		sessionID = "spec"
	case sessionID == "":
		sessionID = "nochat"
	case !naming.UserID.MatchString(sessionID):
		httpError(w, http.StatusBadRequest, "invalid session id")
		return
	}

	if ws {
		g.serveWebSocket(w, r, userID, sessionID, policyName, pol, rest)
		return
	}

	inst, err := g.mgr.ensure(r.Context(), userID, sessionID, policyName, pol)
	if err != nil {
		g.ensureError(w, userID, policyName, err)
		return
	}
	g.proxyHTTP(w, r, inst, rest)
}

func (g *Gateway) ensureError(w http.ResponseWriter, userID, policy string, err error) {
	log.Printf("ensure %s/%s: %v", userID, policy, err)
	switch {
	case errors.Is(err, errCapacity):
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, errUserLimit):
		httpError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	httpError(w, http.StatusBadGateway, "terminal unavailable")
}

func (g *Gateway) checkBearer(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return false
	}
	return g.tokenOK(strings.TrimSpace(h[len(prefix):]))
}

func (g *Gateway) tokenOK(tok string) bool {
	return subtle.ConstantTimeCompare([]byte(tok), []byte(g.cfg.APIKey)) == 1
}

func (g *Gateway) proxyHTTP(w http.ResponseWriter, r *http.Request, inst *instance, rest string) {
	target := &url.URL{Scheme: "http", Host: inst.addr}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = rest
			pr.Out.URL.RawPath = ""
			pr.Out.Host = inst.addr
			pr.Out.Header.Set("Authorization", "Bearer "+inst.key)
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("X-Proxy-Token")
		},
		FlushInterval: -1, // stream command output immediately
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy %s: %v", inst.name, err)
			g.mgr.invalidate(inst.name)
			httpError(w, http.StatusBadGateway, "terminal unavailable")
		},
	}
	g.mgr.touch(inst, 0)
	rp.ServeHTTP(w, r)
	g.mgr.touch(inst, 0)
}

// splitPolicy turns /p/<policy>/rest into (policy, /rest). Paths without the
// prefix use the "default" policy.
func splitPolicy(p string) (string, string, bool) {
	if !strings.HasPrefix(p, "/p/") {
		return "default", p, true
	}
	tail := strings.TrimPrefix(p, "/p/")
	name, rest, _ := strings.Cut(tail, "/")
	if !naming.PolicyName.MatchString(name) {
		return "", "", false
	}
	return name, "/" + rest, true
}

func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"detail": msg})
}
