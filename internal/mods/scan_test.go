package mods

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"docstorio/internal/fsutil"
)

func TestScan(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a_1.0.0.zip", modZip(t, "a", "1.0.0", "2.1"))
	writeFile(t, dir, "a_1.10.0.zip", modZip(t, "a", "1.10.0", "2.1"))
	writeFile(t, dir, "a_1.9.0.zip", modZip(t, "a", "1.9.0", "2.1"))
	writeFile(t, dir, "renamed-by-user.zip", modZip(t, "b", "2.0.0", "2.0"))
	writeFile(t, dir, "corrupt.zip", []byte("PK garbage"))
	writeFile(t, dir, "mod-list.json", []byte(`{"mods":[]}`))
	writeFile(t, dir, "mod-settings.dat", []byte{1, 2, 3})
	writeFile(t, dir, fsutil.TempPrefix+"123", modZip(t, "tmp", "1.0.0", "2.1"))
	writeFile(t, dir, "notazip.txt", modZip(t, "c", "1.0.0", "2.1"))
	if err := os.Mkdir(filepath.Join(dir, "folder.zip"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(dir)
	mods, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range mods {
		got = append(got, m.File)
	}
	want := []string{"a_1.0.0.zip", "a_1.9.0.zip", "a_1.10.0.zip", "renamed-by-user.zip"}
	if len(got) != len(want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("files = %v, want %v", got, want)
		}
	}
	if mods[3].Name != "b" || mods[3].Size == 0 || mods[3].ModTime.IsZero() || mods[3].Builtin {
		t.Errorf("b = %+v", mods[3])
	}

	newest := Newest(mods)
	if len(newest) != 2 || newest["a"].Version != "1.10.0" || newest["b"].Version != "2.0.0" {
		t.Errorf("Newest = %+v", newest)
	}
}

func TestScanCache(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a_1.0.0.zip", modZip(t, "a", "1.0.0", "2.1"))
	s := NewScanner(dir)
	if mods, err := s.Scan(); err != nil || len(mods) != 1 {
		t.Fatalf("first scan: %v %v", mods, err)
	}

	// Same name, size and mtime: the cached info.json is used, the file is
	// not read again (here it is no longer a zip).
	fi, _ := os.Stat(path)
	if err := os.WriteFile(path, make([]byte, fi.Size()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if mods, err := s.Scan(); err != nil || len(mods) != 1 || mods[0].Name != "a" {
		t.Fatalf("cached scan: %v %v", mods, err)
	}

	// Changed mtime: re-read, now invalid and skipped.
	later := fi.ModTime().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if mods, err := s.Scan(); err != nil || len(mods) != 0 {
		t.Fatalf("changed scan: %v %v", mods, err)
	}

	// Removed files drop out of the cache.
	writeFile(t, dir, "b_1.0.0.zip", modZip(t, "b", "1.0.0", "2.1"))
	s.Scan()
	os.Remove(filepath.Join(dir, "b_1.0.0.zip"))
	os.Remove(path)
	if mods, err := s.Scan(); err != nil || len(mods) != 0 || len(s.cache) != 0 {
		t.Fatalf("after removal: %v %v cache=%d", mods, err, len(s.cache))
	}
}

func TestScanMissingDir(t *testing.T) {
	mods, err := NewScanner(filepath.Join(t.TempDir(), "nope")).Scan()
	if err != nil || mods == nil || len(mods) != 0 {
		t.Errorf("got %v %v", mods, err)
	}
}
