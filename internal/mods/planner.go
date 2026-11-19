package mods

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// errOffline is returned when a plan needs the portal but the Manager has
// no ReleaseSource.
var errOffline = errors.New("mods: no mod portal configured")

// Requested is one requested mod. Version "" means "newest compatible
// release satisfying all constraints"; otherwise exactly that release.
type Requested struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// PlanRequest is the body of POST /api/portal/plan and /api/portal/install.
type PlanRequest struct {
	Mods            []Requested `json:"mods"`
	IncludeOptional bool        `json:"includeOptional"`
	// IncludeRecommended defaults to true when absent (nil).
	IncludeRecommended *bool `json:"includeRecommended"`
}

// Recommended returns IncludeRecommended with its default applied.
func (r PlanRequest) Recommended() bool {
	return r.IncludeRecommended == nil || *r.IncludeRecommended
}

// PlanInstall is one release the plan downloads.
type PlanInstall struct {
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Reason  string  `json:"reason"` // e.g. "requested", "required by X", "recommended by X", "update"
	Release Release `json:"-"`      // what Manager.Install / Update download
}

// Plan is the planner output, exactly the DESIGN API shape. All slices are
// non-nil (encode as []). A plan with Conflicts must not be applied.
type Plan struct {
	Install          []PlanInstall `json:"install"`
	Enable           []string      `json:"enable"`
	AlreadySatisfied []string      `json:"alreadySatisfied"`
	Conflicts        []string      `json:"conflicts"`
}

// OK reports whether the plan can be applied.
func (p Plan) OK() bool { return len(p.Conflicts) == 0 }

// Planner computes plans (DESIGN "Planner"). It is a plain value built per
// request from a snapshot of the installed set, so it needs no locking and
// can be unit-tested with a fake ReleaseSource.
type Planner struct {
	Source   ReleaseSource
	Factorio Version         // running Factorio version
	Mods     map[string]Mod  // installed mods by name (newest zip per name, plus built-ins)
	Enabled  map[string]bool // desired enabled state by name (every key of Mods present)
}

// constraint is a version constraint on a mod collected from a dependency
// of another mod (or the request).
type constraint struct {
	dep  Dependency
	from string
}

// planRun is the working state of one Plan call.
type planRun struct {
	p           *Planner
	ctx         context.Context
	req         PlanRequest
	decided     map[string]bool
	pins        map[string]string // requested version of each decided pinned name
	installed   map[string]Mod    // resulting installed set
	enabled     map[string]bool   // resulting enabled set
	constraints map[string][]constraint
	plan        Plan
	conflicts   []string
}

// Plan resolves req recursively for each requested mod (the ones with a
// pinned version are decided first, before any dependency) and each required
// (and, when chosen, optional / recommended) dependency: a built-in must be
// enabled (Enable when disabled); an installed mod that is compatible and
// satisfies the collected constraints is enabled if disabled (Enable) or
// reported in AlreadySatisfied; otherwise the newest compatible release
// satisfying all collected constraints is chosen (Install). Hidden optional
// dependencies are never installed. Then the resulting enabled set is
// checked with CheckSet and its findings go to Conflicts, as does "no
// compatible release". No backtracking. The returned error is for a
// cancelled ctx or a failed portal request (passed through unchanged, so the
// API can answer portal_error).
func (p *Planner) Plan(ctx context.Context, req PlanRequest) (Plan, error) {
	r := &planRun{
		p:           p,
		ctx:         ctx,
		req:         req,
		decided:     map[string]bool{},
		pins:        map[string]string{},
		installed:   make(map[string]Mod, len(p.Mods)),
		enabled:     make(map[string]bool, len(p.Enabled)),
		constraints: map[string][]constraint{},
		plan:        Plan{Install: []PlanInstall{}, Enable: []string{}, AlreadySatisfied: []string{}, Conflicts: []string{}},
	}
	for n, m := range p.Mods {
		r.installed[n] = m
	}
	for n, e := range p.Enabled {
		r.enabled[n] = e
	}
	// Pinned requests are decided before any dependency is walked, so an
	// unpinned visit from a mod that depends on one of them cannot decide it
	// first and drop the pin.
	var walkPinned []Mod
	for _, m := range req.Mods {
		if m.Version == "" {
			continue
		}
		mod, ok, err := r.decide(m.Name, m.Version, "requested")
		if err != nil {
			return Plan{}, err
		}
		if ok {
			walkPinned = append(walkPinned, mod)
		}
	}
	for _, mod := range walkPinned {
		if err := r.walk(mod); err != nil {
			return Plan{}, err
		}
	}
	for _, m := range req.Mods {
		if m.Version != "" {
			continue
		}
		if err := r.visit(m.Name, "", "requested"); err != nil {
			return Plan{}, err
		}
	}

	sort.Strings(r.plan.Enable)
	sort.Strings(r.plan.AlreadySatisfied)
	seen := map[string]bool{}
	for _, c := range append(r.conflicts, CheckSet(r.installed, r.enabled)...) {
		if !seen[c] {
			seen[c] = true
			r.plan.Conflicts = append(r.plan.Conflicts, c)
		}
	}
	return r.plan, nil
}

