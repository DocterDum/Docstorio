package mods

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"docstorio/internal/fsutil"
)

type testEnv struct {
	m      *Manager
	src    *fakeSource
	cfg    Config
	mods   string
	creds  Credentials
	logger *testLog
}

// newTestManager sets up a data dir with the 2.1.24 built-ins, mods/ with
// the given zips, and a Manager over it.
func newTestManager(t *testing.T, src *fakeSource, zips map[string][]byte) *testEnv {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "opt", "data")
	writeBuiltins(t, data)
	modsDir := filepath.Join(root, "mods")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, b := range zips {
		writeFile(t, modsDir, name, b)
	}
	cfg := Config{
		ModsDir:         modsDir,
		FactorioDataDir: data,
		StatePath:       filepath.Join(root, "manager", "mod-state.json"),
		ModListPath:     filepath.Join(modsDir, "mod-list.json"),
		Factorio:        v21,
	}
	if err := os.MkdirAll(filepath.Dir(cfg.StatePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if src != nil {
		cfg.Source = src
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{m: m, src: src, cfg: cfg, mods: modsDir, creds: Credentials{Username: "u", Token: "t"}, logger: &testLog{}}
}

func (e *testEnv) zipFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(e.mods)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".zip") {
			out = append(out, en.Name())
		}
	}
	sort.Strings(out)
	return out
}

func (e *testEnv) enabled(names ...string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		out[n] = e.m.Enabled(n)
	}
	return out
}

