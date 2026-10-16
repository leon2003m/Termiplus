package gateway

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"termiplus/internal/naming"
	"termiplus/internal/sockproxy"
)

func homesHarness(t *testing.T, allowRemove bool) *harness {
	t.Helper()
	adminOpt, _ := withAdmin(t)
	return newHarness(t, adminOpt, func(o *harnessOpts) {
		o.rules = func(r *sockproxy.Rules) { r.AllowHomeRemove = allowRemove }
	})
}

func (h *harness) homes(t *testing.T, method, path, user string, body io.Reader) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, h.gw.URL+"/admin/homes/"+path, body)
	req.Header.Set("X-Admin-Key", testAdminKey)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func tarNames(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("bad tar: %v", err)
		}
		c, _ := io.ReadAll(tr)
		out[h.Name] = string(c)
	}
}

func homeTar(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "user/", Typeflag: tar.TypeDir, Mode: 0o755})
	for n, c := range files {
		_ = tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(c))})
		_, _ = io.WriteString(tw, c)
	}
	_ = tw.Close()
	return &buf
}

func (h *harness) noHelpersLeft(t *testing.T) {
	t.Helper()
	h.docker.mu.Lock()
	defer h.docker.mu.Unlock()
	for n := range h.docker.ctrs {
		if strings.HasPrefix(n, naming.ArchivePrefix) {
			t.Fatalf("archive helper %s left behind", n)
		}
	}
}

func TestHomeExportImport(t *testing.T) {
	h := homesHarness(t, false)
	if code, _ := h.get(t, "/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatal("start failed")
	}
	vol := naming.Volume("alice", "default")
	h.docker.mu.Lock()
	h.docker.files[vol] = map[string]string{"notes.txt": "hello"}
	h.docker.mu.Unlock()

	code, b := h.homes(t, http.MethodGet, "default/archive", "alice", nil)
	if code != 200 || tarNames(t, b)["user/notes.txt"] != "hello" {
		t.Fatalf("export: %d %v", code, tarNames(t, b))
	}
	h.noHelpersLeft(t)
	// The running workspace was not touched by the export.
	if !h.ctr(naming.Container("alice", "default")).running {
		t.Fatal("export stopped the workspace")
	}

	// Import: entries are re-rooted below the home, workspaces using the home
	// (default and sysbox share it) are stopped first.
	code, _ = h.homes(t, http.MethodPut, "default/archive", "alice", homeTar(t, map[string]string{"user/restored.txt": "back", "user/sub/x": "y"}))
	if code != http.StatusNoContent {
		t.Fatalf("import: %d", code)
	}
	h.docker.mu.Lock()
	got, puts := h.docker.files[vol], strings.Join(h.docker.tarPuts, ",")
	h.docker.mu.Unlock()
	if got["restored.txt"] != "back" || got["sub/x"] != "y" || got["notes.txt"] != "hello" {
		t.Fatalf("home after import: %v", got)
	}
	if strings.Contains(puts, "user") {
		t.Fatalf("entries must be relative to the home: %s", puts)
	}
	if h.ctr(naming.Container("alice", "default")).running {
		t.Fatal("workspace must be stopped for an import")
	}
	h.noHelpersLeft(t)
	// It starts again on the next use.
	if code, _ := h.get(t, "/files/cwd", "alice", testAPIKey); code != 200 {
		t.Fatal("restart after import failed")
	}
}

func TestHomeImportCreatesHomeAndChecksInput(t *testing.T) {
	h := homesHarness(t, false)
	if code, _ := h.homes(t, http.MethodPut, "default/archive", "carol", homeTar(t, map[string]string{"user/a": "1"})); code != http.StatusNoContent {
		t.Fatalf("import into a new home: %d", code)
	}
	if h.docker.files[naming.Volume("carol", "default")]["a"] != "1" {
		t.Fatal("new home not filled")
	}
	if code, _ := h.homes(t, http.MethodPut, "default/archive", "carol", homeTar(t, map[string]string{"etc/passwd": "x"})); code != http.StatusBadGateway {
		t.Fatalf("entry outside user/ accepted: %d", code)
	}
	for _, c := range []struct{ method, path, user string }{
		{http.MethodGet, "default/archive", ""},
		{http.MethodGet, "nope/archive", "carol"},
		{http.MethodGet, "scratch/archive", "carol"}, // ephemeral: no home
		{http.MethodGet, "default/archive", "dave"},  // no home yet
		{http.MethodPost, "default/archive", "carol"},
	} {
		if code, _ := h.homes(t, c.method, c.path, c.user, nil); code < 400 {
			t.Errorf("%s %s as %q: %d", c.method, c.path, c.user, code)
		}
	}
}

