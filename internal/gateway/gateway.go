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
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"termiplus/internal/dockerapi"
	"termiplus/internal/naming"
	"termiplus/internal/netpolicy"
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

	// AdminKey gates the /admin network-policy API. It must differ from APIKey
	// and be at least 32 characters. Empty, together with a nil NetPolicy,
	// disables the admin surface (it returns 404).
	AdminKey string
	// NetPolicy is the runtime network-policy store the admin API reads and
	// writes. Required if AdminKey is set, forbidden otherwise.
	NetPolicy *netpolicy.Store
	// ControlKey gates the /control/v1 provisioning API used by the termiplus
	// MCP server. It must be at least 32 characters and differ from APIKey and
	// AdminKey. Empty disables the control surface (it returns 404).
	ControlKey string
	// ForwarderDNS, when set, pins every terminal to this resolver address via
	// HostConfig.Dns (the internal forwarder). The socket proxy accepts only
	// this one address. Empty leaves terminals on Docker's embedded resolver.
	ForwarderDNS string

	// AddrFor maps a container name to host:port. Defaults to Docker DNS on
	// the sandbox network. Tests override it.
	AddrFor func(name string, port int) string
}

type Gateway struct {
	cfg Config
	mgr *manager
	// specPolicy maps every policy to the policy whose container answers
	// user-less spec requests for it (see specPolicies).
	specPolicy map[string]string
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
	if (cfg.AdminKey == "") != (cfg.NetPolicy == nil) {
		return nil, errors.New("admin API needs both an admin key and a netpolicy store, or neither")
	}
	if cfg.AdminKey != "" {
		if len(cfg.AdminKey) < 32 {
			return nil, errors.New("admin API key must be at least 32 characters")
		}
		if subtle.ConstantTimeCompare([]byte(cfg.AdminKey), []byte(cfg.APIKey)) == 1 {
			return nil, errors.New("admin API key must differ from the connection API key")
		}
	}
	if cfg.ControlKey != "" {
		if len(cfg.ControlKey) < 32 {
			return nil, errors.New("control API key must be at least 32 characters")
		}
		if subtle.ConstantTimeCompare([]byte(cfg.ControlKey), []byte(cfg.APIKey)) == 1 ||
			(cfg.AdminKey != "" && subtle.ConstantTimeCompare([]byte(cfg.ControlKey), []byte(cfg.AdminKey)) == 1) {
			return nil, errors.New("control API key must differ from the connection and admin keys")
		}
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
		docker:       cfg.Docker,
		image:        cfg.Image,
		network:      cfg.Network,
		port:         cfg.Port,
		keySecret:    []byte(cfg.KeySecret),
		startWait:    cfg.StartWait,
		idle:         cfg.IdleTimeout,
		maxRunning:   cfg.MaxRunning,
		policies:     cfg.Policies,
		addrFor:      cfg.AddrFor,
		forwarderDNS: cfg.ForwarderDNS,
		instances:    map[string]*instance{},
		locks:        map[string]*sync.Mutex{},
	}
	return &Gateway{cfg: cfg, mgr: m, specPolicy: specPolicies(cfg.Policies)}, nil
}

// specPolicies picks, for each runtime, one policy whose container answers the
// user-less spec requests of every policy with that runtime. Without this each
// policy started its own full terminal container just for three read-only
// GETs. Spec answers are shared per runtime, not globally, because /system
// describes the kernel, which differs between gVisor and Sysbox. Preference:
// "default", then persistent policies, then by name.
func specPolicies(policies map[string]Policy) map[string]string {
	rank := func(name string) string {
		switch {
		case name == "default":
			return "0"
		case !policies[name].ephemeral():
			return "1" + name
		}
		return "2" + name
	}
	best := map[string]string{} // runtime -> policy name
	for name, p := range policies {
		if cur, ok := best[p.Runtime]; !ok || rank(name) < rank(cur) {
			best[p.Runtime] = name
		}
	}
	out := make(map[string]string, len(policies))
	for name, p := range policies {
		out[name] = best[p.Runtime]
	}
	return out
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
	// Reject non-canonical paths (".." segments, "//") before the policy split
	// and the /api/v1 denylist below. net/http does not collapse "..", so
	// "/x/../api/v1/policies" would otherwise skip the denylist and be
	// forwarded verbatim to the terminal, which may normalize it back to the
	// blocked admin path. A single trailing slash is allowed. Mirrors the
	// sibling check in the socket proxy.
	if clean := path.Clean(r.URL.Path); clean != r.URL.Path && clean+"/" != r.URL.Path {
		httpError(w, http.StatusBadRequest, "non-canonical path")
		return
	}

	// The admin network-policy API has its own key and is not a terminal
	// policy; handle it before the policy split and the per-user bearer check.
	if g.serveAdmin(w, r) {
		return
	}
	// Likewise the MCP control API: its own key, handled before routing.
	if g.serveControl(w, r) {
		return
	}
	// The desktop stream is reached by browsers, not Open WebUI: no bearer key,
	// KasmVNC authenticates (see stream.go).
	if g.serveStream(w, r) {
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
		if shared, ok := g.specPolicy[policyName]; ok {
			policyName, pol = shared, g.cfg.Policies[shared]
		}
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
