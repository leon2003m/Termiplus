package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"termiplus/internal/naming"
)

// A home quota charges allocated blocks, an export streams apparent size: a
// sparse file must not let one user push an unbounded stream through the
// shared gateway and socket proxy (security audit run 1). Exports stop at
// ExportMaxBytes, wherever they come from, and leave nothing behind.
func TestExportByteCap(t *testing.T) {
	backups := withBackups(t, 5, false)
	h := newHarness(t, func(o *harnessOpts) {
		backups(o)
		inner := o.gateway
		o.gateway = func(c *Config) {
			inner(c)
			c.ExportMaxBytes = 256 << 10
		}
	})
	h.get(t, "/files/cwd", "alice", testAPIKey)
	vol := naming.Volume("alice", "default")
	setFiles := func(files map[string]string) {
		h.docker.mu.Lock()
		h.docker.files[vol] = files
		h.docker.mu.Unlock()
	}

	setFiles(map[string]string{"small": strings.Repeat("x", 64<<10)})
	if err := h.g.mgr.exportHome(context.Background(), "alice", "default", io.Discard); err != nil {
		t.Fatalf("export within the cap: %v", err)
	}

	setFiles(map[string]string{"sparse": strings.Repeat("\x00", 1<<20)})
	err := h.g.mgr.exportHome(context.Background(), "alice", "default", io.Discard)
	if !errors.Is(err, errExportTooLarge) {
		t.Fatalf("export over the cap: %v", err)
	}
	code, body := h.control(t, http.MethodPost, "workspaces/default/home/snapshots", testControlKey, "alice", "")
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("snapshot over the cap: %d %s", code, body)
	}
	_ = filepath.Walk(h.g.cfg.BackupDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			t.Errorf("left behind: %s", p)
		}
		return nil
	})
	h.noHelpersLeft(t)
}

// Exports share a few global slots; one that cannot get a slot within its
// deadline gives up instead of queueing forever.
func TestExportSlotsHonourContext(t *testing.T) {
	h := newHarness(t, func(o *harnessOpts) {
		o.gateway = func(c *Config) { c.ExportConcurrency = 1 }
	})
	h.get(t, "/files/cwd", "alice", testAPIKey)
	h.g.mgr.exportLimits.slots <- struct{}{} // the one slot is taken
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := h.g.mgr.exportHome(ctx, "alice", "default", io.Discard)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("export without a free slot: err=%v after %s", err, time.Since(start))
	}
	<-h.g.mgr.exportLimits.slots
	if err := h.g.mgr.exportHome(context.Background(), "alice", "default", io.Discard); err != nil {
		t.Fatalf("export with a free slot: %v", err)
	}
}

// The deadline applies to every export, not only to downloads.
func TestExportDeadlineAppliesToManager(t *testing.T) {
	h := newHarness(t, func(o *harnessOpts) {
		o.gateway = func(c *Config) { c.ExportTimeout = 100 * time.Millisecond }
	})
	h.get(t, "/files/cwd", "alice", testAPIKey)
	release, err := h.g.mgr.lockHelper(context.Background(), naming.Archive("alice", "default"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	err = h.g.mgr.exportHome(context.Background(), "alice", "default", io.Discard)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("export past the deadline: err=%v after %s", err, time.Since(start))
	}
}

// A download whose client stops reading must still end at the export
// deadline: a blocked write would otherwise hold the helper lock and the
// export slot until the client goes away (security audit run 2).
func TestExportDeadlineEndsBlockedWrite(t *testing.T) {
	h := newHarness(t, func(o *harnessOpts) {
		o.gateway = func(c *Config) {
			c.ControlKey = testControlKey
			c.ExportTimeout = 300 * time.Millisecond
			c.ExportConcurrency = 1
		}
	})
	h.get(t, "/files/cwd", "alice", testAPIKey)
	vol := naming.Volume("alice", "default")
	h.docker.mu.Lock()
	h.docker.files[vol] = map[string]string{"big": strings.Repeat("x", 64<<20)} // far beyond socket buffers
	h.docker.mu.Unlock()

	conn, err := net.Dial("tcp", strings.TrimPrefix(h.gw.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() // never read: the gateway's write blocks
	fmt.Fprintf(conn, "GET /control/v1/workspaces/default/home/archive HTTP/1.1\r\nHost: gw\r\n%s: %s\r\nX-User-Id: alice\r\n\r\n", controlKeyHeader, testControlKey)

	deadline := time.Now().Add(5 * time.Second)
	for len(h.g.mgr.exportLimits.slots) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond) // wait for the download to take the slot
	}
	if len(h.g.mgr.exportLimits.slots) == 0 {
		t.Fatal("the stalled download never took the export slot")
	}
	for len(h.g.mgr.exportLimits.slots) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stalled download still holds the export slot")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The next export proves that both the slot and helper lock were released.
	// Keep its fixture small: the short deadline is intended to interrupt the
	// stalled client, not to benchmark a second 64 MiB transfer under -race.
	h.docker.mu.Lock()
	h.docker.files[vol] = map[string]string{"small": "after the stalled download"}
	h.docker.mu.Unlock()
	if err := h.g.mgr.exportHome(context.Background(), "alice", "default", io.Discard); err != nil {
		t.Fatalf("export after the stalled one: %v", err)
	}
	h.noHelpersLeft(t)
}

// A download that stalls is cut off by the idle deadline well before the much
// larger ExportTimeout: no progress for ExportIdleTimeout frees the helper lock
// and export slot, so one slow reader cannot hold them for the full 2h bound
// (security audit run 4).
func TestExportIdleDeadlineEndsStalledWrite(t *testing.T) {
	h := newHarness(t, func(o *harnessOpts) {
		o.gateway = func(c *Config) {
			c.ControlKey = testControlKey
			c.ExportTimeout = 30 * time.Second // the absolute bound stays long
			c.ExportIdleTimeout = 300 * time.Millisecond
			c.ExportConcurrency = 1
		}
	})
	h.get(t, "/files/cwd", "alice", testAPIKey)
	vol := naming.Volume("alice", "default")
	h.docker.mu.Lock()
	h.docker.files[vol] = map[string]string{"big": strings.Repeat("x", 64<<20)} // far beyond socket buffers
	h.docker.mu.Unlock()

	conn, err := net.Dial("tcp", strings.TrimPrefix(h.gw.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() // never read: the gateway's write stalls
	fmt.Fprintf(conn, "GET /control/v1/workspaces/default/home/archive HTTP/1.1\r\nHost: gw\r\n%s: %s\r\nX-User-Id: alice\r\n\r\n", controlKeyHeader, testControlKey)

	deadline := time.Now().Add(5 * time.Second)
	for len(h.g.mgr.exportLimits.slots) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond) // wait for the download to take the slot
	}
	if len(h.g.mgr.exportLimits.slots) == 0 {
		t.Fatal("the stalled download never took the export slot")
	}
	// The idle deadline (300ms), not the 30s ExportTimeout, must release it.
	start := time.Now()
	for len(h.g.mgr.exportLimits.slots) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the idle deadline did not release the export slot")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("slot released after %s, not by the idle deadline", elapsed)
	}
	h.docker.mu.Lock()
	h.docker.files[vol] = map[string]string{"small": "after the stalled download"}
	h.docker.mu.Unlock()
	if err := h.g.mgr.exportHome(context.Background(), "alice", "default", io.Discard); err != nil {
		t.Fatalf("export after the stalled one: %v", err)
	}
	h.noHelpersLeft(t)
}