func TestEnableDisableBuiltins(t *testing.T) {
	e := newTestManager(t, nil, map[string][]byte{
		"needs-sa_1.0.0.zip": modZip(t, "needs-sa", "1.0.0", "2.1", "space-age"),
	})
	m := e.m

	// recycler is required by quality and space-age; space-age by needs-sa.
	_, err := m.Disable("recycler", false)
	var dep *DependentsError
	if !errors.As(err, &dep) || dep.Mod != "recycler" || !reflect.DeepEqual(dep.Dependents, []string{"needs-sa", "quality", "space-age"}) {
		t.Fatalf("disable recycler: %v", err)
	}
	if !m.Enabled("recycler") {
		t.Fatal("recycler disabled despite the conflict")
	}

	// quality is only recommended by space-age.
	got, err := m.Disable("quality", false)
	if err != nil || !reflect.DeepEqual(got, []string{"quality"}) {
		t.Fatalf("disable quality: %v %v", got, err)
	}

	_, err = m.Disable("elevated-rails", false)
	if !errors.As(err, &dep) || !reflect.DeepEqual(dep.Dependents, []string{"needs-sa", "space-age"}) {
		t.Fatalf("disable elevated-rails: %v", err)
	}

	got, err = m.Disable("recycler", true)
	if err != nil || !reflect.DeepEqual(got, []string{"needs-sa", "recycler", "space-age"}) {
		t.Fatalf("cascade recycler: %v %v", got, err)
	}
	got, err = m.Disable("elevated-rails", false)
	if err != nil || !reflect.DeepEqual(got, []string{"elevated-rails"}) {
		t.Fatalf("disable elevated-rails: %v %v", got, err)
	}

	// Base-game world: all four expansion mods disabled, explicitly listed.
	if err := m.WriteModList(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(e.cfg.ModListPath)
	if err != nil {
		t.Fatal(err)
	}
	var list ModList
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	want := ModList{Mods: []ModListEntry{
		{"base", true}, {"elevated-rails", false}, {"needs-sa", false},
		{"quality", false}, {"recycler", false}, {"space-age", false},
	}}
	if !reflect.DeepEqual(list, want) {
		t.Errorf("mod-list.json = %s", data)
	}

	// Enabling quality pulls recycler; space-age pulls its required deps,
	// not the recommended quality.
	got, err = m.Enable("quality")
	if err != nil || !reflect.DeepEqual(got, []string{"quality", "recycler"}) {
		t.Fatalf("enable quality: %v %v", got, err)
	}
	m.Disable("quality", false)
	got, err = m.Enable("needs-sa")
	if err != nil || !reflect.DeepEqual(got, []string{"elevated-rails", "needs-sa", "space-age"}) {
		t.Fatalf("enable needs-sa: %v %v", got, err)
	}
	if m.Enabled("quality") {
		t.Error("quality enabled by space-age")
	}
	got, err = m.Enable("space-age")
	if err != nil || len(got) != 0 || got == nil {
		t.Fatalf("enable enabled: %v %v", got, err)
	}

	// Errors.
	if _, err := m.Disable("base", true); !errors.Is(err, ErrBaseRequired) {
		t.Errorf("disable base: %v", err)
	}
	if _, err := m.Disable("nope", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("disable unknown: %v", err)
	}
	if _, err := m.Enable("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("enable unknown: %v", err)
	}

	// The state survives a restart.
	m2, err := New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !m2.Enabled("space-age") || m2.Enabled("quality") || !m2.Enabled("base") || m2.Enabled("not-installed") {
		t.Error("state not persisted")
	}
}

func TestDisableCascadeTransitive(t *testing.T) {
	e := newTestManager(t, nil, map[string][]byte{
		"lib_1.0.0.zip": modZip(t, "lib", "1.0.0", "2.1"),
		"mid_1.0.0.zip": modZip(t, "mid", "1.0.0", "2.1", "~ lib"),
		"top_1.0.0.zip": modZip(t, "top", "1.0.0", "2.1", "mid >= 1.0"),
		"opt_1.0.0.zip": modZip(t, "opt", "1.0.0", "2.1", "? lib", "+ mid"),
	})
	_, err := e.m.Disable("lib", false)
	var dep *DependentsError
	if !errors.As(err, &dep) || !reflect.DeepEqual(dep.Dependents, []string{"mid", "top"}) {
		t.Fatalf("got %v", err)
	}
	got, err := e.m.Disable("lib", true)
	if err != nil || !reflect.DeepEqual(got, []string{"lib", "mid", "top"}) {
		t.Fatalf("cascade: %v %v", got, err)
	}
	if !e.m.Enabled("opt") {
		t.Error("optional dependent was disabled")
	}
	got, err = e.m.Enable("top")
	if err != nil || !reflect.DeepEqual(got, []string{"lib", "mid", "top"}) {
		t.Fatalf("enable top: %v %v", got, err)
	}
}

func TestSeedFromModList(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	writeBuiltins(t, data)
	modsDir := filepath.Join(root, "mods")
	writeFile(t, modsDir, "mod-list.json", []byte(`{"mods":[
		{"name":"base","enabled":true},
		{"name":"elevated-rails","enabled":false},
		{"name":"quality","enabled":false},
		{"name":"recycler","enabled":false},
		{"name":"space-age","enabled":false},
		{"name":"a","enabled":false}
	]}`))
	writeFile(t, modsDir, "a_1.0.0.zip", modZip(t, "a", "1.0.0", "2.1"))
	writeFile(t, modsDir, "b_1.0.0.zip", modZip(t, "b", "1.0.0", "2.1"))
	cfg := Config{ModsDir: modsDir, FactorioDataDir: data, StatePath: filepath.Join(root, "mod-state.json"), ModListPath: filepath.Join(modsDir, "mod-list.json"), Factorio: v21}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if m.Enabled("space-age") || m.Enabled("a") || !m.Enabled("b") || !m.Enabled("base") {
		t.Error("not seeded from mod-list.json")
	}
	if _, err := os.Stat(cfg.StatePath); err != nil {
		t.Errorf("state not written: %v", err)
	}
}

func TestNewErrors(t *testing.T) {
	root := t.TempDir()
	if _, err := New(Config{FactorioDataDir: filepath.Join(root, "missing"), StatePath: filepath.Join(root, "s.json"), ModsDir: root}); err == nil {
		t.Error("missing data dir: no error")
	}
}

func TestAddUpload(t *testing.T) {
	e := newTestManager(t, nil, map[string][]byte{
		"up_0.9.0.zip":    modZip(t, "up", "0.9.0", "2.1"),
		"up_2.0.0.zip":    modZip(t, "up", "2.0.0", "2.1"),
		"other_1.0.0.zip": modZip(t, "other", "1.0.0", "2.1"),
	})
	// Disabled built-in dependency gets enabled along with the upload.
	if _, err := e.m.Disable("space-age", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Disable("up", false); err != nil {
		t.Fatal(err)
	}

	upload := func(b []byte) (Info, error) {
		p, err := fsutil.NewPending(e.mods, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Abort()
		if _, err := p.Write(b); err != nil {
			t.Fatal(err)
		}
		return e.m.AddUpload(p)
	}

	info, err := upload(modZip(t, "up", "1.0.0", "2.0", "space-age"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "up" || info.Version != "1.0.0" {
		t.Errorf("info = %+v", info)
	}
	if got, want := e.zipFiles(t), []string{"other_1.0.0.zip", "up_1.0.0.zip"}; !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	if !e.m.Enabled("up") || !e.m.Enabled("space-age") {
		t.Error("upload not enabled with its dependencies")
	}
	list, err := e.m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range list.Mods {
		if v.Name == "up" && (v.Compatible || v.FactorioVersion != "2.0") {
			t.Errorf("incompatible upload shown as %+v", v)
		}
	}

	if _, err := upload(modZip(t, "quality", "9.0.0", "2.1")); !errors.Is(err, ErrBuiltin) {
		t.Errorf("built-in upload: %v", err)
	}
	if _, err := upload([]byte("not a zip")); !errors.Is(err, ErrInvalidMod) {
		t.Errorf("invalid upload: %v", err)
	}
	if got, want := e.zipFiles(t), []string{"other_1.0.0.zip", "up_1.0.0.zip"}; !reflect.DeepEqual(got, want) {
		t.Errorf("files after rejected uploads = %v, want %v", got, want)
	}
	entries, _ := os.ReadDir(e.mods)
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), fsutil.TempPrefix) {
			t.Errorf("temp file left: %s", en.Name())
		}
	}
}

func TestDelete(t *testing.T) {
	e := newTestManager(t, nil, map[string][]byte{
		"a_1.0.0.zip":    modZip(t, "a", "1.0.0", "2.1"),
		"a_2.0.0.zip":    modZip(t, "a", "2.0.0", "2.1"),
		"weird-name.zip": modZip(t, "a", "3.0.0", "2.1"),
		"b_1.0.0.zip":    modZip(t, "b", "1.0.0", "2.1"),
	})
	e.m.Disable("a", false)
	if err := e.m.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if got := e.zipFiles(t); !reflect.DeepEqual(got, []string{"b_1.0.0.zip"}) {
		t.Errorf("files = %v", got)
	}
	if _, ok := e.m.state.Mods["a"]; ok {
		t.Error("state entry kept")
	}
	if err := e.m.Delete("a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if err := e.m.Delete("space-age"); !errors.Is(err, ErrBuiltin) {
		t.Errorf("delete built-in: %v", err)
	}
	if err := e.m.Delete("../b_1.0.0.zip"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete path: %v", err)
	}
}

func TestListAndInstalledVersions(t *testing.T) {
	src := newFakeSource(t,
		rel("a", "1.0.0", "2.1"), rel("a", "1.5.0", "2.1"), rel("a", "2.0.0", "2.0"),
		rel("b", "1.0.0", "2.1"),
	)
	e := newTestManager(t, src, map[string][]byte{
		"a_1.0.0.zip": modZip(t, "a", "1.0.0", "2.1", "base >= 2.1", "missing-lib", "? missing-opt", "core"),
		"b_1.0.0.zip": modZip(t, "b", "1.0.0", "2.1"),
		"Z_1.0.0.zip": modZip(t, "Z", "1.0.0", "1.1"),
	})
	e.m.Disable("b", false)

	list, err := e.m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]ModView{}
	for _, v := range list.Mods {
		names = append(names, v.Name)
		byName[v.Name] = v
	}
	want := []string{"base", "elevated-rails", "quality", "recycler", "space-age", "a", "b", "Z"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}
	a := byName["a"]
	if a.UpdateAvailable == nil || *a.UpdateAvailable != "1.5.0" || !a.Enabled || !a.Compatible || a.Title != "a title" {
		t.Errorf("a = %+v", a)
	}
	if !reflect.DeepEqual(a.MissingDependencies, []string{"missing-lib"}) || len(a.Dependencies) != 4 {
		t.Errorf("a deps = %v missing = %v", a.Dependencies, a.MissingDependencies)
	}
	if b := byName["b"]; b.Enabled || b.UpdateAvailable != nil || b.MissingDependencies == nil {
		t.Errorf("b = %+v", b)
	}
	if z := byName["Z"]; z.Compatible {
		t.Errorf("Z = %+v", z)
	}
	if sa := byName["space-age"]; !sa.Builtin || !sa.Compatible || sa.UpdateAvailable != nil {
		t.Errorf("space-age = %+v", sa)
	}

	// A portal failure only drops the update information.
	src.err = errors.New("down")
	list, err = e.m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range list.Mods {
		if v.UpdateAvailable != nil {
			t.Errorf("%s update shown while the portal is down", v.Name)
		}
	}

	iv := e.m.InstalledVersions()
	if iv["a"] != "1.0.0" || iv["space-age"] != "2.1.24" || len(iv) != 8 {
		t.Errorf("InstalledVersions = %v", iv)
	}
}

func TestInstall(t *testing.T) {
	src := newFakeSource(t,
		rel("A", "1.0.0", "2.1", "base >= 2.1", "B >= 1.1", "space-age"),
		rel("B", "1.0.0", "2.1"), rel("B", "1.2.0", "2.1"),
	)
	e := newTestManager(t, src, map[string][]byte{
		"B_1.0.0.zip": modZip(t, "B", "1.0.0", "2.1"),
	})
	e.m.Disable("space-age", false)
	ctx := context.Background()
	req := PlanRequest{Mods: []Requested{{Name: "A"}}}

	if changed, err := e.m.Install(ctx, e.logger, req, Credentials{}); !errors.Is(err, ErrCredentials) || changed {
		t.Errorf("no credentials: %v %v", changed, err)
	}
	if changed, err := e.m.Install(ctx, e.logger, req, e.creds); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if got, want := e.zipFiles(t), []string{"A_1.0.0.zip", "B_1.2.0.zip"}; !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	if !e.m.Enabled("A") || !e.m.Enabled("B") || !e.m.Enabled("space-age") || !e.m.Enabled("quality") {
		t.Errorf("enabled = %v", e.enabled("A", "B", "space-age", "quality"))
	}
	if len(e.logger.lines) == 0 {
		t.Error("nothing logged")
	}

	// Installing again changes nothing.
	if changed, err := e.m.Install(ctx, e.logger, req, e.creds); err != nil || changed {
		t.Fatal(changed, err)
	}
	if len(src.downloads) != 2 {
		t.Errorf("downloads = %v", src.downloads)
	}

	// A plan with conflicts fails and logs them.
	src.releases["C"] = []Release{rel("C", "1.0.0", "2.1", "! A")}
	log := &testLog{}
	if changed, err := e.m.Install(ctx, log, PlanRequest{Mods: []Requested{{Name: "C"}}}, e.creds); !errors.Is(err, ErrPlanFailed) || changed {
		t.Errorf("conflict: %v %v", changed, err)
	}
	if len(log.lines) == 0 || !strings.Contains(log.lines[0], "C: incompatible with A") {
		t.Errorf("log = %v", log.lines)
	}
	if _, err := os.Stat(filepath.Join(e.mods, "C_1.0.0.zip")); err == nil {
		t.Error("conflicting mod was downloaded")
	}

	// Invalid requests.
	if _, err := e.m.Plan(ctx, PlanRequest{Mods: []Requested{{Name: "../x"}}}); !errors.Is(err, ErrInvalidMod) {
		t.Errorf("bad name: %v", err)
	}
	if _, err := e.m.Plan(ctx, PlanRequest{Mods: []Requested{{Name: "A", Version: "x"}}}); !errors.Is(err, ErrInvalidMod) {
		t.Errorf("bad version: %v", err)
	}
}

func TestUpdate(t *testing.T) {
	src := newFakeSource(t,
		rel("on", "1.0.0", "2.1"), rel("on", "2.0.0", "2.1", "newdep"), rel("on", "3.0.0", "2.0"),
		rel("off", "1.0.0", "2.1"), rel("off", "1.1.0", "2.1", "newdep"),
		rel("same", "1.0.0", "2.1"),
		rel("newdep", "1.0.0", "2.1", "+ rec"),
		rel("rec", "1.0.0", "2.1"),
	)
	e := newTestManager(t, src, map[string][]byte{
		"on_1.0.0.zip":   modZip(t, "on", "1.0.0", "2.1"),
		"off_1.0.0.zip":  modZip(t, "off", "1.0.0", "2.1"),
		"same_1.0.0.zip": modZip(t, "same", "1.0.0", "2.1"),
	})
	e.m.Disable("off", false)
	ctx := context.Background()

	if _, err := e.m.Update(ctx, e.logger, nil, Credentials{}); !errors.Is(err, ErrCredentials) {
		t.Errorf("no credentials: %v", err)
	}
	if _, err := e.m.Update(ctx, e.logger, []string{"space-age"}, e.creds); !errors.Is(err, ErrBuiltin) {
		t.Errorf("built-in: %v", err)
	}
	if _, err := e.m.Update(ctx, e.logger, []string{"nope"}, e.creds); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}

	if changed, err := e.m.Update(ctx, e.logger, nil, e.creds); err != nil || !changed {
		t.Fatal(changed, err)
	}
	want := []string{"newdep_1.0.0.zip", "off_1.1.0.zip", "on_2.0.0.zip", "same_1.0.0.zip"}
	if got := e.zipFiles(t); !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	if !e.m.Enabled("on") || !e.m.Enabled("newdep") || e.m.Enabled("off") {
		t.Errorf("enabled = %v", e.enabled("on", "newdep", "off"))
	}

	// Nothing left to update.
	before := len(src.downloads)
	if changed, err := e.m.Update(ctx, e.logger, []string{"on"}, e.creds); err != nil || changed {
		t.Fatal(changed, err)
	}
	if len(src.downloads) != before {
		t.Errorf("downloaded again: %v", src.downloads)
	}
}

