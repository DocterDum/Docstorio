package api

import (
	"context"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"docstorio/internal/factorio"
	"docstorio/internal/fsutil"
	"docstorio/internal/jobs"
)

// Saves.

// handleSavesList handles GET /api/saves -> [factorio.SaveInfo] newest
// first.
func (s *Server) handleSavesList(w http.ResponseWriter, r *http.Request) {
	saves, err := s.Factorio.ListSaves()
	if err != nil {
		writeErr(w, err)
		return
	}
	if saves == nil {
		saves = []factorio.SaveInfo{}
	}
	writeJSON(w, http.StatusOK, saves)
}

// handleSaveUpload handles POST /api/saves/upload multipart "file"
// [?overwrite=1] -> 201: the part is streamed into fsutil.NewPending in
// saves/ and committed with Factorio.CommitSaveUpload under the part's file
// name (409 if it exists unless overwrite; never the running save).
func (s *Server) handleSaveUpload(w http.ResponseWriter, r *http.Request) {
	overwrite := false
	if v := r.URL.Query().Get("overwrite"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeErr(w, badRequest("overwrite must be 1 or 0"))
			return
		}
		overwrite = b
	}
	p, name, err := receiveUpload(r, s.Paths.Saves, fsutil.ValidSaveName)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer p.Abort()
	if err := s.Factorio.CommitSaveUpload(p, name, overwrite); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

// handleSaveDownload handles GET /api/saves/{name}/download -> the zip
// (Content-Disposition: attachment).
func (s *Server) handleSaveDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := fsutil.ValidSaveName(name); err != nil {
		writeErr(w, err)
		return
	}
	f, info, err := s.Factorio.OpenSave(name)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// handleSaveDelete handles DELETE /api/saves/{name} -> 204; 409 for the
// running save.
func (s *Server) handleSaveDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := fsutil.ValidSaveName(name); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.Factorio.DeleteSave(name); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createWorldRequest is the body of POST /api/saves.
type createWorldRequest struct {
	Name              string  `json:"name"`
	Seed              *uint32 `json:"seed"`
	UseMapGenSettings bool    `json:"useMapGenSettings"`
	UseMapSettings    bool    `json:"useMapSettings"`
}

// handleSaveCreate handles POST /api/saves {"name", "seed"?,
// "useMapGenSettings", "useMapSettings"} -> {"jobId"} (jobs.Run with
// jobs.KindCreateWorld, then Factorio.CreateWorld). ".zip" is appended to
// name when missing (as Factorio does), then fsutil.ValidSaveName. The
// server must be stopped (409 server_running); an existing name is 409
// conflict. Both are checked here as well as in CreateWorld so the caller
// gets them as an answer rather than as a failed job.
func (s *Server) handleSaveCreate(w http.ResponseWriter, r *http.Request) {
	var req createWorldRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, badRequest("name is empty"))
		return
	}
	if !strings.HasSuffix(name, ".zip") {
		name += ".zip"
	}
	if err := fsutil.ValidSaveName(name); err != nil {
		writeErr(w, err)
		return
	}
	if st := s.Factorio.Status().State; st != factorio.Stopped && st != factorio.Failed {
		writeErr(w, factorio.ErrRunning)
		return
	}
	if _, err := os.Stat(filepath.Join(s.Paths.Saves, name)); err == nil {
		writeErr(w, factorio.ErrSaveExists)
		return
	} else if !errors.Is(err, fs.ErrNotExist) {
		writeErr(w, err)
		return
	}
	opts := factorio.CreateOptions{
		Name:              name,
		Seed:              req.Seed,
		UseMapGenSettings: req.UseMapGenSettings,
		UseMapSettings:    req.UseMapSettings,
	}
	j, err := s.Jobs.Run(jobs.KindCreateWorld, func(ctx context.Context, j *jobs.Job) error {
		return s.Factorio.CreateWorld(ctx, j, opts)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobIDBody{JobID: j.ID()})
}

// jobIDBody is the answer of every endpoint that starts a background job.
type jobIDBody struct {
	JobID string `json:"jobId"`
}
