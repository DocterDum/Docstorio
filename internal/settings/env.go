package settings

import "strings"

// Env is the parsed process environment (DESIGN "Environment variables").
type Env struct {
	DataDir     string // DOCSTORIO_DATA, default "/factorio"
	FactorioDir string // DOCSTORIO_FACTORIO_DIR, default "/opt/factorio"
	HTTPAddr    string // DOCSTORIO_HTTP_ADDR, default ":8080"
	PortalURL   string // DOCSTORIO_PORTAL_URL, default "https://mods.factorio.com", trailing '/' removed
	AssetsURL   string // DOCSTORIO_ASSETS_URL, default "https://assets-mod.factorio.com", trailing '/' removed

	// FactorioVersion is FACTORIO_VERSION as baked into the image; may be
	// empty, in which case main falls back to factorio.DetectVersion.
	FactorioVersion string
	// ManagerVersion is DOCSTORIO_VERSION; "dev" when empty.
	ManagerVersion string
}

// LoadEnv reads the environment through getenv (os.Getenv in production, a
// map lookup in tests) and applies the defaults above. Empty values take the
// default. It never fails.
func LoadEnv(getenv func(string) string) Env {
	get := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	return Env{
		DataDir:         get("DOCSTORIO_DATA", "/factorio"),
		FactorioDir:     get("DOCSTORIO_FACTORIO_DIR", "/opt/factorio"),
		HTTPAddr:        get("DOCSTORIO_HTTP_ADDR", ":8080"),
		PortalURL:       strings.TrimRight(get("DOCSTORIO_PORTAL_URL", "https://mods.factorio.com"), "/"),
		AssetsURL:       strings.TrimRight(get("DOCSTORIO_ASSETS_URL", "https://assets-mod.factorio.com"), "/"),
		FactorioVersion: get("FACTORIO_VERSION", ""),
		ManagerVersion:  get("DOCSTORIO_VERSION", "dev"),
	}
}
