package settings

import (
	"os"
	"path/filepath"
)

// Paths holds every absolute path the manager uses (DESIGN "Runtime
// layout"). Directory fields are directories; the methods return file paths
// inside them. All values are derived from the two roots by NewPaths, so
// tests can point a Paths at a temporary directory.
type Paths struct {
	Data    string // DOCSTORIO_DATA, e.g. /factorio
	Saves   string // <Data>/saves
	Mods    string // <Data>/mods
	Config  string // <Data>/config
	Manager string // <Data>/manager
	Cache   string // <Data>/manager/cache

	FactorioDir  string // DOCSTORIO_FACTORIO_DIR, e.g. /opt/factorio
	FactorioData string // <FactorioDir>/data (read-data; built-in mods, *.example.json)
	FactorioBin  string // <FactorioDir>/bin/x64/factorio
}

// NewPaths derives all paths from the data folder and the Factorio install
// directory. It does not touch the disk.
func NewPaths(dataDir, factorioDir string) Paths {
	return Paths{
		Data:         dataDir,
		Saves:        filepath.Join(dataDir, "saves"),
		Mods:         filepath.Join(dataDir, "mods"),
		Config:       filepath.Join(dataDir, "config"),
		Manager:      filepath.Join(dataDir, "manager"),
		Cache:        filepath.Join(dataDir, "manager", "cache"),
		FactorioDir:  factorioDir,
		FactorioData: filepath.Join(factorioDir, "data"),
		FactorioBin:  filepath.Join(factorioDir, "bin", "x64", "factorio"),
	}
}

// EnsureDirs creates Saves, Mods, Config, Manager and Cache (0755) when
// missing.
func (p Paths) EnsureDirs() error {
	for _, dir := range []string{p.Saves, p.Mods, p.Config, p.Manager, p.Cache} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return nil
}

// File names inside Config. The first six are the files of the config API
// (GET /api/config/files), in that order.
const (
	ServerSettingsFile = "server-settings.json"
	MapGenSettingsFile = "map-gen-settings.json"
	MapSettingsFile    = "map-settings.json"
	AdminListFile      = "server-adminlist.json"
	BanListFile        = "server-banlist.json"
	WhitelistFile      = "server-whitelist.json"
	ConfigINIFile      = "config.ini"
)

// ConfigFile returns <Config>/<name>.
func (p Paths) ConfigFile(name string) string { return filepath.Join(p.Config, name) }

// Example returns Factorio's example for a config file, e.g.
// Example("server-settings.json") -> <FactorioData>/server-settings.example.json.
// Callers must handle a missing example.
func (p Paths) Example(name string) string {
	ext := filepath.Ext(name)
	return filepath.Join(p.FactorioData, name[:len(name)-len(ext)]+".example"+ext)
}

// SettingsFile returns <Manager>/settings.json (0600).
func (p Paths) SettingsFile() string { return filepath.Join(p.Manager, "settings.json") }

// ModState returns <Manager>/mod-state.json.
func (p Paths) ModState() string { return filepath.Join(p.Manager, "mod-state.json") }

// EffectiveServerSettings returns <Manager>/server-settings.effective.json
// (0600).
func (p Paths) EffectiveServerSettings() string {
	return filepath.Join(p.Manager, "server-settings.effective.json")
}

// ModList returns <Mods>/mod-list.json.
func (p Paths) ModList() string { return filepath.Join(p.Mods, "mod-list.json") }
