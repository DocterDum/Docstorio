package api

import (
	"context"
	"net/http"
	"strings"

	"docstorio/internal/jobs"
	"docstorio/internal/mods"
	"docstorio/internal/portal"
)

// Mod portal.

// installedVersion returns a pointer to the installed version of name, or
// nil (JSON null) when it is not installed.
func installedVersion(installed map[string]string, name string) *string {
	v, ok := installed[name]
	if !ok {
		return nil
	}
	return &v
}

// handlePortalSearch handles GET /api/portal/search?q=&page=1&pageSize=24
// &sort=downloads|updated|name -> portal.Page with Installed filled from
// Mods.InstalledVersions.
func (s *Server) handlePortalSearch(w http.ResponseWriter, r *http.Request) {
	page, err := queryInt(r, "page", 1)
	if err != nil {
		writeErr(w, err)
		return
	}
	pageSize, err := queryInt(r, "pageSize", 24)
	if err != nil {
		writeErr(w, err)
		return
	}
	sort := portal.Sort(r.URL.Query().Get("sort"))
	switch sort {
	case "", portal.SortDownloads, portal.SortUpdated, portal.SortName:
	default:
		writeErr(w, badRequest("sort must be downloads, updated or name"))
		return
	}
	res, err := s.Portal.Search(r.Context(), portal.Query{
		Q:        strings.TrimSpace(r.URL.Query().Get("q")),
		Sort:     sort,
		Page:     int(min(page, 1<<30)),
		PageSize: int(min(pageSize, 1<<30)),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if res.Results == nil {
		res.Results = []portal.Result{}
	}
	installed := s.Mods.InstalledVersions()
	for i := range res.Results {
		res.Results[i].Installed = installedVersion(installed, res.Results[i].Name)
	}
	writeJSON(w, http.StatusOK, res)
}

// handlePortalRefresh handles POST /api/portal/refresh -> 202
// (Portal.Refresh in a goroutine).
func (s *Server) handlePortalRefresh(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := s.Portal.Refresh(context.Background()); err != nil {
			s.Logs.Eventf("mod portal refresh failed: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, struct{}{})
}

// handlePortalDetails handles GET /api/portal/mods/{name} -> portal.Details
// with Installed filled.
func (s *Server) handlePortalDetails(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	d, err := s.Portal.Details(r.Context(), name)
	if err != nil {
		writeErr(w, err)
		return
	}
	if d.Releases == nil {
		d.Releases = []portal.DetailsRelease{}
	}
	d.Installed = installedVersion(s.Mods.InstalledVersions(), d.Name)
	writeJSON(w, http.StatusOK, d)
}

// handlePortalThumb handles GET /api/portal/thumb/{name} -> 302 to
// Portal.Thumbnail / 404.
func (s *Server) handlePortalThumb(w http.ResponseWriter, r *http.Request) {
	url, err := s.Portal.Thumbnail(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// readPlanRequest decodes and checks the body of plan and install.
func readPlanRequest(w http.ResponseWriter, r *http.Request) (mods.PlanRequest, bool) {
	var req mods.PlanRequest
	if !decodeJSON(w, r, &req) {
		return req, false
	}
	if len(req.Mods) == 0 {
		writeErr(w, badRequest(`"mods" is empty`))
		return req, false
	}
	for _, m := range req.Mods {
		if strings.TrimSpace(m.Name) == "" {
			writeErr(w, badRequest("a requested mod has no name"))
			return req, false
		}
	}
	return req, true
}

// handlePortalPlan handles POST /api/portal/plan mods.PlanRequest ->
// mods.Plan (Mods.Plan).
func (s *Server) handlePortalPlan(w http.ResponseWriter, r *http.Request) {
	req, ok := readPlanRequest(w, r)
	if !ok {
		return
	}
	plan, err := s.Mods.Plan(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// handlePortalInstall handles POST /api/portal/install mods.PlanRequest ->
// {"jobId"} (jobs.Run with jobs.KindModInstall, Mods.Install); 400
// credentials_required when no username/token.
func (s *Server) handlePortalInstall(w http.ResponseWriter, r *http.Request) {
	req, ok := readPlanRequest(w, r)
	if !ok {
		return
	}
	creds, err := s.credentials()
	if err != nil {
		writeErr(w, err)
		return
	}
	j, err := s.Jobs.Run(jobs.KindModInstall, func(ctx context.Context, j *jobs.Job) error {
		changed, err := s.Mods.Install(ctx, j, req, creds)
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
