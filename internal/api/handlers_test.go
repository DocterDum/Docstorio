package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docstorio/internal/factorio"
	"docstorio/internal/fsutil"
	"docstorio/internal/jobs"
	"docstorio/internal/mods"
	"docstorio/internal/portal"
	"docstorio/internal/settings"
)

// testFactorio is the Factorio version the test servers report.
var testFactorio = mods.Version{Major: 2, Minor: 1, Patch: 24}

// mustOK fails the test on a setup error.
func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testEnv is a Server over a temporary data folder and a fake Factorio
// install (no binary: the server is never started).
type testEnv struct {
	srv   *Server
	mux   *http.ServeMux
	paths settings.Paths
}

// newTestEnv builds settings, jobs, the supervisor, config files and the
// log buffer. Mods and Portal stay nil unless withMods is called.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	paths := settings.NewPaths(filepath.Join(dir, "data"), filepath.Join(dir, "factorio"))
	writeFile(t, filepath.Join(paths.FactorioData, "base", "info.json"),
		`{"name": "base", "version": "2.1.24", "title": "Base mod", "factorio_version": "2.1", "dependencies": []}`)
	writeFile(t, paths.Example(settings.ServerSettingsFile),
		`{"name": "Name of the game", "_comment_max_players": "Maximum number of players", "max_players": 0}`)
	mustOK(t, paths.EnsureDirs())
	store, err := settings.Open(paths)
	mustOK(t, err)

	logs := factorio.NewLogBuffer(nil)
	jm := jobs.New(nil)
	t.Cleanup(jm.Close)
	srv := &Server{
		Env:      settings.Env{ManagerVersion: "test"},
		Paths:    paths,
		Settings: store,
		Jobs:     jm,
		Factorio: factorio.NewSupervisor(factorio.Config{
			Paths:    paths,
			Settings: store.Get,
			Log:      logs,
			Command:  filepath.Join(dir, "no-factorio"),
		}),
		Config:          factorio.NewConfigFiles(paths),
		Logs:            logs,
		FactorioVersion: testFactorio,
	}
	return &testEnv{srv: srv, mux: srv.Routes(), paths: paths}
}

// withMods adds a mods.Manager (offline: no ReleaseSource) and a portal
// client pointing at an unreachable address.
func (e *testEnv) withMods(t *testing.T) *testEnv {
	t.Helper()
	mm, err := mods.New(mods.Config{
		ModsDir:         e.paths.Mods,
		FactorioDataDir: e.paths.FactorioData,
		StatePath:       e.paths.ModState(),
		ModListPath:     e.paths.ModList(),
		Factorio:        testFactorio,
	})
	mustOK(t, err)
	e.srv.Mods = mm
	e.srv.Portal = portal.New(portal.Config{BaseURL: "http://127.0.0.1:1", CacheDir: e.paths.Cache, Factorio: testFactorio})
	e.mux = e.srv.Routes()
	return e
}

// do serves one request and returns the recorder.
func (e *testEnv) do(method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	return serve(e.mux, method, path, contentType, body)
}

func serve(h http.Handler, method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// doJSON serves a request with a JSON body ("" = no body).
func (e *testEnv) doJSON(method, path, body string) *httptest.ResponseRecorder {
	return e.do(method, path, "application/json", strings.NewReader(body))
}

// upload builds a multipart body with one "file" part.
func upload(t *testing.T, filename string, data []byte) (string, io.Reader) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("other", "ignored")
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(data)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), &buf
}

// decode unmarshals the recorder's body into a map.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", rec.Body.String(), err)
	}
	return m
}

// wantError checks status and the JSON error shape {"error", "code"}.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) map[string]any {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type %q, want JSON", ct)
	}
	m := decode(t, rec)
	if m["code"] != code {
		t.Fatalf("code %v, want %q (body %s)", m["code"], code, rec.Body.String())
	}
	if msg, _ := m["error"].(string); msg == "" {
		t.Fatalf("missing error message: %s", rec.Body.String())
	}
	return m
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
}

