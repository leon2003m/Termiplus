package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"termiplus/internal/mqtt"
	"termiplus/internal/naming"
)

// Metrics: counts of what the gateway runs and has done, for Prometheus
// (MetricsHandler, on its own listener) and MQTT (RunMQTT, e.g. into Home
// Assistant). They hold no user ids or names.

type counters struct {
	starts, startFailures, idleStops, sandboxesDeleted, capacityRefusals, imageRecreates atomic.Int64
}

// Metrics is one snapshot.
type Metrics struct {
	RunningWorkspaces int   `json:"running_workspaces"` // persistent, running
	StoppedWorkspaces int   `json:"stopped_workspaces"`
	RunningSandboxes  int   `json:"running_sandboxes"` // ephemeral
	OpenSessions      int   `json:"open_sessions"`     // open terminal/desktop connections
	Users             int   `json:"users"`             // distinct users with a running container
	Starts            int64 `json:"starts_total"`
	StartFailures     int64 `json:"start_failures_total"`
	IdleStops         int64 `json:"idle_stops_total"`
	SandboxesDeleted  int64 `json:"sandboxes_deleted_total"`
	CapacityRefusals  int64 `json:"capacity_refusals_total"`
	ImageRecreates    int64 `json:"image_recreates_total"`
	ProxyAllowed      int64 `json:"proxy_allowed_total"`
	ProxyDenied       int64 `json:"proxy_denied_total"`
	ProxyFailed       int64 `json:"proxy_failed_total"`
	MaxRunning        int   `json:"max_running"`
}

// Metrics collects a snapshot. Container counts come from Docker (through the
// proxy), so they include containers started before this gateway did.
func (g *Gateway) Metrics(ctx context.Context) (Metrics, error) {
	m := g.mgr
	out := Metrics{
		Starts:           m.stats.starts.Load(),
		StartFailures:    m.stats.startFailures.Load(),
		IdleStops:        m.stats.idleStops.Load(),
		SandboxesDeleted: m.stats.sandboxesDeleted.Load(),
		CapacityRefusals: m.stats.capacityRefusals.Load(),
		ImageRecreates:   m.stats.imageRecreates.Load(),
		MaxRunning:       m.maxRunning,
	}
	list, err := m.docker.List(ctx)
	if err != nil {
		return out, err
	}
	users := map[string]bool{}
	for _, c := range list {
		if len(c.Names) == 0 {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		running := c.State == "running"
		switch {
		case naming.EphemeralName.MatchString(name) && running:
			out.RunningSandboxes++
		case naming.ContainerName.MatchString(name) && running:
			out.RunningWorkspaces++
		case naming.ContainerName.MatchString(name):
			out.StoppedWorkspaces++
		}
		if running && naming.IsManagedContainer(name) {
			users[naming.UserHash(name)] = true
		}
	}
	out.Users = len(users)
	m.mu.Lock()
	for _, inst := range m.instances {
		out.OpenSessions += inst.conns
	}
	m.mu.Unlock()
	if ps, err := m.docker.ProxyStats(ctx); err == nil {
		out.ProxyAllowed, out.ProxyDenied, out.ProxyFailed = ps.Allowed, ps.Denied, ps.Failed
	}
	return out, nil
}

type metricDesc struct {
	key, help, kind, unit, icon string
}

// metricDescs lists every field of Metrics, in output order.
var metricDescs = []metricDesc{
	{"running_workspaces", "Persistent workspaces running", "gauge", "", "mdi:console"},
	{"stopped_workspaces", "Persistent workspaces stopped (home kept)", "gauge", "", "mdi:console-line"},
	{"running_sandboxes", "Temporary sandboxes running", "gauge", "", "mdi:package-variant"},
	{"open_sessions", "Open terminal and desktop connections", "gauge", "", "mdi:lan-connect"},
	{"users", "Users with a running workspace or sandbox", "gauge", "", "mdi:account-multiple"},
	{"max_running", "Limit on running containers", "gauge", "", "mdi:gauge-full"},
	{"starts_total", "Containers started", "counter", "", "mdi:play"},
	{"start_failures_total", "Workspace requests that failed", "counter", "", "mdi:alert"},
	{"idle_stops_total", "Workspaces stopped when idle", "counter", "", "mdi:sleep"},
	{"sandboxes_deleted_total", "Temporary sandboxes deleted", "counter", "", "mdi:delete"},
	{"capacity_refusals_total", "Requests refused at the running limit", "counter", "", "mdi:cancel"},
	{"image_recreates_total", "Stopped workspaces recreated on a new image", "counter", "", "mdi:update"},
	{"proxy_allowed_total", "Docker calls the socket proxy allowed", "counter", "", "mdi:shield-check"},
	{"proxy_denied_total", "Docker calls the socket proxy refused", "counter", "", "mdi:shield-alert"},
	{"proxy_failed_total", "Allowed Docker calls that failed", "counter", "", "mdi:shield-off"},
}

func (mt Metrics) values() map[string]float64 {
	var raw map[string]float64
	b, _ := json.Marshal(mt)
	_ = json.Unmarshal(b, &raw)
	return raw
}

// MetricsHandler serves the Prometheus text format at /metrics.
func (g *Gateway) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		mt, err := g.Metrics(ctx)
		up := 1
		if err != nil {
			up = 0
			log.Printf("metrics: %v", err)
		}
		v := mt.values()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP termiplus_docker_up Whether the gateway could list containers.\n# TYPE termiplus_docker_up gauge\ntermiplus_docker_up %d\n", up)
		for _, d := range metricDescs {
			fmt.Fprintf(w, "# HELP termiplus_%s %s.\n# TYPE termiplus_%s %s\ntermiplus_%s %g\n", d.key, d.help, d.key, d.kind, d.key, v[d.key])
		}
	})
}

