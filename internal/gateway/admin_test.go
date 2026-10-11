package gateway

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"termiplus/internal/netpolicy"
	"termiplus/internal/sockproxy"
)

const testAdminKey = "admin-api-key-distinct-0123456789abcdef"

// withAdmin enables the admin API on the harness, backed by a real store in a
// temp dir. It returns the option plus the store so a test can read it back.
func withAdmin(t *testing.T) (func(*harnessOpts), *netpolicy.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := netpolicy.NewStore(
		filepath.Join(dir, "policy.json"),
		filepath.Join(dir, "dnsmasq.hosts"),
		filepath.Join(dir, "egress.rules"),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return func(o *harnessOpts) {
		o.gateway = func(c *Config) {
			c.AdminKey = testAdminKey
			c.NetPolicy = store
		}
	}, store
}

func (h *harness) admin(t *testing.T, method, key, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, h.gw.URL+"/admin/netpolicy", r)
	if key != "" {
		req.Header.Set("X-Admin-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAdminDisabledReturns404(t *testing.T) {
	h := newHarness(t) // no admin configured
	if code, _ := h.admin(t, http.MethodGet, testAdminKey, ""); code != http.StatusNotFound {
		t.Fatalf("admin GET with feature off: want 404, got %d", code)
	}
}

func TestAdminAuth(t *testing.T) {
	opt, _ := withAdmin(t)
	h := newHarness(t, opt)

	cases := []struct {
		name string
		key  string
		want int
	}{
		{"no key", "", http.StatusUnauthorized},
		{"wrong key", "nope-nope-nope-nope-nope-nope-nope", http.StatusUnauthorized},
		{"connection key is not the admin key", testAPIKey, http.StatusUnauthorized},
		{"correct admin key", testAdminKey, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, _ := h.admin(t, http.MethodGet, c.key, ""); code != c.want {
				t.Fatalf("want %d, got %d", c.want, code)
			}
		})
	}
}

func TestAdminPutAndGet(t *testing.T) {
	opt, store := withAdmin(t)
	h := newHarness(t, opt)

	body := `{"dns_entries":[{"name":"db.internal","ip":"10.0.0.5"}],
	          "egress_allow":[{"cidr":"10.0.0.5/32","port":5432,"proto":"tcp"}]}`
	if code, resp := h.admin(t, http.MethodPut, testAdminKey, body); code != http.StatusOK {
		t.Fatalf("PUT valid policy: %d %s", code, resp)
	}
	if got := store.Get(); len(got.DNS) != 1 || len(got.Egress) != 1 {
		t.Fatalf("store not updated: %+v", got)
	}
	code, resp := h.admin(t, http.MethodGet, testAdminKey, "")
	if code != http.StatusOK || !strings.Contains(resp, "db.internal") || !strings.Contains(resp, "5432") {
		t.Fatalf("GET after PUT: %d %s", code, resp)
	}
}

func TestAdminPutDenyListWins(t *testing.T) {
	opt, store := withAdmin(t)
	h := newHarness(t, opt)

	// Seed a good policy, then try to open the metadata address: it must be
	// refused and leave the stored policy unchanged.
	if code, _ := h.admin(t, http.MethodPut, testAdminKey,
		`{"egress_allow":[{"cidr":"10.0.0.5/32","port":5432,"proto":"tcp"}]}`); code != http.StatusOK {
		t.Fatal("seed PUT failed")
	}
	code, resp := h.admin(t, http.MethodPut, testAdminKey,
		`{"egress_allow":[{"cidr":"169.254.169.254/32","port":80,"proto":"tcp"}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("deny-listed PUT: want 400, got %d %s", code, resp)
	}
	if got := store.Get(); len(got.Egress) != 1 || got.Egress[0].CIDR != "10.0.0.5/32" {
		t.Fatalf("rejected PUT changed the stored policy: %+v", got)
	}
}

func TestAdminPutRejectsUnknownField(t *testing.T) {
	opt, _ := withAdmin(t)
	h := newHarness(t, opt)
	if code, _ := h.admin(t, http.MethodPut, testAdminKey, `{"dns_entries":[],"extra":true}`); code != http.StatusBadRequest {
		t.Fatalf("unknown field: want 400, got %d", code)
	}
}

func TestAdminMethodNotAllowed(t *testing.T) {
	opt, _ := withAdmin(t)
	h := newHarness(t, opt)
	if code, _ := h.admin(t, http.MethodDelete, testAdminKey, ""); code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE: want 405, got %d", code)
	}
}

func TestForwarderPinsTerminalDNS(t *testing.T) {
	const fwd = "172.30.0.3"
	h := newHarness(t, func(o *harnessOpts) {
		o.rules = func(r *sockproxy.Rules) { r.AllowedDNS = fwd }
		o.gateway = func(c *Config) { c.ForwarderDNS = fwd }
	})
	// Any request that starts a container is enough.
	if code, _ := h.get(t, "/files/cwd", "alice", testAPIKey); code != http.StatusOK {
		t.Fatalf("request failed: %d", code)
	}
	h.docker.mu.Lock()
	defer h.docker.mu.Unlock()
	if len(h.docker.seen) == 0 {
		t.Fatal("no container was created")
	}
	for _, body := range h.docker.seen {
		if !strings.Contains(body, `"Dns":["172.30.0.3"]`) {
			t.Fatalf("create body not pinned to the forwarder: %s", body)
		}
	}
}
