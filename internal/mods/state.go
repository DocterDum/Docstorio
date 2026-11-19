package mods

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"docstorio/internal/fsutil"
)

// State is the desired enabled/disabled state (manager/mod-state.json):
// {"mods": {"<name>": {"enabled": bool}}}. Factorio's own mod-list.json is
// overwritten from it before every launch (DESIGN "Desired state").
type State struct {
	Mods map[string]ModState `json:"mods"`
}

// ModState is one entry of State.
type ModState struct {
	Enabled bool `json:"enabled"`
}

// Enabled reports the desired state of name. A mod without an entry is
// enabled. "base" is always enabled.
func (s State) Enabled(name string) bool {
	if name == Base {
		return true
	}
	if st, ok := s.Mods[name]; ok {
		return st.Enabled
	}
	return true
}

// Set records the desired state of name (allocating the map if needed).
func (s *State) Set(name string, enabled bool) {
	if s.Mods == nil {
		s.Mods = make(map[string]ModState)
	}
	s.Mods[name] = ModState{Enabled: enabled}
}

// LoadState reads statePath. When it does not exist, the state is seeded
// from modListPath (an existing Factorio mod-list.json: its enabled flags
// are copied) or, if that is missing too, left empty (everything enabled);
// the seeded state is then saved. An unreadable or invalid file is an
// error.
func LoadState(statePath, modListPath string) (State, error) {
	s := State{Mods: make(map[string]ModState)}
	data, err := os.ReadFile(statePath)
	if err == nil {
		if err := json.Unmarshal(data, &s); err != nil {
			return State{}, fmt.Errorf("%s: %w", statePath, err)
		}
		if s.Mods == nil {
			s.Mods = make(map[string]ModState)
		}
		return s, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return State{}, err
	}

	data, err = os.ReadFile(modListPath)
	switch {
	case err == nil:
		var list ModList
		if err := json.Unmarshal(data, &list); err != nil {
			return State{}, fmt.Errorf("%s: %w", modListPath, err)
		}
		for _, e := range list.Mods {
			if e.Name != "" && e.Name != Base {
				s.Set(e.Name, e.Enabled)
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		return State{}, err
	}
	if err := SaveState(statePath, s); err != nil {
		return State{}, err
	}
	return s, nil
}

// SaveState writes s to statePath (fsutil.AtomicWrite, 0644).
func SaveState(statePath string, s State) error {
	if s.Mods == nil {
		s.Mods = map[string]ModState{}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(statePath, append(data, '\n'), 0o644)
}

// ModList is Factorio's mods/mod-list.json.
type ModList struct {
	Mods []ModListEntry `json:"mods"`
}

// ModListEntry is one mod-list.json entry.
type ModListEntry struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// RenderModList returns the complete mod-list.json content: one entry for
// every built-in and every installed mod name (deduplicated, "base" first,
// then sorted by name), enabled per s. installed is the built-ins plus the
// zips.
func RenderModList(installed []Mod, s State) ModList {
	seen := make(map[string]bool, len(installed))
	names := []string{}
	for _, m := range installed {
		if !seen[m.Name] {
			seen[m.Name] = true
			names = append(names, m.Name)
		}
	}
	sort.Slice(names, func(i, j int) bool { return lessName(names[i], names[j]) })
	list := ModList{Mods: make([]ModListEntry, 0, len(names))}
	for _, n := range names {
		list.Mods = append(list.Mods, ModListEntry{Name: n, Enabled: s.Enabled(n)})
	}
	return list
}