// TestUpdateDependencyBeforeItsDependent updates a library together with a
// mod that requires it and sorts first: both updates are applied in one go.
func TestUpdateDependencyBeforeItsDependent(t *testing.T) {
	src := newFakeSource(t,
		rel("A", "1.0.0", "2.1", "B >= 1.0.0"), rel("A", "2.0.0", "2.1", "B >= 1.0.0"),
		rel("B", "1.0.0", "2.1"), rel("B", "2.0.0", "2.1"),
	)
	e := newTestManager(t, src, map[string][]byte{
		"A_1.0.0.zip": modZip(t, "A", "1.0.0", "2.1", "B >= 1.0.0"),
		"B_1.0.0.zip": modZip(t, "B", "1.0.0", "2.1"),
	})
	if changed, err := e.m.Update(context.Background(), e.logger, nil, e.creds); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if got, want := e.zipFiles(t), []string{"A_2.0.0.zip", "B_2.0.0.zip"}; !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

// failingSource fails the download of one release.
type failingSource struct {
	*fakeSource
	fail string // "name version"
}

func (f *failingSource) Download(ctx context.Context, r Release, creds Credentials, destDir string) (string, error) {
	if r.Name+" "+r.Version == f.fail {
		return "", errors.New("network down")
	}
	return f.fakeSource.Download(ctx, r, creds, destDir)
}

// TestInstallPartialFailureReportsChange checks that changed is true when a
// later download fails after an earlier one was committed, and false when
// nothing was committed.
func TestInstallPartialFailureReportsChange(t *testing.T) {
	fake := newFakeSource(t, rel("A", "1.0.0", "2.1", "B"), rel("B", "1.0.0", "2.1"))
	e := newTestManager(t, nil, nil)
	e.m.cfg.Source = &failingSource{fakeSource: fake, fail: "B 1.0.0"}
	ctx := context.Background()
	req := PlanRequest{Mods: []Requested{{Name: "A"}}}

	changed, err := e.m.Install(ctx, e.logger, req, e.creds)
	if err == nil || !changed {
		t.Errorf("B fails after A: changed=%v err=%v, want true and an error", changed, err)
	}

	e.m.cfg.Source = &failingSource{fakeSource: fake, fail: "A 1.0.0"}
	if err := os.Remove(filepath.Join(e.mods, "A_1.0.0.zip")); err != nil {
		t.Fatal(err)
	}
	changed, err = e.m.Install(ctx, e.logger, req, e.creds)
	if err == nil || changed {
		t.Errorf("A fails first: changed=%v err=%v, want false and an error", changed, err)
	}
}

func TestEnabledSpaceAge(t *testing.T) {
	e := newTestManager(t, nil, nil)
	if !e.m.Enabled(SpaceAge) || !e.m.Enabled(Base) {
		t.Error("fresh install: space-age not enabled")
	}
	if _, err := e.m.Disable(SpaceAge, false); err != nil {
		t.Fatal(err)
	}
	if e.m.Enabled(SpaceAge) {
		t.Error("space-age still enabled")
	}
	if e.m.Factorio() != v21 {
		t.Error("Factorio version")
	}
}
