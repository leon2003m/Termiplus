package sockproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"termiplus/internal/naming"
)

var testRules = Rules{
	Image:       "registry.local/ot-terminal@sha256:abc",
	Runtimes:    map[string]bool{"runsc": true, "sysbox-runc": true},
	Network:     "ot-sandbox",
	MaxMemory:   4 << 30,
	MaxNanoCPUs: 4e9,
	MaxPids:     1024,
}

func goodCreate(name string) map[string]any {
	suffix := strings.TrimPrefix(name, naming.ContainerPrefix)
	return map[string]any{
		"Image":  testRules.Image,
		"Env":    []string{"OPEN_TERMINAL_API_KEY=secret"},
		"Labels": map[string]string{naming.LabelUser: "u1", naming.LabelPolicy: "default"},
		"HostConfig": map[string]any{
			"Runtime":     "runsc",
			"NetworkMode": "ot-sandbox",
			"Memory":      1 << 30,
			"NanoCpus":    1e9,
			"PidsLimit":   256,
			"Mounts": []map[string]any{{
				"Type": "volume", "Source": naming.VolumePrefix + suffix, "Target": "/home/user",
			}},
		},
	}
}

func body(t *testing.T, v any) *strings.Reader {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReader(string(b))
}

func TestCreateAccepted(t *testing.T) {
	name := naming.Container("u1", "default")
	out, err := testRules.ValidateCreate(name, body(t, goodCreate(name)))
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	hc := m["HostConfig"].(map[string]any)
	if hc["Privileged"] != false || hc["Runtime"] != "runsc" {
		t.Fatalf("forced fields missing: %v", hc)
	}
	if m["Labels"].(map[string]any)[naming.ManagedLabel] != "true" {
		t.Fatal("managed label not forced")
	}
}

func TestCreateRejected(t *testing.T) {
	name := naming.Container("u1", "default")
	other := naming.Volume("u2", "default")

	cases := map[string]func(m map[string]any){
		"privileged": func(m map[string]any) { hc(m)["Privileged"] = true },
		"binds":      func(m map[string]any) { hc(m)["Binds"] = []string{"/:/host"} },
		"bind mount": func(m map[string]any) { mounts(m)[0]["Type"] = "bind"; mounts(m)[0]["Source"] = "/" },
		"volume opts": func(m map[string]any) {
			mounts(m)[0]["VolumeOptions"] = map[string]any{"DriverConfig": map[string]any{"Name": "local", "Options": map[string]string{"type": "none", "o": "bind", "device": "/"}}}
		},
		"other volume":  func(m map[string]any) { mounts(m)[0]["Source"] = other },
		"wrong target":  func(m map[string]any) { mounts(m)[0]["Target"] = "/etc" },
		"two mounts":    func(m map[string]any) { hc(m)["Mounts"] = append(mounts(m), mounts(m)[0]) },
		"cap add":       func(m map[string]any) { hc(m)["CapAdd"] = []string{"SYS_ADMIN"} },
		"devices":       func(m map[string]any) { hc(m)["Devices"] = []any{map[string]string{"PathOnHost": "/dev/sda"}} },
		"secopt":        func(m map[string]any) { hc(m)["SecurityOpt"] = []string{"seccomp=unconfined"} },
		"pid host":      func(m map[string]any) { hc(m)["PidMode"] = "host" },
		"runc":          func(m map[string]any) { hc(m)["Runtime"] = "runc" },
		"no runtime":    func(m map[string]any) { delete(hc(m), "Runtime") },
		"host network":  func(m map[string]any) { hc(m)["NetworkMode"] = "host" },
		"other image":   func(m map[string]any) { m["Image"] = "alpine" },
		"no memory":     func(m map[string]any) { delete(hc(m), "Memory") },
		"too much mem":  func(m map[string]any) { hc(m)["Memory"] = int64(64 << 30) },
		"no pids":       func(m map[string]any) { delete(hc(m), "PidsLimit") },
		"bad env":       func(m map[string]any) { m["Env"] = []string{"LD_PRELOAD=/x.so"} },
		"env newline":   func(m map[string]any) { m["Env"] = []string{"OPEN_TERMINAL_X=a\nb"} },
		"foreign label": func(m map[string]any) { m["Labels"] = map[string]string{"traefik.enable": "true"} },
		"user field":    func(m map[string]any) { m["User"] = "root" },
		"entrypoint":    func(m map[string]any) { m["Entrypoint"] = []string{"/bin/sh"} },
		"case trick":    func(m map[string]any) { hc(m)["privileged"] = true },
		"port bindings": func(m map[string]any) {
			hc(m)["PortBindings"] = map[string]any{"8000/tcp": []any{map[string]string{"HostPort": "8000"}}}
		},
		"sysctls":        func(m map[string]any) { hc(m)["Sysctls"] = map[string]string{"net.ipv4.ip_forward": "1"} },
		"userns host":    func(m map[string]any) { hc(m)["UsernsMode"] = "host" },
		"cgroup parent":  func(m map[string]any) { hc(m)["CgroupParent"] = "/" },
		"volumes driver": func(m map[string]any) { hc(m)["VolumeDriver"] = "x" },
	}
	for label, mutate := range cases {
		t.Run(label, func(t *testing.T) {
			m := goodCreate(name)
			mutate(m)
			if _, err := testRules.ValidateCreate(name, body(t, m)); err == nil {
				t.Fatalf("%s: expected rejection", label)
			}
		})
	}
}