func TestHomeRemove(t *testing.T) {
	h := homesHarness(t, false)
	h.get(t, "/files/cwd", "alice", testAPIKey)
	vol := naming.Volume("alice", "default")
	if code, _ := h.homes(t, http.MethodDelete, "default", "alice", nil); code != http.StatusBadGateway {
		t.Fatalf("remove without ALLOW_HOME_REMOVE: %d", code)
	}
	if _, ok := h.docker.volumes[vol]; !ok {
		t.Fatal("volume deleted although the proxy does not allow it")
	}

	h = homesHarness(t, true)
	h.get(t, "/files/cwd", "alice", testAPIKey)
	h.get(t, "/p/sysbox/files/cwd", "alice", testAPIKey)
	h.get(t, "/files/cwd", "bob", testAPIKey)
	if code, _ := h.homes(t, http.MethodDelete, "default", "alice", nil); code != http.StatusNoContent {
		t.Fatalf("remove: %d", code)
	}
	for _, n := range []string{naming.Container("alice", "default"), naming.Container("alice", "sysbox")} {
		if h.ctr(n) != nil {
			t.Fatalf("%s still exists", n)
		}
	}
	if _, ok := h.docker.volumes[vol]; ok {
		t.Fatal("home volume still exists")
	}
	if h.ctr(naming.Container("bob", "default")) == nil || h.docker.volumes[naming.Volume("bob", "default")] == nil {
		t.Fatal("another user's workspace was touched")
	}
}

func TestProxyArchiveRules(t *testing.T) {
	h := homesHarness(t, false)
	h.get(t, "/files/cwd", "alice", testAPIKey)
	term := naming.Container("alice", "default")
	do := func(method, path string, body io.Reader) int {
		req, _ := http.NewRequest(method, h.proxy.URL+path, body)
		req.Header.Set("X-Proxy-Token", "proxy-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, c := range []struct {
		method, path string
	}{
		{http.MethodGet, "/containers/" + term + "/archive?path=/home/user"}, // terminals: never
		{http.MethodPut, "/containers/" + term + "/archive?path=/home/user"}, // terminals: never
		{http.MethodGet, "/containers/" + term + "/archive?path=/etc"},       // other paths: never
		{http.MethodGet, "/containers/" + term + "/archive?path=/home/user&path=/etc"},
		{http.MethodHead, "/containers/" + term + "/archive?path=/home/user"},
		{http.MethodDelete, "/volumes/" + naming.Volume("alice", "default")}, // removal off
		{http.MethodDelete, "/volumes/x"},
	} {
		if code := do(c.method, c.path, nil); code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", c.method, c.path, code)
		}
	}
	// A helper exists only while the gateway uses it; create one by hand to
	// check it can never be started.
	helper := naming.Archive("alice", "default")
	if err := h.g.mgr.withHelper(t.Context(), "alice", "default", func(name string) error {
		if code := do(http.MethodPost, "/containers/"+name+"/start", nil); code != http.StatusForbidden {
			t.Errorf("helper start: %d", code)
		}
		if code := do(http.MethodGet, "/containers/"+name+"/archive?path=/etc", nil); code != http.StatusForbidden {
			t.Errorf("helper other path: %d", code)
		}
		return nil
	}); err != nil || helper == "" {
		t.Fatal(err)
	}
	// A helper must mount its own home, not someone else's.
	body := `{"Image":"` + testImage + `","Labels":{},"HostConfig":{"Runtime":"runsc","NetworkMode":"ot-sandbox","Memory":1073741824,"NanoCpus":1000000000,"PidsLimit":256,"Mounts":[{"Type":"volume","Source":"` + naming.Volume("alice", "sysbox") + `","Target":"/home/user"}]}}`
	if code := do(http.MethodPost, "/containers/create?name="+helper, strings.NewReader(body)); code != http.StatusForbidden {
		t.Fatalf("helper with another home: %d", code)
	}
}

func TestRerootHomeTar(t *testing.T) {
	var out bytes.Buffer
	if err := rerootHomeTar(homeTar(t, map[string]string{"user/a": "1", "./user/b": "2"}), &out); err != nil {
		t.Fatal(err)
	}
	got := tarNames(t, out.Bytes())
	if len(got) != 2 || got["a"] != "1" || got["b"] != "2" {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"other/a", "a", "user/../x", "user/a/../../x", "user//a", "/user/a"} {
		var o bytes.Buffer
		if err := rerootHomeTar(homeTar(t, map[string]string{bad: "x"}), &o); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
