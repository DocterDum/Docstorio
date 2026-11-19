package portal

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"docstorio/internal/mods"
)

// fakeClock is a settable clock for cache lifetimes.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Add(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

// fakePortal serves the parts of the portal API that the client uses.
type fakePortal struct {
	t        *testing.T
	srv      *httptest.Server
	mux      *http.ServeMux
	requests atomic.Int64
	fail     atomic.Bool // answer every request with 500
}

func newFakePortal(t *testing.T) *fakePortal {
	t.Helper()
	f := &fakePortal{t: t, mux: http.NewServeMux()}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if got := r.Header.Get("User-Agent"); got != "docstorio/1.2.3" {
			t.Errorf("User-Agent = %q", got)
		}
		if f.fail.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePortal) client(clock *fakeClock, cacheDir string) *Client {
	return New(Config{
		BaseURL:   f.srv.URL + "/",
		AssetsURL: "https://assets.example/",
		CacheDir:  cacheDir,
		Factorio:  mods.Version{Major: 2, Minor: 1, Patch: 24},
		Version:   "1.2.3",
		HTTP:      f.srv.Client(),
		Now:       clock.Now,
	})
}

// writeJSON answers with v as JSON, gzipped when the client asks for it.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	data, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		gz.Write(data)
		gz.Close()
		data = buf.Bytes()
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.WriteHeader(status)
	w.Write(data)
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
}

func release(version, fv, released string) map[string]any {
	return map[string]any{
		"download_url": "/download/x/" + version,
		"file_name":    "x_" + version + ".zip",
		"info_json":    map[string]any{"factorio_version": fv},
		"released_at":  released,
		"version":      version,
		"sha1":         "abc",
	}
}

// listBody is a small /api/mods?version=2.1 answer.
func listBody() map[string]any {
	return map[string]any{
		"pagination": nil,
		"results": []map[string]any{
			{"name": "alpha", "title": "Alpha Mod", "owner": "zed", "summary": "Makes belts faster",
				"downloads_count": 10, "category": "tweaks",
				"latest_release": release("1.0.0", "2.1", "2026-01-01T00:00:00.000000Z")},
			{"name": "beta", "title": "Beta", "owner": "Ann", "summary": "Trains",
				"downloads_count": 500, "category": "content", "requires_space_age": true,
				"latest_release": release("2.0.0", "2.1", "2025-06-01T00:00:00Z")},
			{"name": "Gamma", "title": "Gamma", "owner": "bob", "summary": "More BELTS",
				"downloads_count": 50, "category": "",
				"latest_release": release("0.1.0", "2.1", "2026-03-01T00:00:00Z")},
			{"name": "norelease", "title": "No release", "owner": "x", "summary": "belts",
				"downloads_count": 9999, "latest_release": nil},
		},
	}
}

func (f *fakePortal) serveList(count *atomic.Int64) {
	f.mux.HandleFunc("GET /api/mods", func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		q := r.URL.Query()
		if q.Get("page_size") != "max" || q.Get("version") != "2.1" {
			f.t.Errorf("list query = %q", r.URL.RawQuery)
		}
		if r.Header.Get("Accept-Encoding") != "gzip" {
			f.t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
		}
		writeJSON(w, r, http.StatusOK, listBody())
	})
}

func names(p Page) []string {
	out := []string{}
	for _, r := range p.Results {
		out = append(out, r.Name)
	}
	return out
}

