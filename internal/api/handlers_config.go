package api

import (
	"net/http"

	"docstorio/internal/factorio"
)

// Config files. Every successful write calls
// Factorio.MarkRestartRequired.

// handleConfigFiles handles GET /api/config/files ->
// [factorio.ConfigFileInfo].
func (s *Server) handleConfigFiles(w http.ResponseWriter, r *http.Request) {
	files := s.Config.List()
	if files == nil {
		files = []factorio.ConfigFileInfo{}
	}
	writeJSON(w, http.StatusOK, files)
}

// configFileBody is the body of GET /api/config/files/{name}.
type configFileBody struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Exists  bool   `json:"exists"`
}

// handleConfigFileGet handles GET /api/config/files/{name} -> {"name",
// "content", "exists"}.
func (s *Server) handleConfigFileGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	content, exists, err := s.Config.Read(name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, configFileBody{Name: name, Content: content, Exists: exists})
}

// handleConfigFilePut handles PUT /api/config/files/{name} {"content"} ->
// 204; 400 invalid_json with line/column.
func (s *Server) handleConfigFilePut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content *string `json:"content"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Content == nil {
		writeErr(w, badRequest(`"content" is required`))
		return
	}
	if err := s.Config.Write(r.PathValue("name"), *req.Content); err != nil {
		writeErr(w, err)
		return
	}
	s.Factorio.MarkRestartRequired()
	w.WriteHeader(http.StatusNoContent)
}

// handleServerSettingsGet handles GET /api/config/server-settings ->
// {"fields": [factorio.Field]}.
func (s *Server) handleServerSettingsGet(w http.ResponseWriter, r *http.Request) {
	fields, err := s.Config.ServerSettingsFields()
	if err != nil {
		writeErr(w, err)
		return
	}
	if fields == nil {
		fields = []factorio.Field{}
	}
	writeJSON(w, http.StatusOK, map[string][]factorio.Field{"fields": fields})
}

// handleServerSettingsPut handles PUT /api/config/server-settings
// {"values": {key: value}} -> 204 (partial update).
func (s *Server) handleServerSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Values map[string]any `json:"values"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Values == nil {
		writeErr(w, badRequest(`"values" is required`))
		return
	}
	if err := s.Config.UpdateServerSettings(req.Values); err != nil {
		writeErr(w, err)
		return
	}
	s.Factorio.MarkRestartRequired()
	w.WriteHeader(http.StatusNoContent)
}
