package mods

import "testing"

func TestParseDependency(t *testing.T) {
	tests := []struct {
		in      string
		kind    DepKind
		name    string
		op      Op
		version string
	}{
		// Every dependency string seen on the portal (FACTS).
		{"base >= 2.1.22", Required, "base", OpGE, "2.1.22"},
		{"base>=1.1.100", Required, "base", OpGE, "1.1.100"},
		{"base >= 2.1", Required, "base", OpGE, "2.1.0"},
		{"Krastorio2", Required, "Krastorio2", OpNone, ""},
		{"Krastorio2Assets = 2.0.3", Required, "Krastorio2Assets", OpEQ, "2.0.3"},
		{"! space-age", Incompatible, "space-age", OpNone, ""},
		{"! Power Armor MK3", Incompatible, "Power Armor MK3", OpNone, ""},
		{"? elevated-rails", Optional, "elevated-rails", OpNone, ""},
		{"?alien-biomes-hr-terrain >= 0.3.1", Optional, "alien-biomes-hr-terrain", OpGE, "0.3.1"},
		{"(?) Flow Control >= 3.0.5", HiddenOptional, "Flow Control", OpGE, "3.0.5"},
		{"(?)IndustrialRevolution >= 0.3.1", HiddenOptional, "IndustrialRevolution", OpGE, "0.3.1"},
		{"(?) metal-and-stars  >= 0.1.10", HiddenOptional, "metal-and-stars", OpGE, "0.1.10"},
		{"~ Krastorio2 >= 1.2.7", RequiredNoLoad, "Krastorio2", OpGE, "1.2.7"},
		{"+ ChangeInserterDropLane >= 1.3.0", Recommended, "ChangeInserterDropLane", OpGE, "1.3.0"},
		// Built-ins (FACTS).
		{"+ quality >= 2.1.0", Recommended, "quality", OpGE, "2.1.0"},
		// All operators, with and without whitespace.
		{"a < 1.0.0", Required, "a", OpLT, "1.0.0"},
		{"a<1.0.0", Required, "a", OpLT, "1.0.0"},
		{"a <= 1.0", Required, "a", OpLE, "1.0.0"},
		{"a<=1.0", Required, "a", OpLE, "1.0.0"},
		{"a > 0.1.2", Required, "a", OpGT, "0.1.2"},
		{"a>0.1.2", Required, "a", OpGT, "0.1.2"},
		{"a=1.2.3", Required, "a", OpEQ, "1.2.3"},
		{"  ~a  ", RequiredNoLoad, "a", OpNone, ""},
		{"!a", Incompatible, "a", OpNone, ""},
		{"+a", Recommended, "a", OpNone, ""},
		{"\tMy Mod With Spaces >= 1.0.0\t", Required, "My Mod With Spaces", OpGE, "1.0.0"},
		{"? Some Mod", Optional, "Some Mod", OpNone, ""},
	}
	for _, tt := range tests {
		d, err := ParseDependency(tt.in)
		if err != nil {
			t.Errorf("ParseDependency(%q): %v", tt.in, err)
			continue
		}
		if d.Kind != tt.kind || d.Name != tt.name || d.Op != tt.op || d.Raw != tt.in {
			t.Errorf("ParseDependency(%q) = %+v, want kind %v name %q op %q", tt.in, d, tt.kind, tt.name, tt.op)
		}
		if tt.op != OpNone && d.Version != MustParseVersion(tt.version) {
			t.Errorf("ParseDependency(%q) version = %v, want %s", tt.in, d.Version, tt.version)
		}
	}
}

func TestParseDependencyErrors(t *testing.T) {
	for _, in := range []string{"", "   ", "a >= x.y", "a >= 1.2.3.4", "a = -1", "a >= 99999999"} {
		if d, err := ParseDependency(in); err == nil {
			t.Errorf("ParseDependency(%q) = %+v, want error", in, d)
		}
	}
}

func TestParseDependencies(t *testing.T) {
	deps, errs := ParseDependencies([]string{"base >= 2.1", "bad >= x", "? b"})
	if len(deps) != 2 || deps[0].Name != "base" || deps[1].Name != "b" {
		t.Errorf("deps = %+v", deps)
	}
	if len(errs) != 1 {
		t.Errorf("errs = %v", errs)
	}
	if deps, errs := ParseDependencies(nil); deps == nil || len(deps) != 0 || errs != nil {
		t.Errorf("nil list: %v %v", deps, errs)
	}
}

func TestDependencySatisfied(t *testing.T) {
	tests := []struct {
		dep, version string
		want         bool
	}{
		{"a", "0.0.1", true},
		{"a >= 1.0.0", "1.0.0", true},
		{"a >= 1.0.0", "0.9.9", false},
		{"a >= 2.1", "2.1.0", true},
		{"a > 1.0", "1.0.0", false},
		{"a > 1.0", "1.0.1", true},
		{"a < 2.0.0", "1.99.99", true},
		{"a < 2.0.0", "2.0.0", false},
		{"a <= 2.0", "2.0.0", true},
		{"a <= 2.0", "2.0.1", false},
		{"a = 2.0.3", "2.0.3", true},
		{"a = 2.0", "2.0.0", true},
		{"a = 2.0.3", "2.0.4", false},
		{"a >= 1.10.0", "1.9.0", false},
		{"a >= 1.9.0", "1.10.0", true},
	}
	for _, tt := range tests {
		d, err := ParseDependency(tt.dep)
		if err != nil {
			t.Fatal(err)
		}
		if got := d.Satisfied(MustParseVersion(tt.version)); got != tt.want {
			t.Errorf("%q satisfied by %s = %v, want %v", tt.dep, tt.version, got, tt.want)
		}
	}
}

func TestDepKindAndConstraint(t *testing.T) {
	for _, k := range []DepKind{Required, RequiredNoLoad, Optional, HiddenOptional, Recommended, Incompatible} {
		d, err := ParseDependency(k.Prefix() + "x >= 1.2")
		if err != nil || d.Kind != k {
			t.Errorf("prefix %q round trip: %+v %v", k.Prefix(), d, err)
		}
		if d.Constraint() != ">= 1.2.0" {
			t.Errorf("Constraint = %q", d.Constraint())
		}
		if want := k == Required || k == RequiredNoLoad; k.IsRequired() != want {
			t.Errorf("%v IsRequired = %v", k, k.IsRequired())
		}
	}
	if d, _ := ParseDependency("x"); d.Constraint() != "" {
		t.Errorf("no constraint: %q", d.Constraint())
	}
}
