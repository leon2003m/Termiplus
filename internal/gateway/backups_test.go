package gateway

import (
	"errors"
	"io"
	"os"
	"path/filepath"
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
		bk, err := b.create(kindSnapshot, vol, func(w io.Writer) error { _, err := w.Write([]byte{byte(i)}); return err })
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
		_, _ = b.create(kindArchived, vol, func(w io.Writer) error { return nil })
	}
	if l, _ := b.list(kindArchived, vol); len(l) != 5 {
		t.Fatalf("archives: %d", len(l))
	}
	// A failed write leaves nothing behind.
	if _, err := b.create(kindSnapshot, vol, func(io.Writer) error { return errors.New("boom") }); err == nil {
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
	bk, _ := b.create(kindSnapshot, vol, func(w io.Writer) error { return nil })
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
	arch, _ := b.create(kindArchived, vol, func(w io.Writer) error { return nil })
	if f, err := b.open(kindSnapshot, vol, "../../"+kindArchived+"/"+vol+"/"+arch.ID); err == nil {
		f.Close()
		t.Fatal("snapshot id escaped into the archives")
	}
	if err := b.remove(kindSnapshot, vol, "../../"+kindArchived+"/"+vol+"/"+arch.ID); err == nil {
		t.Fatal("removed an archive through a snapshot id")
	}
	if _, err := b.create("x", vol, func(io.Writer) error { return nil }); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