func TestSearchFetchesGzipListAndCachesOnDisk(t *testing.T) {
	f := newFakePortal(t)
	var lists atomic.Int64
	f.serveList(&lists)
	clock := newClock()
	dir := t.TempDir()
	c := f.client(clock, dir)
	ctx := context.Background()

	p, err := c.Search(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 3 || len(p.Results) != 3 {
		t.Fatalf("total %d, results %v", p.Total, names(p))
	}
	if lists.Load() != 1 {
		t.Fatalf("list requests = %d", lists.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, "mods-2.1.json")); err != nil {
		t.Fatalf("cache file: %v", err)
	}

	clock.Add(time.Hour)
	p, err = c.Search(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if lists.Load() != 1 {
		t.Fatalf("fresh list fetched again: %d", lists.Load())
	}
	if p.CacheAge != 3600 {
		t.Fatalf("cacheAge = %d", p.CacheAge)
	}

	// A new client (manager restart) uses the disk cache.
	c2 := f.client(clock, dir)
	p, err = c2.Search(ctx, Query{Q: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if lists.Load() != 1 || len(p.Results) != 1 || p.CacheAge != 3600 {
		t.Fatalf("disk cache not used: requests %d, %v, age %d", lists.Load(), names(p), p.CacheAge)
	}
	r := p.Results[0]
	want := Result{Name: "beta", Title: "Beta", Owner: "Ann", Summary: "Trains", Downloads: 500,
		LatestVersion: "2.0.0", Updated: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
		Category: "content", RequiresSpaceAge: true}
	if r.Name != want.Name || r.Title != want.Title || r.Owner != want.Owner || r.Summary != want.Summary ||
		r.Downloads != want.Downloads || r.LatestVersion != want.LatestVersion || !r.Updated.Equal(want.Updated) ||
		r.Category != want.Category || r.RequiresSpaceAge != want.RequiresSpaceAge || r.Installed != nil {
		t.Fatalf("result = %+v", r)
	}
}

func TestSearchLazyRefreshAndFallback(t *testing.T) {
	f := newFakePortal(t)
	var lists atomic.Int64
	f.serveList(&lists)
	clock := newClock()
	c := f.client(clock, t.TempDir())
	ctx := context.Background()

	if _, err := c.Search(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	clock.Add(ListMaxAge + time.Second)
	if _, err := c.Search(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	if lists.Load() != 2 {
		t.Fatalf("stale list not refreshed: %d", lists.Load())
	}

	// Portal down: the old list is still used.
	f.fail.Store(true)
	clock.Add(ListMaxAge + time.Second)
	p, err := c.Search(ctx, Query{})
	if err != nil {
		t.Fatalf("fallback to old list: %v", err)
	}
	if p.Total != 3 {
		t.Fatalf("total = %d", p.Total)
	}
}

func TestSearchWithoutListFails(t *testing.T) {
	f := newFakePortal(t)
	f.fail.Store(true)
	c := f.client(newClock(), t.TempDir())
	p, err := c.Search(context.Background(), Query{})
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != http.StatusInternalServerError {
		t.Fatalf("err = %v", err)
	}
	if p.Results == nil {
		t.Fatal("results must not be nil")
	}
}

func TestRefreshFetchesEvenWhenFresh(t *testing.T) {
	f := newFakePortal(t)
	var lists atomic.Int64
	f.serveList(&lists)
	c := f.client(newClock(), t.TempDir())
	ctx := context.Background()
	if _, err := c.Search(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if lists.Load() != 2 {
		t.Fatalf("list requests = %d", lists.Load())
	}
	f.fail.Store(true)
	if err := c.Refresh(ctx); err == nil {
		t.Fatal("refresh against a failing portal succeeded")
	}
	if _, err := c.Search(ctx, Query{}); err != nil {
		t.Fatalf("old list lost after failed refresh: %v", err)
	}
}

func TestSearchMatchSortAndPaging(t *testing.T) {
	f := newFakePortal(t)
	var lists atomic.Int64
	f.serveList(&lists)
	c := f.client(newClock(), t.TempDir())
	ctx := context.Background()

	// Default sort is downloads; the mod with a null latest_release is
	// skipped; q matches name, title, owner and summary case-insensitively;
	// Page 0 means 1 and PageSize <= 0 means the default.
	tests := []struct {
		q    Query
		want string
	}{
		{Query{}, "beta,Gamma,alpha"},
		{Query{Sort: SortDownloads}, "beta,Gamma,alpha"},
		{Query{Sort: SortUpdated}, "Gamma,alpha,beta"},
		{Query{Sort: SortName}, "alpha,beta,Gamma"},
		{Query{Q: "belts"}, "Gamma,alpha"},
		{Query{Q: "  ANN "}, "beta"},
		{Query{Q: "alpha mod"}, "alpha"},
		{Query{Q: "gam"}, "Gamma"},
		{Query{Q: "nothing"}, ""},
		{Query{Sort: SortName, PageSize: 2}, "alpha,beta"},
		{Query{Sort: SortName, PageSize: 2, Page: 2}, "Gamma"},
		{Query{Sort: SortName, PageSize: 2, Page: 9}, ""},
		{Query{Sort: SortName, PageSize: -1, Page: -3}, "alpha,beta,Gamma"},
	}
	for _, tt := range tests {
		p, err := c.Search(ctx, tt.q)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(names(p), ","); got != tt.want {
			t.Errorf("%+v: got %q, want %q", tt.q, got, tt.want)
		}
		if p.Results == nil {
			t.Errorf("%+v: nil results", tt.q)
		}
	}
	p, _ := c.Search(ctx, Query{Sort: SortName, PageSize: 2, Page: 2})
	if p.Total != 3 {
		t.Errorf("total = %d, want matches before paging", p.Total)
	}
}

func TestUpstreamErrorCarriesPortalMessage(t *testing.T) {
	f := newFakePortal(t)
	f.mux.HandleFunc("GET /api/mods", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, http.StatusBadRequest, map[string]string{"message": "Invalid factorio version"})
	})
	c := f.client(newClock(), t.TempDir())
	_, err := c.Search(context.Background(), Query{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400: Invalid factorio version") {
		t.Fatalf("err = %v", err)
	}
}

func fullBody() map[string]any {
	r1 := release("1.0.0", "2.0", "2025-01-01T00:00:00Z")
	r2 := release("1.1.0", "2.1", "2026-01-01T00:00:00Z")
	r2["info_json"] = map[string]any{"factorio_version": "2.1", "dependencies": []string{"base >= 2.1", "? quality"}}
	return map[string]any{
		"name": "My Mod", "title": "My Mod Title", "owner": "zed", "summary": "Sum",
		"description": "# Desc", "homepage": "https://home.example", "source_url": nil,
		"thumbnail": "/assets/abc.thumb.png",
		"releases":  []any{r1, r2},
	}
}

func TestDetailsAndReleases(t *testing.T) {
	f := newFakePortal(t)
	var fulls atomic.Int64
	f.mux.HandleFunc("GET /api/mods/{name}/full", func(w http.ResponseWriter, r *http.Request) {
		fulls.Add(1)
		if r.PathValue("name") != "My Mod" {
			writeJSON(w, r, http.StatusNotFound, map[string]string{"message": "Mod not found"})
			return
		}
		writeJSON(w, r, http.StatusOK, fullBody())
	})
	clock := newClock()
	c := f.client(clock, t.TempDir())
	ctx := context.Background()

	d, err := c.Details(ctx, "My Mod")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "My Mod" || d.Title != "My Mod Title" || d.Owner != "zed" || d.Summary != "Sum" ||
		d.Description != "# Desc" || d.Homepage != "https://home.example" || d.SourceURL != "" || d.Installed != nil {
		t.Fatalf("details = %+v", d)
	}
	if len(d.Releases) != 2 {
		t.Fatalf("releases = %+v", d.Releases)
	}
	r0, r1 := d.Releases[0], d.Releases[1]
	if r0.Version != "1.0.0" || r0.FactorioVersion != "2.0" || r0.Compatible || r0.Dependencies == nil || len(r0.Dependencies) != 0 {
		t.Errorf("release 0 = %+v", r0)
	}
	if r1.Version != "1.1.0" || !r1.Compatible || strings.Join(r1.Dependencies, "|") != "base >= 2.1|? quality" ||
		!r1.Released.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("release 1 = %+v", r1)
	}

	rels, err := c.Releases(ctx, "My Mod")
	if err != nil {
		t.Fatal(err)
	}
	if fulls.Load() != 1 {
		t.Fatalf("Releases did not use the details cache: %d requests", fulls.Load())
	}
	if len(rels) != 2 || rels[0].Dependencies == nil || rels[1].Name != "My Mod" ||
		rels[1].DownloadURL != "/download/x/1.1.0" || rels[1].SHA1 != "abc" || len(rels[1].Dependencies) != 2 {
		t.Fatalf("releases = %+v", rels)
	}

	clock.Add(DetailsMaxAge - time.Second)
	if _, err := c.Details(ctx, "My Mod"); err != nil {
		t.Fatal(err)
	}
	if fulls.Load() != 1 {
		t.Fatalf("cached details fetched again")
	}
	clock.Add(2 * time.Second)
	if _, err := c.Details(ctx, "My Mod"); err != nil {
		t.Fatal(err)
	}
	if fulls.Load() != 2 {
		t.Fatalf("expired details not fetched again")
	}

	if _, err := c.Details(ctx, "unknown"); !errors.Is(err, mods.ErrNotFound) {
		t.Fatalf("unknown mod: %v", err)
	}
	if _, err := c.Releases(ctx, "unknown"); !errors.Is(err, mods.ErrNotFound) {
		t.Fatalf("unknown mod releases: %v", err)
	}
	f.fail.Store(true)
	clock.Add(DetailsMaxAge)
	var ue *UpstreamError
	if _, err := c.Details(ctx, "My Mod"); !errors.As(err, &ue) {
		t.Fatalf("portal failure: %v", err)
	}
}

func TestThumbnail(t *testing.T) {
	f := newFakePortal(t)
	var lookups atomic.Int64
	f.mux.HandleFunc("GET /api/mods/{name}", func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		switch r.PathValue("name") {
		case "with thumb":
			writeJSON(w, r, http.StatusOK, map[string]any{"name": "with thumb", "thumbnail": "/assets/abc.thumb.png"})
		case "none":
			writeJSON(w, r, http.StatusOK, map[string]any{"name": "none", "thumbnail": nil})
		default:
			writeJSON(w, r, http.StatusNotFound, map[string]string{"message": "Mod not found"})
		}
	})
	dir := t.TempDir()
	clock := newClock()
	c := f.client(clock, dir)
	ctx := context.Background()

	u, err := c.Thumbnail(ctx, "with thumb")
	if err != nil {
		t.Fatal(err)
	}
	if u != "https://assets.example/assets/abc.thumb.png" {
		t.Fatalf("url = %q", u)
	}
	if _, err := c.Thumbnail(ctx, "none"); !errors.Is(err, mods.ErrNotFound) {
		t.Fatalf("no thumbnail: %v", err)
	}
	if _, err := c.Thumbnail(ctx, "unknown"); !errors.Is(err, mods.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if lookups.Load() != 3 {
		t.Fatalf("lookups = %d", lookups.Load())
	}

	// Cached, also across a restart (thumbs.json).
	c.Thumbnail(ctx, "with thumb")
	c.Thumbnail(ctx, "none")
	c2 := f.client(clock, dir)
	if u, err := c2.Thumbnail(ctx, "with thumb"); err != nil || u != "https://assets.example/assets/abc.thumb.png" {
		t.Fatalf("from disk: %q %v", u, err)
	}
	if _, err := c2.Thumbnail(ctx, "none"); !errors.Is(err, mods.ErrNotFound) {
		t.Fatalf("none from disk: %v", err)
	}
	if lookups.Load() != 3 {
		t.Fatalf("cache not used: lookups = %d", lookups.Load())
	}
	data, err := os.ReadFile(filepath.Join(dir, "thumbs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]string
	if err := json.Unmarshal(data, &disk); err != nil || len(disk) != 2 || disk["none"] != "" {
		t.Fatalf("thumbs.json = %s (%v)", data, err)
	}
}

func TestUpdateReleases(t *testing.T) {
	f := newFakePortal(t)
	var calls atomic.Int64
	var mu sync.Mutex
	var asked []string
	f.mux.HandleFunc("GET /api/mods", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("page_size") != "max" {
			t.Errorf("page_size = %q", q.Get("page_size"))
		}
		list := strings.Split(q.Get("namelist"), ",")
		if len(list) > NamelistChunk {
			t.Errorf("chunk of %d names", len(list))
		}
		mu.Lock()
		asked = append(asked, list...)
		mu.Unlock()
		var results []map[string]any
		for _, n := range list {
			if strings.HasPrefix(n, "unknown") {
				continue
			}
			results = append(results, map[string]any{"name": n, "releases": []any{
				release("1.0.0", "2.0", "2025-01-01T00:00:00Z"),
				release("1.1.0", "2.1", "2026-01-01T00:00:00Z"),
			}})
		}
		writeJSON(w, r, http.StatusOK, map[string]any{"pagination": nil, "results": results})
	})
	clock := newClock()
	c := f.client(clock, t.TempDir())
	ctx := context.Background()

	var req []string
	for i := range NamelistChunk + 10 {
		req = append(req, "mod "+string(rune('a'+i%26))+strings.Repeat("x", i))
	}
	req = append(req, "unknown1", req[0]) // duplicate is asked once

	got, err := c.UpdateReleases(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want 2 chunks", calls.Load())
	}
	if len(asked) != NamelistChunk+11 {
		t.Fatalf("asked %d names", len(asked))
	}
	if len(got) != NamelistChunk+10 {
		t.Fatalf("got %d mods", len(got))
	}
	if _, ok := got["unknown1"]; ok {
		t.Fatal("unknown name present")
	}
	rels := got[req[0]]
	if len(rels) != 2 || rels[1].Version != "1.1.0" || rels[1].FactorioVersion != "2.1" ||
		rels[1].Name != req[0] || rels[1].Dependencies != nil || rels[1].SHA1 != "abc" {
		t.Fatalf("releases = %+v", rels)
	}

	// Cached for UpdatesMaxAge, unknown names included.
	clock.Add(UpdatesMaxAge - time.Second)
	got, err = c.UpdateReleases(ctx, []string{req[0], "unknown1"})
	if err != nil || calls.Load() != 2 || len(got) != 1 {
		t.Fatalf("cache not used: %v, calls %d, %d mods", err, calls.Load(), len(got))
	}
	clock.Add(2 * time.Second)
	if _, err := c.UpdateReleases(ctx, []string{req[0]}); err != nil || calls.Load() != 3 {
		t.Fatalf("expired entry not fetched: %v, calls %d", err, calls.Load())
	}

	f.fail.Store(true)
	var ue *UpstreamError
	if _, err := c.UpdateReleases(ctx, []string{"new"}); !errors.As(err, &ue) {
		t.Fatalf("portal failure: %v", err)
	}
	if got, err := c.UpdateReleases(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty names: %v %v", got, err)
	}
}

const testToken = "s3cr3t-token"

func zipBytes() []byte {
	return append([]byte("PK\x03\x04"), bytes.Repeat([]byte("zipdata"), 1000)...)
}

func sha1Hex(b []byte) string {
	s := sha1.Sum(b)
	return hex.EncodeToString(s[:])
}

func TestDownload(t *testing.T) {
	f := newFakePortal(t)
	good := zipBytes()
	f.mux.HandleFunc("GET /download/{what}", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("username") != "user" || q.Get("token") != testToken {
			http.Redirect(w, r, "/login?next=/download", http.StatusFound)
			return
		}
		switch r.PathValue("what") {
		case "good":
			w.Write(good)
		case "forbidden":
			http.Error(w, "<html>forbidden</html>", http.StatusForbidden)
		case "html":
			w.Write([]byte("<html>not a zip</html>"))
		case "gone":
			http.Error(w, "gone", http.StatusGone)
		}
	})
	f.mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("PK login page that must not be followed"))
	})
	c := f.client(newClock(), t.TempDir())
	ctx := context.Background()
	creds := mods.Credentials{Username: "user", Token: testToken}

	dest := t.TempDir()
	other := filepath.Join(dest, "my mod_0.9.0.zip")
	if err := os.WriteFile(other, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel := mods.Release{Name: "my mod", Version: "1.0.0", DownloadURL: "/download/good", SHA1: strings.ToUpper(sha1Hex(good))}
	path, err := c.Download(ctx, rel, creds, dest)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dest, "my mod_1.0.0.zip") {
		t.Fatalf("path = %q", path)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, good) {
		t.Fatalf("downloaded file: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other version touched: %v", err)
	}

	checkFails := func(name string, rel mods.Release, creds mods.Credentials, wantCreds bool) {
		t.Helper()
		_, err := c.Download(ctx, rel, creds, dest)
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		if errors.Is(err, mods.ErrCredentials) != wantCreds {
			t.Fatalf("%s: err = %v, credentials error wanted: %v", name, err, wantCreds)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatalf("%s: token in error %q", name, err)
		}
	}
	bad := mods.Release{Name: "bad", Version: "1.0.0", SHA1: "00"}
	withURL := func(u, sum string) mods.Release { r := bad; r.DownloadURL = u; r.SHA1 = sum; return r }

	checkFails("redirect to login", withURL("/download/good", sha1Hex(good)), mods.Credentials{Username: "user", Token: "wrong"}, true)
	checkFails("403", withURL("/download/forbidden", "00"), creds, true)
	checkFails("no credentials", withURL("/download/good", sha1Hex(good)), mods.Credentials{}, true)
	checkFails("not a zip", withURL("/download/html", "00"), creds, false)
	checkFails("sha1 mismatch", withURL("/download/good", sha1Hex([]byte("other"))), creds, false)
	checkFails("http error", withURL("/download/gone", "00"), creds, false)
	checkFails("bad url", withURL("https://evil.example/x", "00"), creds, false)
	checkFails("bad name", mods.Release{Name: "../x", Version: "1.0.0", DownloadURL: "/download/good", SHA1: sha1Hex(good)}, creds, false)

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, ",") != "my mod_0.9.0.zip,my mod_1.0.0.zip" {
		t.Fatalf("files left in dest: %v", left)
	}

	// Transport errors must not leak the token (url.Error holds the URL).
	f.srv.Close()
	checkFails("server down", withURL("/download/good", sha1Hex(good)), creds, false)
}
