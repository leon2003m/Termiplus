package gateway

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"termiplus/internal/naming"
)

type unexpectedArchiveBody struct{ read bool }

func (r *unexpectedArchiveBody) Read([]byte) (int, error) {
	r.read = true
	return 0, errors.New("expanded sparse body must not be read")
}

func TestSparseExportRejectedAtHeader(t *testing.T) {
	var header bytes.Buffer
	tw := tar.NewWriter(&header)
	if err := tw.WriteHeader(&tar.Header{Name: "user/sparse", Mode: 0o600, Size: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	body := &unexpectedArchiveBody{}
	err := copyHomeArchive(io.Discard, io.MultiReader(&header, body), 1<<20)
	if !errors.Is(err, errExportTooLarge) || body.read {
		t.Fatalf("oversized member: err=%v, read expanded body=%v", err, body.read)
	}
}

func TestArchiveBudgetIncludesPreviousMembers(t *testing.T) {
	var headers bytes.Buffer
	tw := tar.NewWriter(&headers)
	if err := tw.WriteHeader(&tar.Header{Name: "user/first", Mode: 0o600, Size: 512}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(make([]byte, 512)); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "user/second", Mode: 0o600, Size: 1024}); err != nil {
		t.Fatal(err)
	}
	body := &unexpectedArchiveBody{}
	err := copyHomeArchive(io.Discard, io.MultiReader(&headers, body), 2048)
	if !errors.Is(err, errExportTooLarge) || body.read {
		t.Fatalf("cumulative budget: err=%v, read second body=%v", err, body.read)
	}
}

func TestArchiveBudgetCountsEncodedSparseHoles(t *testing.T) {
	// Go's tar writer deliberately does not generate sparse metadata. Build
	// a PAX 0.1 extended header from ordinary tar blocks, then let tar.Reader
	// independently verify the fixture before exercising the export limit.
	const logicalSize = 4096
	var sparse bytes.Buffer
	for _, name := range []string{"user/first", "user/second"} {
		var records string
		for _, pair := range [][2]string{
			{"GNU.sparse.major", "0"}, {"GNU.sparse.minor", "1"},
			{"GNU.sparse.size", "4096"}, {"GNU.sparse.numblocks", "1"},
			{"GNU.sparse.map", "0,1"},
		} {
			body := pair[0] + "=" + pair[1] + "\n"
			n := len(body) + 2
			for len(fmt.Sprintf("%d %s", n, body)) != n {
				n = len(fmt.Sprintf("%d %s", n, body))
			}
			records += fmt.Sprintf("%d %s", n, body)
		}
		var member bytes.Buffer
		tw := tar.NewWriter(&member)
		if err := tw.WriteHeader(&tar.Header{Name: "pax", Mode: 0o600, Size: int64(len(records))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, records); err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte{'x'}); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		raw := member.Bytes()
		raw[156] = tar.TypeXHeader
		for i := 148; i < 156; i++ {
			raw[i] = ' '
		}
		var sum int
		for _, b := range raw[:512] {
			sum += int(b)
		}
		copy(raw[148:156], fmt.Sprintf("%06o\x00 ", sum))
		sparse.Write(raw[:len(raw)-1024]) // concatenate without end markers
	}
	sparse.Write(make([]byte, 1024))
	tr := tar.NewReader(bytes.NewReader(sparse.Bytes()))
	for range 2 {
		h, err := tr.Next()
		if err != nil || h.Size != logicalSize {
			t.Fatalf("invalid sparse fixture: header=%v err=%v", h, err)
		}
		if n, err := io.Copy(io.Discard, tr); err != nil || n != logicalSize {
			t.Fatalf("invalid sparse body: bytes=%d err=%v", n, err)
		}
	}
	if err := copyHomeArchive(io.Discard, bytes.NewReader(sparse.Bytes()), 8192); !errors.Is(err, errExportTooLarge) {
		t.Fatalf("encoded sparse holes bypassed cumulative budget: %v", err)
	}
}

func TestArchiveExportPreservesBytesAndCapsPadding(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "user/file", Mode: 0o600, Size: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	archive.Write(make([]byte, 1024)) // Docker may append additional padding.
	raw := archive.Bytes()
	for _, cap := range []int64{0, int64(len(raw)), int64(len(raw) - 1)} {
		var out bytes.Buffer
		err := copyHomeArchive(&out, bytes.NewReader(raw), cap)
		if cap == int64(len(raw)-1) {
			if !errors.Is(err, errExportTooLarge) || int64(out.Len()) > cap {
				t.Fatalf("padding bypassed budget: err=%v, bytes=%d cap=%d", err, out.Len(), cap)
			}
		} else if err != nil || !bytes.Equal(raw, out.Bytes()) {
			t.Fatalf("archive changed at cap=%d: err=%v, bytes=%d", cap, err, out.Len())
		}
	}
}

func TestInvalidArchiveDoesNotPublishSnapshot(t *testing.T) {
	var header bytes.Buffer
	tw := tar.NewWriter(&header)
	if err := tw.WriteHeader(&tar.Header{Name: "user/incomplete", Mode: 0o600, Size: 4096}); err != nil {
		t.Fatal(err)
	}
	for _, input := range [][]byte{[]byte("invalid tar header"), header.Bytes()} {
		store, err := newBackupStore(t.TempDir(), 5)
		if err != nil {
			t.Fatal(err)
		}
		volume := naming.Volume("alice", "default")
		if _, err := store.create(kindSnapshot, volume, false, func(w io.Writer) error {
			return copyHomeArchive(w, bytes.NewReader(input), 1<<20)
		}); err == nil {
			t.Fatal("published an invalid or truncated archive")
		}
		entries, err := os.ReadDir(filepath.Join(store.dir, kindSnapshot, volume))
		if err != nil || len(entries) != 0 {
			t.Fatalf("failed archive left a snapshot or temporary file: entries=%v err=%v", entries, err)
		}
	}
}

func TestOneUserCannotOccupyOtherExportSlots(t *testing.T) {
	backups := withBackups(t, 5, false)
	h := newHarness(t, func(o *harnessOpts) {
		backups(o)
		inner := o.gateway
		o.gateway = func(c *Config) {
			inner(c)
			c.ExportConcurrency = 2
		}
	})
	h.get(t, "/files/cwd", "alice", testAPIKey)
	h.get(t, "/files/cwd", "bob", testAPIKey)
	release, err := h.g.mgr.lockHelper(context.Background(), naming.Archive("alice", "default"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.g.mgr.exportHome(ctx, "alice", "default", io.Discard) }()
	deadline := time.Now().Add(2 * time.Second)
	for len(h.g.mgr.exportLimits.slots) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("first export did not acquire a slot")
		}
		time.Sleep(time.Millisecond)
	}
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "workspaces/default/home/archive"},
		{http.MethodPost, "workspaces/default/home/snapshots"},
	} {
		code, body := h.control(t, request.method, request.path, testControlKey, "alice", "")
		if code != http.StatusTooManyRequests {
			t.Fatalf("overlapping export %s: %d %s", request.path, code, body)
		}
	}
	otherCtx, otherCancel := context.WithTimeout(context.Background(), time.Second)
	defer otherCancel()
	if err := h.g.mgr.exportHome(otherCtx, "bob", "default", io.Discard); err != nil {
		t.Fatalf("another user's export was blocked: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel waiting export: %v", err)
	}
	if len(h.g.mgr.exportLimits.slots) != 0 {
		t.Fatal("canceled export retained its slot")
	}
	h.g.mgr.exportLimits.mu.Lock()
	defer h.g.mgr.exportLimits.mu.Unlock()
	if len(h.g.mgr.exportLimits.users) != 0 {
		t.Fatal("completed exports retained per-user reservations")
	}
}
