package api

import "net/http"

// Jobs.

// handleJobGet handles GET /api/jobs/{id}?after=<n> -> jobs.Snapshot; 404
// not_found for an unknown id.
func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request) {
	after, err := queryInt(r, "after", 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	j, ok := s.Jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "no such job")
		return
	}
	writeJSON(w, http.StatusOK, j.Snapshot(after))
}
