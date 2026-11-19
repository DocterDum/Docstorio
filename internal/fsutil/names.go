package fsutil

import (
	"errors"
	"path/filepath"
	"strings"
)

// ErrInvalidName reports a file name taken from a request that is not a
// single path element (empty, ".", "..", contains '/', '\\' or NUL), that
// starts with TempPrefix (reserved for the manager's temp files, which
// listings skip) or that breaks the caller's rule (a save name not ending in
// ".zip"). API: 400 bad_request.
var ErrInvalidName = errors.New("invalid file name")

// ValidName returns nil when name is a single path element: non-empty, not
// "." or "..", free of '/', '\\' and NUL, and not starting with TempPrefix.
// Otherwise ErrInvalidName.
func ValidName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || strings.HasPrefix(name, TempPrefix) {
		return ErrInvalidName
	}
	return nil
}

// ValidSaveName is ValidName plus the rule that save names end in ".zip"
// (DESIGN "File handling").
func ValidSaveName(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if !strings.HasSuffix(name, ".zip") {
		return ErrInvalidName
	}
	return nil
}

// Join checks name with ValidName and returns dir/name.
func Join(dir, name string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}
