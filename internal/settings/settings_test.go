package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	p := NewPaths(t.TempDir(), t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p Paths) map[string]any {
	t.Helper()
	data, err := os.ReadFile(p.SettingsFile())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLoadEnvDefaults(t *testing.T) {
	env := LoadEnv(func(string) string { return "" })
	want := Env{
		DataDir:        "/factorio",
		FactorioDir:    "/opt/factorio",
		HTTPAddr:       ":8080",
		PortalURL:      "https://mods.factorio.com",
		AssetsURL:      "https://assets-mod.factorio.com",
		ManagerVersion: "dev",
	}
	if env != want {
		t.Fatalf("LoadEnv = %+v, want %+v", env, want)
	}
}

func TestLoadEnvValues(t *testing.T) {
	vals := map[string]string{
		"DOCSTORIO_DATA":         "/data",
		"DOCSTORIO_FACTORIO_DIR": "/fac",
		"DOCSTORIO_HTTP_ADDR":    "127.0.0.1:9000",
		"DOCSTORIO_PORTAL_URL":   "http://portal.test/",
		"DOCSTORIO_ASSETS_URL":   "http://assets.test//",
		"FACTORIO_VERSION":       "2.1.24",
		"DOCSTORIO_VERSION":      "0.1.0",
	}
	env := LoadEnv(func(k string) string { return vals[k] })
	want := Env{
		DataDir:         "/data",
		FactorioDir:     "/fac",
		HTTPAddr:        "127.0.0.1:9000",
		PortalURL:       "http://portal.test",
		AssetsURL:       "http://assets.test",
		FactorioVersion: "2.1.24",
		ManagerVersion:  "0.1.0",
	}
	if env != want {
		t.Fatalf("LoadEnv = %+v, want %+v", env, want)
	}
}

func TestNewPathsAndEnsureDirs(t *testing.T) {
	data := t.TempDir()
	p := NewPaths(data, "/opt/factorio")
	if p.Cache != filepath.Join(data, "manager", "cache") {
		t.Fatalf("Cache = %q", p.Cache)
	}
	if got := p.Example("server-settings.json"); got != filepath.Join("/opt/factorio", "data", "server-settings.example.json") {
		t.Fatalf("Example = %q", got)
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("second EnsureDirs: %v", err)
	}
	for _, d := range []string{p.Saves, p.Mods, p.Config, p.Manager, p.Cache} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("%s: %v", d, err)
		}
	}
}

func TestOpenFresh(t *testing.T) {
	p := testPaths(t)
	st, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s := st.Get()
	if !s.Autostart || s.GamePort != 34197 || s.WhitelistEnabled || s.FactorioUsername != "" || s.FactorioToken != "" {
		t.Fatalf("defaults wrong: %+v", s)
	}
	if len(s.RCONPassword) < 20 {
		t.Fatalf("RCON password %q too short", s.RCONPassword)
	}
	m := readFile(t, p)
	if m["rconPassword"] != s.RCONPassword || m["gamePort"] != float64(34197) || m["autostart"] != true {
		t.Fatalf("file = %v", m)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(p.SettingsFile())
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0600 {
			t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}

	// Reopening keeps the same password.
	st2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Get() != s {
		t.Fatalf("reopen = %+v, want %+v", st2.Get(), s)
	}
}

func TestOpenFillsMissingKeys(t *testing.T) {
	p := testPaths(t)
	if err := os.WriteFile(p.SettingsFile(), []byte(`{"gamePort": 40000, "factorioUsername": "me"}`), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s := st.Get()
	if s.GamePort != 40000 || s.FactorioUsername != "me" || !s.Autostart || s.RCONPassword == "" {
		t.Fatalf("settings = %+v", s)
	}
	m := readFile(t, p)
	for _, k := range settingsKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("key %s not written back", k)
		}
	}
}

func TestOpenKeepsAutostartFalse(t *testing.T) {
	p := testPaths(t)
	if err := os.WriteFile(p.SettingsFile(), []byte(`{"autostart": false, "rconPassword": "pw"}`), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Get(); s.Autostart || s.RCONPassword != "pw" {
		t.Fatalf("settings = %+v", s)
	}
}

func TestOpenCorrupt(t *testing.T) {
	p := testPaths(t)
	if err := os.WriteFile(p.SettingsFile(), []byte(`{"gamePort": `), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("expected error for corrupt settings.json")
	}
	if err := os.WriteFile(p.SettingsFile(), []byte(`{"gamePort": "x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("expected error for wrong type")
	}
}

func ptr[T any](v T) *T { return &v }

func TestApply(t *testing.T) {
	p := testPaths(t)
	st, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	pw := st.Get().RCONPassword

	s, restart, err := st.Apply(Patch{Autostart: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if restart || s.Autostart || s.GamePort != 34197 {
		t.Fatalf("autostart patch: %+v restart=%v", s, restart)
	}

	s, restart, err = st.Apply(Patch{GamePort: ptr(34200), FactorioUsername: ptr("me"), FactorioToken: ptr("secret")})
	if err != nil {
		t.Fatal(err)
	}
	if !restart || s.GamePort != 34200 || s.FactorioUsername != "me" || s.FactorioToken != "secret" || s.Autostart {
		t.Fatalf("port patch: %+v restart=%v", s, restart)
	}
	if s.RCONPassword != pw {
		t.Fatal("RCON password changed")
	}

	// Same values again: no restart needed.
	if _, restart, err = st.Apply(Patch{GamePort: ptr(34200), WhitelistEnabled: ptr(false)}); err != nil || restart {
		t.Fatalf("unchanged patch: restart=%v err=%v", restart, err)
	}

	// Empty token clears it.
	s, restart, err = st.Apply(Patch{FactorioToken: ptr("")})
	if err != nil || !restart || s.FactorioToken != "" || s.View().FactorioTokenSet {
		t.Fatalf("clear token: %+v restart=%v err=%v", s, restart, err)
	}

	m := readFile(t, p)
	if m["gamePort"] != float64(34200) || m["factorioUsername"] != "me" || m["factorioToken"] != "" || m["autostart"] != false {
		t.Fatalf("file = %v", m)
	}
	if st.Get() != s {
		t.Fatalf("Get = %+v, want %+v", st.Get(), s)
	}
}

func TestApplyInvalidPort(t *testing.T) {
	st, err := Open(testPaths(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{0, -1, 65536} {
		if _, _, err := st.Apply(Patch{GamePort: ptr(port), Autostart: ptr(false)}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("port %d: err = %v, want ErrInvalid", port, err)
		}
	}
	if s := st.Get(); s.GamePort != 34197 || !s.Autostart {
		t.Fatalf("invalid patch changed settings: %+v", s)
	}
}

func TestView(t *testing.T) {
	s := Settings{Autostart: true, GamePort: 1, FactorioUsername: "u", FactorioToken: "t", RCONPassword: "pw"}
	data, err := json.Marshal(s.View())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"autostart":true,"gamePort":1,"whitelistEnabled":false,"factorioUsername":"u","factorioTokenSet":true}`
	if string(data) != want {
		t.Fatalf("View = %s, want %s", data, want)
	}
}
