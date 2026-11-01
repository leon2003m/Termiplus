package gateway

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"termiplus/internal/naming"
)

func TestBackupStoreOrderAndPruning(t *testing.T) {
	b, err := newBackupStore(t.TempDir(), 3)
	if err != nil {
		t.Fatal(err)
	}
	vol := naming.Volume("u", "default")
	var ids []string
	for i := 0; i < 15; i++ { // many within one second: suffixes past -009
		bk, err := b.create(kindSnapshot, vol, true, func(w io.Writer) error { _, err := w.Write([]byte{byte(i)}); return err })
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, bk.ID)
	}
	list, _ := b.list(kindSnapshot, vol)
	if len(list) != 3 || list[0].ID != ids[14] || list[1].ID != ids[13] || list[2].ID != ids[12] {
		t.Fatalf("kept %v, created %v", list, ids)
	}
	// Archives are never pruned.
	for i := 0; i < 5; i++ {
		_, _ = b.create(kindArchived, vol, false, func(w io.Writer) error { return nil })
	}
	if l, _ := b.list(kindArchived, vol); len(l) != 5 {
		t.Fatalf("archives: %d", len(l))
	}
	// A failed write leaves nothing behind.
	if _, err := b.create(kindSnapshot, vol, true, func(io.Writer) error { return errors.New("boom") }); err == nil {
		t.Fatal("failed write accepted")
	}
	entries, _ := os.ReadDir(filepath.Join(b.dir, kindSnapshot, vol))
	if len(entries) != 3 {
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestBackupStorePaths(t *testing.T) {
	b, _ := newBackupStore(t.TempDir(), 3)
	vol := naming.Volume("u", "default")
	bk, _ := b.create(kindSnapshot, vol, true, func(w io.Writer) error { return nil })
	if f, err := b.open(kindSnapshot, vol, bk.ID); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	for _, c := range []struct{ kind, vol, id string }{
		{kindSnapshot, vol, "../" + bk.ID},
		{kindSnapshot, vol, bk.ID + "/../x"},
		{kindSnapshot, "../" + vol, bk.ID},
		{"../snapshots", vol, bk.ID},
		{kindArchived, vol, bk.ID}, // wrong kind
		{kindSnapshot, naming.Volume("other", "default"), bk.ID},
	} {
		if f, err := b.open(c.kind, c.vol, c.id); err == nil {
			f.Close()
			t.Errorf("%+v opened", c)
		}
		if err := b.remove(c.kind, c.vol, c.id); err == nil {
			t.Errorf("%+v removed", c)
		}
	}
	// A snapshot id must not reach into the archives (or anywhere else).
	arch, _ := b.create(kindArchived, vol, false, func(w io.Writer) error { return nil })
	if f, err := b.open(kindSnapshot, vol, "../../"+kindArchived+"/"+vol+"/"+arch.ID); err == nil {
		f.Close()
		t.Fatal("snapshot id escaped into the archives")
	}
	if err := b.remove(kindSnapshot, vol, "../../"+kindArchived+"/"+vol+"/"+arch.ID); err == nil {
		t.Fatal("removed an archive through a snapshot id")
	}
	if _, err := b.create("x", vol, false, func(io.Writer) error { return nil }); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

// Two overlapping creates that were not allowed to prune, at keep-1: both
// passed a separate capacity check, but only one may commit; the oldest
// snapshot stays (security audit run 2).
func TestBackupStoreConcurrentCreateKeepsOldest(t *testing.T) {
	b, _ := newBackupStore(t.TempDir(), 2)
	vol := naming.Volume("u", "default")
	seed, err := b.create(kindSnapshot, vol, false, func(w io.Writer) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var started sync.WaitGroup
	started.Add(2)
	release := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := b.create(kindSnapshot, vol, false, func(w io.Writer) error {
				started.Done()
				<-release // both writes are in flight before either commits
				return nil
			})
			errs <- err
		}()
	}
	started.Wait()
	close(release)
	var ok, refused int
	for i := 0; i < 2; i++ {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, errWouldPrune):
			refused++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("ok %d, refused %d", ok, refused)
	}
	list, _ := b.list(kindSnapshot, vol)
	if len(list) != 2 || list[1].ID != seed.ID {
		t.Fatalf("the seeded snapshot was pruned: %v", list)
	}
	entries, _ := os.ReadDir(filepath.Join(b.dir, kindSnapshot, vol))
	if len(entries) != 2 {
		t.Fatalf("leftovers: %v", entries)
	}
	// With the permission, the oldest goes.
	if _, err := b.create(kindSnapshot, vol, true, func(w io.Writer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if list, _ := b.list(kindSnapshot, vol); len(list) != 2 || list[1].ID == seed.ID {
		t.Fatalf("prune with permission: %v", list)
	}
}
