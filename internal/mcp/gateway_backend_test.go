package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayBackendWorkspaceLabels(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/control/v1/workspaces" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"name":"default","runtime":"runsc","persistent":true},
			{"name":"scratch","label":"Temporary terminal","description":"Deleted when idle.","runtime":"runsc","persistent":false}
		]`))
	}))
	defer gw.Close()
	ws, err := NewGatewayBackend(gw.URL, "", "k", "").ListWorkspaces(context.Background(), Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 {
		t.Fatalf("got %+v", ws)
	}
	if ws[0].Label != "" || ws[0].Description != "runtime runsc" {
		t.Fatalf("default should fall back to the runtime: %+v", ws[0])
	}
	if ws[1].Label != "Temporary terminal" || ws[1].Description != "Deleted when idle." {
		t.Fatalf("scratch should keep its label and description: %+v", ws[1])
	}
}
