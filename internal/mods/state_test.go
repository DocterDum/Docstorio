package mods

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadStateFresh(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "mod-state.json")
	s, err := LoadState(statePath, filepath.Join(dir, "mod-list.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Mods == nil || len(s.Mods) != 0 || !s.Enabled("anything") {
		t.Errorf("state = %+v", s)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state not saved: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil || back["mods"] == nil {
		t.Errorf("saved %s (%v)", data, err)
	}
}

func TestLoadStateSeedsFromModList(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "mod-state.json")
	modList := writeFile(t, dir, "mod-list.json", []byte(`{"mods":[
		{"name":"base","enabled":false},
		{"name":"space-age","enabled":false},
		{"name":"quality","enabled":false},
		{"name":"Some Mod","enabled":true,"version":"1.0.0"}
	]}`))
	s, err := LoadState(statePath, modList)
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled("space-age") || s.Enabled("quality") || !s.Enabled("Some Mod") || !s.Enabled("recycler") || !s.Enabled("base") {
		t.Errorf("state = %+v", s)
	}
	if _, ok := s.Mods["base"]; ok {
		t.Error("base was seeded")
	}

	// The seeded state is saved; a later mod-list.json does not matter.
	writeFile(t, dir, "mod-list.json", []byte(`{"mods":[{"name":"space-age","enabled":true}]}`))
	s2, err := LoadState(statePath, modList)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s, s2) {
		t.Errorf("reload = %+v, want %+v", s2, s)
	}
}

func TestLoadStateErrors(t *testing.T) {
	dir := t.TempDir()
	bad := writeFile(t, dir, "bad.json", []byte("{"))
	if _, err := LoadState(bad, filepath.Join(dir, "none")); err == nil {
		t.Error("corrupt state: no error")
	}
	if _, err := LoadState(filepath.Join(dir, "state.json"), bad); err == nil {
		t.Error("corrupt mod-list.json: no error")
	}
	null := writeFile(t, dir, "null.json", []byte(`{"mods":null}`))
	if s, err := LoadState(null, ""); err != nil || s.Mods == nil {
		t.Errorf("null mods: %+v %v", s, err)
	}
}

func TestSaveStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	var s State
	s.Set("a", false)
	s.Set("b", true)
	if err := SaveState(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path, "")
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Errorf("got %+v %v, want %+v", got, err, s)
	}
}

func TestRenderModList(t *testing.T) {
	var installed []Mod
	for _, m := range builtinMods() {
		installed = append(installed, m)
	}
	installed = append(installed,
		zipMod("zeta", "1.0.0", "2.1"),
		zipMod("Alpha", "1.0.0", "2.1"),
		zipMod("Alpha", "2.0.0", "2.1"),
	)
	var s State
	s.Set("space-age", false)
	s.Set("quality", false)
	s.Set("recycler", false)
	s.Set("elevated-rails", false)
	s.Set("zeta", false)
	s.Set("base", false) // ignored: base is always enabled
	s.Set("gone", true)  // no zip: not listed

	got := RenderModList(installed, s)
	want := ModList{Mods: []ModListEntry{
		{"base", true},
		{"Alpha", true},
		{"elevated-rails", false},
		{"quality", false},
		{"recycler", false},
		{"space-age", false},
		{"zeta", false},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if empty := RenderModList(nil, State{}); empty.Mods == nil {
		t.Error("empty list is nil")
	}
}
