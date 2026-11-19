package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// TempPrefix starts the name of every temporary file created by AtomicWrite
// and Pending. Directory listings (saves, mods) must skip names with this
// prefix; it starts with a dot and never ends in ".zip".
const TempPrefix = ".docstorio-tmp-"

// AtomicWrite writes data to path: a temp file in the same directory is
// created with perm, written, closed and renamed over path. On any error the
// temp file is removed and path is left untouched.
func AtomicWrite(path string, data []byte, perm fs.FileMode) error {
	p, err := NewPending(filepath.Dir(path), perm)
	if err != nil {
		return err
	}
	defer p.Abort()
	if _, err := p.Write(data); err != nil {
		return err
	}
	return p.commitPath(path)
}

// Pending is a temp file inside a destination directory that becomes a real
// file on Commit. It is used for uploads and downloads, where the final name
// may only be known after the content has been inspected (a mod zip is named
// from its info.json).
//
// A Pending ends with Commit or Abort; Abort after a successful Commit does
// nothing, so "defer p.Abort()" is the expected pattern. A Pending is not
// safe for concurrent use.
type Pending struct {
	dir  string
	f    *os.File
	size int64
	done bool
}

// NewPending creates a temp file named TempPrefix+random in dir with mode
// perm.
func NewPending(dir string, perm fs.FileMode) (*Pending, error) {
	f, err := os.CreateTemp(dir, TempPrefix+"*")
	if err != nil {
		return nil, err
	}
	// CreateTemp uses 0600; set the final mode now so the rename needs no
	// further change. Chmod is not subject to the umask.
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return &Pending{dir: dir, f: f}, nil
}

// Write appends to the temp file.
func (p *Pending) Write(b []byte) (int, error) {
	if p.done {
		return 0, os.ErrClosed
	}
	n, err := p.f.Write(b)
	p.size += int64(n)
	return n, err
}

// File returns the underlying temp file, e.g. to read it back as an
// io.ReaderAt (mod zip inspection) before committing. Callers must not close
// it.
func (p *Pending) File() *os.File {
	return p.f
}

// Size returns the number of bytes written so far.
func (p *Pending) Size() int64 {
	return p.size
}

// Commit closes the temp file and renames it to name, which must pass
// ValidName and is placed in the Pending's directory (an existing file of
// that name is replaced).
func (p *Pending) Commit(name string) error {
	path, err := Join(p.dir, name)
	if err != nil {
		return err
	}
	return p.commitPath(path)
}

// commitPath syncs and closes the temp file and renames it to path. On error
// the Pending stays open so that Abort removes the temp file.
func (p *Pending) commitPath(path string) error {
	if p.done {
		return os.ErrClosed
	}
	if err := p.f.Sync(); err != nil {
		return err
	}
	if err := p.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(p.f.Name(), path); err != nil {
		// The file is closed; remove it here because Abort's Close would fail.
		os.Remove(p.f.Name())
		p.done = true
		return err
	}
	p.done = true
	return nil
}

// Abort closes and removes the temp file. It is safe to call after Commit or
// more than once.
func (p *Pending) Abort() error {
	if p.done {
		return nil
	}
	p.done = true
	closeErr := p.f.Close()
	removeErr := os.Remove(p.f.Name())
	if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
		return closeErr
	}
	if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
		return removeErr
	}
	return nil
}