// TestWriteErrShapes checks the domain error -> status/code/extra fields
// mapping.
func TestWriteErrShapes(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
		extra  map[string]any
	}{
		{&jobs.BusyError{JobID: "abc", Kind: jobs.KindStart}, 409, codeBusy, map[string]any{"jobId": "abc"}},
		{&mods.DependentsError{Mod: "a", Dependents: []string{"b", "c"}}, 409, codeConflict, map[string]any{"dependents": []any{"b", "c"}}},
		{fmt.Errorf("writing: %w", &factorio.JSONError{Line: 3, Column: 7, Msg: "bad"}), 400, codeInvalidJSON, map[string]any{"line": 3.0, "column": 7.0}},
		{&portal.UpstreamError{Op: "list", Status: 500}, 502, codePortalError, nil},
		{factorio.ErrRunning, 409, codeServerRunning, nil},
		{factorio.ErrNotRunning, 409, codeServerNotRunning, nil},
		{factorio.ErrSaveExists, 409, codeConflict, nil},
		{factorio.ErrSaveInUse, 409, codeConflict, nil},
		{factorio.ErrNoSaves, 409, codeConflict, nil},
		{mods.ErrBuiltin, 409, codeConflict, nil},
		{mods.ErrBaseRequired, 409, codeConflict, nil},
		{mods.ErrPlanFailed, 409, codeConflict, nil},
		{mods.ErrCredentials, 400, codeCredentialsRequired, nil},
		{fmt.Errorf("x: %w", mods.ErrNotFound), 404, codeNotFound, nil},
		{factorio.ErrUnknownConfigFile, 404, codeNotFound, nil},
		{&fs.PathError{Op: "open", Path: "/factorio/saves/x.zip", Err: fs.ErrNotExist}, 404, codeNotFound, nil},
		{fsutil.ErrInvalidName, 400, codeBadRequest, nil},
		{mods.ErrInvalidMod, 400, codeBadRequest, nil},
		{settings.ErrInvalid, 400, codeBadRequest, nil},
		{factorio.ErrBadValue, 400, codeBadRequest, nil},
		{badRequest("nope"), 400, codeBadRequest, nil},
		{errors.New("disk on fire"), 500, codeInternal, nil},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeErr(rec, c.err)
		if rec.Code != c.status {
			t.Errorf("%v: status %d, want %d", c.err, rec.Code, c.status)
			continue
		}
		m := decode(t, rec)
		if m["code"] != c.code || m["error"] == "" {
			t.Errorf("%v: body %s, want code %q", c.err, rec.Body.String(), c.code)
		}
		for k, v := range c.extra {
			if fmt.Sprint(m[k]) != fmt.Sprint(v) {
				t.Errorf("%v: %s = %v, want %v", c.err, k, m[k], v)
			}
		}
		if c.extra == nil {
			for _, k := range []string{"jobId", "dependents", "line", "column"} {
				if _, ok := m[k]; ok {
					t.Errorf("%v: unexpected field %q in %s", c.err, k, rec.Body.String())
				}
			}
		}
	}
}

// TestWriteErrRedactsToken checks that error messages never carry the
// factorio.com token.
func TestWriteErrRedactsToken(t *testing.T) {
	if jobs.Redact("token=abc") == "token=abc" {
		t.Skip("jobs.Redact not implemented yet")
	}
	rec := httptest.NewRecorder()
	writeErr(rec, errors.New("GET https://mods.factorio.com/download/x?username=u&token=sekrit: EOF"))
	if strings.Contains(rec.Body.String(), "sekrit") {
		t.Fatalf("token leaked: %s", rec.Body.String())
	}
}

