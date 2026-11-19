package mods

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// world is an installed set for planner tests: the 2.1.24 built-ins plus
// extra zip mods, all enabled unless listed in disabled.
func world(extra []Mod, disabled ...string) (map[string]Mod, map[string]bool) {
	mods := builtinMods()
	for _, m := range extra {
		mods[m.Name] = m
	}
	enabled := map[string]bool{}
	for n := range mods {
		enabled[n] = true
	}
	for _, n := range disabled {
		enabled[n] = false
	}
	return mods, enabled
}

type installWant struct{ name, version, reason string }

func installs(p Plan) []installWant {
	out := []installWant{}
	for _, in := range p.Install {
		out = append(out, installWant{in.Name, in.Version, in.Reason})
	}
	return out
}

func boolPtr(b bool) *bool { return &b }

func TestPlanScenarios(t *testing.T) {
	expansion := []string{"elevated-rails", "quality", "recycler", "space-age"}
	tests := []struct {
		name      string
		installed []Mod
		disabled  []string
		releases  []Release
		req       PlanRequest
		install   []installWant
		enable    []string
		satisfied []string
		conflicts []string
	}{
		{
			name: "simple with required dependency",
			releases: []Release{
				rel("A", "1.0.0", "2.1", "base >= 2.1", "B >= 1.0"),
				rel("B", "0.9.0", "2.1"),
				rel("B", "1.1.0", "2.1"),
				rel("B", "2.0.0", "2.0"), // newer but for Factorio 2.0
			},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}, {"B", "1.1.0", "required by A"}},
			satisfied: []string{"base"},
		},
		{
			name: "diamond picks one release satisfying both",
			releases: []Release{
				rel("A", "1.0.0", "2.1", "B", "~ C"),
				rel("B", "1.0.0", "2.1", "D >= 1.0.0"),
				rel("C", "1.0.0", "2.1", "D >= 1.1.0"),
				rel("D", "1.0.0", "2.1"),
				rel("D", "1.1.0", "2.1"),
				rel("D", "1.2.0", "2.1"),
			},
			req: PlanRequest{Mods: []Requested{{Name: "A"}}},
			install: []installWant{
				{"A", "1.0.0", "requested"},
				{"B", "1.0.0", "required by A"},
				{"D", "1.2.0", "required by B"},
				{"C", "1.0.0", "required by A"},
			},
		},
		{
			name: "diamond with upper bound collected first",
			releases: []Release{
				rel("A", "1.0.0", "2.1", "B", "C"),
				rel("B", "1.0.0", "2.1", "D < 1.2.0"),
				rel("C", "1.0.0", "2.1", "D >= 1.1.0"),
				rel("D", "1.0.0", "2.1"),
				rel("D", "1.1.0", "2.1"),
				rel("D", "1.2.0", "2.1"),
			},
			req: PlanRequest{Mods: []Requested{{Name: "A"}}},
			install: []installWant{
				{"A", "1.0.0", "requested"},
				{"B", "1.0.0", "required by A"},
				{"D", "1.1.0", "required by B"},
				{"C", "1.0.0", "required by A"},
			},
		},
		{
			name: "diamond without backtracking reports the conflict",
			releases: []Release{
				rel("A", "1.0.0", "2.1", "B", "C"),
				rel("B", "1.0.0", "2.1", "D = 1.0.0"),
				rel("C", "1.0.0", "2.1", "D >= 1.1.0"),
				rel("D", "1.0.0", "2.1"),
				rel("D", "1.1.0", "2.1"),
			},
			req: PlanRequest{Mods: []Requested{{Name: "A"}}},
			install: []installWant{
				{"A", "1.0.0", "requested"},
				{"B", "1.0.0", "required by A"},
				{"D", "1.0.0", "required by B"},
				{"C", "1.0.0", "required by A"},
			},
			conflicts: []string{"C: dependency D >= 1.1.0 is not satisfied (active: D 1.0.0)"},
		},
		{
			name: "unsatisfiable constraint",
			releases: []Release{
				rel("A", "1.0.0", "2.1", "D >= 3.0.0"),
				rel("D", "2.0.0", "2.1"),
				rel("D", "3.0.0", "2.0"),
			},
			req:     PlanRequest{Mods: []Requested{{Name: "A"}}},
			install: []installWant{{"A", "1.0.0", "requested"}},
			conflicts: []string{
				"D: no release for Factorio 2.1 satisfies >= 3.0.0 (from A)",
				"A: missing required dependency D >= 3.0.0",
			},
		},
		{
			name:      "requested mod without a compatible release",
			releases:  []Release{rel("Old", "1.0.0", "2.0")},
			req:       PlanRequest{Mods: []Requested{{Name: "Old"}}},
			install:   []installWant{},
			conflicts: []string{"Old: no release for Factorio 2.1 (requested)"},
		},
		{
			name:      "dependency not on the portal",
			releases:  []Release{rel("A", "1.0.0", "2.1", "Ghost")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			conflicts: []string{"Ghost: not found on the mod portal (required by A)", "A: missing required dependency Ghost"},
		},
		{
			name:      "new mod incompatible with an enabled mod",
			releases:  []Release{rel("A", "1.0.0", "2.1", "! space-age")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			conflicts: []string{"A: incompatible with space-age"},
		},
		{
			name:      "enabled mod incompatible with the new mod",
			installed: []Mod{zipMod("X", "1.0.0", "2.1", "! A")},
			releases:  []Release{rel("A", "1.0.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			conflicts: []string{"X: incompatible with A"},
		},
		{
			name:      "incompatible with a disabled mod is fine",
			installed: []Mod{zipMod("X", "1.0.0", "2.1", "! A")},
			disabled:  append([]string{"X"}, expansion...),
			releases:  []Release{rel("A", "1.0.0", "2.1", "! space-age")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
		},
		{
			name:      "optional constraint checked when present",
			installed: []Mod{zipMod("B", "1.0.0", "2.1")},
			releases:  []Release{rel("A", "1.0.0", "2.1", "? B >= 2.0.0"), rel("B", "2.0.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			conflicts: []string{"A: dependency B >= 2.0.0 is not satisfied (active: B 1.0.0)"},
		},
		{
			name:      "optional constraint satisfied by including optional",
			installed: []Mod{zipMod("B", "1.0.0", "2.1")},
			releases:  []Release{rel("A", "1.0.0", "2.1", "? B >= 2.0.0"), rel("B", "2.0.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}, IncludeOptional: true},
			install:   []installWant{{"A", "1.0.0", "requested"}, {"B", "2.0.0", "optional for A"}},
		},
		{
			name:     "optional constraint ignored when absent",
			releases: []Release{rel("A", "1.0.0", "2.1", "? B >= 2.0.0"), rel("B", "2.0.0", "2.1")},
			req:      PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:  []installWant{{"A", "1.0.0", "requested"}},
		},
		{
			name:      "optional constraint ignored when disabled",
			installed: []Mod{zipMod("B", "1.0.0", "2.1")},
			disabled:  []string{"B"},
			releases:  []Release{rel("A", "1.0.0", "2.1", "? B >= 2.0.0")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
		},
		{
			name:     "hidden optional never installed",
			releases: []Release{rel("A", "1.0.0", "2.1", "(?) H", "? O"), rel("H", "1.0.0", "2.1"), rel("O", "1.0.0", "2.1")},
			req:      PlanRequest{Mods: []Requested{{Name: "A"}}, IncludeOptional: true},
			install:  []installWant{{"A", "1.0.0", "requested"}, {"O", "1.0.0", "optional for A"}},
		},
		{
			name:     "recommended proposed by default",
			releases: []Release{rel("A", "1.0.0", "2.1", "+ R >= 1.0.0", "? O"), rel("R", "1.3.0", "2.1"), rel("O", "1.0.0", "2.1")},
			req:      PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:  []installWant{{"A", "1.0.0", "requested"}, {"R", "1.3.0", "recommended by A"}},
		},
		{
			name:     "recommended can be left out",
			releases: []Release{rel("A", "1.0.0", "2.1", "+ R >= 1.0.0"), rel("R", "1.3.0", "2.1")},
			req:      PlanRequest{Mods: []Requested{{Name: "A"}}, IncludeRecommended: boolPtr(false)},
			install:  []installWant{{"A", "1.0.0", "requested"}},
		},
		{
			name:      "disabled built-ins are enabled, with space-age's recommended quality",
			disabled:  expansion,
			releases:  []Release{rel("A", "1.0.0", "2.1", "base >= 2.1", "space-age >= 2.1.0")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			enable:    []string{"elevated-rails", "quality", "recycler", "space-age"},
			satisfied: []string{"base"},
		},
		{
			name:      "disabled built-ins without recommended",
			disabled:  expansion,
			releases:  []Release{rel("A", "1.0.0", "2.1", "space-age")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}, IncludeRecommended: boolPtr(false)},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			enable:    []string{"elevated-rails", "recycler", "space-age"},
			satisfied: []string{"base"},
		},
		{
			name:      "quality pulls recycler",
			disabled:  expansion,
			releases:  []Release{rel("A", "1.0.0", "2.1", "quality")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			enable:    []string{"quality", "recycler"},
			satisfied: []string{"base"},
		},
		{
			name:      "built-in too old",
			releases:  []Release{rel("A", "1.0.0", "2.1", "base >= 2.2.0")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			satisfied: []string{"base"},
			conflicts: []string{"A: dependency base >= 2.2.0 is not satisfied (active: base 2.1.24)"},
		},
		{
			name:      "installed disabled dependency is enabled",
			installed: []Mod{zipMod("B", "1.0.0", "2.1")},
			disabled:  []string{"B"},
			releases:  []Release{rel("A", "1.0.0", "2.1", "B")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			enable:    []string{"B"},
		},
		{
			name:      "installed mod already satisfies the request",
			installed: []Mod{zipMod("A", "1.0.0", "2.1", "base")},
			releases:  []Release{rel("A", "2.0.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{},
			satisfied: []string{"A", "base"},
		},
		{
			name:      "installed dependency too old is replaced",
			installed: []Mod{zipMod("B", "1.0.0", "2.1")},
			releases:  []Release{rel("A", "1.0.0", "2.1", "B >= 1.5"), rel("B", "1.5.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}, {"B", "1.5.0", "required by A"}},
		},
		{
			name:      "installed incompatible dependency is replaced",
			installed: []Mod{zipMod("B", "1.0.0", "2.0")},
			releases:  []Release{rel("A", "1.0.0", "2.1", "B"), rel("B", "1.1.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}, {"B", "1.1.0", "required by A"}},
		},
		{
			name:     "pinned version",
			releases: []Release{rel("A", "1.0.0", "2.1"), rel("A", "2.0.0", "2.1")},
			req:      PlanRequest{Mods: []Requested{{Name: "A", Version: "1.0.0"}}},
			install:  []installWant{{"A", "1.0.0", "requested"}},
		},
		{
			name:      "pinned version missing",
			releases:  []Release{rel("A", "1.0.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A", Version: "3.0.0"}}},
			install:   []installWant{},
			conflicts: []string{"A 3.0.0: no such release on the mod portal (requested)"},
		},
		{
			name:      "pinned version incompatible",
			releases:  []Release{rel("A", "1.0.0", "2.0"), rel("A", "2.0.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A", Version: "1.0.0"}}},
			install:   []installWant{},
			conflicts: []string{"A 1.0.0: made for Factorio 2.0, not 2.1"},
		},
		{
			name:      "pinned dependency requested after its dependent keeps the pin",
			installed: []Mod{zipMod("A", "1.0.0", "2.1", "B >= 1.0.0"), zipMod("B", "1.0.0", "2.1")},
			releases:  []Release{rel("A", "2.0.0", "2.1", "B >= 1.0.0"), rel("B", "2.0.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A", Version: "2.0.0"}, {Name: "B", Version: "2.0.0"}}},
			install:   []installWant{{"A", "2.0.0", "requested"}, {"B", "2.0.0", "requested"}},
		},
		{
			name:      "pinned older dependency is not replaced by the newest",
			installed: []Mod{zipMod("B", "1.0.0", "2.1")},
			releases:  []Release{rel("A", "1.0.0", "2.1", "B"), rel("B", "1.0.0", "2.1"), rel("B", "2.0.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}, {Name: "B", Version: "1.0.0"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			satisfied: []string{"B"},
		},
		{
			name:      "two different pins of one mod",
			releases:  []Release{rel("A", "1.0.0", "2.1"), rel("A", "2.0.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A", Version: "1.0.0"}, {Name: "A", Version: "2.0.0"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			conflicts: []string{"A: requested as both 1.0.0 and 2.0.0"},
		},
		{
			name:     "dependency cycle terminates",
			releases: []Release{rel("A", "1.0.0", "2.1", "B"), rel("B", "1.0.0", "2.1", "A >= 1.0.0")},
			req:      PlanRequest{Mods: []Requested{{Name: "A"}, {Name: "A"}}},
			install:  []installWant{{"A", "1.0.0", "requested"}, {"B", "1.0.0", "required by A"}},
		},
		{
			name:      "core dependency is always satisfied",
			releases:  []Release{rel("A", "1.0.0", "2.1", "core", "base")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			satisfied: []string{"base"},
		},
		{
			name:      "existing broken mod is reported",
			installed: []Mod{zipMod("Broken", "1.0.0", "2.1", "Missing >= 1.0")},
			releases:  []Release{rel("A", "1.0.0", "2.1")},
			req:       PlanRequest{Mods: []Requested{{Name: "A"}}},
			install:   []installWant{{"A", "1.0.0", "requested"}},
			conflicts: []string{"Broken: missing required dependency Missing >= 1.0.0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mods, enabled := world(tt.installed, tt.disabled...)
			p := &Planner{Source: newFakeSource(t, tt.releases...), Factorio: v21, Mods: mods, Enabled: enabled}
			plan, err := p.Plan(context.Background(), tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if got := installs(plan); !reflect.DeepEqual(got, orEmpty(tt.install)) {
				t.Errorf("install = %v, want %v", got, tt.install)
			}
			if !reflect.DeepEqual(plan.Enable, orEmptyS(tt.enable)) {
				t.Errorf("enable = %v, want %v", plan.Enable, tt.enable)
			}
			if tt.satisfied != nil && !reflect.DeepEqual(plan.AlreadySatisfied, tt.satisfied) {
				t.Errorf("alreadySatisfied = %v, want %v", plan.AlreadySatisfied, tt.satisfied)
			}
			if !reflect.DeepEqual(plan.Conflicts, orEmptyS(tt.conflicts)) {
				t.Errorf("conflicts = %q, want %q", plan.Conflicts, tt.conflicts)
			}
			if plan.OK() != (len(tt.conflicts) == 0) {
				t.Errorf("OK = %v", plan.OK())
			}
			for _, in := range plan.Install {
				if in.Release.Name != in.Name || in.Release.Version != in.Version {
					t.Errorf("install %s %s carries release %+v", in.Name, in.Version, in.Release)
				}
			}
		})
	}
}

func orEmpty(s []installWant) []installWant {
	if s == nil {
		return []installWant{}
	}
	return s
}

func orEmptyS(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestPlanEmptyRequest(t *testing.T) {
	mods, enabled := world(nil)
	plan, err := (&Planner{Factorio: v21, Mods: mods, Enabled: enabled}).Plan(context.Background(), PlanRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Install == nil || plan.Enable == nil || plan.AlreadySatisfied == nil || plan.Conflicts == nil || !plan.OK() {
		t.Errorf("plan = %+v", plan)
	}
}

func TestPlanErrors(t *testing.T) {
	mods, enabled := world(nil)
	boom := errors.New("portal down")
	src := newFakeSource(t)
	src.err = boom
	p := &Planner{Source: src, Factorio: v21, Mods: mods, Enabled: enabled}
	if _, err := p.Plan(context.Background(), PlanRequest{Mods: []Requested{{Name: "A"}}}); !errors.Is(err, boom) {
		t.Errorf("portal error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Source = newFakeSource(t, rel("A", "1.0.0", "2.1"))
	if _, err := p.Plan(ctx, PlanRequest{Mods: []Requested{{Name: "A"}}}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}

	p.Source = nil
	if _, err := p.Plan(context.Background(), PlanRequest{Mods: []Requested{{Name: "A"}}}); err == nil {
		t.Error("offline: no error")
	}
	// Offline still works when nothing needs the portal.
	if plan, err := p.Plan(context.Background(), PlanRequest{Mods: []Requested{{Name: "space-age"}}}); err != nil || !plan.OK() {
		t.Errorf("offline built-in: %+v %v", plan, err)
	}
}

func TestPlanRequestRecommendedDefault(t *testing.T) {
	if !(PlanRequest{}).Recommended() || !(PlanRequest{IncludeRecommended: boolPtr(true)}).Recommended() || (PlanRequest{IncludeRecommended: boolPtr(false)}).Recommended() {
		t.Error("Recommended default wrong")
	}
}

func TestCheckSet(t *testing.T) {
	tests := []struct {
		name     string
		extra    []Mod
		disabled []string
		want     []string
	}{
		{"fresh 2.1.24", nil, nil, []string{}},
		{"base game only", nil, []string{"elevated-rails", "quality", "recycler", "space-age"}, []string{}},
		{"quality without recycler", nil, []string{"elevated-rails", "recycler", "space-age"}, []string{"quality: required dependency recycler is disabled"}},
		{"space-age without quality is fine", nil, []string{"quality"}, []string{}},
		{"space-age without elevated-rails", nil, []string{"elevated-rails"}, []string{"space-age: required dependency elevated-rails is disabled"}},
		{
			"incompatible both directions",
			[]Mod{zipMod("A", "1.0.0", "2.1", "! B"), zipMod("B", "1.0.0", "2.1", "! A")},
			nil,
			[]string{"A: incompatible with B", "B: incompatible with A"},
		},
		{
			"constraints of every kind",
			[]Mod{
				zipMod("A", "1.0.0", "2.1", "B >= 2.0", "? C < 1.0", "+ D = 1.0.0", "(?) E > 5.0", "core", "A"),
				zipMod("B", "1.0.0", "2.1"),
				zipMod("C", "1.0.0", "2.1"),
				zipMod("D", "1.0.0", "2.1"),
				zipMod("E", "1.0.0", "2.1"),
			},
			nil,
			[]string{
				"A: dependency B >= 2.0.0 is not satisfied (active: B 1.0.0)",
				"A: dependency C < 1.0.0 is not satisfied (active: C 1.0.0)",
				"A: dependency E > 5.0.0 is not satisfied (active: E 1.0.0)",
			},
		},
		{"disabled mods are not checked", []Mod{zipMod("A", "1.0.0", "2.1", "Missing")}, []string{"A"}, []string{}},
	}
	for _, tt := range tests {
		mods, enabled := world(tt.extra, tt.disabled...)
		if got := CheckSet(mods, enabled); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}
