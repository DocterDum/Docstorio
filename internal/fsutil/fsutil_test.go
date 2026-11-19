package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	if err := AtomicWrite(path, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(path, []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two" {
		t.Fatalf("content = %q, want %q", got, "two")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0600 {
			t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Fatalf("dir has %v, want only a.json", names)
	}
}

func TestAtomicWriteMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "a.json")
	if err := AtomicWrite(path, []byte("x"), 0644); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestAtomicWriteRenameFailsLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory as the target makes the rename fail.
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "x"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(target, []byte("x"), 0644); err == nil {
		t.Fatal("expected rename error")
	}
	for _, n := range listDir(t, dir) {
		if strings.HasPrefix(n, TempPrefix) {
			t.Fatalf("temp file left behind: %s", n)
		}
	}
}

func TestPendingCommit(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPending(dir, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Abort()
	if !strings.HasPrefix(filepath.Base(p.File().Name()), TempPrefix) {
		t.Fatalf("temp name %q lacks prefix", p.File().Name())
	}
	if _, err := p.Write([]byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	if p.Size() != 11 {
		t.Fatalf("Size = %d, want 11", p.Size())
	}
	buf := make([]byte, 5)
	if _, err := p.File().ReadAt(buf, 6); err != nil || string(buf) != "world" {
		t.Fatalf("ReadAt = %q, %v", buf, err)
	}
	if err := p.Commit("../escape"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Commit(../escape) = %v, want ErrInvalidName", err)
	}
	if err := p.Commit("out.zip"); err != nil {
		t.Fatal(err)
	}
	if err := p.Abort(); err != nil {
		t.Fatalf("Abort after Commit = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "out.zip"))
	if err != nil || string(got) != "hello world" {
		t.Fatalf("committed = %q, %v", got, err)
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Fatalf("dir has %v, want only out.zip", names)
	}
	if err := p.Commit("again.zip"); err == nil {
		t.Fatal("second Commit should fail")
	}
}

func TestPendingAbort(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPending(dir, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := p.Abort(); err != nil {
		t.Fatal(err)
	}
	if err := p.Abort(); err != nil {
		t.Fatalf("second Abort = %v", err)
	}
	if names := listDir(t, dir); len(names) != 0 {
		t.Fatalf("dir has %v, want empty", names)
	}
	if _, err := p.Write([]byte("y")); err == nil {
		t.Fatal("Write after Abort should fail")
	}
}

func TestValidName(t *testing.T) {
	good := []string{"a", "my save.zip", "mod name_1.0.0.zip", "..a", "a..b"}
	bad := []string{"", ".", "..", "a/b", "../a", "a\\b", "a\x00b", "/abs", TempPrefix + "x.zip"}
	for _, n := range good {
		if err := ValidName(n); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidName(n); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidName(%q) = %v, want ErrInvalidName", n, err)
		}
	}
}

func TestValidSaveName(t *testing.T) {
	if err := ValidSaveName("world.zip"); err != nil {
		t.Errorf("world.zip: %v", err)
	}
	for _, n := range []string{"world", "world.ZIP", "world.zip.txt", "../w.zip", "a/w.zip", ""} {
		if err := ValidSaveName(n); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidSaveName(%q) = %v, want ErrInvalidName", n, err)
		}
	}
}

func TestJoin(t *testing.T) {
	dir := t.TempDir()
	got, err := Join(dir, "x.zip")
	if err != nil || got != filepath.Join(dir, "x.zip") {
		t.Fatalf("Join = %q, %v", got, err)
	}
	if _, err := Join(dir, ".."); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Join(..) = %v, want ErrInvalidName", err)
	}
}
