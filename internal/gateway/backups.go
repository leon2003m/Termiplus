package gateway

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"termiplus/internal/naming"
)

// Backups of homes, as gzip-compressed tars (entries "user/...", the format
// of exportHome) under BACKUP_DIR:
//
//	<dir>/snapshots/<volume>/<id>.tar.gz  made by the user, at most keep per home
//	<dir>/archived/<volume>/<id>.tar.gz   homes of departed users
//
// The id is the UTC creation time, e.g. 20261016T221500Z. Volume names carry
// the user hash, so one user's paths never name another's backups.

const (
	kindSnapshot = "snapshots"
	kindArchived = "archived"
)

// Within one second the suffix counts up (-001, -002, ...), so ids sort in
// creation order and a pruned id is never reused.
var backupID = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z(-[0-9]{3})?$`)

var errNoBackup = errors.New("no such backup")

// Backup describes one stored archive.
type Backup struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Size    int64     `json:"size"`
}

type backupStore struct {
	dir  string
	keep int // snapshots per home; the oldest beyond it are deleted

	mu sync.Mutex // serializes creation and pruning
}

func newBackupStore(dir string, keep int) (*backupStore, error) {
	if keep <= 0 {
		keep = 5
	}
	for _, k := range []string{kindSnapshot, kindArchived} {
		if err := os.MkdirAll(filepath.Join(dir, k), 0o700); err != nil {
			return nil, fmt.Errorf("backup dir: %w", err)
		}
	}
	return &backupStore{dir: dir, keep: keep}, nil
}

func (b *backupStore) homeDir(kind, volume string) (string, error) {
	if (kind != kindSnapshot && kind != kindArchived) || !naming.VolumeName.MatchString(volume) {
		return "", errors.New("invalid backup location")
	}
	return filepath.Join(b.dir, kind, volume), nil
}

// list returns the backups of one home, newest first.
func (b *backupStore) list(kind, volume string) ([]Backup, error) {
	dir, err := b.homeDir(kind, volume)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Backup{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Backup{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".tar.gz")
		if !ok || !backupID.MatchString(id) || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		created, _ := time.Parse("20060102T150405Z", id[:16])
		out = append(out, Backup{ID: id, Created: created, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// create writes a new backup from write (which produces the plain tar) and,
// for snapshots, deletes the oldest beyond keep. Nothing is left behind if
// write fails.
func (b *backupStore) create(kind, volume string, write func(io.Writer) error) (Backup, error) {
	dir, err := b.homeDir(kind, volume)
	if err != nil {
		return Backup{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Backup{}, err
	}
	tmp, err := os.CreateTemp(dir, ".new-*")
	if err != nil {
		return Backup{}, err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	zw := gzip.NewWriter(tmp)
	werr := write(zw)
	if cerr := zw.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return Backup{}, werr
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now().UTC()
	base := now.Format("20060102T150405Z")
	next := 0
	if existing, err := b.list(kind, volume); err == nil {
		for _, e := range existing {
			if e.ID == base && next < 1 {
				next = 1
			}
			if n, ok := strings.CutPrefix(e.ID, base+"-"); ok {
				var v int
				if _, err := fmt.Sscanf(n, "%03d", &v); err == nil && v+1 > next {
					next = v + 1
				}
			}
		}
	}
	if next > 999 {
		return Backup{}, errors.New("too many backups in one second")
	}
	id := base
	if next > 0 {
		id = fmt.Sprintf("%s-%03d", base, next)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, id+".tar.gz")); err != nil {
		return Backup{}, err
	}
	syncDir(dir)
	if kind == kindSnapshot {
		if all, err := b.list(kind, volume); err == nil {
			for _, old := range all[min(len(all), b.keep):] {
				_ = os.Remove(filepath.Join(dir, old.ID+".tar.gz"))
			}
		}
	}
	info, err := os.Stat(filepath.Join(dir, id+".tar.gz"))
	if err != nil {
		return Backup{}, err
	}
	return Backup{ID: id, Created: now.Truncate(time.Second), Size: info.Size()}, nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// open returns the stored file of one backup.
func (b *backupStore) open(kind, volume, id string) (*os.File, error) {
	dir, err := b.homeDir(kind, volume)
	if err != nil || !backupID.MatchString(id) {
		return nil, errNoBackup
	}
	f, err := os.Open(filepath.Join(dir, id+".tar.gz"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoBackup
	}
	return f, err
}

func (b *backupStore) remove(kind, volume, id string) error {
	dir, err := b.homeDir(kind, volume)
	if err != nil || !backupID.MatchString(id) {
		return errNoBackup
	}
	err = os.Remove(filepath.Join(dir, id+".tar.gz"))
	if errors.Is(err, fs.ErrNotExist) {
		return errNoBackup
	}
	return err
}
