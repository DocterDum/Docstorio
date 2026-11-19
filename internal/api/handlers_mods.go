package api

import (
	"context"
	"net/http"

	"docstorio/internal/jobs"
	"docstorio/internal/mods"
)

// Installed mods. Every change takes the busy lock and, on success, calls
// Factorio.MarkRestartRequired.

// handleModsList handles GET /api/mods -> mods.ListResult.
func (s *Server) handleModsList(w http.ResponseWriter, r *http.Request) {
	res, err := s.Mods.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if res.Mods == nil {
		res.Mods = []mods.ModView{}
	}
	writeJSON(w, http.StatusOK, res)
}

// modChange runs one short mod change under the busy lock and marks a
// restart as required when it succeeded and changed something.
func (s *Server) modChange(kind jobs.Kind, op func() (changed bool, err error)) error {
	j, err := s.Jobs.TryBegin(kind)
	if err != nil {
		return err
	}
	changed, err := op()
	j.Finish(err)
	if err == nil && changed {
		s.Factorio.MarkRestartRequired()
	}
	return err
}

// handleModEnable handles POST /api/mods/{name}/enable -> {"enabled":
// [names]} (jobs.KindModEnable, Mods.Enable).
func (s *Server) handleModEnable(w http.ResponseWriter, r *http.Request) {
	var names []string
	err := s.modChange(jobs.KindModEnable, func() (bool, error) {
		var err error
		names, err = s.Mods.Enable(r.PathValue("name"))
		return len(names) > 0, err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"enabled": nonNil(names)})
}

// handleModDisable handles POST /api/mods/{name}/disable {"cascade"?} ->
// {"disabled": [names]} or 409 conflict with "dependents"
// (jobs.KindModDisable, Mods.Disable). An empty body means cascade false.
func (s *Server) handleModDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cascade bool `json:"cascade"`
	}
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	var names []string
	err := s.modChange(jobs.KindModDisable, func() (bool, error) {
		var err error
		names, err = s.Mods.Disable(r.PathValue("name"), req.Cascade)
		return len(names) > 0, err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"disabled": nonNil(names)})
}

// handleModDelete handles DELETE /api/mods/{name} -> 204; 409 for built-ins
// (jobs.KindModDelete, Mods.Delete).
func (s *Server) handleModDelete(w http.ResponseWriter, r *http.Request) {
	err := s.modChange(jobs.KindModDelete, func() (bool, error) {
		return true, s.Mods.Delete(r.PathValue("name"))
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleModUpload handles POST /api/mods/upload multipart "file" -> 201
// {"name", "version"} (jobs.KindModUpload; the part is streamed into
// fsutil.NewPending in mods/, then Mods.AddUpload). The upload itself is
// received before the busy lock is taken, so a slow client never holds it;
// the temp file is ignored by mod scans.
func (s *Server) handleModUpload(w http.ResponseWriter, r *http.Request) {
	p, _, err := receiveUpload(r, s.Paths.Mods, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer p.Abort()
	var info mods.Info
	err = s.modChange(jobs.KindModUpload, func() (bool, error) {
		var err error
		info, err = s.Mods.AddUpload(p)
		return true, err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": info.Name, "version": info.Version})
}

// credentials returns the stored factorio.com login, or an error wrapping
// mods.ErrCredentials when either part is missing.
func (s *Server) credentials() (mods.Credentials, error) {
	set := s.Settings.Get()
	creds := mods.Credentials{Username: set.FactorioUsername, Token: set.FactorioToken}
	if creds.Empty() {
		return creds, mods.ErrCredentials
	}
	return creds, nil
}

// handleModUpdate handles POST /api/mods/update {"names"?: []} -> {"jobId"}
// (jobs.Run with jobs.KindModUpdate, Mods.Update); 400 credentials_required
// when no username/token.
func (s *Server) handleModUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"names"`
	}
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	creds, err := s.credentials()
	if err != nil {
		writeErr(w, err)
		return
	}
	j, err := s.Jobs.Run(jobs.KindModUpdate, func(ctx context.Context, j *jobs.Job) error {
		changed, err := s.Mods.Update(ctx, j, req.Names, creds)
		if changed {
			s.Factorio.MarkRestartRequired()
		}
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobIDBody{JobID: j.ID()})
}

// nonNil returns names, or an empty slice when it is nil, so it encodes as
// [].
func nonNil(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}