func TestCreateBadNames(t *testing.T) {
	for _, n := range []string{"", "evil", "ot-u-xyz", "ot-u-0123456789abcdef-Default", "/ot-u-0123456789abcdef-default", "ot-u-0123456789abcdef-default/../x"} {
		if _, err := testRules.ValidateCreate(n, body(t, goodCreate(naming.Container("u1", "default")))); err == nil {
			t.Fatalf("name %q accepted", n)
		}
	}
}

func TestVolume(t *testing.T) {
	ok := map[string]any{"Name": naming.Volume("u1", "default")}
	if _, err := testRules.ValidateVolume(body(t, ok)); err != nil {
		t.Fatalf("expected ok: %v", err)
	}
	bad := []map[string]any{
		{"Name": naming.Volume("u1", "default"), "DriverOpts": map[string]string{"type": "none", "o": "bind", "device": "/"}},
		{"Name": naming.Volume("u1", "default"), "Driver": "nfs"},
		{"Name": "evil"},
		{"Name": naming.Volume("u1", "default"), "Labels": map[string]string{"x": "y"}},
	}
	for i, b := range bad {
		if _, err := testRules.ValidateVolume(body(t, b)); err == nil {
			t.Fatalf("bad volume %d accepted", i)
		}
	}
}

func TestNormalizePath(t *testing.T) {
	good := map[string]string{
		"/v1.47/containers/create": "/containers/create",
		"/containers/json":         "/containers/json",
		"/_ping":                   "/_ping",
	}
	for in, want := range good {
		u, _ := url.Parse(in)
		got, err := normalizePath(u)
		if err != nil || got != want {
			t.Fatalf("%s: got %q %v", in, got, err)
		}
	}
	for _, in := range []string{
		"/containers/../images/create", "//containers/create", "/containers%2Fcreate",
		"/v1.47/../build", "/containers/x/", "/containers/create;x",
	} {
		u, err := url.ParseRequestURI(in)
		if err != nil {
			continue
		}
		if _, err := normalizePath(u); err == nil {
			t.Fatalf("%s accepted", in)
		}
	}
}

func hc(m map[string]any) map[string]any { return m["HostConfig"].(map[string]any) }
func mounts(m map[string]any) []map[string]any {
	return hc(m)["Mounts"].([]map[string]any)
}

func TestProxyTokenSingleValue(t *testing.T) {
	p := New(testRules, "/nonexistent.sock", "right-token")
	for name, vals := range map[string][]string{
		"missing": nil, "wrong": {"x"}, "duplicate": {"right-token", "x"}, "duplicate-rev": {"x", "right-token"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/_ping", nil)
		for _, v := range vals {
			req.Header.Add("X-Proxy-Token", v)
		}
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: got %d", name, rec.Code)
		}
	}
}

func TestEphemeralCreate(t *testing.T) {
	name := naming.Ephemeral("u1", "chat-1", "scratch")
	m := goodCreate(naming.Container("u1", "default"))
	delete(hc(m), "Mounts")
	out, err := testRules.ValidateCreate(name, body(t, m))
	if err != nil {
		t.Fatalf("expected ok: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	ghc := got["HostConfig"].(map[string]any)
	if ghc["AutoRemove"] != true || ghc["Mounts"] != nil {
		t.Fatalf("ephemeral must auto-remove without mounts: %v", ghc)
	}
	// ephemeral with any mount, even the user's own volume: rejected
	withMount := goodCreate(naming.Container("u1", "default"))
	if _, err := testRules.ValidateCreate(name, body(t, withMount)); err == nil {
		t.Fatal("ephemeral with a mount accepted")
	}
	// persistent without a mount: rejected
	if _, err := testRules.ValidateCreate(naming.Container("u1", "default"), body(t, m)); err == nil {
		t.Fatal("persistent without a mount accepted")
	}
	// client cannot turn off AutoRemove or smuggle it for persistent ones
	m2 := goodCreate(naming.Container("u1", "default"))
	hc(m2)["AutoRemove"] = false
	if _, err := testRules.ValidateCreate(naming.Container("u1", "default"), body(t, m2)); err == nil {
		t.Fatal("unknown field AutoRemove accepted")
	}
}
