package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"docstorio/internal/fsutil"
	"docstorio/internal/mods"
)

// DetailsRelease is one release in Details, oldest first.
type DetailsRelease struct {
	Version         string    `json:"version"`
	FactorioVersion string    `json:"factorioVersion"`
	Released        time.Time `json:"released"`
	Compatible      bool      `json:"compatible"` // mods.Compatible with Config.Factorio
	Dependencies    []string  `json:"dependencies"`
}

// Details is the body of GET /api/portal/mods/{name}.
type Details struct {
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	Owner       string           `json:"owner"`
	Summary     string           `json:"summary"`
	Description string           `json:"description"` // markdown, shown as plain text
	Homepage    string           `json:"homepage"`
	SourceURL   string           `json:"sourceUrl"`
	Releases    []DetailsRelease `json:"releases"`
	// Installed is filled by the api; portal leaves it nil.
	Installed *string `json:"installed"`
}

// fullMod is the part of /api/mods/{name}/full that is used. JSON null
// leaves a string empty.
type fullMod struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Owner       string          `json:"owner"`
	Summary     string          `json:"summary"`
	Description string          `json:"description"`
	Homepage    string          `json:"homepage"`
	SourceURL   string          `json:"source_url"`
	Releases    []portalRelease `json:"releases"`
}

type detailsEntry struct {
	at  time.Time
	mod *fullMod
}

// Details returns /api/mods/{name}/full (cached DetailsMaxAge). Unknown mod:
// mods.ErrNotFound.
func (c *Client) Details(ctx context.Context, name string) (Details, error) {
	m, err := c.full(ctx, name)
	if err != nil {
		return Details{}, err
	}
	d := Details{
		Name:        m.Name,
		Title:       m.Title,
		Owner:       m.Owner,
		Summary:     m.Summary,
		Description: m.Description,
		Homepage:    m.Homepage,
		SourceURL:   m.SourceURL,
		Releases:    make([]DetailsRelease, 0, len(m.Releases)),
	}
	for _, r := range m.Releases {
		deps := r.InfoJSON.Dependencies
		if deps == nil {
			deps = []string{}
		}
		d.Releases = append(d.Releases, DetailsRelease{
			Version:         r.Version,
			FactorioVersion: r.InfoJSON.FactorioVersion,
			Released:        r.ReleasedAt,
			Compatible:      mods.Compatible(r.InfoJSON.FactorioVersion, c.cfg.Factorio),
			Dependencies:    deps,
		})
	}
	return d, nil
}

// Releases implements mods.ReleaseSource (from the Details cache).
// Dependencies are never nil.
func (c *Client) Releases(ctx context.Context, name string) ([]mods.Release, error) {
	m, err := c.full(ctx, name)
	if err != nil {
		return nil, err
	}
	out := make([]mods.Release, 0, len(m.Releases))
	for _, r := range m.Releases {
		rel := r.toRelease(m.Name)
		if rel.Dependencies == nil {
			rel.Dependencies = []string{}
		}
		out = append(out, rel)
	}
	return out, nil
}

// full returns the cached or freshly fetched /full document of name.
func (c *Client) full(ctx context.Context, name string) (*fullMod, error) {
	if name == "" {
		return nil, fmt.Errorf("empty mod name: %w", mods.ErrNotFound)
	}
	now := c.cfg.Now()
	c.mu.Lock()
	e, ok := c.details[name]
	c.mu.Unlock()
	if ok && now.Sub(e.at) < DetailsMaxAge {
		return e.mod, nil
	}

	var m fullMod
	if err := c.getJSON(ctx, "details", modPath(name)+"/full", &m); err != nil {
		if isStatus(err, http.StatusNotFound) {
			return nil, fmt.Errorf("%s: %w", name, mods.ErrNotFound)
		}
		return nil, err
	}
	if m.Name == "" {
		m.Name = name
	}

	c.mu.Lock()
	for k, old := range c.details {
		if now.Sub(old.at) >= DetailsMaxAge {
			delete(c.details, k)
		}
	}
	c.details[name] = detailsEntry{at: now, mod: &m}
	c.mu.Unlock()
	return &m, nil
}

// noThumbnail is the portal's placeholder path for a mod without one.
const noThumbnail = "/assets/.thumb.png"

// Thumbnail returns the absolute thumbnail URL ({assets}{thumbnail}) for
// name. No thumbnail or unknown mod: mods.ErrNotFound. Lookups of existing
// mods (including "none") are cached in thumbs.json; unknown mods are not.
func (c *Client) Thumbnail(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty mod name: %w", mods.ErrNotFound)
	}
	c.loadThumbs()
	c.mu.Lock()
	thumb, ok := c.thumbs[name]
	c.mu.Unlock()

	if !ok {
		var body struct {
			Thumbnail string `json:"thumbnail"`
		}
		if err := c.getJSON(ctx, "thumbnail", modPath(name), &body); err != nil {
			if isStatus(err, http.StatusNotFound) {
				return "", fmt.Errorf("%s: %w", name, mods.ErrNotFound)
			}
			return "", err
		}
		thumb = body.Thumbnail
		if thumb == noThumbnail {
			thumb = ""
		}
		if thumb != "" && !strings.HasPrefix(thumb, "/") {
			thumb = "/" + thumb
		}
		c.mu.Lock()
		c.thumbs[name] = thumb
		c.mu.Unlock()
		// A failed cache write only costs a lookup next time.
		_ = c.saveThumbs()
	}

	if thumb == "" {
		return "", fmt.Errorf("%s has no thumbnail: %w", name, mods.ErrNotFound)
	}
	return c.cfg.AssetsURL + thumb, nil
}

func (c *Client) thumbsPath() string {
	return filepath.Join(c.cfg.CacheDir, "thumbs.json")
}

// loadThumbs reads thumbs.json once; a missing or bad file starts empty.
func (c *Client) loadThumbs() {
	c.mu.Lock()
	loaded := c.thumbsLoaded
	c.mu.Unlock()
	if loaded {
		return
	}
	var disk map[string]string
	if data, err := os.ReadFile(c.thumbsPath()); err == nil {
		_ = json.Unmarshal(data, &disk)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.thumbsLoaded {
		return
	}
	for k, v := range disk {
		if _, ok := c.thumbs[k]; !ok {
			c.thumbs[k] = v
		}
	}
	c.thumbsLoaded = true
}

// saveThumbs writes the thumbnail cache. thumbsMu keeps the snapshots in
// order, so the last write holds every entry.
func (c *Client) saveThumbs() error {
	c.thumbsMu.Lock()
	defer c.thumbsMu.Unlock()
	c.mu.Lock()
	data, err := json.Marshal(c.thumbs)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(c.thumbsPath(), data, 0o644)
}
