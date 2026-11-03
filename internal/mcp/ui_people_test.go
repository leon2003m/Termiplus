package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

type pickerDirectory struct {
	Directory
	calls     int
	canonical string
}

func (d *pickerDirectory) FindUser(_ context.Context, username string) (*DirUser, error) {
	d.calls++
	if d.canonical != "" {
		username = d.canonical
	}
	return &DirUser{ID: "private-directory-id", Username: username, Email: "private@example.invalid", Enabled: true}, nil
}
func TestPersonPickerOwnerPrivacyAndRate(t *testing.T) {
	f := newUIFixture(t)
	d := &pickerDirectory{}
	f.ui.srv.WithDirectory(d, "")
	f.backend.sessions["desktop"] = Session{ID: "desktop", UserID: "owui-alice", Workspace: "desk", Running: true}
	path := "/ui/api/desktops/desktop/people?q=someone"
	for _, sub := range []string{"", "kc-bob"} {
		code, _, _ := f.do(t, http.MethodGet, path, sub, "", "", nil)
		if code != 401 && code != 404 {
			t.Fatalf("non-owner: %d", code)
		}
	}
	if d.calls != 0 {
		t.Fatal("non-owner queried directory")
	}
	code, body, _ := f.do(t, http.MethodGet, path, "kc-alice", "", "", nil)
	if code != 200 || !strings.Contains(body, "someone") || strings.Contains(body, "private") || strings.Contains(body, "email") {
		t.Fatalf("lookup leaked data: %d %s", code, body)
	}
	if code, _, _ := f.do(t, http.MethodGet, path, "kc-alice", "", "", nil); code != 429 {
		t.Fatalf("rate limit: %d", code)
	}
	deny, _ := ParseEntitlements([]byte(`{"default":"deny","workspaces":{}}`))
	f.ui.srv.WithEntitlements(deny)
	if code, _, _ := f.do(t, http.MethodGet, path, "kc-alice", "", "", nil); code != 404 {
		t.Fatalf("revoked entitlement: %d", code)
	}
	if d.calls != 1 {
		t.Fatalf("directory called %d times", d.calls)
	}
}

func TestPersonPickerCanonicalNameAtCapacity(t *testing.T) {
	f := newUIFixture(t)
	f.ui.srv.WithDirectory(&pickerDirectory{canonical: "someone"}, "")
	f.backend.sessions["desktop"] = Session{ID: "desktop", UserID: "owui-alice", Workspace: "desk", Running: true}
	f.ui.lookups = map[string]time.Time{"owui-alice": time.Now().Add(-2 * time.Second)}
	for i := 0; i < 1023; i++ {
		f.ui.lookups[fmt.Sprint(i)] = time.Now()
	}
	code, body, _ := f.do(t, http.MethodGet, "/ui/api/desktops/desktop/people?q=SomeOne", "kc-alice", "", "", nil)
	if code != 200 || !strings.Contains(body, "someone") {
		t.Fatalf("canonical lookup: %d %s", code, body)
	}
}

func TestPersonPickerSupportedUsernameLengths(t *testing.T) {
	for _, name := range []string{"a", strings.Repeat("a", 255)} {
		f := newUIFixture(t)
		f.ui.srv.WithDirectory(&pickerDirectory{}, "")
		f.backend.sessions["desktop"] = Session{ID: "desktop", UserID: "owui-alice", Workspace: "desk", Running: true}
		code, body, _ := f.do(t, http.MethodGet, "/ui/api/desktops/desktop/people?q="+name, "kc-alice", "", "", nil)
		if code != 200 || !strings.Contains(body, name) {
			t.Fatalf("length %d: %d %s", len(name), code, body)
		}
	}
}
