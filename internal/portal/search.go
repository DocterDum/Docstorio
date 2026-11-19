package portal

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"docstorio/internal/fsutil"
)

// Sort orders search results.
type Sort string

// Search sort orders; "" (or anything unknown) means SortDownloads.
const (
	SortDownloads Sort = "downloads" // downloads_count descending
	SortUpdated   Sort = "updated"   // latest_release.released_at descending
	SortName      Sort = "name"      // name ascending, case-insensitive
)

// DefaultPageSize is used when Query.PageSize is 0 or negative.
const DefaultPageSize = 24

// Query is a search request. Page starts at 1 (0 means 1); PageSize 0 means
// DefaultPageSize.
type Query struct {
	Q        string
	Sort     Sort
	Page     int
	PageSize int
}

// Result is one search hit (GET /api/portal/search "results").
type Result struct {
	Name             string    `json:"name"`
	Title            string    `json:"title"`
	Owner            string    `json:"owner"`
	Summary          string    `json:"summary"`
	Downloads        int       `json:"downloads"`
	LatestVersion    string    `json:"latestVersion"`
	Updated          time.Time `json:"updated"` // latest_release.released_at
	Category         string    `json:"category"`
	RequiresSpaceAge bool      `json:"requiresSpaceAge"`
	// Installed is filled by the api from mods.Manager.InstalledVersions;
	// portal leaves it nil.
	Installed *string `json:"installed"`
}

// Page is the body of GET /api/portal/search.
type Page struct {
	Total int `json:"total"` // matches before paging
	// CacheAge is the age of the cached list in whole seconds.
	CacheAge int64    `json:"cacheAge"`
	Results  []Result `json:"results"` // never nil
}

// listMod is one entry of the portal list (and of the disk cache, which
// keeps the portal's field names).
type listMod struct {
	Name             string         `json:"name"`
	Title            string         `json:"title"`
	Owner            string         `json:"owner"`
	Summary          string         `json:"summary"`
	DownloadsCount   int            `json:"downloads_count"`
	Category         string         `json:"category"`
	RequiresSpaceAge bool           `json:"requires_space_age"`
	LatestRelease    *portalRelease `json:"latest_release"`
}

// modList is the cached list, in memory and in manager/cache/mods-X.Y.json.
type modList struct {
	FetchedAt time.Time `json:"fetchedAt"`
	Mods      []listMod `json:"mods"`
}

// Search matches q case-insensitively against name, title, owner and summary
// (empty q matches all) over the cached list, refreshing it first when
// missing or older than ListMaxAge. Mods without a latest_release are
// skipped. If a refresh fails but a cached list exists, the old list is
// used.
func (c *Client) Search(ctx context.Context, q Query) (Page, error) {
	list, err := c.currentList(ctx)
	if err != nil {
		return Page{Results: []Result{}}, err
	}

	needle := strings.ToLower(strings.TrimSpace(q.Q))
	var hits []*listMod
	for i := range list.Mods {
		m := &list.Mods[i]
		if m.LatestRelease == nil {
			continue
		}
		if needle == "" || matches(m, needle) {
			hits = append(hits, m)
		}
	}
	sortHits(hits, q.Sort)

	size := q.PageSize
	if size <= 0 {
		size = DefaultPageSize
	}
	page := max(q.Page, 1)
	start := min((page-1)*size, len(hits))
	end := min(start+size, len(hits))

	results := make([]Result, 0, end-start)
	for _, m := range hits[start:end] {
		results = append(results, Result{
			Name:             m.Name,
			Title:            m.Title,
			Owner:            m.Owner,
			Summary:          m.Summary,
			Downloads:        m.DownloadsCount,
			LatestVersion:    m.LatestRelease.Version,
			Updated:          m.LatestRelease.ReleasedAt,
			Category:         m.Category,
			RequiresSpaceAge: m.RequiresSpaceAge,
		})
	}
	age := int64(c.cfg.Now().Sub(list.FetchedAt) / time.Second)
	return Page{Total: len(hits), CacheAge: max(age, 0), Results: results}, nil
}

func matches(m *listMod, needle string) bool {
	for _, field := range []string{m.Name, m.Title, m.Owner, m.Summary} {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

func sortHits(hits []*listMod, s Sort) {
	byName := func(a, b *listMod) int {
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	}
	switch s {
	case SortName:
		slices.SortStableFunc(hits, byName)
	case SortUpdated:
		slices.SortStableFunc(hits, func(a, b *listMod) int {
			if c := b.LatestRelease.ReleasedAt.Compare(a.LatestRelease.ReleasedAt); c != 0 {
				return c
			}
			return byName(a, b)
		})
	default:
		slices.SortStableFunc(hits, func(a, b *listMod) int {
			if c := cmp.Compare(b.DownloadsCount, a.DownloadsCount); c != 0 {
				return c
			}
			return byName(a, b)
		})
	}
}

// Refresh downloads the list now (POST /api/portal/refresh). The api calls
// it in a goroutine and answers 202. On failure the old list is kept.
func (c *Client) Refresh(ctx context.Context) error {
	c.listMu.Lock()
	defer c.listMu.Unlock()
	_, err := c.fetchList(ctx)
	return err
}

// currentList returns the list, loading it from disk or downloading it when
// missing or older than ListMaxAge. A failed download falls back to the old
// list when there is one.
func (c *Client) currentList(ctx context.Context) (*modList, error) {
	if l := c.cachedList(); l != nil && c.fresh(l) {
		return l, nil
	}

	c.listMu.Lock()
	defer c.listMu.Unlock()
	// Another search may have refreshed the list while we waited.
	l := c.cachedList()
	if l == nil {
		l = c.readListFile()
		if l != nil {
			c.mu.Lock()
			c.list = l
			c.mu.Unlock()
		}
	}
	if l != nil && c.fresh(l) {
		return l, nil
	}
	fetched, err := c.fetchList(ctx)
	if fetched != nil {
		// A cache-file write error does not stop the search.
		return fetched, nil
	}
	if l != nil {
		return l, nil
	}
	return nil, err
}

func (c *Client) cachedList() *modList {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.list
}

func (c *Client) fresh(l *modList) bool {
	return c.cfg.Now().Sub(l.FetchedAt) < ListMaxAge
}

// listPath is manager/cache/mods-X.Y.json.
func (c *Client) listPath() string {
	return filepath.Join(c.cfg.CacheDir, "mods-"+c.cfg.Factorio.MajorMinor()+".json")
}

// readListFile loads the disk cache; a missing or unreadable file is nil.
func (c *Client) readListFile() *modList {
	data, err := os.ReadFile(c.listPath())
	if err != nil {
		return nil
	}
	var l modList
	if json.Unmarshal(data, &l) != nil {
		return nil
	}
	return &l
}

// fetchList downloads the list for the running major.minor, stores it in
// memory and writes the disk cache. The caller holds listMu. When only the
// cache write fails, the new list is returned together with the error.
func (c *Client) fetchList(ctx context.Context) (*modList, error) {
	var body struct {
		Results []listMod `json:"results"`
	}
	q := "/api/mods?page_size=max&version=" + c.cfg.Factorio.MajorMinor()
	if err := c.getJSON(ctx, "list", q, &body); err != nil {
		return nil, err
	}
	l := &modList{FetchedAt: c.cfg.Now(), Mods: body.Results}
	if l.Mods == nil {
		l.Mods = []listMod{}
	}
	c.mu.Lock()
	c.list = l
	c.mu.Unlock()

	data, err := json.Marshal(l)
	if err == nil {
		err = fsutil.AtomicWrite(c.listPath(), data, 0o644)
	}
	return l, err
}
