package mods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"docstorio/internal/fsutil"
	"docstorio/internal/jobs"
)

var (
	// ErrBuiltin reports deleting a built-in mod, or uploading a zip with a
	// built-in's name. API: 409 conflict.
	ErrBuiltin = errors.New("built-in mods cannot be changed this way")
	// ErrBaseRequired reports an attempt to disable "base". API: 409 conflict.
	ErrBaseRequired = errors.New("the base mod cannot be disabled")
	// ErrPlanFailed reports a plan with conflicts during Install / Update
	// (the job fails after logging the conflicts).
	ErrPlanFailed = errors.New("the mod plan has conflicts")
)

// DependentsError is returned when disabling a mod would break enabled mods
// that require it and cascade was not requested.
// API: 409 {"error", "code": "conflict", "dependents": [...]}.
type DependentsError struct {
	Mod        string   // the mod being disabled
	Dependents []string // enabled mods that require it, sorted
}

func (e *DependentsError) Error() string {
	return fmt.Sprintf("%s is required by: %s", e.Mod, strings.Join(e.Dependents, ", "))
}

// Config configures a Manager.
type Config struct {
	ModsDir         string        // /factorio/mods
	FactorioDataDir string        // /opt/factorio/data (built-ins)
	StatePath       string        // /factorio/manager/mod-state.json
	ModListPath     string        // /factorio/mods/mod-list.json
	Factorio        Version       // running (baked) Factorio version
	Source          ReleaseSource // nil = offline: plans and updates fail
}

// ModView is one entry of GET /api/mods.
type ModView struct {
	Name                string   `json:"name"`
	Title               string   `json:"title"`
	Version             string   `json:"version"`
	Enabled             bool     `json:"enabled"`
	Builtin             bool     `json:"builtin"`
	FactorioVersion     string   `json:"factorioVersion"`
	Compatible          bool     `json:"compatible"`
	Dependencies        []string `json:"dependencies"`        // raw strings
	MissingDependencies []string `json:"missingDependencies"` // required deps not installed (raw strings)
	UpdateAvailable     *string  `json:"updateAvailable"`     // newest compatible newer version, or null
}

// ListResult is the body of GET /api/mods.
type ListResult struct {
	Mods []ModView `json:"mods"` // built-ins first, then by name; never nil
}

// Manager is the facade over installed mods and their desired state. mu
// protects state, builtins and the scanner use; it is never held while
// calling the ReleaseSource. Methods that change files in mods/ or the
// state expect the caller to hold the jobs busy lock.
type Manager struct {
	cfg      Config
	mu       sync.Mutex
	state    State
	builtins []Mod
	scanner  *Scanner
}

// New discovers the built-ins, loads (or seeds) the state and scans mods/.
func New(cfg Config) (*Manager, error) {
	builtins, err := Builtins(cfg.FactorioDataDir)
	if err != nil {
		return nil, err
	}
	state, err := LoadState(cfg.StatePath, cfg.ModListPath)
	if err != nil {
		return nil, fmt.Errorf("mod state: %w", err)
	}
	m := &Manager{cfg: cfg, state: state, builtins: builtins, scanner: NewScanner(cfg.ModsDir)}
	if _, err := m.scanner.Scan(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", cfg.ModsDir, err)
	}
	return m, nil
}

// Factorio returns the running Factorio version the Manager was built with.
func (m *Manager) Factorio() Version {
	return m.cfg.Factorio
}

// installedLocked returns every installed mod by name (built-ins, plus the
// newest zip of every other name) and the zips themselves. m.mu must be
// held.
func (m *Manager) installedLocked() (map[string]Mod, []Mod, error) {
	zips, err := m.scanner.Scan()
	if err != nil {
		return nil, nil, err
	}
	all := Newest(zips)
	for _, b := range m.builtins {
		all[b.Name] = b // a zip cannot replace a built-in
	}
	return all, zips, nil
}