// visit decides how name ends up installed and enabled, then walks its
// dependencies.
func (r *planRun) visit(name, pinned, reason string) error {
	m, ok, err := r.decide(name, pinned, reason)
	if err != nil || !ok {
		return err
	}
	return r.walk(m)
}

// decide decides how name ends up installed and enabled and returns the mod
// whose dependencies are to be walked (ok false: nothing to walk). A name is
// decided once; later constraints on it are checked by CheckSet at the end,
// and a second, different pinned version is a conflict.
func (r *planRun) decide(name, pinned, reason string) (Mod, bool, error) {
	if err := r.ctx.Err(); err != nil {
		return Mod{}, false, err
	}
	if name == Core {
		return Mod{}, false, nil
	}
	if r.decided[name] {
		if first := r.pins[name]; pinned != "" && first != "" && MustParseVersion(first) != MustParseVersion(pinned) {
			r.conflicts = append(r.conflicts, fmt.Sprintf("%s: requested as both %s and %s", name, first, pinned))
		}
		return Mod{}, false, nil
	}
	r.decided[name] = true
	if pinned != "" {
		r.pins[name] = pinned
	}

	m, ok := r.p.Mods[name]
	if ok && (m.Builtin || r.usable(m, pinned)) {
		if r.enabled[name] {
			r.plan.AlreadySatisfied = append(r.plan.AlreadySatisfied, name)
		} else {
			r.enabled[name] = true
			r.plan.Enable = append(r.plan.Enable, name)
		}
		return m, true, nil
	}

	rel, found, err := r.pickRelease(name, pinned, reason)
	if err != nil || !found {
		return Mod{}, false, err
	}
	r.plan.Install = append(r.plan.Install, PlanInstall{Name: name, Version: rel.Version, Reason: reason, Release: rel})
	planned := Mod{Info: Info{
		Name:            name,
		Version:         rel.Version,
		Title:           m.Title,
		FactorioVersion: rel.FactorioVersion,
		Dependencies:    rel.Dependencies,
	}}
	r.installed[name] = planned
	r.enabled[name] = true
	return planned, true, nil
}

// walk records the constraints of m's dependencies and visits the ones the
// request includes.
func (r *planRun) walk(m Mod) error {
	deps, _ := ParseDependencies(m.Dependencies)
	for _, d := range deps {
		if d.Kind == Incompatible {
			continue // checked by CheckSet
		}
		r.constraints[d.Name] = append(r.constraints[d.Name], constraint{dep: d, from: m.Name})
		var reason string
		switch {
		case d.Kind.IsRequired():
			reason = "required by " + m.Name
		case d.Kind == Recommended && r.req.Recommended():
			reason = "recommended by " + m.Name
		case d.Kind == Optional && r.req.IncludeOptional:
			reason = "optional for " + m.Name
		default:
			continue
		}
		if err := r.visit(d.Name, "", reason); err != nil {
			return err
		}
	}
	return nil
}

// usable reports whether the installed mod m can be kept: it is compatible
// with the running Factorio, matches the pinned version (if any) and
// satisfies every constraint collected so far.
func (r *planRun) usable(m Mod, pinned string) bool {
	v, err := ParseVersion(m.Version)
	if err != nil || !Compatible(m.FactorioVersion, r.p.Factorio) {
		return false
	}
	if pinned != "" && v != MustParseVersion(pinned) {
		return false
	}
	return r.satisfiesAll(m.Name, v)
}

// satisfiesAll reports whether version v of name meets every collected
// constraint.
func (r *planRun) satisfiesAll(name string, v Version) bool {
	for _, c := range r.constraints[name] {
		if !c.dep.Satisfied(v) {
			return false
		}
	}
	return true
}

