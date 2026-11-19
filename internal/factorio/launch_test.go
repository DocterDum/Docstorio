package factorio

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"docstorio/internal/mods"
	"docstorio/internal/settings"
)

func TestArgs(t *testing.T) {
	p := settings.NewPaths("/factorio", "/opt/factorio")
	s := settings.Settings{GamePort: 34197, RCONPassword: "secret"}
	want := []string{
		"-c", "/factorio/config/config.ini",
		"--mod-directory", "/factorio/mods",
		"--start-server", "/factorio/saves/world.zip",
		"--server-settings", "/factorio/manager/server-settings.effective.json",
		"--port", "34197",
		"--rcon-bind", "127.0.0.1:27015", "--rcon-password", "secret",
		"--server-adminlist", "/factorio/config/server-adminlist.json",
		"--server-banlist", "/factorio/config/server-banlist.json",
	}
	if runtime.GOOS == "windows" {
		t.Skip("paths are POSIX")
	}
	if got := Args(p, s, "/factorio/saves/world.zip"); !slices.Equal(got, want) {
		t.Fatalf("Args =\n%q\nwant\n%q", got, want)
	}
	s.WhitelistEnabled = true
	s.GamePort = 40000
	got := Args(p, s, "/factorio/saves/world.zip")
	want[9] = "40000"
	want = append(want, "--use-server-whitelist", "--server-whitelist", "/factorio/config/server-whitelist.json")
	if !slices.Equal(got, want) {
		t.Fatalf("Args with whitelist =\n%q\nwant\n%q", got, want)
	}
}

// Real error output from FACTS (mod load failure, missing save).
var (
	modErrorOutput = []string{
		"   0.000 2026-11-19 10:00:00; Factorio 2.1.24 (build 84519, linux64, headless, space-age)",
		"   0.250 Loading mod core 0.0.0 (data.lua)",
		`   0.300 Error Util.cpp:81: Failed to load mod "depmod":`,
		"• depmod",
		"    • Missing required dependency missingdep >= 1.0.0",
		"    • Dependency modv21 >= 2.0.0 is not satisfied (active: modv21 1.0.0)",
		"    • Incompatible with modv21",
		"   0.310 Goodbye",
	}
	modErrorText = `Error Util.cpp:81: Failed to load mod "depmod":
• depmod
    • Missing required dependency missingdep >= 1.0.0
    • Dependency modv21 >= 2.0.0 is not satisfied (active: modv21 1.0.0)
    • Incompatible with modv21`

	missingSaveOutput = []string{
		`   0.400 Error ServerMultiplayerManager.cpp:91: MultiplayerManager failed: "File /factorio/saves/x.zip does not exist."`,
		"   0.401 Info ServerMultiplayerManager.cpp:808: updateTick(0) changing state from(CreatingGame) to(InitializationFailed)",
	}
	missingSaveText = `Error ServerMultiplayerManager.cpp:91: MultiplayerManager failed: "File /factorio/saves/x.zip does not exist."`
)

func TestExtractErrors(t *testing.T) {
	if got := ExtractErrors(modErrorOutput); got != modErrorText {
		t.Errorf("mod errors =\n%s\nwant\n%s", got, modErrorText)
	}
	if got := ExtractErrors(missingSaveOutput); got != missingSaveText {
		t.Errorf("missing save =\n%s\nwant\n%s", got, missingSaveText)
	}
	// A bullet without a preceding Error line is not an error.
	if got := ExtractErrors([]string{"   1.000 Info x", "• not an error"}); got != "" {
		t.Errorf("got %q", got)
	}
	// Two error blocks are both kept.
	both := append(append([]string{}, missingSaveOutput...), modErrorOutput...)
	if got := ExtractErrors(both); got != missingSaveText+"\n"+modErrorText {
		t.Errorf("two blocks =\n%s", got)
	}
	if got := ExtractErrors(nil); got != "" {
		t.Errorf("nil output gave %q", got)
	}
}

func TestExtractErrorsKeepsNewest(t *testing.T) {
	var out []string
	for i := 0; i < maxErrorLines+20; i++ {
		out = append(out, "   1.000 Error x.cpp:1: problem")
	}
	out = append(out, "   2.000 Error x.cpp:1: last")
	lines := strings.Split(ExtractErrors(out), "\n")
	if len(lines) != maxErrorLines || lines[len(lines)-1] != "Error x.cpp:1: last" {
		t.Fatalf("%d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func TestWriteEffectiveSettings(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "server-settings.json")
	dst := filepath.Join(dir, "effective.json")
	writeFile(t, src, `{
  "name": "My <server>",
  "_comment_credentials": "Your factorio.com login credentials.",
  "username": "old",
  "password": "",
  "token": "",
  "visibility": {"public": true, "lan": true}
}`)

	if err := WriteEffectiveSettings(src, dst, "alice", "tok123"); err != nil {
		t.Fatal(err)
	}
	want := `{
  "name": "My <server>",
  "_comment_credentials": "Your factorio.com login credentials.",
  "username": "alice",
  "password": "",
  "token": "tok123",
  "visibility": {
    "public": true,
    "lan": true
  }
}
`
	if got := readFile(t, dst); got != want {
		t.Fatalf("effective settings =\n%s\nwant\n%s", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, want 0600", info.Mode().Perm())
		}
	}

	// Without a token the user's values stay.
	if err := WriteEffectiveSettings(src, dst, "alice", ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dst); !strings.Contains(got, `"username": "old"`) || strings.Contains(got, "alice") {
		t.Fatalf("credentials applied without a token:\n%s", got)
	}

	writeFile(t, src, `[1]`)
	if err := WriteEffectiveSettings(src, dst, "", ""); err == nil {
		t.Error("a non-object file was accepted")
	}
	if err := WriteEffectiveSettings(filepath.Join(dir, "missing.json"), dst, "", ""); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestDetectVersion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "data", "base", "info.json"), `{"name": "base", "version": "2.1.24", "title": "Base Mod"}`)
	v, err := DetectVersion(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v != (mods.Version{Major: 2, Minor: 1, Patch: 24}) {
		t.Fatalf("version %v", v)
	}
	if _, err := DetectVersion(t.TempDir()); err == nil {
		t.Fatal("missing info.json accepted")
	}
}

func TestParsePlayersOnline(t *testing.T) {
	tests := []struct {
		reply string
		want  []string
	}{
		{"Online players (0):\n", []string{}},
		{"Online players (2):\n  alice (online)\n  bob (online)\n", []string{"alice", "bob"}},
		{"Online players (1):\r\n\tcarol\r\n", []string{"carol"}},
		{"online players (1):\n  Some Name (online)", []string{"Some Name"}},
		{"Unknown command \"players\". Type /h for help.", []string{}},
		{"", []string{}},
	}
	for _, tt := range tests {
		got := ParsePlayersOnline(tt.reply)
		if got == nil || !slices.Equal(got, tt.want) {
			t.Errorf("ParsePlayersOnline(%q) = %q, want %q", tt.reply, got, tt.want)
		}
	}
}

// writeFile writes content to path, creating parent directories.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readFile returns the content of path.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