// MQTTConfig configures RunMQTT.
type MQTTConfig struct {
	Options mqtt.Options
	// Prefix is the topic prefix: <prefix>/state (JSON, retained) and
	// <prefix>/status ("online"/"offline", retained; offline is the will).
	Prefix string
	// DiscoveryPrefix enables Home Assistant MQTT discovery under it (usually
	// "homeassistant"). Empty: no discovery messages.
	DiscoveryPrefix string
	Interval        time.Duration
}

// RunMQTT publishes the metrics every Interval until ctx ends, reconnecting
// with backoff when the broker is unreachable.
func (g *Gateway) RunMQTT(ctx context.Context, cfg MQTTConfig) {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	status := cfg.Prefix + "/status"
	cfg.Options.WillTopic, cfg.Options.WillPayload = status, "offline"
	backoff := time.Second
	for ctx.Err() == nil {
		c, err := mqtt.Dial(cfg.Options)
		if err != nil {
			log.Printf("%v (retry in %s)", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 5*time.Minute)
			continue
		}
		backoff = time.Second
		log.Printf("mqtt: connected, publishing to %s/state every %s", cfg.Prefix, cfg.Interval)
		err = g.publishLoop(ctx, c, cfg, status)
		if ctx.Err() != nil {
			_ = c.Publish(status, []byte("offline"), true)
			_ = c.Close()
			return
		}
		log.Printf("mqtt: connection lost: %v", err)
		_ = c.Close()
	}
}

func (g *Gateway) publishLoop(ctx context.Context, c *mqtt.Client, cfg MQTTConfig, status string) error {
	if cfg.DiscoveryPrefix != "" {
		for _, d := range metricDescs {
			topic, payload := discoveryMessage(cfg, d, status)
			if err := c.Publish(topic, payload, true); err != nil {
				return err
			}
		}
	}
	if err := c.Publish(status, []byte("online"), true); err != nil {
		return err
	}
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		mctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		mt, err := g.Metrics(mctx)
		cancel()
		if err != nil {
			log.Printf("mqtt: metrics: %v", err)
		} else {
			b, _ := json.Marshal(mt)
			if err := c.Publish(cfg.Prefix+"/state", b, true); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.Done():
			return fmt.Errorf("broker closed the connection")
		case <-t.C:
		}
	}
}

// discoveryMessage is the Home Assistant config for one sensor.
func discoveryMessage(cfg MQTTConfig, d metricDesc, status string) (string, []byte) {
	id := "termiplus_" + d.key
	sc := "measurement"
	if d.kind == "counter" {
		sc = "total_increasing"
	}
	payload := map[string]any{
		"name":                  strings.TrimSuffix(d.help, "."),
		"unique_id":             id,
		"object_id":             id,
		"state_topic":           cfg.Prefix + "/state",
		"value_template":        "{{ value_json." + d.key + " }}",
		"availability_topic":    status,
		"state_class":           sc,
		"icon":                  d.icon,
		"device":                map[string]any{"identifiers": []string{"termiplus_" + sanitizeID(cfg.Options.ClientID)}, "name": "termiplus", "manufacturer": "termiplus"},
		"payload_available":     "online",
		"payload_not_available": "offline",
	}
	if d.unit != "" {
		payload["unit_of_measurement"] = d.unit
	}
	if d.kind == "counter" {
		payload["entity_category"] = "diagnostic"
	}
	b, _ := json.Marshal(payload)
	return cfg.DiscoveryPrefix + "/sensor/" + sanitizeID(cfg.Options.ClientID) + "/" + d.key + "/config", b
}

func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
