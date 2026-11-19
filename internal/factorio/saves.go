package factorio

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"docstorio/internal/fsutil"
)

var (
	// ErrSaveExists reports an upload or world name that already exists.
	// API: 409 conflict.
	ErrSaveExists = errors.New("a save with this name already exists")
	// ErrSaveInUse reports deleting or overwriting the save the server is
	// running. API: 409 conflict.
	ErrSaveInUse = errors.New("the save is in use by the running server")
)

// SaveInfo is one entry of GET /api/saves.
type SaveInfo struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Running  bool      `json:"running"` // loaded by the server (starting/running/stopping)
	Next     bool      `json:"next"`    // what Start("") would load now
}

// ResolveSave returns the save file name to load from savesDir: override
// when non-empty (it must pass fsutil.ValidSaveName and exist, else an
// error wrapping fs.ErrNotExist), otherwise the newest *.zip by mtime (ties:
// name ascending). No zip: ErrNoSaves.
func ResolveSave(savesDir, override string) (string, error) {
	if override != "" {
		path, err := savePath(savesDir, override)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err == nil && !info.Mode().IsRegular() {
			err = fs.ErrNotExist
		}
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("save %s: %w", override, fs.ErrNotExist)
			}
			return "", err
		}
		return override, nil
	}
	saves, err := listSaves(savesDir)
	if err != nil {
		return "", err
	}
	if len(saves) == 0 {
		return "", ErrNoSaves
	}
	return saves[0].Name, nil
}

// savePath checks a save name from a request and returns its path.
func savePath(savesDir, name string) (string, error) {
	if err := fsutil.ValidSaveName(name); err != nil {
		return "", err
	}
	return fsutil.Join(savesDir, name)
}

// listSaves returns the *.zip files of dir newest first (ties: name
// ascending), skipping directories and temp files. A missing dir gives an
// empty list.
func listSaves(dir string) ([]SaveInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []SaveInfo{}, nil
		}
		return nil, err
	}
	saves := []SaveInfo{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".zip") || strings.HasPrefix(name, fsutil.TempPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue // removed meanwhile, or not a file
		}
		saves = append(saves, SaveInfo{Name: name, Size: info.Size(), Modified: info.ModTime()})
	}
	slices.SortFunc(saves, func(a, b SaveInfo) int {
		if c := b.Modified.Compare(a.Modified); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return saves, nil
}

// runningSave returns the save of the current process, or "" when none
// runs. s.mu must be held.
func (s *Supervisor) runningSave() string {
	if s.proc == nil {
		return ""
	}
	return s.st.Save
}

// ListSaves returns all saves newest first with Running and Next filled in.
// Never nil.
func (s *Supervisor) ListSaves() ([]SaveInfo, error) {
	saves, err := listSaves(s.cfg.Paths.Saves)
	if err != nil {
		return []SaveInfo{}, err
	}
	s.mu.Lock()
	running := s.runningSave()
	s.mu.Unlock()
	for i := range saves {
		saves[i].Running = saves[i].Name == running
		saves[i].Next = i == 0
	}
	return saves, nil
}

// OpenSave opens saves/<name> for download (name checked with
// fsutil.ValidSaveName).
func (s *Supervisor) OpenSave(name string) (*os.File, os.FileInfo, error) {
	path, err := savePath(s.cfg.Paths.Saves, name)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("save %s: %w", name, fs.ErrNotExist)
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

// DeleteSave removes saves/<name>; ErrSaveInUse for the running save.
func (s *Supervisor) DeleteSave(name string) error {
	path, err := savePath(s.cfg.Paths.Saves, name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == s.runningSave() {
		return ErrSaveInUse
	}
	return os.Remove(path)
}

// CommitSaveUpload commits an uploaded save written to p (a Pending in
// saves/) as name (fsutil.ValidSaveName). An existing name is ErrSaveExists
// unless overwrite; the running save is always ErrSaveInUse.
func (s *Supervisor) CommitSaveUpload(p *fsutil.Pending, name string, overwrite bool) error {
	path, err := savePath(s.cfg.Paths.Saves, name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == s.runningSave() {
		return ErrSaveInUse
	}
	if !overwrite {
		if _, err := os.Lstat(path); err == nil {
			return ErrSaveExists
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return p.Commit(name)
}