// enabledLocked returns the desired enabled state of every installed mod.
// m.mu must be held.
func (m *Manager) enabledLocked(installed map[string]Mod) map[string]bool {
	enabled := make(map[string]bool, len(installed))
	for name := range installed {
		enabled[name] = m.state.Enabled(name)
	}
	return enabled
}

// List returns the installed mods for the API. Update information comes from
// Source.UpdateReleases; a portal failure leaves UpdateAvailable nil rather
// than failing the call.
func (m *Manager) List(ctx context.Context) (ListResult, error) {
	m.mu.Lock()
	installed, _, err := m.installedLocked()
	enabled := m.enabledLocked(installed)
	m.mu.Unlock()
	if err != nil {
		return ListResult{Mods: []ModView{}}, err
	}

	var zipNames []string
	for name, mod := range installed {
		if !mod.Builtin {
			zipNames = append(zipNames, name)
		}
	}
	sort.Strings(zipNames)
	var updates map[string]string
	if m.cfg.Source != nil && len(zipNames) > 0 {
		if rels, err := m.cfg.Source.UpdateReleases(ctx, zipNames); err == nil {
			updates = m.newerReleases(installed, rels)
		}
	}

	views := make([]ModView, 0, len(installed))
	for name, mod := range installed {
		v := ModView{
			Name:                name,
			Title:               mod.Title,
			Version:             mod.Version,
			Enabled:             enabled[name],
			Builtin:             mod.Builtin,
			FactorioVersion:     mod.FactorioVersion,
			Compatible:          mod.Builtin || Compatible(mod.FactorioVersion, m.cfg.Factorio),
			Dependencies:        append([]string{}, mod.Dependencies...),
			MissingDependencies: []string{},
		}
		deps, _ := ParseDependencies(mod.Dependencies)
		for _, d := range deps {
			if _, ok := installed[d.Name]; d.Kind.IsRequired() && !ok && d.Name != Core {
				v.MissingDependencies = append(v.MissingDependencies, d.Raw)
			}
		}
		if u, ok := updates[name]; ok {
			v.UpdateAvailable = &u
		}
		views = append(views, v)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Builtin != views[j].Builtin {
			return views[i].Builtin
		}
		return lessName(views[i].Name, views[j].Name)
	})
	return ListResult{Mods: views}, nil
}

// newerReleases returns, per installed zip mod, the newest compatible
// release that is newer than the installed version.
func (m *Manager) newerReleases(installed map[string]Mod, rels map[string][]Release) map[string]string {
	out := map[string]string{}
	for name, list := range rels {
		mod, ok := installed[name]
		if !ok || mod.Builtin {
			continue
		}
		if rel, ok := newestCompatible(list, m.cfg.Factorio); ok &&
			MustParseVersion(rel.Version).Compare(MustParseVersion(mod.Version)) > 0 {
			out[name] = rel.Version
		}
	}
	return out
}

// newestCompatible returns the release with the highest version that is
// compatible with running.
func newestCompatible(list []Release, running Version) (Release, bool) {
	var best Release
	var bestV Version
	found := false
	for _, rel := range list {
		v, err := ParseVersion(rel.Version)
		if err != nil || !Compatible(rel.FactorioVersion, running) {
			continue
		}
		if !found || v.Compare(bestV) > 0 {
			best, bestV, found = rel, v, true
		}
	}
	return best, found
}

// InstalledVersions returns name -> version of every installed mod (newest
// zip) and built-in, for the "installed" fields of portal results.
func (m *Manager) InstalledVersions() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	installed, _, err := m.installedLocked()
	if err != nil {
		return out
	}
	for name, mod := range installed {
		out[name] = mod.Version
	}
	return out
}

// Enabled reports the desired state of name (used for the status
// "spaceAge" flag). A mod that is not installed is not enabled.
func (m *Manager) Enabled(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	installed, _, err := m.installedLocked()
	if err != nil {
		return false
	}
	_, ok := installed[name]
	return ok && m.state.Enabled(name)
}

