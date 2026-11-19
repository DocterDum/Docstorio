package mods

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"1.2.3", Version{1, 2, 3}, true},
		{"2.1", Version{2, 1, 0}, true},
		{"2", Version{2, 0, 0}, true},
		{"0.14.0", Version{0, 14, 0}, true},
		{"2.00", Version{2, 0, 0}, true},
		{"01.01", Version{1, 1, 0}, true},
		{" 1.1.100 ", Version{1, 1, 100}, true},
		{"65535.65535.65535", Version{65535, 65535, 65535}, true},
		{"00000.0.1", Version{0, 0, 1}, true},
		{"", Version{}, false},
		{" ", Version{}, false},
		{"1.2.3.4", Version{}, false},
		{"1..2", Version{}, false},
		{"1.2.", Version{}, false},
		{".1", Version{}, false},
		{"-1.0.0", Version{}, false},
		{"+1.0.0", Version{}, false},
		{"1.0.0-beta", Version{}, false},
		{"a.b.c", Version{}, false},
		{"65536.0.0", Version{}, false},
		{"123456.0.0", Version{}, false},
		{"1. 2.3", Version{}, false},
	}
	for _, tt := range tests {
		got, err := ParseVersion(tt.in)
		if (err == nil) != tt.ok {
			t.Errorf("ParseVersion(%q) err = %v, want ok=%v", tt.in, err, tt.ok)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseVersion(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"2.1", "2.1.0", 0},
		{"1.10.0", "1.9.0", 1},
		{"1.2.10", "1.2.9", 1},
		{"0.9.99", "1.0.0", -1},
		{"2.0.0", "1.99.99", 1},
		{"1.0.1", "1.0", 1},
		{"01.01", "1.1.0", 0},
	}
	for _, tt := range tests {
		if got := MustParseVersion(tt.a).Compare(MustParseVersion(tt.b)); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestVersionFormat(t *testing.T) {
	v := MustParseVersion("2.1")
	if v.String() != "2.1.0" || v.MajorMinor() != "2.1" {
		t.Errorf("got %q / %q", v.String(), v.MajorMinor())
	}
	if MustParseVersion("bad") != (Version{}) {
		t.Error("MustParseVersion(bad) is not the zero Version")
	}
}

func TestCompatible(t *testing.T) {
	tests := []struct {
		fv   string
		want bool
	}{
		{"2.1", true},
		{"2.1.0", true},
		{"2.1.24", true},
		{"02.01", true},
		{"2.10", false},
		{"2.0", false},
		{"2.00", false},
		{"1.1", false},
		{"01.01", false},
		{"0.14.0", false},
		{"3.1", false},
		{"", false},
		{"two", false},
	}
	for _, tt := range tests {
		if got := Compatible(tt.fv, v21); got != tt.want {
			t.Errorf("Compatible(%q, 2.1.24) = %v, want %v", tt.fv, got, tt.want)
		}
	}
}
