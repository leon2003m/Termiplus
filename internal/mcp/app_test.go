package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppAdvertisedOnInitialize(t *testing.T) {
	var out struct {
		Capabilities struct {
			Resources  map[string]any            `json:"resources"`
			Extensions map[string]map[string]any `json:"extensions"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(call(t, newTestServer(), "", "initialize", nil).Result, &out); err != nil {
		t.Fatal(err)
	}
	if out.Capabilities.Resources == nil || out.Capabilities.Extensions[appExtension] == nil {
		t.Fatalf("MCP App extension not advertised: %+v", out.Capabilities)
	}
}

func TestToolsListAppMetadata(t *testing.T) {
	var out struct {
		Tools []toolSpec `json:"tools"`
	}
	if err := json.Unmarshal(call(t, newTestServer(), "", "tools/list", nil).Result, &out); err != nil {
		t.Fatal(err)
	}
	vis := map[string]string{}
	for _, tl := range out.Tools {
		ui, _ := tl.Meta["ui"].(map[string]any)
		v, _ := json.Marshal(ui["visibility"])
		vis[tl.Name] = string(v)
		if tl.Name == appToolName && ui["resourceUri"] != appResourceURI {
			t.Fatalf("open_workspaces has no UI resource: %+v", tl.Meta)
		}
	}
	if vis[appToolName] != `["model","app"]` {
		t.Fatalf("open_workspaces visibility = %s", vis[appToolName])
	}
	for _, name := range []string{"list_workspaces", "request_session", "destroy_session", "reset_workspace", "manage_snapshots", "share_desktop"} {
		if _, listed := vis[name]; listed && vis[name] != `["app"]` {
			t.Errorf("%s should be app-only, got %s", name, vis[name])
		}
	}
}

func TestAppResourcesNeedAuth(t *testing.T) {
	s := newTestServer()
	for _, m := range []string{"resources/list", "resources/read"} {
		resp := call(t, s, "", m, map[string]any{"uri": appResourceURI})
		if resp.Error == nil || resp.Error.Code != codeUnauthenticated {
			t.Fatalf("%s without auth: %+v", m, resp)
		}
	}
}

func TestAppResourceRead(t *testing.T) {
	s := newTestServer()
	var out struct {
		Contents []struct {
			URI      string         `json:"uri"`
			MIMEType string         `json:"mimeType"`
			Text     string         `json:"text"`
			Meta     map[string]any `json:"_meta"`
		} `json:"contents"`
	}
	resp := call(t, s, "user-tok", "resources/read", map[string]any{"uri": appResourceURI})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil || len(out.Contents) != 1 {
		t.Fatalf("decode: %v %s", err, resp.Result)
	}
	c := out.Contents[0]
	if c.MIMEType != appMIMEType || !strings.Contains(c.Text, "<script>") {
		t.Fatalf("unexpected resource: %s", c.MIMEType)
	}
	// Self-contained: no remote scripts, styles, frames or innerHTML sinks.
	for _, bad := range []string{"<script src", "<link", "<iframe", "innerHTML", "outerHTML", "insertAdjacentHTML", "eval(", "request_session"} {
		if strings.Contains(c.Text, bad) {
			t.Errorf("panel contains %q", bad)
		}
	}
	csp, _ := json.Marshal(c.Meta["ui"].(map[string]any)["csp"])
	if strings.Contains(string(csp), "http") {
		t.Fatalf("panel CSP allows remote origins: %s", csp)
	}
	resp = call(t, s, "user-tok", "resources/read", map[string]any{"uri": "ui://termiplus/other"})
	if resp.Error == nil || resp.Error.Code != codeInvalidParams {
		t.Fatalf("unknown resource: %+v", resp)
	}
}

func TestOpenWorkspacesIsReadOnlyAndScoped(t *testing.T) {
	s := newTestServer()
	var mine Session
	result(t, toolCall(t, s, "user-tok", "request_session", map[string]any{"workspace": "default"}), &mine)

	resp := toolCall(t, s, "persist-tok", appToolName, nil)
	var bob struct {
		Workspaces []Workspace `json:"workspaces"`
		Sessions   []Session   `json:"sessions"`
	}
	result(t, resp, &bob)
	if len(bob.Sessions) != 0 || len(bob.Workspaces) == 0 {
		t.Fatalf("bob's overview leaked or empty: %+v", bob)
	}
	var tr toolResult
	_ = json.Unmarshal(resp.Result, &tr)
	if len(tr.StructuredContent) == 0 {
		t.Fatal("open_workspaces should return structuredContent for the panel")
	}

	var alice struct {
		Sessions []Session `json:"sessions"`
	}
	result(t, toolCall(t, s, "user-tok", appToolName, nil), &alice)
	if len(alice.Sessions) != 1 || alice.Sessions[0].ID != mine.ID {
		t.Fatalf("alice overview: %+v", alice)
	}
	// Opening the panel never provisions anything.
	var after []Session
	result(t, toolCall(t, s, "user-tok", "list_sessions", nil), &after)
	if len(after) != 1 {
		t.Fatalf("open_workspaces changed sessions: %d", len(after))
	}
}

// App-only metadata changes nothing on the server: a forged call without
// _confirmed is still refused.
func TestAppOnlyToolsStillNeedConfirmation(t *testing.T) {
	s := newTestServer()
	var sess Session
	result(t, toolCall(t, s, "user-tok", "request_session", map[string]any{"workspace": "default"}), &sess)
	resp := toolCall(t, s, "user-tok", "destroy_session", map[string]any{"session_id": sess.ID})
	if resp.Error == nil || resp.Error.Code != codeConfirmRequired {
		t.Fatalf("want confirmation-required, got %+v", resp.Error)
	}
}

func TestNotificationsGet202(t *testing.T) {
	s := newTestServer()
	for _, body := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}`,
	} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body))))
		if w.Code != http.StatusAccepted || w.Body.Len() != 0 {
			t.Fatalf("%s: %d %q", body, w.Code, w.Body.String())
		}
	}
}
