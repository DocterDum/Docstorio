package api

import "net/http"

// handleAPINotFound answers every /api/ request that matched no endpoint
// (unknown path or wrong method) with 404 not_found.
func (s *Server) handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, codeNotFound, "no such API endpoint")
}

// handleStatic serves the embedded web UI (index.html for "/"). Only GET and
// HEAD are served; files carry Cache-Control: no-cache so a new image is
// picked up on reload.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Static == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.FileServerFS(s.Static).ServeHTTP(w, r)
}
