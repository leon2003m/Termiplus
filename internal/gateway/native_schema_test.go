package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDesktopDescriptionsPreserveNativeContract(t *testing.T) {
	for _, name := range []string{"Action", "open_terminal__desktop__Action"} {
		t.Run(name, func(t *testing.T) {
			original := `{"components":{"schemas":{"` + name + `":{"properties":{"x":{"type":"integer","minimum":0},"to_x":{"type":"integer"}},"required":["action"],"additionalProperties":false}}},"paths":{"/desktop/action":{"post":{"description":"Native behavior","security":[{"HTTPBearer":[]}]}}}}`
			r := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": {"old"}}, Body: io.NopCloser(strings.NewReader(original))}
			if err := describeDesktopActions(r); err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(r.Body)
			for _, wanted := range []string{`"minimum":0`, `"required":["action"]`, `"additionalProperties":false`, `"security":[{"HTTPBearer":[]}]`, "Drag destination X ONLY", "Native behavior", "move require x AND y"} {
				if !strings.Contains(string(body), wanted) {
					t.Errorf("missing native contract or model guidance %q", wanted)
				}
			}
			if r.ContentLength != int64(len(body)) || r.Header.Get("ETag") != "" {
				t.Fatal("stale response metadata")
			}
		})
	}
}

func TestDesktopDescriptionsPassThroughOtherBodies(t *testing.T) {
	for _, original := range []string{`{"detail":"not a schema"}`, `not JSON`, strings.Repeat("x", (2<<20)+10)} {
		r := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(original))}
		if err := describeDesktopActions(r); err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if string(body) != original {
			t.Fatal("unrelated or oversized response changed")
		}
	}
}