// pickRelease returns the newest compatible release of name satisfying all
// collected constraints (or exactly the pinned version). When there is none,
// a conflict is recorded and found is false.
func (r *planRun) pickRelease(name, pinned, reason string) (rel Release, found bool, err error) {
	if r.p.Source == nil {
		return Release{}, false, errOffline
	}
	releases, err := r.p.Source.Releases(r.ctx, name)
	if errors.Is(err, ErrNotFound) {
		r.conflicts = append(r.conflicts, fmt.Sprintf("%s: not found on the mod portal (%s)", name, reason))
		return Release{}, false, nil
	}
	if err != nil {
		return Release{}, false, err
	}

	type candidate struct {
		rel Release
		v   Version
	}
	cands := make([]candidate, 0, len(releases))
	for _, rel := range releases {
		if v, err := ParseVersion(rel.Version); err == nil {
			cands = append(cands, candidate{rel, v})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].v.Compare(cands[j].v) > 0 })

	if pinned != "" {
		want := MustParseVersion(pinned)
		for _, c := range cands {
			if c.v != want {
				continue
			}
			if !Compatible(c.rel.FactorioVersion, r.p.Factorio) {
				r.conflicts = append(r.conflicts, fmt.Sprintf("%s %s: made for Factorio %s, not %s", name, pinned, c.rel.FactorioVersion, r.p.Factorio.MajorMinor()))
				return Release{}, false, nil
			}
			if !r.satisfiesAll(name, c.v) {
				r.conflicts = append(r.conflicts, fmt.Sprintf("%s %s does not satisfy %s", name, pinned, r.describeConstraints(name)))
				return Release{}, false, nil
			}
			return c.rel, true, nil
		}
		r.conflicts = append(r.conflicts, fmt.Sprintf("%s %s: no such release on the mod portal (%s)", name, pinned, reason))
		return Release{}, false, nil
	}

	for _, c := range cands {
		if Compatible(c.rel.FactorioVersion, r.p.Factorio) && r.satisfiesAll(name, c.v) {
			return c.rel, true, nil
		}
	}
	msg := fmt.Sprintf("%s: no release for Factorio %s", name, r.p.Factorio.MajorMinor())
	if cs := r.describeConstraints(name); cs != "" {
		msg += " satisfies " + cs
	} else {
		msg += " (" + reason + ")"
	}
	r.conflicts = append(r.conflicts, msg)
	return Release{}, false, nil
}

// describeConstraints formats the version constraints collected for name,
// e.g. ">= 2.0.0 (from A), < 3.0.0 (from B)".
func (r *planRun) describeConstraints(name string) string {
	var parts []string
	for _, c := range r.constraints[name] {
		if c.dep.Op != OpNone {
			parts = append(parts, c.dep.Constraint()+" (from "+c.from+")")
		}
	}
	return strings.Join(parts, ", ")
}

// CheckSet checks an enabled set: every enabled mod's required dependencies
// are installed, enabled and satisfy their constraints; optional and
// recommended constraints are satisfied when that dependency is installed
// and enabled; no enabled mod is "!"-incompatible with another enabled mod
// (either direction). Built-ins are part of installed; a dependency on
// "core" is always satisfied. The texts read like Factorio's ("X: missing
// required dependency Y >= 1.0.0", "X: required dependency Y is disabled",
// "X: dependency Y >= 2.0.0 is not satisfied (active: Y 1.0.0)", "X:
// incompatible with Y") and are sorted.
func CheckSet(installed map[string]Mod, enabled map[string]bool) []string {
	out := []string{}
	active := func(name string) (Mod, bool) {
		m, ok := installed[name]
		return m, ok && enabled[name]
	}
	for name, m := range installed {
		if !enabled[name] {
			continue
		}
		deps, _ := ParseDependencies(m.Dependencies)
		for _, d := range deps {
			if d.Name == Core || d.Name == name {
				continue
			}
			dm, on := active(d.Name)
			switch {
			case d.Kind == Incompatible:
				if on {
					out = append(out, fmt.Sprintf("%s: incompatible with %s", name, d.Name))
				}
			case d.Kind.IsRequired() && !on:
				if _, ok := installed[d.Name]; ok {
					out = append(out, fmt.Sprintf("%s: required dependency %s is disabled", name, d.Name))
				} else {
					out = append(out, fmt.Sprintf("%s: missing required dependency %s", name, d.describe()))
				}
			case on && !d.Satisfied(MustParseVersion(dm.Version)):
				out = append(out, fmt.Sprintf("%s: dependency %s is not satisfied (active: %s %s)", name, d.describe(), d.Name, dm.Version))
			}
		}
	}
	sort.Strings(out)
	return out
}
