package mods

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a dotted numeric version with up to three components. Missing
// components are 0 ("2.1" == "2.1.0"); leading zeros are accepted
// ("01.01" == "1.1.0") because old portal releases use them (FACTS).
type Version struct {
	Major, Minor, Patch int
}

// ParseVersion parses "A", "A.B" or "A.B.C" where each component is 1-5
// decimal digits (value <= 65535). Surrounding whitespace is trimmed.
// Anything else (empty, signs, more than three components, letters) is an
// error.
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) > 3 {
		return Version{}, fmt.Errorf("invalid version %q", s)
	}
	var nums [3]int
	for i, p := range parts {
		if len(p) < 1 || len(p) > 5 {
			return Version{}, fmt.Errorf("invalid version %q", s)
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return Version{}, fmt.Errorf("invalid version %q", s)
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil || n > 65535 {
			return Version{}, fmt.Errorf("invalid version %q", s)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}, nil
}

// MustParseVersion is ParseVersion for constants in tests and tables; it
// returns the zero Version on error instead of panicking.
func MustParseVersion(s string) Version {
	v, _ := ParseVersion(s)
	return v
}

// Compare returns -1, 0 or +1 comparing v with o component by component.
func (v Version) Compare(o Version) int {
	a := [3]int{v.Major, v.Minor, v.Patch}
	b := [3]int{o.Major, o.Minor, o.Patch}
	for i := range a {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// String formats v as "A.B.C".
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// MajorMinor formats v as "A.B" (the portal's version filter and the cache
// file name use it).
func (v Version) MajorMinor() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// Compatible reports whether a mod or release whose info.json
// factorio_version is factorioVersion can run on running: both parsed as
// numbers, major and minor must be equal. An unparsable factorioVersion is
// incompatible. This is the only implementation of the rule (DESIGN
// "Compatibility"); every other package calls it.
func Compatible(factorioVersion string, running Version) bool {
	v, err := ParseVersion(factorioVersion)
	if err != nil {
		return false
	}
	return v.Major == running.Major && v.Minor == running.Minor
}