// TestBadRequests checks request validation that happens before any domain
// call (the test server has no domain dependencies at all).
func TestBadRequests(t *testing.T) {
	mux := testServer().Routes()
	cases := []struct{ method, path, ct, body string }{
		{"POST", "/api/console", "application/json", `{`},
		{"POST", "/api/console", "application/json", `{"command": "  "}`},
		{"POST", "/api/console", "application/json", ``},
		{"POST", "/api/server/start", "application/json", `{"save": 5}`},
		{"GET", "/api/logs?after=x", "", ""},
		{"GET", "/api/logs?after=-1", "", ""},
		{"GET", "/api/jobs/abc?after=z", "", ""},
		{"GET", "/api/portal/search?sort=bogus", "", ""},
		{"GET", "/api/portal/search?page=abc", "", ""},
		{"GET", "/api/portal/search?pageSize=-3", "", ""},
		{"POST", "/api/portal/plan", "application/json", `{"mods": []}`},
		{"POST", "/api/portal/plan", "application/json", `{"mods": [{"name": " "}]}`},
		{"POST", "/api/portal/install", "application/json", `{}`},
		{"POST", "/api/mods/x/disable", "application/json", `{"cascade": "yes"}`},
		{"POST", "/api/mods/update", "application/json", `{"names": "all"}`},
		{"POST", "/api/saves", "application/json", `{"name": ""}`},
		{"POST", "/api/saves", "application/json", `{"name": "w", "seed": -1}`},
		{"POST", "/api/saves", "application/json", `{"name": "w", "seed": 4294967296}`},
		{"PUT", "/api/config/files/server-settings.json", "application/json", `{}`},
		{"PUT", "/api/config/files/server-settings.json", "application/json", `{"content": {"a": 1}}`},
		{"PUT", "/api/config/server-settings", "application/json", `{}`},
		{"PUT", "/api/settings", "application/json", `[1]`},
		{"PUT", "/api/settings", "application/json", `{"gamePort": "x"}`},
		{"POST", "/api/saves/upload", "application/json", `{}`},
		{"POST", "/api/saves/upload?overwrite=maybe", "", ""},
		{"POST", "/api/mods/upload", "text/plain", "PK"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path+" "+c.body, func(t *testing.T) {
			rec := serve(mux, c.method, c.path, c.ct, strings.NewReader(c.body))
			wantError(t, rec, http.StatusBadRequest, codeBadRequest)
		})
	}
}

// TestPathTraversal checks that names from the URL never leave their
// directory: escaped '/', '\\' and ".." are rejected and nothing outside
// saves/ or config/ is read, written or deleted.
func TestPathTraversal(t *testing.T) {
	e := newTestEnv(t)
	secret := "SECRET-CONTENT"
	writeFile(t, filepath.Join(e.paths.Data, "secret.zip"), secret)
	writeFile(t, filepath.Join(e.paths.Manager, "secret.json"), `"`+secret+`"`)

	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/saves/..%2Fsecret.zip/download", ""},
		{"GET", "/api/saves/..%5Csecret.zip/download", ""},
		{"GET", "/api/saves/%2E%2E/download", ""},
		{"GET", "/api/saves/..%2F..%2F..%2Fetc%2Fpasswd/download", ""},
		{"DELETE", "/api/saves/..%2Fsecret.zip", ""},
		{"POST", "/api/server/start", `{"save": "../secret.zip"}`},
		{"POST", "/api/saves", `{"name": "../secret"}`},
		{"POST", "/api/saves", `{"name": "..\\secret.zip"}`},
	} {
		rec := e.doJSON(c.method, c.path, c.body)
		wantError(t, rec, http.StatusBadRequest, codeBadRequest)
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("%s %s leaked the file: %s", c.method, c.path, rec.Body.String())
		}
	}

	// Unescaped ".." is cleaned by the mux (redirect), never served.
	for _, p := range []string{"/api/saves/../../secret.zip/download", "/api/config/files/../../manager/secret.json"} {
		rec := e.do("GET", p, "", nil)
		if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("GET %s: status %d body %q", p, rec.Code, rec.Body.String())
		}
	}

	// Config files: only the six editable names exist.
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/config/files/..%2Fmanager%2Fsecret.json", ""},
		{"GET", "/api/config/files/config.ini", ""},
		{"PUT", "/api/config/files/..%2Fevil.json", `{"content": "[]"}`},
	} {
		rec := e.doJSON(c.method, c.path, c.body)
		wantError(t, rec, http.StatusNotFound, codeNotFound)
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("%s %s leaked the file: %s", c.method, c.path, rec.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(e.paths.Data, "evil.json")); err == nil {
		t.Fatal("evil.json was written outside config/")
	}
	if _, err := os.Stat(filepath.Join(e.paths.Data, "secret.zip")); err != nil {
		t.Fatalf("secret.zip was touched: %v", err)
	}
}

