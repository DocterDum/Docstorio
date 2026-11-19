package mods

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// zipEntry is one file in a test zip.
type zipEntry struct {
	name string
	body string
}

// makeZip builds a zip in memory (stored, not compressed).
func makeZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// infoJSON renders an info.json; deps nil omits the "dependencies" key.
func infoJSON(t *testing.T, name, version, factorioVersion string, deps []string) string {
	t.Helper()
	m := map[string]any{"name": name, "version": version, "title": name + " title", "factorio_version": factorioVersion}
	if deps != nil {
		m["dependencies"] = deps
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// modZip builds a normal mod zip: <name>_<version>/info.json plus a file.
func modZip(t *testing.T, name, version, factorioVersion string, deps ...string) []byte {
	t.Helper()
	if deps == nil {
		deps = []string{}
	}
	folder := name + "_" + version
	return makeZip(t,
		zipEntry{folder + "/", ""},
		zipEntry{folder + "/info.json", infoJSON(t, name, version, factorioVersion, deps)},
		zipEntry{folder + "/data.lua", "-- data"},
	)
}

// writeFile writes data to dir/name.
func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// builtinDeps are the built-in mods of Factorio 2.1.24 (FACTS).
var builtinDeps = map[string][]string{
	"base":           nil, // no "dependencies" key: must not default to itself
	"elevated-rails": {"base >= 2.1.0"},
	"recycler":       {"base >= 2.1.0"},
	"quality":        {"base >= 2.1.0", "recycler >= 2.1.0"},
	"space-age":      {"base >= 2.1.0", "elevated-rails >= 2.1.0", "+ quality >= 2.1.0", "recycler >= 2.1.0"},
}

// writeBuiltins creates a Factorio data dir with core and the five
// built-ins.
func writeBuiltins(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, dir, "core/info.json", []byte(infoJSON(t, "core", "2.1.24", "2.1", []string{})))
	for name, deps := range builtinDeps {
		writeFile(t, dir, name+"/info.json", []byte(infoJSON(t, name, "2.1.24", "2.1", deps)))
	}
}

// builtinMods returns the built-ins as in-memory Mods.
func builtinMods() map[string]Mod {
	out := map[string]Mod{}
	for name, deps := range builtinDeps {
		if deps == nil {
			deps = []string{}
		}
		out[name] = Mod{Info: Info{Name: name, Version: "2.1.24", FactorioVersion: "2.1", Dependencies: deps}, Builtin: true}
	}
	return out
}

// zipMod returns an installed zip Mod.
func zipMod(name, version, factorioVersion string, deps ...string) Mod {
	if deps == nil {
		deps = []string{}
	}
	return Mod{Info: Info{Name: name, Version: version, FactorioVersion: factorioVersion, Dependencies: deps}, File: name + "_" + version + ".zip"}
}

// rel builds a portal release.
func rel(name, version, factorioVersion string, deps ...string) Release {
	if deps == nil {
		deps = []string{}
	}
	return Release{
		Name:            name,
		Version:         version,
		FactorioVersion: factorioVersion,
		Dependencies:    deps,
		DownloadURL:     "/download/" + name + "/" + version,
	}
}

// fakeSource is an in-memory ReleaseSource. Download writes a real mod
// zip built from the release.
type fakeSource struct {
	t        *testing.T
	releases map[string][]Release
	err      error // returned by Releases and UpdateReleases when set

	mu        sync.Mutex
	downloads []string
}

func newFakeSource(t *testing.T, rels ...Release) *fakeSource {
	f := &fakeSource{t: t, releases: map[string][]Release{}}
	for _, r := range rels {
		f.releases[r.Name] = append(f.releases[r.Name], r)
	}
	return f
}

func (f *fakeSource) Releases(ctx context.Context, name string) ([]Release, error) {
	if f.err != nil {
		return nil, f.err
	}
	r, ok := f.releases[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return r, nil
}

func (f *fakeSource) UpdateReleases(ctx context.Context, names []string) (map[string][]Release, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string][]Release{}
	for _, n := range names {
		for _, r := range f.releases[n] {
			r.Dependencies = nil
			out[n] = append(out[n], r)
		}
	}
	return out, nil
}

func (f *fakeSource) Download(ctx context.Context, r Release, creds Credentials, destDir string) (string, error) {
	if creds.Empty() {
		return "", ErrCredentials
	}
	f.mu.Lock()
	f.downloads = append(f.downloads, r.Name+" "+r.Version)
	f.mu.Unlock()
	deps := r.Dependencies
	if deps == nil {
		deps = []string{}
	}
	return writeFile(f.t, destDir, r.Name+"_"+r.Version+".zip", modZip(f.t, r.Name, r.Version, r.FactorioVersion, deps...)), nil
}

// testLog collects job log lines.
type testLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLog) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

var v21 = Version{Major: 2, Minor: 1, Patch: 24}
