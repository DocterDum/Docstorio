package mods

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"docstorio/internal/fsutil"
)

// Newest returns, per mod name, the newest version in mods (the one Factorio
// loads when no version is pinned; FACTS).
func Newest(mods []Mod) map[string]Mod {
	out := make(map[string]Mod, len(mods))
	for _, m := range mods {
		cur, ok := out[m.Name]
		if !ok || MustParseVersion(m.Version).Compare(MustParseVersion(cur.Version)) > 0 {
			out[m.Name] = m
		}
	}
	return out
}

// Scanner lists the mod zips in a directory. Parsed info.json results are
// cached by (file name, size, mtime); mu protects the cache. Only regular
// *.zip files are considered; mod-list.json, mod-settings.dat and temp files
// (fsutil.TempPrefix) are ignored. Zips whose info.json cannot be read are
// skipped.
type Scanner struct {
	dir   string
	mu    sync.Mutex
	cache map[scanKey]Info
}

type scanKey struct {
	name    string
	size    int64
	modTime time.Time
}

// NewScanner returns a Scanner for the mods directory.
func NewScanner(dir string) *Scanner {
	return &Scanner{dir: dir, cache: make(map[scanKey]Info)}
}

// Scan reads the directory and returns every valid zip (one mod may appear
// in several versions), sorted by name and version. Cache entries for files
// that disappeared are dropped. A missing directory gives an empty list.
func (s *Scanner) Scan() ([]Mod, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Mod{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Mod{}
	cache := make(map[scanKey]Info, len(s.cache))
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".zip") || strings.HasPrefix(name, fsutil.TempPrefix) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue // removed while scanning
		}
		key := scanKey{name: name, size: fi.Size(), modTime: fi.ModTime()}
		info, ok := s.cache[key]
		if !ok {
			info, err = readZipInfo(filepath.Join(s.dir, name), fi.Size())
			if err != nil {
				continue // Factorio skips unreadable zips too
			}
		}
		cache[key] = info
		out = append(out, Mod{Info: info, File: name, Size: fi.Size(), ModTime: fi.ModTime()})
	}
	s.cache = cache
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return lessName(out[i].Name, out[j].Name)
		}
		return MustParseVersion(out[i].Version).Compare(MustParseVersion(out[j].Version)) < 0
	})
	return out, nil
}

// readZipInfo opens path and reads its info.json with ReadInfo.
func readZipInfo(path string, size int64) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	return ReadInfo(f, size)
}
