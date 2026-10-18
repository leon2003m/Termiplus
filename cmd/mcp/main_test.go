package main

import "testing"

// The console and the desktops must be two origins (security audit run 1):
// default ports, case and a trailing dot must not hide that they are one.
func TestSameOrigin(t *testing.T) {
	same := [][2]string{
		{"https://desk.example", "https://desk.example/other"},
		{"https://desk.example", "https://DESK.example:443"},
		{"https://desk.example.", "https://desk.example"},
		{"http://127.0.0.1", "http://127.0.0.1:80/x"},
		{"https://desk.example", "%zz"},
	}
	for _, c := range same {
		if !sameOrigin(c[0], c[1]) {
			t.Errorf("%q and %q: want same origin", c[0], c[1])
		}
	}
	other := [][2]string{
		{"https://desk.example", "https://desktops.example"},
		{"https://desk.example", "http://desk.example"},
		{"https://desk.example", "https://desk.example:8443"},
		{"http://127.0.0.1:4181", "http://127.0.0.1:4183"},
	}
	for _, c := range other {
		if sameOrigin(c[0], c[1]) {
			t.Errorf("%q and %q: want different origins", c[0], c[1])
		}
	}
}
