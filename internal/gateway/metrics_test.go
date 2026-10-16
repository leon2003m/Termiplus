package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"termiplus/internal/mqtt"
	"termiplus/internal/mqtt/mqtttest"
)

func TestMetrics(t *testing.T) {
	h := newHarness(t)
	h.get(t, "/files/cwd", "alice", testAPIKey)
	h.get(t, "/files/cwd", "bob", testAPIKey)
	h.getChat(t, "/p/scratch/files/cwd", "alice", "chat1", testAPIKey)
	h.get(t, "/files/cwd", "carol", testAPIKey) // MaxRunning 3: refused
	// One denied proxy call.
	req, _ := http.NewRequest(http.MethodGet, h.proxy.URL+"/images/json", nil)
	req.Header.Set("X-Proxy-Token", "proxy-token")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}

	mt, err := h.g.Metrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mt.RunningWorkspaces != 2 || mt.RunningSandboxes != 1 || mt.Users != 2 || mt.Starts != 3 ||
		mt.CapacityRefusals != 1 || mt.StartFailures != 1 || mt.ProxyDenied < 1 || mt.ProxyAllowed < 5 || mt.MaxRunning != 3 {
		t.Fatalf("%+v", mt)
	}

	rec := httptest.NewRecorder()
	h.g.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{"termiplus_docker_up 1\n", "termiplus_running_workspaces 2\n", "termiplus_running_sandboxes 1\n",
		"# TYPE termiplus_starts_total counter\n", "termiplus_capacity_refusals_total 1\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	if strings.Contains(body, "alice") || strings.Contains(body, "ot-u-") {
		t.Fatal("metrics must not name users or containers")
	}
	// Every field has a description, and nothing else is exported.
	if len(mt.values()) != len(metricDescs) {
		t.Fatalf("%d fields, %d descriptions", len(mt.values()), len(metricDescs))
	}
	for _, d := range metricDescs {
		if _, ok := mt.values()[d.key]; !ok {
			t.Errorf("description for unknown field %s", d.key)
		}
	}
	rec = httptest.NewRecorder()
	h.g.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/other", nil))
	if rec.Code != 404 {
		t.Fatal("only /metrics is served")
	}
}

func TestMetricsMQTT(t *testing.T) {
	h := newHarness(t)
	h.get(t, "/files/cwd", "alice", testAPIKey)
	b := mqtttest.NewBroker(t, "ha", "pw")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.g.RunMQTT(ctx, MQTTConfig{
			Options:         mqtt.Options{URL: b.URL(), ClientID: "termiplus-gateway", Username: "ha", Password: "pw"},
			Prefix:          "termiplus",
			DiscoveryPrefix: "homeassistant",
			Interval:        200 * time.Millisecond,
		})
		close(done)
	}()
	waitFor := func(what string, ok func([]mqtttest.Pub) bool) []mqtttest.Pub {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if p := b.Pubs(); ok(p) {
				return p
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s: %+v", what, b.Pubs())
		return nil
	}
	count := func(p []mqtttest.Pub, topic string) int {
		n := 0
		for _, x := range p {
			if x.Topic == topic {
				n++
			}
		}
		return n
	}
	pubs := waitFor("two states", func(p []mqtttest.Pub) bool { return count(p, "termiplus/state") >= 2 })

	cn := b.Connects()[0]
	if cn.WillTopic != "termiplus/status" || cn.WillPayload != "offline" || cn.User != "ha" {
		t.Fatalf("connect: %+v", cn)
	}
	var cfg map[string]any
	var state Metrics
	for _, p := range pubs {
		switch {
		case p.Topic == "homeassistant/sensor/termiplus-gateway/running_workspaces/config":
			_ = json.Unmarshal([]byte(p.Payload), &cfg)
		case p.Topic == "termiplus/state":
			if err := json.Unmarshal([]byte(p.Payload), &state); err != nil {
				t.Fatal(err)
			}
		}
	}
	if cfg["state_topic"] != "termiplus/state" || cfg["value_template"] != "{{ value_json.running_workspaces }}" ||
		cfg["availability_topic"] != "termiplus/status" || cfg["unique_id"] != "termiplus_running_workspaces" {
		t.Fatalf("discovery config: %v", cfg)
	}
	if count(pubs, "termiplus/status") < 1 || state.RunningWorkspaces != 1 {
		t.Fatalf("status/state: %+v", state)
	}
	discovery := 0
	for _, p := range pubs {
		if strings.HasPrefix(p.Topic, "homeassistant/") {
			discovery++
		}
	}
	if discovery != len(metricDescs) {
		t.Fatalf("%d discovery messages for %d metrics", discovery, len(metricDescs))
	}

	// The broker restarts: the gateway reconnects and announces itself again.
	b.DropClients()
	waitFor("reconnect", func(p []mqtttest.Pub) bool { return len(b.Connects()) >= 2 && count(p, "termiplus/status") >= 2 })

	// Shutdown says offline.
	cancel()
	<-done
	pubs = b.Pubs()
	if last := pubs[len(pubs)-1]; last.Topic != "termiplus/status" || last.Payload != "offline" {
		t.Fatalf("last message: %+v", last)
	}
}
