package gateway

import "testing"

func TestRemovedPortForwardControlRoute(t *testing.T) {
	h := newHarness(t, withControl)
	for _, path := range []string{"forward/ot-u-000000000000-default/3000/", "stream/ot-u-000000000000-default/"} {
		if code, _ := h.control(t, "GET", path, testControlKey, "alice", ""); code != 404 {
			t.Fatalf("retired route %s: %d", path, code)
		}
	}
	if h.docker.creates != 0 {
		t.Fatal("retired route started a container")
	}
}
