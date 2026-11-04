package gateway

import (
	"net/http"
	"strings"
	"testing"

	"termiplus/internal/naming"
	"termiplus/internal/sockproxy"
)

func offlineHarness(t *testing.T) *harness {
	t.Helper()
	const fwd = "172.30.0.3"
	return newHarness(t, func(o *harnessOpts) {
		o.rules = func(r *sockproxy.Rules) {
			r.AllowedDNS = fwd
			r.OfflineNetwork = "ot-offline"
		}
		o.gateway = func(c *Config) {
			c.ForwarderDNS = fwd
			c.OfflineNetwork = "ot-offline"
			c.Policies["off"] = Policy{Runtime: "runsc", MemoryMB: 1024, CPUs: 1, Pids: 256, Egress: "offline"}
		}
	})
}

func hostNetwork(c *fakeCtr) any {
	return c.body["HostConfig"].(map[string]any)["NetworkMode"]
}

func TestOfflinePolicyJoinsOfflineNetworkWithoutResolver(t *testing.T) {
	h := offlineHarness(t)
	if code, b := h.get(t, "/p/off/files/cwd", "alice", testAPIKey); code != http.StatusOK {
		t.Fatalf("offline workspace: %d %s", code, b)
	}
	c := h.ctr(naming.Container("alice", "off"))
	if nw := hostNetwork(c); nw != "ot-offline" {
		t.Fatalf("offline workspace on %v", nw)
	}
	if dns, _ := c.body["HostConfig"].(map[string]any)["Dns"].([]any); len(dns) > 0 {
		t.Fatalf("offline workspace pinned to a resolver: %v", c.body["HostConfig"])
	}
	for _, e := range c.body["Env"].([]any) {
		if strings.HasPrefix(e.(string), "OPEN_TERMINAL_RESOLVER=") {
			t.Fatalf("offline workspace told a resolver: %v", e)
		}
	}

	// The default policy keeps the normal network and the forwarder.
	if code, b := h.get(t, "/files/cwd", "alice", testAPIKey); code != http.StatusOK {
		t.Fatalf("default workspace: %d %s", code, b)
	}
	d := h.ctr(naming.Container("alice", "default"))
	if nw := hostNetwork(d); nw != "ot-sandbox" {
		t.Fatalf("default workspace on %v", nw)
	}
	if dns, _ := d.body["HostConfig"].(map[string]any)["Dns"].([]any); len(dns) != 1 {
		t.Fatal("default workspace lost the forwarder")
	}
}

// A container whose network no longer matches its policy (moved by hand, or
// adopted from before the policy went offline) must never be reused: it would
// keep the internet access the policy forbids.
func TestNetworkMismatchRecreates(t *testing.T) {
	h := offlineHarness(t)
	name := naming.Container("alice", "off")
	if code, _ := h.get(t, "/p/off/files/cwd", "alice", testAPIKey); code != http.StatusOK {
		t.Fatal("first request failed")
	}
	h.stopBehindGateway(name)
	h.docker.mu.Lock()
	h.docker.ctrs[name].body["HostConfig"].(map[string]any)["NetworkMode"] = "ot-sandbox"
	h.docker.mu.Unlock()

	if code, b := h.get(t, "/p/off/files/cwd", "alice", testAPIKey); code != http.StatusOK {
		t.Fatalf("%d %s", code, b)
	}
	if h.docker.creates != 2 {
		t.Fatalf("mismatched network must recreate, creates=%d", h.docker.creates)
	}
	if nw := hostNetwork(h.ctr(name)); nw != "ot-offline" {
		t.Fatalf("recreated on %v", nw)
	}
}

func TestEgressValidated(t *testing.T) {
	base := func(egress, offlineNet string) Config {
		return Config{APIKey: testAPIKey, KeySecret: testKeySecret, Image: testImage, Network: "ot-sandbox",
			OfflineNetwork: offlineNet, Policies: map[string]Policy{
				"default": {Runtime: "runsc", MemoryMB: 1, CPUs: 1, Pids: 1, Egress: egress},
			}}
	}
	for _, tc := range []struct {
		name, egress, offline string
		ok                    bool
	}{
		{"unset", "", "", true},
		{"internet", "internet", "", true},
		{"offline configured", "offline", "ot-offline", true},
		{"offline without network fails closed", "offline", "", false},
		{"unknown value", "lan", "ot-offline", false},
		{"case trick", "Offline", "ot-offline", false},
		{"offline network equals sandbox", "offline", "ot-sandbox", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(base(tc.egress, tc.offline))
			if (err == nil) != tc.ok {
				t.Fatalf("egress=%q offline=%q: err=%v, want ok=%v", tc.egress, tc.offline, err, tc.ok)
			}
		})
	}
}

// Policies that do not set egress must hash exactly as before, so upgrading
// recreates no existing workspace.
func TestConfigHashUnchangedWithoutEgress(t *testing.T) {
	m := &manager{image: testImage, network: "ot-sandbox", offlineNetwork: "ot-offline", port: 8000, keySecret: []byte(testKeySecret)}
	pol := Policy{Runtime: "runsc", MemoryMB: 1024, CPUs: 1, Pids: 256}
	plain := &manager{image: testImage, network: "ot-sandbox", port: 8000, keySecret: []byte(testKeySecret)}
	if m.configHash(pol, "default") != plain.configHash(pol, "default") {
		t.Fatal("configuring an offline network changed the hash of internet policies")
	}
	off := pol
	off.Egress = "offline"
	if m.configHash(off, "default") == m.configHash(pol, "default") {
		t.Fatal("going offline must change the hash")
	}
}
