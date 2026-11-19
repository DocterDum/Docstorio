package factorio

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"docstorio/internal/settings"
)

// exampleServerSettings is a shortened server-settings.example.json in the
// shape of Factorio 2.1's.
const exampleServerSettings = `{
  "name": "Name of the game as it will appear in the game listing",
  "description": "Description of the game that will appear in the listing",
  "tags": ["game", "tags"],

  "_comment_max_players": "Maximum number of players allowed, admins can join even a full server. 0 means unlimited.",
  "max_players": 0,

  "_comment_visibility": ["public: Game will be published on the official Factorio matching server",
                          "lan: Game will be broadcast on LAN"],
  "visibility":
  {
    "public": true,
    "lan": true
  },

  "_comment_credentials": "Your factorio.com login credentials. Required for games with visibility public",
  "username": "",
  "password": "",

  "_comment_token": "Authentication token. May be used instead of 'password' above.",
  "token": "",

  "game_password": "",

  "_comment_allow_commands": "possible values are, true, false and admins-only",
  "allow_commands": "admins-only",

  "_comment_auto_pause": "Whether should the server be paused when no players are present.",
  "auto_pause": true,

  "admins": []
}
`

// newTestPaths returns paths in a temp dir with the directories created and
// Factorio's example files in place.
func newTestPaths(t *testing.T) settings.Paths {
	t.Helper()
	root := t.TempDir()
	p := settings.NewPaths(filepath.Join(root, "data"), filepath.Join(root, "opt"))
	for _, d := range []string{p.Saves, p.Mods, p.Config, p.Manager, p.FactorioData} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, p.Example(settings.ServerSettingsFile), exampleServerSettings)
	writeFile(t, p.Example(settings.MapGenSettingsFile), `{"width": 0}`)
	return p
}

