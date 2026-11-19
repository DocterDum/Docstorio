package factorio

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"docstorio/internal/fsutil"
)

// writeSave creates saves/<name> with the given age (mtime = now - age).
func writeSave(t *testing.T, savesDir, name string, age time.Duration) {
	t.Helper()
	path := filepath.Join(savesDir, name)
	writeFile(t, path, "PK fake save "+name)
	mtime := time.Now().Add(-age)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestResolveSave(t *testing.T) {
	dir := t.TempDir()
	if _, err := ResolveSave(filepath.Join(dir, "missing"), ""); !errors.Is(err, ErrNoSaves) {
		t.Fatalf("missing dir: %v", err)
	}
	if _, err := ResolveSave(dir, ""); !errors.Is(err, ErrNoSaves) {
		t.Fatalf("empty dir: %v", err)
	}

	writeSave(t, dir, "old.zip", 3*time.Hour)
	writeSave(t, dir, "b.zip", time.Hour)
	writeSave(t, dir, "a.zip", time.Hour) // same mtime as b.zip: name order
	writeSave(t, dir, "notes.txt", 0)
	writeSave(t, dir, fsutil.TempPrefix+"upload", 0)
	if err := os.Mkdir(filepath.Join(dir, "dir.zip"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Same mtime for a and b, exactly.
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	os.Chtimes(filepath.Join(dir, "a.zip"), mtime, mtime)
	os.Chtimes(filepath.Join(dir, "b.zip"), mtime, mtime)

	got, err := ResolveSave(dir, "")
	if err != nil || got != "a.zip" {
		t.Fatalf("newest = %q, %v; want a.zip", got, err)
	}
	if got, err := ResolveSave(dir, "old.zip"); err != nil || got != "old.zip" {
		t.Fatalf("override = %q, %v", got, err)
	}
	if _, err := ResolveSave(dir, "nope.zip"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing override: %v", err)
	}
	if _, err := ResolveSave(dir, "dir.zip"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("directory override: %v", err)
	}
	for _, bad := range []string{"../old.zip", "notes.txt", "a/b.zip"} {
		if _, err := ResolveSave(dir, bad); !errors.Is(err, fsutil.ErrInvalidName) {
			t.Errorf("override %q: %v, want ErrInvalidName", bad, err)
		}
	}
}

func TestListSavesAndFileOps(t *testing.T) {
	s, p := newTestSupervisor(t, "run")
	writeSave(t, p.Saves, "new.zip", time.Minute)
	writeSave(t, p.Saves, "old.zip", time.Hour)

	saves, err := s.ListSaves()
	if err != nil {
		t.Fatal(err)
	}
	if len(saves) != 2 || saves[0].Name != "new.zip" || !saves[0].Next || saves[1].Next || saves[0].Running {
		t.Fatalf("saves = %+v", saves)
	}
	if saves[0].Size != int64(len("PK fake save new.zip")) {
		t.Errorf("size %d", saves[0].Size)
	}

	// Run old.zip: it is "running" and cannot be deleted or overwritten.
	if err := s.Start("old.zip"); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Running)
	saves, _ = s.ListSaves()
	if saves[1].Name != "old.zip" || !saves[1].Running || saves[0].Running {
		t.Fatalf("saves while running = %+v", saves)
	}
	if err := s.DeleteSave("old.zip"); !errors.Is(err, ErrSaveInUse) {
		t.Fatalf("delete running save: %v", err)
	}
	pending, err := fsutil.NewPending(p.Saves, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer pending.Abort()
	pending.Write([]byte("PK upload"))
	if err := s.CommitSaveUpload(pending, "old.zip", true); !errors.Is(err, ErrSaveInUse) {
		t.Fatalf("overwrite running save: %v", err)
	}
	if err := s.CommitSaveUpload(pending, "new.zip", false); !errors.Is(err, ErrSaveExists) {
		t.Fatalf("upload over existing: %v", err)
	}
	if err := s.CommitSaveUpload(pending, "../x.zip", false); !errors.Is(err, fsutil.ErrInvalidName) {
		t.Fatalf("bad upload name: %v", err)
	}
	if err := s.CommitSaveUpload(pending, "new.zip", true); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got := readFile(t, filepath.Join(p.Saves, "new.zip")); got != "PK upload" {
		t.Fatalf("overwritten content %q", got)
	}

	f, info, err := s.OpenSave("new.zip")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "PK upload" || info.Size() != int64(len(data)) {
		t.Fatalf("download %q, size %d", data, info.Size())
	}
	if _, _, err := s.OpenSave("missing.zip"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("open missing: %v", err)
	}
	if err := s.DeleteSave("new.zip"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSave("new.zip"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("delete missing: %v", err)
	}
	if err := s.DeleteSave("../config.zip"); !errors.Is(err, fsutil.ErrInvalidName) {
		t.Fatalf("delete bad name: %v", err)
	}

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSave("old.zip"); err != nil {
		t.Fatalf("delete after stop: %v", err)
	}
}
