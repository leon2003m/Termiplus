package mcp

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var iconLink = regexp.MustCompile(`<link rel="icon" [^>]*href="([^"]+)"`)

// Every HTML page termiplus serves links an icon that exists, so browser tabs
// and bookmarks show termiplus rather than a blank page.
func TestEveryPageLinksFavicon(t *testing.T) {
	pages := map[string]string{
		"stoppedPage":       stoppedPage("https://console.example", "s1"),
		"sharedStoppedPage": sharedStoppedPage,
	}
	err := fs.WalkDir(uiFiles, "ui", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !strings.HasSuffix(path, ".html") {
			return err
		}
		b, err := uiFiles.ReadFile(path)
		pages[path] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, page := range pages {
		links := iconLink.FindAllStringSubmatch(page, -1)
		if len(links) == 0 {
			t.Errorf("%s: no <link rel=\"icon\">", name)
		}
		for _, l := range links {
			// Embedded pages are served under /ui/, desktop pages from the root.
			file := "ui/" + strings.TrimPrefix(l[1], "/")
			if _, err := uiFiles.ReadFile(file); err != nil {
				t.Errorf("%s: icon %s is not embedded", name, l[1])
			}
		}
	}
}

func TestFaviconServed(t *testing.T) {
	f := newUIFixture(t)
	desk, err := NewDesktopHandler(f.ui.srv, f.ui.ids, "http://gateway.example", "key")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		h    http.Handler
		path string
		typ  string
	}{
		{f.ui, "/ui/favicon.svg", "image/svg+xml"},
		{f.ui, "/ui/favicon.png", "image/png"},
		{f.ui, "/favicon.ico", "image/png"},
		{desk, "/favicon.ico", "image/png"},
		{desk, "/favicon.svg", "image/svg+xml"},
		{desk, "/favicon.png", "image/png"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		c.h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, c.path, nil))
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != c.typ || w.Body.Len() == 0 {
			t.Errorf("%T %s: %d %q", c.h, c.path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	w := httptest.NewRecorder()
	desk.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/favicon.ico", nil))
	if w.Code == http.StatusOK {
		t.Error("POST /favicon.ico answered")
	}
}
