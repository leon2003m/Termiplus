package gateway

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"termiplus/internal/mcp"
	"termiplus/internal/naming"
	"termiplus/internal/sockproxy"
)

// End to end through MCP server -> gateway control API -> socket proxy ->
// fake dockerd: workspace reset, snapshots, shared desktops and the
// departed-user cleanup.

func withBackups(t *testing.T, keep int, allowRemove bool) func(*harnessOpts) {
	dir := t.TempDir()
	return func(o *harnessOpts) {
		o.gateway = func(c *Config) {
			c.ControlKey = testControlKey
			c.BackupDir = dir
			c.SnapshotKeep = keep
		}
		o.rules = func(r *sockproxy.Rules) { r.AllowHomeRemove = allowRemove }
	}
}

func newHomesChain(t *testing.T, opt func(*harnessOpts)) *mcpChain {
	t.Helper()
	h := newHarness(t, opt)
	auth := mcp.NewStaticAuthenticator(map[string]mcp.Principal{
		"alice-tok": {UserID: "alice"},
		"bob-tok":   {UserID: "bob", Roles: []string{mcp.RolePersist}},
		"root-tok":  {UserID: "root", Subject: "kc-root", Roles: []string{mcp.RoleAdmin}},
	})
	backend := mcp.NewGatewayBackend(h.gw.URL, "https://terminals.example", testControlKey, "")
	srv := mcp.NewServer(auth, backend, "test").WithUIBase("https://desk.example")
	return &mcpChain{h: h, srv: srv}
}

func (c *mcpChain) files(vol string) map[string]string {
	c.h.docker.mu.Lock()
	defer c.h.docker.mu.Unlock()
	out := map[string]string{}
	for k, v := range c.h.docker.files[vol] {
		out[k] = v
	}
	return out
}

func (c *mcpChain) setFiles(vol string, files map[string]string) {
	c.h.docker.mu.Lock()
	defer c.h.docker.mu.Unlock()
	c.h.docker.files[vol] = files
}

func (c *mcpChain) code(t *testing.T, token, tool string, args map[string]any) int {
	t.Helper()
	out := c.call(t, token, tool, args)
	if out.Error != nil {
		return out.Error.Code
	}
	if out.Result.IsError {
		return 1
	}
	return 0
}

func TestMCPResetWorkspace(t *testing.T) {
	c := newHomesChain(t, withBackups(t, 3, false))
	var s mcp.Session
	c.ok(t, "bob-tok", "request_session", map[string]any{"workspace": "default", "persistent": true, "_confirmed": true}, &s)
	vol := naming.Volume("bob", "default")
	c.setFiles(vol, map[string]string{"keep.txt": "1"})
	firstID := c.h.ctr(s.ID).id

	if code := c.code(t, "bob-tok", "reset_workspace", map[string]any{"session_id": s.ID}); code != -32010 {
		t.Fatalf("reset without confirmation: %d", code)
	}
	if code := c.code(t, "alice-tok", "reset_workspace", map[string]any{"session_id": s.ID, "_confirmed": true}); code != 1 {
		t.Fatalf("alice resetting bob's workspace: %d", code)
	}
	if c.h.ctr(s.ID).id != firstID {
		t.Fatal("container replaced without a valid reset")
	}
	var after map[string]any
	c.ok(t, "bob-tok", "reset_workspace", map[string]any{"session_id": s.ID, "_confirmed": true}, &after)
	ct := c.h.ctr(s.ID)
	if ct == nil || ct.id == firstID || !ct.running || after["running"] != true {
		t.Fatalf("reset should give a fresh running container: %+v %v", ct, after)
	}
	if c.files(vol)["keep.txt"] != "1" {
		t.Fatal("reset lost the home")
	}

	// A temporary sandbox is just removed.
	var e mcp.Session
	c.ok(t, "alice-tok", "request_session", map[string]any{"workspace": "scratch"}, &e)
	c.ok(t, "alice-tok", "reset_workspace", map[string]any{"session_id": e.ID, "_confirmed": true}, &after)
	if c.h.ctr(e.ID) != nil {
		t.Fatal("sandbox still there after reset")
	}
}