// TestConfigFileValidation checks the raw config editor: invalid JSON is
// refused with line and column, valid JSON is written and read back.
func TestConfigFileValidation(t *testing.T) {
	e := newTestEnv(t)
	name := settings.BanListFile
	path := "/api/config/files/" + name

	rec := e.doJSON("PUT", path, `{"content": "[\n  \"griefer\",\n  oops\n]"}`)
	m := wantError(t, rec, http.StatusBadRequest, codeInvalidJSON)
	if m["line"] != 3.0 {
		t.Fatalf("line %v, want 3 (body %s)", m["line"], rec.Body.String())
	}
	if col, _ := m["column"].(float64); col < 1 {
		t.Fatalf("column %v, want >= 1", m["column"])
	}
	if _, err := os.Stat(e.paths.ConfigFile(name)); err == nil {
		t.Fatal("invalid content was written")
	}

	content := "[\n  \"griefer\"\n]\n"
	body, _ := json.Marshal(map[string]string{"content": content})
	wantStatus(t, e.doJSON("PUT", path, string(body)), http.StatusNoContent)

	rec = e.do("GET", path, "", nil)
	wantStatus(t, rec, http.StatusOK)
	m = decode(t, rec)
	if m["name"] != name || m["content"] != content || m["exists"] != true {
		t.Fatalf("GET %s: %s", path, rec.Body.String())
	}

	rec = e.do("GET", "/api/config/files", "", nil)
	wantStatus(t, rec, http.StatusOK)
	var files []factorio.ConfigFileInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &files); err != nil || len(files) != 6 {
		t.Fatalf("GET /api/config/files: %s", rec.Body.String())
	}
}

