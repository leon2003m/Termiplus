package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// Responses go out with a Content-Length, not chunked: through oauth2-proxy
// about 1 in 100 large chunked bodies arrived truncated.
func TestResponsesCarryContentLength(t *testing.T) {
	auth := NewStaticAuthenticator(map[string]Principal{"tok": {UserID: "alice"}})
	s := NewServer(auth, NewStubDevBackend("https://gw.example"), "test")
	srv := httptest.NewServer(s)
	defer srv.Close()

	post := func(method string, params any) *http.Response {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	for name, resp := range map[string]*http.Response{
		"resources/read": post("resources/read", map[string]any{"uri": appResourceURI}),
		"tools/list":     post("tools/list", map[string]any{}),
		"error":          post("nope", map[string]any{}),
	} {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.ContentLength != int64(len(b)) || resp.Header.Get("Content-Length") != strconv.Itoa(len(b)) || len(resp.TransferEncoding) != 0 {
			t.Errorf("%s: Content-Length %q (parsed %d), transfer-encoding %v, body %d bytes", name, resp.Header.Get("Content-Length"), resp.ContentLength, resp.TransferEncoding, len(b))
		}
		if !json.Valid(b) {
			t.Errorf("%s: body is not JSON", name)
		}
	}
}

type orderWriter struct {
	http.ResponseWriter
	events []string
}

func (o *orderWriter) Flush() { o.events = append(o.events, "flush") }
func (o *orderWriter) Write(b []byte) (int, error) {
	o.events = append(o.events, "write")
	return len(b), nil
}

// The headers leave before the body, in their own TCP write (see writeBody).
func TestWriteBodyFlushesHeadersFirst(t *testing.T) {
	o := &orderWriter{ResponseWriter: httptest.NewRecorder()}
	writeBody(o, http.StatusOK, []byte(`{}`))
	if len(o.events) != 2 || o.events[0] != "flush" || o.events[1] != "write" {
		t.Fatalf("events = %v, want [flush write]", o.events)
	}
}

// The console API (also behind an oauth2-proxy) is written the same way.
func TestUIJSONCarriesContentLength(t *testing.T) {
	w := httptest.NewRecorder()
	big := make([]string, 4000)
	for i := range big {
		big[i] = "workspace-entry"
	}
	uiJSON(w, http.StatusOK, map[string]any{"items": big})
	if got, want := w.Header().Get("Content-Length"), strconv.Itoa(w.Body.Len()); got != want {
		t.Fatalf("Content-Length %q, body %s", got, want)
	}
}