// Enable enables name and, recursively, its installed required
// dependencies. It returns every name that changed from disabled to enabled,
// sorted. Missing dependencies do not fail the call (they show as
// missingDependencies). Unknown name: ErrNotFound.
func (m *Manager) Enable(name string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	installed, _, err := m.installedLocked()
	if err != nil {
		return nil, err
	}
	if _, ok := installed[name]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	changed := m.enableLocked(installed, name)
	if len(changed) > 0 {
		if err := SaveState(m.cfg.StatePath, m.state); err != nil {
			return nil, err
		}
	}
	return changed, nil
}

// enableLocked enables name and its installed required dependencies in
// m.state (without saving) and returns the names that changed, sorted.
func (m *Manager) enableLocked(installed map[string]Mod, name string) []string {
	changed := []string{}
	visited := map[string]bool{}
	stack := []string{name}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		mod, ok := installed[n]
		if visited[n] || !ok {
			continue
		}
		visited[n] = true
		if !m.state.Enabled(n) {
			m.state.Set(n, true)
			changed = append(changed, n)
		}
		deps, _ := ParseDependencies(mod.Dependencies)
		for _, d := range deps {
			if d.Kind.IsRequired() {
				stack = append(stack, d.Name)
			}
		}
	}
	sort.Strings(changed)
	return changed
}

// Disable disables name. If enabled mods require it, it returns
// *DependentsError (listing every enabled mod that would have to be
// disabled with it) unless cascade, in which case those mods are disabled
// too. Returns every name that changed, sorted. "base": ErrBaseRequired.
// Unknown name: ErrNotFound.
func (m *Manager) Disable(name string, cascade bool) ([]string, error) {
	if name == Base {
		return nil, ErrBaseRequired
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	installed, _, err := m.installedLocked()
	if err != nil {
		return nil, err
	}
	if _, ok := installed[name]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	// Closure of enabled mods that (transitively) require name.
	off := map[string]bool{name: true}
	for grew := true; grew; {
		grew = false
		for n, mod := range installed {
			if off[n] || !m.state.Enabled(n) {
				continue
			}
			deps, _ := ParseDependencies(mod.Dependencies)
			for _, d := range deps {
				if d.Kind.IsRequired() && off[d.Name] {
					off[n] = true
					grew = true
					break
				}
			}
		}
	}
	dependents := []string{}
	for n := range off {
		if n != name {
			dependents = append(dependents, n)
		}
	}
	sort.Strings(dependents)
	if len(dependents) > 0 && !cascade {
		return nil, &DependentsError{Mod: name, Dependents: dependents}
	}
	if off[Base] {
		return nil, ErrBaseRequired
	}

	changed := []string{}
	for n := range off {
		if m.state.Enabled(n) {
			changed = append(changed, n)
		}
		m.state.Set(n, false)
	}
	sort.Strings(changed)
	if err := SaveState(m.cfg.StatePath, m.state); err != nil {
		return nil, err
	}
	return changed, nil
}

// Delete removes every zip of name from mods/ and its state entry.
// Built-in: ErrBuiltin. Not installed: ErrNotFound.
func (m *Manager) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	installed, zips, err := m.installedLocked()
	if err != nil {
		return err
	}
	if mod, ok := installed[name]; ok && mod.Builtin {
		return ErrBuiltin
	}
	if _, ok := installed[name]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err := m.removeZipsLocked(zips, name, ""); err != nil {
		return err
	}
	delete(m.state.Mods, name)
	return SaveState(m.cfg.StatePath, m.state)
}