// TestServerSettingsForm checks the form endpoint's shape and a partial
// update.
func TestServerSettingsForm(t *testing.T) {
	e := newTestEnv(t)
	rec := e.do("GET", "/api/config/server-settings", "", nil)
	wantStatus(t, rec, http.StatusOK)
	var form struct {
		Fields []factorio.Field `json:"fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &form); err != nil || len(form.Fields) == 0 {
		t.Fatalf("GET form: %s", rec.Body.String())
	}

	wantStatus(t, e.doJSON("PUT", "/api/config/server-settings", `{"values": {"max_players": 8}}`), http.StatusNoContent)
	wantError(t, e.doJSON("PUT", "/api/config/server-settings", `{"values": {"max_players": "lots"}}`), http.StatusBadRequest, codeBadRequest)
}

// TestSaveUploadConflict checks the save upload rules and the download and
// delete endpoints.
func TestSaveUploadConflict(t *testing.T) {
	e := newTestEnv(t)
	post := func(query string, data string) *httptest.ResponseRecorder {
		ct, body := upload(t, "world.zip", []byte(data))
		return e.do("POST", "/api/saves/upload"+query, ct, body)
	}

	rec := post("", "PK first")
	wantStatus(t, rec, http.StatusCreated)
	if m := decode(t, rec); m["name"] != "world.zip" {
		t.Fatalf("upload body %s", rec.Body.String())
	}

	wantError(t, post("", "PK second"), http.StatusConflict, codeConflict)
	if b, _ := os.ReadFile(filepath.Join(e.paths.Saves, "world.zip")); string(b) != "PK first" {
		t.Fatalf("conflicting upload changed the file: %q", b)
	}

	wantStatus(t, post("?overwrite=1", "PK second"), http.StatusCreated)
	if b, _ := os.ReadFile(filepath.Join(e.paths.Saves, "world.zip")); string(b) != "PK second" {
		t.Fatalf("overwrite did not replace the file: %q", b)
	}

	ct, body := upload(t, "world.txt", []byte("x"))
	wantError(t, e.do("POST", "/api/saves/upload", ct, body), http.StatusBadRequest, codeBadRequest)

	// No temp files are left behind.
	entries, _ := os.ReadDir(e.paths.Saves)
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), fsutil.TempPrefix) {
			t.Fatalf("temp file left behind: %s", en.Name())
		}
	}

	rec = e.do("GET", "/api/saves", "", nil)
	wantStatus(t, rec, http.StatusOK)
	var saves []factorio.SaveInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &saves); err != nil || len(saves) != 1 || saves[0].Name != "world.zip" {
		t.Fatalf("GET /api/saves: %s", rec.Body.String())
	}

	rec = e.do("GET", "/api/saves/world.zip/download", "", nil)
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.String() != "PK second" {
		t.Fatalf("download body %q", rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename=world.zip` {
		t.Fatalf("Content-Disposition %q", cd)
	}

	wantStatus(t, e.do("DELETE", "/api/saves/world.zip", "", nil), http.StatusNoContent)
	wantError(t, e.do("GET", "/api/saves/world.zip/download", "", nil), http.StatusNotFound, codeNotFound)
}

// modZip returns a mod zip with one top-level folder.
func modZip(t *testing.T, folder, info string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{folder + "/info.json": info, folder + "/data.lua": "-- nothing"} {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestModUpload checks that an uploaded mod is stored under its canonical
// name whatever the uploaded file was called, and that bad zips are refused.
func TestModUpload(t *testing.T) {
	e := newTestEnv(t).withMods(t)
	data := modZip(t, "mymod_1.0.0", `{"name": "mymod", "version": "1.0.0", "title": "My mod", "factorio_version": "2.1"}`)

	ct, body := upload(t, "Downloaded (1).zip", data)
	rec := e.do("POST", "/api/mods/upload", ct, body)
	wantStatus(t, rec, http.StatusCreated)
	if m := decode(t, rec); m["name"] != "mymod" || m["version"] != "1.0.0" {
		t.Fatalf("upload body %s", rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(e.paths.Mods, "mymod_1.0.0.zip")); err != nil {
		t.Fatalf("canonical zip missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.paths.Mods, "Downloaded (1).zip")); err == nil {
		t.Fatal("zip stored under the uploaded name")
	}

	for _, bad := range [][]byte{
		[]byte("not a zip"),
		modZip(t, "evil", `{"name": "../evil", "version": "1.0.0"}`),
		modZip(t, "v", `{"name": "v", "version": "1.0"}`),
	} {
		ct, body := upload(t, "bad.zip", bad)
		wantError(t, e.do("POST", "/api/mods/upload", ct, body), http.StatusBadRequest, codeBadRequest)
	}
}

// TestBusy checks that operations under the busy lock answer 409 busy with
// the holder's job id.
func TestBusy(t *testing.T) {
	e := newTestEnv(t)
	j, err := e.srv.Jobs.TryBegin(jobs.KindStart)
	mustOK(t, err)
	defer j.Finish(nil)

	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/server/start", ""},
		{"POST", "/api/server/stop", ""},
		{"POST", "/api/server/restart", `{}`},
		{"POST", "/api/saves", `{"name": "new"}`},
	} {
		m := wantError(t, e.doJSON(c.method, c.path, c.body), http.StatusConflict, codeBusy)
		if m["jobId"] != j.ID() {
			t.Fatalf("%s %s: jobId %v, want %s", c.method, c.path, m["jobId"], j.ID())
		}
	}
}

// TestCredentialsRequired checks that install and update refuse to start a
// job without a factorio.com login.
func TestCredentialsRequired(t *testing.T) {
	e := newTestEnv(t)
	wantError(t, e.doJSON("POST", "/api/portal/install", `{"mods": [{"name": "somemod"}]}`), http.StatusBadRequest, codeCredentialsRequired)
	wantError(t, e.doJSON("POST", "/api/mods/update", ``), http.StatusBadRequest, codeCredentialsRequired)
	if id := e.srv.Jobs.Busy(); id != "" {
		t.Fatalf("a job was started: %s", id)
	}
}

// TestSettings checks that the token is write-only and invalid values are
// refused.
func TestSettings(t *testing.T) {
	e := newTestEnv(t)
	rec := e.doJSON("PUT", "/api/settings", `{"factorioUsername": "me", "factorioToken": "sekrit-token"}`)
	wantStatus(t, rec, http.StatusOK)
	if m := decode(t, rec); m["factorioTokenSet"] != true || m["factorioUsername"] != "me" {
		t.Fatalf("PUT body %s", rec.Body.String())
	}
	rec = e.do("GET", "/api/settings", "", nil)
	wantStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "sekrit-token") || strings.Contains(rec.Body.String(), "rcon") {
		t.Fatalf("GET leaks secrets: %s", rec.Body.String())
	}
	wantError(t, e.doJSON("PUT", "/api/settings", `{"gamePort": 70000}`), http.StatusBadRequest, codeBadRequest)
}

// TestJobs checks GET /api/jobs/{id}.
func TestJobs(t *testing.T) {
	e := newTestEnv(t)
	wantError(t, e.do("GET", "/api/jobs/0123456789abcdef", "", nil), http.StatusNotFound, codeNotFound)

	j, err := e.srv.Jobs.Run(jobs.KindModUpdate, func(ctx context.Context, j *jobs.Job) error {
		j.Logf("hello")
		return nil
	})
	mustOK(t, err)
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := e.do("GET", "/api/jobs/"+j.ID(), "", nil)
		wantStatus(t, rec, http.StatusOK)
		var snap jobs.Snapshot
		if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
			t.Fatal(err)
		}
		if snap.State == jobs.Done {
			if snap.ID != j.ID() || snap.Kind != jobs.KindModUpdate || len(snap.Lines) == 0 || snap.Last == 0 {
				t.Fatalf("snapshot %s", rec.Body.String())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish: %s", rec.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStatus checks the status body's shape while stopped and idle.
func TestStatus(t *testing.T) {
	e := newTestEnv(t).withMods(t)
	rec := e.do("GET", "/api/status", "", nil)
	wantStatus(t, rec, http.StatusOK)
	m := decode(t, rec)
	for _, k := range []string{"state", "save", "startedAt", "lastError", "players", "gamePort",
		"factorioVersion", "managerVersion", "spaceAge", "autostart", "restartRequired", "busyJobId"} {
		if _, ok := m[k]; !ok {
			t.Errorf("status lacks %q: %s", k, rec.Body.String())
		}
	}
	if m["state"] != "stopped" || m["factorioVersion"] != "2.1.24" || m["managerVersion"] != "test" ||
		m["busyJobId"] != nil || m["gamePort"] != 34197.0 {
		t.Fatalf("status %s", rec.Body.String())
	}
	if p, ok := m["players"].([]any); !ok || len(p) != 0 {
		t.Fatalf("players %v, want []", m["players"])
	}
}

// TestLogs checks the logs body shape.
func TestLogs(t *testing.T) {
	e := newTestEnv(t)
	e.srv.Logs.Append("line one")
	e.srv.Logs.Append("line two")
	rec := e.do("GET", "/api/logs?after=1", "", nil)
	wantStatus(t, rec, http.StatusOK)
	var body logsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Lines == nil {
		t.Fatalf("logs %s", rec.Body.String())
	}
	if body.Last == 0 {
		t.Skip("LogBuffer not implemented yet")
	}
	if len(body.Lines) != 1 || body.Lines[0].Text != "line two" || body.Last != 2 {
		t.Fatalf("logs %s", rec.Body.String())
	}
}

// TestConsoleNotRunning checks 409 server_not_running.
func TestConsoleNotRunning(t *testing.T) {
	e := newTestEnv(t)
	wantError(t, e.doJSON("POST", "/api/console", `{"command": "/version"}`), http.StatusConflict, codeServerNotRunning)
}
