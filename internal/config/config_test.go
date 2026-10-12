package config

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSecretOptionalEmpty(t *testing.T) {
	t.Setenv("TP_TEST_TOK", "")
	t.Setenv("TP_TEST_TOK_FILE", "")
	if got := Secret("TP_TEST_TOK", false); got != "" {
		t.Fatalf("optional missing secret = %q, want empty", got)
	}
}

func TestSecretRequiredHappyPath(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef" // 32 chars
	t.Setenv("TP_TEST_TOK", tok)
	t.Setenv("TP_TEST_TOK_FILE", "")
	if got := Secret("TP_TEST_TOK", true); got != tok {
		t.Fatalf("Secret = %q, want %q", got, tok)
	}
}

func TestSecretFromFile(t *testing.T) {
	const tok = "file-provided-secret-at-least-32ch!!"
	f := t.TempDir() + "/tok"
	if err := os.WriteFile(f, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TP_TEST_TOK", "")
	t.Setenv("TP_TEST_TOK_FILE", f)
	if got := Secret("TP_TEST_TOK", true); got != tok {
		t.Fatalf("Secret from file = %q, want %q (trimmed)", got, tok)
	}
}

// A required secret shorter than 32 chars (or missing) must terminate the
// process rather than start an unauthenticated proxy. Secret uses log.Fatalf,
// so the failure path is exercised in a child process.
func TestSecretRequiredTooShortExits(t *testing.T) {
	if os.Getenv("TP_TEST_CHILD") == "1" {
		Secret("TP_TEST_TOK", true) // must exit non-zero before returning
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestSecretRequiredTooShortExits")
	cmd.Env = append(os.Environ(), "TP_TEST_CHILD=1", "TP_TEST_TOK=tooshort", "TP_TEST_TOK_FILE=")
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.Success() {
		t.Fatalf("want non-zero exit on short required secret, got err=%v out=%q", err, out)
	}
	if !strings.Contains(string(out), "at least 32 characters") {
		t.Fatalf("missing expected message, got %q", out)
	}
}