// removeZipsLocked deletes every zip of name except the file keep.
func (m *Manager) removeZipsLocked(zips []Mod, name, keep string) error {
	for _, z := range zips {
		if z.Name != name || z.File == keep {
			continue
		}
		path, err := fsutil.Join(m.cfg.ModsDir, z.File)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// AddUpload reads info.json from the uploaded zip (ReadInfo on p.File()),
// commits it as <name>_<version>.zip regardless of the uploaded file name,
// deletes other versions of the same mod and enables it (with its installed
// required dependencies, like Enable). An incompatible mod is accepted
// (List shows compatible=false). A built-in's name is rejected with
// ErrBuiltin. The caller still owns p and must Abort it (which does nothing
// after a successful commit).
func (m *Manager) AddUpload(p *fsutil.Pending) (Info, error) {
	info, err := ReadInfo(p.File(), p.Size())
	if err != nil {
		return Info{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.builtins {
		if b.Name == info.Name {
			return Info{}, fmt.Errorf("%w: %s is a built-in mod", ErrBuiltin, info.Name)
		}
	}
	if err := p.Commit(info.FileName()); err != nil {
		return Info{}, err
	}
	_, zips, err := m.installedLocked()
	if err != nil {
		return Info{}, err
	}
	if err := m.removeZipsLocked(zips, info.Name, info.FileName()); err != nil {
		return Info{}, err
	}
	installed, _, err := m.installedLocked()
	if err != nil {
		return Info{}, err
	}
	m.enableLocked(installed, info.Name)
	if err := SaveState(m.cfg.StatePath, m.state); err != nil {
		return Info{}, err
	}
	return info, nil
}

// validateRequest checks the requested names and pinned versions.
func validateRequest(req PlanRequest) error {
	for _, r := range req.Mods {
		if !nameRe.MatchString(r.Name) {
			return fmt.Errorf("%w: mod name %q is not allowed", ErrInvalidMod, r.Name)
		}
		if r.Version != "" {
			if _, err := ParseVersion(r.Version); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidMod, err)
			}
		}
	}
	return nil
}

// planner builds a Planner from the current installed set and state.
func (m *Manager) planner() (*Planner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	installed, _, err := m.installedLocked()
	if err != nil {
		return nil, err
	}
	return &Planner{
		Source:   m.cfg.Source,
		Factorio: m.cfg.Factorio,
		Mods:     installed,
		Enabled:  m.enabledLocked(installed),
	}, nil
}

// Plan builds a Planner from the current state and runs it. Invalid
// requested names or versions: ErrInvalidMod.
func (m *Manager) Plan(ctx context.Context, req PlanRequest) (Plan, error) {
	if err := validateRequest(req); err != nil {
		return Plan{}, err
	}
	p, err := m.planner()
	if err != nil {
		return Plan{}, err
	}
	return p.Plan(ctx, req)
}

// Install plans req; a plan with conflicts fails with ErrPlanFailed after
// logging them. Otherwise it downloads every Install entry through Source,
// deletes other versions of each downloaded mod, enables the downloaded
// mods and every Enable entry, and logs progress to log. changed reports
// whether mods/ or the desired state was changed, also when err is not nil
// (a later download can fail after an earlier one was committed).
func (m *Manager) Install(ctx context.Context, log jobs.Logger, req PlanRequest, creds Credentials) (changed bool, err error) {
	if creds.Empty() {
		return false, ErrCredentials
	}
	plan, err := m.Plan(ctx, req)
	if err != nil {
		return false, err
	}
	return m.apply(ctx, log, plan, creds)
}

// apply logs plan and, when it has no conflicts, downloads and enables it.
// changed is as for Install.
func (m *Manager) apply(ctx context.Context, log jobs.Logger, plan Plan, creds Credentials) (changed bool, err error) {
	if !plan.OK() {
		for _, c := range plan.Conflicts {
			log.Logf("conflict: %s", c)
		}
		return false, fmt.Errorf("%w (%d)", ErrPlanFailed, len(plan.Conflicts))
	}
	if len(plan.Install) == 0 && len(plan.Enable) == 0 {
		log.Logf("nothing to do")
		return false, nil
	}
	for _, in := range plan.Install {
		committed, err := m.download(ctx, log, in.Release, creds)
		changed = changed || committed
		if err != nil {
			return changed, err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, in := range plan.Install {
		m.state.Set(in.Name, true)
	}
	for _, n := range plan.Enable {
		m.state.Set(n, true)
	}
	if err := SaveState(m.cfg.StatePath, m.state); err != nil {
		return true, err
	}
	if len(plan.Enable) > 0 {
		log.Logf("enabled: %s", strings.Join(plan.Enable, ", "))
	}
	return true, nil
}

// download fetches rel into mods/ and deletes the other versions of that
// mod. committed reports whether the release was written to mods/, also when
// err is not nil.
func (m *Manager) download(ctx context.Context, log jobs.Logger, rel Release, creds Credentials) (committed bool, err error) {
	if m.cfg.Source == nil {
		return false, errOffline
	}
	log.Logf("downloading %s %s", rel.Name, rel.Version)
	path, err := m.cfg.Source.Download(ctx, rel, creds, m.cfg.ModsDir)
	if err != nil {
		return false, fmt.Errorf("download %s %s: %w", rel.Name, rel.Version, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, zips, err := m.installedLocked()
	if err != nil {
		return true, err
	}
	if err := m.removeZipsLocked(zips, rel.Name, filepath.Base(path)); err != nil {
		return true, err
	}
	log.Logf("installed %s %s", rel.Name, rel.Version)
	return true, nil
}

// Update installs the newest compatible release of each named zip mod (empty
// names = every installed zip mod with an update) through the planner, so
// new dependencies come along; conflicts fail with ErrPlanFailed. Disabled
// mods are updated without planning and stay disabled. A built-in name:
// ErrBuiltin; a name that is not installed: ErrNotFound. changed is as for
// Install.
func (m *Manager) Update(ctx context.Context, log jobs.Logger, names []string, creds Credentials) (changed bool, err error) {
	if creds.Empty() {
		return false, ErrCredentials
	}
	if m.cfg.Source == nil {
		return false, errOffline
	}
	m.mu.Lock()
	installed, _, err := m.installedLocked()
	enabled := m.enabledLocked(installed)
	m.mu.Unlock()
	if err != nil {
		return false, err
	}

	all := len(names) == 0
	if all {
		for name, mod := range installed {
			if !mod.Builtin {
				names = append(names, name)
			}
		}
		sort.Strings(names)
	}
	for _, name := range names {
		mod, ok := installed[name]
		if !ok {
			return false, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		if mod.Builtin {
			return false, fmt.Errorf("%w: %s", ErrBuiltin, name)
		}
	}
	if len(names) == 0 {
		log.Logf("no mods installed")
		return false, nil
	}

	rels, err := m.cfg.Source.UpdateReleases(ctx, names)
	if err != nil {
		return false, err
	}
	newer := m.newerReleases(installed, rels)
	req := PlanRequest{IncludeRecommended: new(bool)} // updates do not add recommendations
	var disabled []Release
	for _, name := range names {
		v, ok := newer[name]
		if !ok {
			if !all {
				log.Logf("%s %s is up to date", name, installed[name].Version)
			}
			continue
		}
		if enabled[name] {
			req.Mods = append(req.Mods, Requested{Name: name, Version: v})
			continue
		}
		rel, _ := newestCompatible(rels[name], m.cfg.Factorio)
		disabled = append(disabled, rel)
	}
	if len(req.Mods) == 0 && len(disabled) == 0 {
		log.Logf("all mods are up to date")
		return false, nil
	}

	if len(req.Mods) > 0 {
		p, err := m.planner()
		if err != nil {
			return false, err
		}
		plan, err := p.Plan(ctx, req)
		if err != nil {
			return false, err
		}
		for i := range plan.Install {
			if plan.Install[i].Reason == "requested" {
				plan.Install[i].Reason = "update"
			}
		}
		changed, err = m.apply(ctx, log, plan, creds)
		if err != nil {
			return changed, err
		}
	}
	for _, rel := range disabled {
		committed, err := m.download(ctx, log, rel, creds)
		changed = changed || committed
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// WriteModList rescans mods/ and writes mods/mod-list.json from the state
// (RenderModList, fsutil.AtomicWrite). The supervisor calls it before every
// launch and world creation (factorio.Config.WriteModList).
func (m *Manager) WriteModList() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	zips, err := m.scanner.Scan()
	if err != nil {
		return err
	}
	list := RenderModList(append(append([]Mod{}, m.builtins...), zips...), m.state)
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(m.cfg.ModListPath, append(data, '\n'), 0o644)
}
