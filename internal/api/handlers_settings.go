package api

import (
	"net/http"

	"docstorio/internal/settings"
)

// Manager settings.

// handleSettingsGet handles GET /api/settings -> settings.View.
func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Settings.Get().View())
}

// handleSettingsPut handles PUT /api/settings settings.Patch ->
// settings.View; calls Factorio.MarkRestartRequired when Settings.Apply
// says so.
func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var patch settings.Patch
	if !decodeJSON(w, r, &patch) {
		return
	}
	set, restartRequired, err := s.Settings.Apply(patch)
	if err != nil {
		writeErr(w, err)
		return
	}
	if restartRequired {
		s.Factorio.MarkRestartRequired()
	}
	writeJSON(w, http.StatusOK, set.View())
}
