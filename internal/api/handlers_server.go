package api

import (
	"net/http"
	"strings"

	"docstorio/internal/factorio"
	"docstorio/internal/fsutil"
	"docstorio/internal/jobs"
	"docstorio/internal/mods"
)

// Server control, logs and console.

// statusBody is the body of GET /api/status and of the start, stop and
// restart responses.
type statusBody struct {
	factorio.Status
	GamePort        int     `json:"gamePort"`
	FactorioVersion string  `json:"factorioVersion"`
	ManagerVersion  string  `json:"managerVersion"`
	SpaceAge        bool    `json:"spaceAge"`
	Autostart       bool    `json:"autostart"`
	BusyJobID       *string `json:"busyJobId"` // null when idle
}

// status assembles the status body. It never blocks on a long operation.
func (s *Server) status() statusBody {
	st := s.Factorio.Status()
	if st.Players == nil {
		st.Players = []string{}
	}
	set := s.Settings.Get()
	body := statusBody{
		Status:          st,
		GamePort:        set.GamePort,
		FactorioVersion: s.FactorioVersion.String(),
		ManagerVersion:  s.Env.ManagerVersion,
		SpaceAge:        s.Mods.Enabled(mods.SpaceAge),
		Autostart:       set.Autostart,
	}
	if id := s.Jobs.Busy(); id != "" {
		body.BusyJobID = &id
	}
	return body
}

// handleStatus handles GET /api/status -> factorio.Status plus "gamePort",
// "factorioVersion", "managerVersion", "spaceAge" (Mods.Enabled("space-age")),
// "autostart" and "busyJobId" (Jobs.Busy).
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.status())
}

// saveRequest is the optional body of start and restart.
type saveRequest struct {
	Save string `json:"save"`
}

// readSaveRequest decodes the optional {"save"} body and checks the name.
func readSaveRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req saveRequest
	if !decodeOptionalJSON(w, r, &req) {
		return "", false
	}
	if req.Save != "" {
		if err := fsutil.ValidSaveName(req.Save); err != nil {
			writeErr(w, err)
			return "", false
		}
	}
	return req.Save, true
}

// runControl runs one server control operation under the busy lock and
// answers with the status.
func (s *Server) runControl(w http.ResponseWriter, kind jobs.Kind, op func() error) {
	j, err := s.Jobs.TryBegin(kind)
	if err != nil {
		writeErr(w, err)
		return
	}
	err = op()
	j.Finish(err)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.status())
}

// handleServerStart handles POST /api/server/start {"save"?} -> status; busy
// lock (jobs.KindStart); 409 server_running.
func (s *Server) handleServerStart(w http.ResponseWriter, r *http.Request) {
	save, ok := readSaveRequest(w, r)
	if !ok {
		return
	}
	s.runControl(w, jobs.KindStart, func() error { return s.Factorio.Start(save) })
}

// handleServerStop handles POST /api/server/stop -> status; busy lock
// (jobs.KindStop).
func (s *Server) handleServerStop(w http.ResponseWriter, r *http.Request) {
	s.runControl(w, jobs.KindStop, s.Factorio.Stop)
}

// handleServerRestart handles POST /api/server/restart {"save"?} -> status;
// busy lock (jobs.KindRestart).
func (s *Server) handleServerRestart(w http.ResponseWriter, r *http.Request) {
	save, ok := readSaveRequest(w, r)
	if !ok {
		return
	}
	s.runControl(w, jobs.KindRestart, func() error { return s.Factorio.Restart(save) })
}

// logsBody is the body of GET /api/logs.
type logsBody struct {
	Lines []factorio.Line `json:"lines"`
	Last  int64           `json:"last"`
}

// handleLogs handles GET /api/logs?after=<n> -> {"lines": [{n, text}],
// "last"} (at most factorio.LogPageMax lines).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	after, err := queryInt(r, "after", 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	lines, last := s.Logs.Since(after, factorio.LogPageMax)
	if lines == nil {
		lines = []factorio.Line{}
	}
	writeJSON(w, http.StatusOK, logsBody{Lines: lines, Last: last})
}

// handleConsole handles POST /api/console {"command"} -> {"output"}; 409
// server_not_running.
func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string `json:"command"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		writeErr(w, badRequest("command is empty"))
		return
	}
	out, err := s.Factorio.Console(r.Context(), req.Command)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}