func TestSeed(t *testing.T) {
	p := newTestPaths(t)
	writeFile(t, p.ConfigFile(settings.BanListFile), `["griefer"]`)
	if err := Seed(p); err != nil {
		t.Fatal(err)
	}
	ini := readFile(t, p.ConfigFile(settings.ConfigINIFile))
	if ini != "[path]\nread-data="+p.FactorioData+"\nwrite-data="+p.Data+"\n" {
		t.Errorf("config.ini =\n%s", ini)
	}
	for name, want := range map[string]string{
		settings.AdminListFile: "[]\n",
		settings.BanListFile:   `["griefer"]`, // existing files are kept
		settings.WhitelistFile: "[]\n",
	} {
		if got := readFile(t, p.ConfigFile(name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	data := []byte(readFile(t, p.ConfigFile(settings.ServerSettingsFile)))
	obj, err := parseObject(data)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, m := range obj {
		keys = append(keys, m.Key)
	}
	if keys[0] != "name" || keys[1] != "description" || keys[len(keys)-1] != "admins" {
		t.Errorf("key order changed: %q", keys)
	}
	var s struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Visibility  map[string]bool `json:"visibility"`
		MaxPlayers  int             `json:"max_players"`
		Comment     []string        `json:"_comment_visibility"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	if s.Name != "Docstorio" || s.Description != "" || s.Visibility["public"] || !s.Visibility["lan"] || len(s.Comment) != 2 {
		t.Errorf("seeded server settings = %+v", s)
	}

	// A second run changes nothing.
	writeFile(t, p.ConfigFile(settings.ServerSettingsFile), `{"name": "mine"}`)
	if err := Seed(p); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p.ConfigFile(settings.ServerSettingsFile)); got != `{"name": "mine"}` {
		t.Errorf("existing server settings overwritten: %s", got)
	}
}

func TestSeedWithoutExample(t *testing.T) {
	p := newTestPaths(t)
	os.Remove(p.Example(settings.ServerSettingsFile))
	if err := Seed(p); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"name\": \"Docstorio\",\n  \"description\": \"\",\n  \"visibility\": {\n    \"public\": false\n  }\n}\n"
	if got := readFile(t, p.ConfigFile(settings.ServerSettingsFile)); got != want {
		t.Errorf("server settings =\n%s", got)
	}
}

func TestConfigFilesListReadWrite(t *testing.T) {
	p := newTestPaths(t)
	c := NewConfigFiles(p)
	writeFile(t, p.ConfigFile(settings.AdminListFile), `["alice"]`)

	list := c.List()
	if len(list) != 6 || list[0].Name != settings.ServerSettingsFile || list[5].Name != settings.WhitelistFile {
		t.Fatalf("list = %+v", list)
	}
	for _, f := range list {
		if f.Exists != (f.Name == settings.AdminListFile) {
			t.Errorf("%s exists = %v", f.Name, f.Exists)
		}
	}

	tests := []struct {
		name, content string
		exists        bool
	}{
		{settings.AdminListFile, `["alice"]`, true},
		{settings.MapGenSettingsFile, `{"width": 0}`, false}, // the example
		{settings.MapSettingsFile, "[]", false},              // no example
		{settings.ServerSettingsFile, exampleServerSettings, false},
	}
	for _, tt := range tests {
		content, exists, err := c.Read(tt.name)
		if err != nil || content != tt.content || exists != tt.exists {
			t.Errorf("Read(%s) = %q, %v, %v", tt.name, content, exists, err)
		}
	}
	for _, bad := range []string{"config.ini", "../manager/settings.json", ""} {
		if _, _, err := c.Read(bad); !errors.Is(err, ErrUnknownConfigFile) {
			t.Errorf("Read(%q): %v", bad, err)
		}
		if err := c.Write(bad, "[]"); !errors.Is(err, ErrUnknownConfigFile) {
			t.Errorf("Write(%q): %v", bad, err)
		}
	}

	if err := c.Write(settings.BanListFile, "[\"x\"]\n"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p.ConfigFile(settings.BanListFile)); got != "[\"x\"]\n" {
		t.Fatalf("written %q", got)
	}
}

func TestConfigFilesInvalidJSON(t *testing.T) {
	c := NewConfigFiles(newTestPaths(t))
	tests := []struct {
		content   string
		line, col int
	}{
		{`{"a": x}`, 1, 7},
		{"{\n  \"a\": 1,\n  \"é\": tru\n}", 3, 11},
		{"[1, 2", 1, 5},
		{"", 1, 1},
		{"[] []", 1, 4},
	}
	for _, tt := range tests {
		err := c.Write(settings.MapSettingsFile, tt.content)
		var je *JSONError
		if !errors.As(err, &je) {
			t.Errorf("Write(%q) = %v, want *JSONError", tt.content, err)
			continue
		}
		if je.Line != tt.line || je.Column != tt.col {
			t.Errorf("Write(%q): line %d column %d, want %d %d (%s)", tt.content, je.Line, je.Column, tt.line, tt.col, je.Msg)
		}
	}
	if _, err := os.Stat(c.paths.ConfigFile(settings.MapSettingsFile)); err == nil {
		t.Error("invalid content was written")
	}
}

func TestServerSettingsFields(t *testing.T) {
	p := newTestPaths(t)
	c := NewConfigFiles(p)
	writeFile(t, p.ConfigFile(settings.ServerSettingsFile),
		`{"name": "My server", "visibility": {"public": false}, "max_players": 8, "game_password": "pw", "extra": 1}`)

	fields, err := c.ServerSettingsFields()
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		key, typ, value, help string
	}
	want := []row{
		{"name", "string", "My server", ""},
		{"description", "string", "Description of the game that will appear in the listing", ""},
		{"max_players", "int", "8", "Maximum number of players allowed, admins can join even a full server. 0 means unlimited."},
		{"visibility.public", "bool", "false", "Game will be published on the official Factorio matching server"},
		{"visibility.lan", "bool", "true", "Game will be broadcast on LAN"},
		{"game_password", "password", "pw", ""},
		{"allow_commands", "enum", "admins-only", "possible values are, true, false and admins-only"},
		{"auto_pause", "bool", "true", "Whether should the server be paused when no players are present."},
	}
	if len(fields) != len(want) {
		t.Fatalf("fields = %+v", fields)
	}
	for i, w := range want {
		f := fields[i]
		value, _ := json.Marshal(f.Value)
		got := row{f.Key, f.Type, strings.Trim(string(value), `"`), f.Help}
		if got != w {
			t.Errorf("field %d = %+v, want %+v", i, got, w)
		}
	}
	if opts := fields[6].Options; strings.Join(opts, ",") != "true,false,admins-only" {
		t.Errorf("allow_commands options %q", opts)
	}
}

func TestUpdateServerSettings(t *testing.T) {
	p := newTestPaths(t)
	c := NewConfigFiles(p)
	path := p.ConfigFile(settings.ServerSettingsFile)
	writeFile(t, path, `{"name": "old", "tags": ["a"], "visibility": {"public": true, "lan": true}, "token": "keep"}`)

	err := c.UpdateServerSettings(map[string]any{
		"name":              "New <name>",
		"max_players":       float64(12), // as decoded by encoding/json
		"visibility.public": false,
		"allow_commands":    "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "name": "New <name>",
  "tags": [
    "a"
  ],
  "visibility": {
    "public": false,
    "lan": true
  },
  "token": "keep",
  "allow_commands": "true",
  "max_players": 12
}
`
	if got := readFile(t, path); got != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}

	for _, bad := range []map[string]any{
		{"max_players": "12"},
		{"max_players": 1.5},
		{"auto_pause": "yes"},
		{"allow_commands": "sometimes"},
		{"name": 3.0},
		{"username": "u"},
		{"tags": []any{"x"}},
		{"unknown": true},
	} {
		if err := c.UpdateServerSettings(bad); !errors.Is(err, ErrBadValue) {
			t.Errorf("UpdateServerSettings(%v) = %v, want ErrBadValue", bad, err)
		}
	}
	if got := readFile(t, path); got != want {
		t.Fatalf("a rejected update changed the file:\n%s", got)
	}
}

func TestUpdateServerSettingsMissingFile(t *testing.T) {
	p := newTestPaths(t)
	c := NewConfigFiles(p)
	if err := c.UpdateServerSettings(map[string]any{"name": "x"}); err != nil {
		t.Fatal(err)
	}
	obj, err := readObject(p.ConfigFile(settings.ServerSettingsFile))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := obj.get("name"); string(v) != `"x"` {
		t.Errorf("name = %s", v)
	}
	if _, ok := obj.get("_comment_max_players"); !ok {
		t.Error("the example's keys were not kept")
	}
}
