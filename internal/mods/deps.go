package mods

import (
	"fmt"
	"regexp"
	"strings"
)

// DepKind is the kind of a dependency, from its prefix.
type DepKind int

// Dependency kinds (DESIGN "Dependency grammar").
const (
	Required       DepKind = iota // no prefix
	RequiredNoLoad                // "~": required, no load-order effect
	Optional                      // "?"
	HiddenOptional                // "(?)"
	Recommended                   // "+": optional, enabled by default (2.1.7+)
	Incompatible                  // "!"
)

// IsRequired reports whether k must be installed and enabled.
func (k DepKind) IsRequired() bool { return k == Required || k == RequiredNoLoad }

// Prefix returns the dependency prefix for k ("", "~", "?", "(?)", "+", "!").
func (k DepKind) Prefix() string {
	switch k {
	case RequiredNoLoad:
		return "~"
	case Optional:
		return "?"
	case HiddenOptional:
		return "(?)"
	case Recommended:
		return "+"
	case Incompatible:
		return "!"
	}
	return ""
}

// Op is a version comparison operator; "" means no version constraint.
type Op string

// Version operators.
const (
	OpNone Op = ""
	OpLT   Op = "<"
	OpLE   Op = "<="
	OpEQ   Op = "="
	OpGE   Op = ">="
	OpGT   Op = ">"
)

// Dependency is one parsed entry of info.json "dependencies".
type Dependency struct {
	Kind    DepKind
	Name    string  // may contain spaces
	Op      Op      // OpNone when no constraint
	Version Version // meaningful only when Op != OpNone
	Raw     string  // the original string, for messages and the API
}

// depRe is the DESIGN dependency grammar.
var depRe = regexp.MustCompile(`^\s*(\(\?\)|[!?~+])?\s*(.+?)\s*(?:(<=|>=|<|>|=)\s*(\S+))?\s*$`)

// ParseDependency parses one dependency string with the DESIGN grammar
//
//	^\s*(\(\?\)|[!?~+])?\s*(.+?)\s*(?:(<=|>=|<|>|=)\s*(\S+))?\s*$
//
// An empty name or an unparsable version is an error.
func ParseDependency(s string) (Dependency, error) {
	m := depRe.FindStringSubmatch(s)
	if m == nil || strings.TrimSpace(m[2]) == "" {
		return Dependency{}, fmt.Errorf("invalid dependency %q", s)
	}
	d := Dependency{Name: m[2], Op: Op(m[3]), Raw: s}
	switch m[1] {
	case "~":
		d.Kind = RequiredNoLoad
	case "?":
		d.Kind = Optional
	case "(?)":
		d.Kind = HiddenOptional
	case "+":
		d.Kind = Recommended
	case "!":
		d.Kind = Incompatible
	}
	if d.Op != OpNone {
		v, err := ParseVersion(m[4])
		if err != nil {
			return Dependency{}, fmt.Errorf("invalid dependency %q: %w", s, err)
		}
		d.Version = v
	}
	return d, nil
}

// ParseDependencies parses a list, skipping entries that fail to parse; the
// failures are returned as errors naming the raw string.
func ParseDependencies(list []string) ([]Dependency, []error) {
	deps := make([]Dependency, 0, len(list))
	var errs []error
	for _, s := range list {
		d, err := ParseDependency(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		deps = append(deps, d)
	}
	return deps, errs
}

// Satisfied reports whether version v meets d's constraint (always true
// when d.Op is OpNone).
func (d Dependency) Satisfied(v Version) bool {
	c := v.Compare(d.Version)
	switch d.Op {
	case OpLT:
		return c < 0
	case OpLE:
		return c <= 0
	case OpEQ:
		return c == 0
	case OpGE:
		return c >= 0
	case OpGT:
		return c > 0
	}
	return true
}

// Constraint formats the constraint part, e.g. ">= 2.1.0", or "".
func (d Dependency) Constraint() string {
	if d.Op == OpNone {
		return ""
	}
	return string(d.Op) + " " + d.Version.String()
}

// describe formats the dependency name with its constraint, e.g.
// "base >= 2.1.0", for messages.
func (d Dependency) describe() string {
	if d.Op == OpNone {
		return d.Name
	}
	return d.Name + " " + d.Constraint()
}