func TestMCPSnapshots(t *testing.T) {
	c := newHomesChain(t, withBackups(t, 3, false))
	var s mcp.Session
	c.ok(t, "bob-tok", "request_session", map[string]any{"workspace": "default", "persistent": true, "_confirmed": true}, &s)
	vol := naming.Volume("bob", "default")
	c.setFiles(vol, map[string]string{"a": "1"})

	var snap struct {
		ID          string `json:"id"`
		DownloadURL string `json:"download_url"`
	}
	c.ok(t, "bob-tok", "manage_snapshots", map[string]any{"action": "create", "workspace": "default"}, &snap)
	if snap.ID == "" || snap.DownloadURL != "https://desk.example/ui/api/workspaces/default/home/snapshots/"+snap.ID {
		t.Fatalf("snapshot: %+v", snap)
	}
	c.setFiles(vol, map[string]string{"a": "2", "b": "new"})

	if code := c.code(t, "bob-tok", "manage_snapshots", map[string]any{"action": "restore", "workspace": "default", "snapshot_id": snap.ID}); code != -32010 {
		t.Fatalf("restore without confirmation: %d", code)
	}
	c.ok(t, "bob-tok", "manage_snapshots", map[string]any{"action": "restore", "workspace": "default", "snapshot_id": snap.ID, "_confirmed": true}, nil)
	if f := c.files(vol); f["a"] != "1" || f["b"] != "new" {
		t.Fatalf("after restore: %v", f)
	}
	if c.h.ctr(s.ID).running {
		t.Fatal("restore must stop the workspace")
	}

	// The download is the gzip'd tar of the home at snapshot time.
	hb := mcp.NewGatewayBackend(c.h.gw.URL, "", testControlKey, "")
	resp, err := hb.HomeDownload(context.Background(), mcp.Principal{UserID: "bob"}, "default", snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	got := map[string]string{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		got[h.Name] = string(b)
	}
	resp.Body.Close()
	if got["user/a"] != "1" || got["user/b"] != "" {
		t.Fatalf("snapshot content: %v", got)
	}
	// Alice has no home there, and cannot reach bob's snapshot.
	if _, err := hb.HomeDownload(context.Background(), mcp.Principal{UserID: "alice"}, "default", snap.ID); err == nil {
		t.Fatal("alice downloaded bob's snapshot")
	}

	// Only the newest `keep` (3) stay. Below keep a create needs no
	// confirmation; once it would push out the oldest snapshot it does, like
	// delete (security audit run 1).
	created := 1
	for i := 0; i < 4; i++ {
		args := map[string]any{"action": "create", "workspace": "default"}
		if created >= 3 {
			if code := c.code(t, "bob-tok", "manage_snapshots", args); code != -32010 {
				t.Fatalf("pruning create without confirmation: %d", code)
			}
			args["_confirmed"] = true
		}
		c.ok(t, "bob-tok", "manage_snapshots", args, nil)
		created++
	}
	var list struct {
		Snapshots []struct{ ID string } `json:"snapshots"`
		Current   string                `json:"current_home_download_url"`
	}
	c.ok(t, "bob-tok", "manage_snapshots", map[string]any{"action": "list", "workspace": "default"}, &list)
	if len(list.Snapshots) != 3 || list.Current != "https://desk.example/ui/api/workspaces/default/home/archive" {
		t.Fatalf("list: %+v", list)
	}
	for _, x := range list.Snapshots {
		if x.ID == snap.ID {
			t.Fatal("the oldest snapshot should have been pruned")
		}
	}
	if code := c.code(t, "bob-tok", "manage_snapshots", map[string]any{"action": "delete", "workspace": "default", "snapshot_id": list.Snapshots[0].ID}); code != -32010 {
		t.Fatalf("delete without confirmation: %d", code)
	}
	c.ok(t, "bob-tok", "manage_snapshots", map[string]any{"action": "delete", "workspace": "default", "snapshot_id": list.Snapshots[0].ID, "_confirmed": true}, nil)
	c.ok(t, "bob-tok", "manage_snapshots", map[string]any{"action": "list", "workspace": "default"}, &list)
	if len(list.Snapshots) != 2 {
		t.Fatalf("after delete: %+v", list)
	}

	for _, bad := range []map[string]any{
		{"action": "create", "workspace": "scratch"},                                                // no home
		{"action": "restore", "workspace": "default", "snapshot_id": "../../x", "_confirmed": true}, // bad id
		{"action": "create", "workspace": "nope"},
	} {
		if code := c.code(t, "bob-tok", "manage_snapshots", bad); code == 0 {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestMCPShareDesktop(t *testing.T) {
	var h *harness
	h = newHarness(t, withDesktop(&h))
	auth := mcp.NewStaticAuthenticator(map[string]mcp.Principal{
		"alice-tok": {UserID: "alice", Roles: []string{mcp.RolePersist}},
	})
	backend := mcp.NewGatewayBackend(h.gw.URL, "", testControlKey, "").WithStreamBase("https://desk.example")
	dir := &fakeDir{users: []mcp.DirUser{{ID: "kc-bob", Username: "bob", Enabled: true}, {ID: "kc-carol", Username: "carol", Enabled: true}}}
	sharesPath := filepath.Join(t.TempDir(), "shares.json")
	shares, err := mcp.OpenShareStore(sharesPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(auth, backend, "test").WithUIBase("https://console.example").WithDesktopBase("https://desk.example").WithShares(shares).WithDirectory(dir, "")
	c := &mcpChain{h: h, srv: srv}
	var s mcp.Session
	c.ok(t, "alice-tok", "request_session", map[string]any{"workspace": "desk", "persistent": true, "_confirmed": true}, &s)
	var term mcp.Session
	c.ok(t, "alice-tok", "request_session", map[string]any{"workspace": "default", "persistent": true, "_confirmed": true}, &term)

	if code := c.code(t, "alice-tok", "share_desktop", map[string]any{"action": "create", "session_id": s.ID}); code != -32010 {
		t.Fatalf("share without confirmation: %d", code)
	}
	if code := c.code(t, "alice-tok", "share_desktop", map[string]any{"action": "create", "session_id": term.ID, "_confirmed": true}); code != 1 {
		t.Fatalf("sharing a session without desktop: %d", code)
	}
	var any1, onlyBob struct {
		ID  string `json:"share_id"`
		URL string `json:"url"`
	}
	c.ok(t, "alice-tok", "share_desktop", map[string]any{"action": "create", "session_id": s.ID, "_confirmed": true}, &any1)
	c.ok(t, "alice-tok", "share_desktop", map[string]any{"action": "create", "session_id": s.ID, "for_user": "bob", "minutes": 10, "_confirmed": true}, &onlyBob)
	if !strings.HasPrefix(any1.URL, "https://desk.example/share/") || len(strings.TrimSuffix(strings.TrimPrefix(any1.URL, "https://desk.example/share/"), "/")) != 64 {
		t.Fatalf("share url: %q", any1.URL)
	}
	// The store never holds the token itself.
	raw, _ := os.ReadFile(sharesPath)
	if strings.Contains(string(raw), strings.TrimSuffix(strings.TrimPrefix(any1.URL, "https://desk.example/share/"), "/")) {
		t.Fatal("share token stored in clear")
	}

	ids, _ := mcp.OpenIdentityStore(filepath.Join(t.TempDir(), "ids.json"))
	ui, err := mcp.NewDesktopHandler(srv, ids, h.gw.URL, testControlKey)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(ui)
	t.Cleanup(front.Close)
	get := func(sub, path string, hdr map[string]string) (int, string, http.Header) {
		req, _ := http.NewRequest(http.MethodGet, front.URL+path, nil)
		if sub != "" {
			req.Header.Set("X-Forwarded-User", sub)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	p1 := strings.TrimPrefix(any1.URL, "https://desk.example")
	p2 := strings.TrimPrefix(onlyBob.URL, "https://desk.example")
	tok1 := strings.TrimSuffix(strings.TrimPrefix(p1, "/share/"), "/")

	// The page gets the WebSocket path below the share, view only.
	if code, _, hdr := get("kc-carol", p1, nil); code != http.StatusFound ||
		hdr.Get("Location") != p1+"?path=share%2F"+tok1+"%2Fwebsockify&view_only=1" {
		t.Fatalf("share page: %d %q", code, hdr.Get("Location"))
	}
	// Any signed-in user (carol is not even linked to Open WebUI) watches as
	// KasmVNC's view-only user; client attempts to switch to the owner fail.
	code, body, _ := get("kc-carol", p1+"app.js", map[string]string{"X-Stream-View": "0", "Authorization": "Basic dXNlcjp4"})
	if code != 200 || !strings.Contains(body, `"as":"viewer"`) {
		t.Fatalf("viewer: %d %s", code, body)
	}
	if code, _, _ := get("", p1+"app.js", nil); code != http.StatusUnauthorized {
		t.Fatalf("not signed in: %d", code)
	}
	// The named share only for bob.
	if code, _, _ := get("kc-carol", p2+"app.js", nil); code != http.StatusForbidden {
		t.Fatalf("carol on bob's share: %d", code)
	}
	if code, body, _ := get("kc-bob", p2+"app.js", nil); code != 200 || !strings.Contains(body, `"as":"viewer"`) {
		t.Fatalf("bob: %d %s", code, body)
	}
	// Cross-origin WebSockets are refused here too.
	if code, _, _ := get("kc-bob", p2+"websockify", map[string]string{"Connection": "Upgrade", "Upgrade": "websocket", "Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Fatalf("cross-origin ws: %d", code)
	}
	// Unknown or guessed tokens.
	for _, p := range []string{"/share/" + strings.Repeat("0", 64) + "/app.js", "/share/x/app.js", "/share/" + tok1[:63] + "/app.js"} {
		if code, _, _ := get("kc-bob", p, nil); code != http.StatusNotFound {
			t.Errorf("%s: %d", p, code)
		}
	}
	// Revoke ends it.
	var list struct {
		Shares []struct {
			ID string `json:"share_id"`
		} `json:"shares"`
	}
	c.ok(t, "alice-tok", "share_desktop", map[string]any{"action": "list"}, &list)
	if len(list.Shares) != 2 {
		t.Fatalf("list: %+v", list)
	}
	c.ok(t, "alice-tok", "share_desktop", map[string]any{"action": "revoke", "share_id": any1.ID}, nil)
	if code, _, _ := get("kc-carol", p1+"app.js", nil); code != http.StatusNotFound {
		t.Fatalf("revoked share still works: %d", code)
	}
	// The owner's own stream is unchanged: full user, even if a client asks
	// for view mode.
	ids2, _ := mcp.OpenIdentityStore(filepath.Join(t.TempDir(), "ids2.json"))
	_ = ids2.Record("kc-alice", "alice")
	ui2, _ := mcp.NewDesktopHandler(srv, ids2, h.gw.URL, testControlKey)
	front2 := httptest.NewServer(ui2)
	t.Cleanup(front2.Close)
	req, _ := http.NewRequest(http.MethodGet, front2.URL+"/stream/"+s.ID+"/app.js", nil)
	req.Header.Set("X-Forwarded-User", "kc-alice")
	req.Header.Set("X-Stream-View", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"as":"user"`) {
		t.Fatalf("owner stream: %s", b)
	}
}

// fakeDir is a Directory for the departed-user and share tests.
type fakeDir struct {
	mu    sync.Mutex
	users []mcp.DirUser
	fail  bool
}

func (f *fakeDir) set(id string, enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].Enabled = enabled
		}
	}
}

func (f *fakeDir) Groups(context.Context) ([]mcp.DirGroup, error)             { return nil, nil }
func (f *fakeDir) Members(context.Context, string) ([]mcp.DirUser, error)     { return nil, nil }
func (f *fakeDir) UserGroups(context.Context, string) ([]mcp.DirGroup, error) { return nil, nil }
func (f *fakeDir) AddToGroup(context.Context, string, string) error           { return nil }
func (f *fakeDir) RemoveFromGroup(context.Context, string, string) error      { return nil }
func (f *fakeDir) FindUser(_ context.Context, name string) (*mcp.DirUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.Username == name {
			u := u
			return &u, nil
		}
	}
	return nil, nil
}
func (f *fakeDir) Users(context.Context) ([]mcp.DirUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("keycloak down")
	}
	return append([]mcp.DirUser(nil), f.users...), nil
}

func TestDepartedUsers(t *testing.T) {
	opt := withBackups(t, 3, true)
	c := newHomesChain(t, opt)
	dir := &fakeDir{users: []mcp.DirUser{
		{ID: "kc-bob", Username: "bob", Enabled: true},
		{ID: "kc-dave", Username: "dave", Enabled: true},
		{ID: "kc-root", Username: "root", Enabled: true}, // the admin calling manage_departed
	}}
	ids, _ := mcp.OpenIdentityStore(filepath.Join(t.TempDir(), "ids.json"))
	_ = ids.Record("kc-bob", "bob")   // bob's workspaces belong to Open WebUI user "bob"
	_ = ids.Record("kc-erin", "erin") // erin was deleted from the directory
	store, err := mcp.OpenDepartedStore(filepath.Join(t.TempDir(), "departed.json"))
	if err != nil {
		t.Fatal(err)
	}
	audit, _ := mcp.OpenAuditLog(filepath.Join(t.TempDir(), "audit.log"), 0)
	c.srv.WithDirectory(dir, "").WithIdentities(ids).WithDeparted(store, 20*time.Millisecond).WithAuditLog(audit)

	var s mcp.Session
	c.ok(t, "bob-tok", "request_session", map[string]any{"workspace": "default", "persistent": true, "_confirmed": true}, &s)
	vol := naming.Volume("bob", "default")
	c.setFiles(vol, map[string]string{"thesis.tex": "chapter 1"})

	ctx := context.Background()
	// A failing directory changes nothing.
	dir.fail = true
	if err := c.srv.CheckDeparted(ctx); err == nil {
		t.Fatal("directory error ignored")
	}
	dir.fail = false
	// Neither does an empty one (a misconfigured realm is not everyone leaving).
	saved := dir.users
	dir.users = nil
	if err := c.srv.CheckDeparted(ctx); err != nil {
		t.Fatal(err)
	}
	var none struct {
		Departed []mcp.DepartedEntry `json:"departed"`
	}
	c.ok(t, "root-tok", "manage_departed", map[string]any{"action": "list"}, &none)
	if len(none.Departed) != 0 {
		t.Fatalf("empty listing marked users departed: %+v", none.Departed)
	}
	dir.users = saved

	dir.set("kc-bob", false)
	if err := c.srv.CheckDeparted(ctx); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Departed []mcp.DepartedEntry `json:"departed"`
	}
	c.ok(t, "root-tok", "manage_departed", map[string]any{"action": "list"}, &list)
	reasons := map[string]string{}
	for _, e := range list.Departed {
		reasons[e.Subject] = e.Reason
		if e.ArchivedAt != nil {
			t.Fatalf("archived before the grace period: %+v", e)
		}
	}
	if reasons["kc-bob"] != "disabled" || reasons["kc-erin"] != "deleted" || len(reasons) != 2 {
		t.Fatalf("departed: %v", reasons)
	}
	if c.h.ctr(s.ID) == nil {
		t.Fatal("workspace removed before the grace period")
	}

	time.Sleep(30 * time.Millisecond)
	if err := c.srv.CheckDeparted(ctx); err != nil {
		t.Fatal(err)
	}
	if c.h.ctr(s.ID) != nil || c.h.docker.volumes[vol] != nil {
		t.Fatal("bob's workspace and home should be archived away")
	}
	c.ok(t, "root-tok", "manage_departed", map[string]any{"action": "list"}, &list)
	for _, e := range list.Departed {
		if e.Subject == "kc-bob" && (e.ArchivedAt == nil || e.Error != "") {
			t.Fatalf("bob: %+v", e)
		}
	}
	entries, _ := audit.Read(mcp.AuditFilter{Tool: "manage_departed"})
	if len(entries) == 0 || entries[0].Actor != "termiplus" {
		t.Fatalf("archiving must be in the audit log: %+v", entries)
	}

	// Bob comes back: his home is restored.
	dir.set("kc-bob", true)
	if err := c.srv.CheckDeparted(ctx); err != nil {
		t.Fatal(err)
	}
	if c.files(vol)["thesis.tex"] != "chapter 1" {
		t.Fatalf("home not restored: %v", c.files(vol))
	}

	// Admin tool: enabled users cannot be archived; non-admins see nothing.
	if code := c.code(t, "root-tok", "manage_departed", map[string]any{"action": "archive", "username": "dave", "_confirmed": true}); code != 1 {
		t.Fatalf("archiving an enabled user: %d", code)
	}
	if code := c.code(t, "bob-tok", "manage_departed", map[string]any{"action": "list"}); code != -32003 {
		t.Fatalf("non-admin: %d", code)
	}
	if code := c.code(t, "root-tok", "manage_departed", map[string]any{"action": "restore", "username": "bob"}); code != -32010 {
		t.Fatalf("restore without confirmation: %d", code)
	}

	// The archive file is a gzip'd tar of the home.
	var found bool
	_ = filepath.Walk(c.h.g.cfg.BackupDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && strings.Contains(p, "/archived/"+vol+"/") && strings.HasSuffix(p, ".tar.gz") {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatal("no archive file")
	}
}
