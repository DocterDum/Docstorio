package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// designEndpoints lists every endpoint of DESIGN "HTTP API" plus the job
// endpoint of DESIGN "Jobs", with a concrete path. It guards against a route
// being lost or shadowed while handlers are written.
var designEndpoints = []struct{ method, path string }{
	{"GET", "/api/status"},
	{"POST", "/api/server/start"},
	{"POST", "/api/server/stop"},
	{"POST", "/api/server/restart"},
	{"GET", "/api/logs"},
	{"POST", "/api/console"},
	{"GET", "/api/saves"},
	{"POST", "/api/saves/upload"},
	{"GET", "/api/saves/world.zip/download"},
	{"DELETE", "/api/saves/world.zip"},
	{"POST", "/api/saves"},
	{"GET", "/api/config/files"},
	{"GET", "/api/config/files/server-settings.json"},
	{"PUT", "/api/config/files/server-settings.json"},
	{"GET", "/api/config/server-settings"},
	{"PUT", "/api/config/server-settings"},
	{"GET", "/api/mods"},
	{"POST", "/api/mods/Some%20Mod/enable"},
	{"POST", "/api/mods/Some%20Mod/disable"},
	{"DELETE", "/api/mods/Some%20Mod"},
	{"POST", "/api/mods/upload"},
	{"POST", "/api/mods/update"},
	{"GET", "/api/portal/search"},
	{"POST", "/api/portal/refresh"},
	{"GET", "/api/portal/mods/somemod"},
	{"GET", "/api/portal/thumb/somemod"},
	{"POST", "/api/portal/plan"},
	{"POST", "/api/portal/install"},
	{"GET", "/api/settings"},
	{"PUT", "/api/settings"},
	{"GET", "/api/jobs/0123456789abcdef"},
}

func testServer() *Server {
	return &Server{Static: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}}
}

// TestRoutesRegistered checks that every DESIGN endpoint reaches a handler
// (anything but the catch-alls).
func TestRoutesRegistered(t *testing.T) {
	mux := testServer().Routes()
	for _, e := range designEndpoints {
		req := httptest.NewRequest(e.method, e.path, strings.NewReader("{}"))
		_, pattern := mux.Handler(req)
		if pattern == "" || pattern == "/api/" || pattern == "/" {
			t.Errorf("%s %s: no endpoint registered (matched %q)", e.method, e.path, pattern)
		}
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	mux := testServer().Routes()
	for _, e := range []struct{ method, path string }{
		{"GET", "/api/nope"},
		{"POST", "/api/status"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(e.method, e.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status %d, want 404", e.method, e.path, rec.Code)
			continue
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["code"] != codeNotFound {
			t.Errorf("%s %s: body %q, want JSON code %q", e.method, e.path, rec.Body.String(), codeNotFound)
		}
	}
}

func TestStaticIndex(t *testing.T) {
	mux := testServer().Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Fatalf("GET /: status %d body %q", rec.Code, rec.Body.String())
	}
}
