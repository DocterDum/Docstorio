package api

import (
	"io/fs"
	"net/http"

	"docstorio/internal/factorio"
	"docstorio/internal/jobs"
	"docstorio/internal/mods"
	"docstorio/internal/portal"
	"docstorio/internal/settings"
)

// Server holds every dependency of the HTTP layer. main fills all fields
// before calling Routes; handlers only read them (each dependency is itself
// safe for concurrent use).
type Server struct {
	Env      settings.Env
	Paths    settings.Paths
	Settings *settings.Store
	Jobs     *jobs.Manager
	Mods     *mods.Manager
	Portal   *portal.Client
	Factorio *factorio.Supervisor
	Config   *factorio.ConfigFiles
	Logs     *factorio.LogBuffer

	// FactorioVersion is the baked Factorio version.
	FactorioVersion mods.Version
	// Static is the web UI file tree with index.html at its root
	// (fs.Sub(web.Static, "static")).
	Static fs.FS
}

// Routes registers every endpoint and returns the handler for http.Server.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Server.
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/server/start", s.handleServerStart)
	mux.HandleFunc("POST /api/server/stop", s.handleServerStop)
	mux.HandleFunc("POST /api/server/restart", s.handleServerRestart)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("POST /api/console", s.handleConsole)

	// Saves.
	mux.HandleFunc("GET /api/saves", s.handleSavesList)
	mux.HandleFunc("POST /api/saves/upload", s.handleSaveUpload)
	mux.HandleFunc("GET /api/saves/{name}/download", s.handleSaveDownload)
	mux.HandleFunc("DELETE /api/saves/{name}", s.handleSaveDelete)
	mux.HandleFunc("POST /api/saves", s.handleSaveCreate)

	// Config.
	mux.HandleFunc("GET /api/config/files", s.handleConfigFiles)
	mux.HandleFunc("GET /api/config/files/{name}", s.handleConfigFileGet)
	mux.HandleFunc("PUT /api/config/files/{name}", s.handleConfigFilePut)
	mux.HandleFunc("GET /api/config/server-settings", s.handleServerSettingsGet)
	mux.HandleFunc("PUT /api/config/server-settings", s.handleServerSettingsPut)

	// Mods.
	mux.HandleFunc("GET /api/mods", s.handleModsList)
	mux.HandleFunc("POST /api/mods/{name}/enable", s.handleModEnable)
	mux.HandleFunc("POST /api/mods/{name}/disable", s.handleModDisable)
	mux.HandleFunc("DELETE /api/mods/{name}", s.handleModDelete)
	mux.HandleFunc("POST /api/mods/upload", s.handleModUpload)
	mux.HandleFunc("POST /api/mods/update", s.handleModUpdate)

	// Portal.
	mux.HandleFunc("GET /api/portal/search", s.handlePortalSearch)
	mux.HandleFunc("POST /api/portal/refresh", s.handlePortalRefresh)
	mux.HandleFunc("GET /api/portal/mods/{name}", s.handlePortalDetails)
	mux.HandleFunc("GET /api/portal/thumb/{name}", s.handlePortalThumb)
	mux.HandleFunc("POST /api/portal/plan", s.handlePortalPlan)
	mux.HandleFunc("POST /api/portal/install", s.handlePortalInstall)

	// Settings.
	mux.HandleFunc("GET /api/settings", s.handleSettingsGet)
	mux.HandleFunc("PUT /api/settings", s.handleSettingsPut)

	// Jobs.
	mux.HandleFunc("GET /api/jobs/{id}", s.handleJobGet)

	// Unknown API paths and methods: JSON 404 instead of the mux's text
	// response. (A method-less "/api/" pattern does not conflict with the
	// method patterns above because it is strictly less specific.)
	mux.HandleFunc("/api/", s.handleAPINotFound)

	// Web UI: everything else.
	mux.HandleFunc("/", s.handleStatic)
	return mux
}
