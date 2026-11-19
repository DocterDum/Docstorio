package portal

import (
	"context"
	"net/url"
	"strings"
	"time"

	"docstorio/internal/mods"
)

// NamelistChunk is the number of names per namelist request (keeps URLs
// short).
const NamelistChunk = 50

type updateEntry struct {
	at       time.Time
	known    bool // false: the portal does not know the name
	releases []mods.Release
}

// UpdateReleases implements mods.ReleaseSource: GET
// /api/mods?namelist=a,b&page_size=max in chunks of NamelistChunk, cached
// per name for UpdatesMaxAge. Dependencies are nil; names unknown to the
// portal are absent from the map. Releases keep the portal's order.
func (c *Client) UpdateReleases(ctx context.Context, names []string) (map[string][]mods.Release, error) {
	now := c.cfg.Now()
	out := make(map[string][]mods.Release)
	var missing []string
	seen := make(map[string]bool)

	c.mu.Lock()
	for _, n := range names {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		e, ok := c.updates[n]
		switch {
		case !ok || now.Sub(e.at) >= UpdatesMaxAge:
			missing = append(missing, n)
		case e.known:
			out[n] = e.releases
		}
	}
	c.mu.Unlock()

	for start := 0; start < len(missing); start += NamelistChunk {
		chunk := missing[start:min(start+NamelistChunk, len(missing))]
		got, err := c.fetchNamelist(ctx, chunk)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		for _, n := range chunk {
			rels, ok := got[n]
			c.updates[n] = updateEntry{at: now, known: ok, releases: rels}
			if ok {
				out[n] = rels
			}
		}
		c.mu.Unlock()
	}
	return out, nil
}

// fetchNamelist requests the releases of names in one call.
func (c *Client) fetchNamelist(ctx context.Context, names []string) (map[string][]mods.Release, error) {
	var body struct {
		Results []struct {
			Name     string          `json:"name"`
			Releases []portalRelease `json:"releases"`
		} `json:"results"`
	}
	q := url.Values{"namelist": {strings.Join(names, ",")}, "page_size": {"max"}}
	if err := c.getJSON(ctx, "updates", "/api/mods?"+q.Encode(), &body); err != nil {
		return nil, err
	}
	got := make(map[string][]mods.Release, len(body.Results))
	for _, m := range body.Results {
		rels := make([]mods.Release, 0, len(m.Releases))
		for _, r := range m.Releases {
			rel := r.toRelease(m.Name)
			rel.Dependencies = nil // namelist does not carry them (contract)
			rels = append(rels, rel)
		}
		got[m.Name] = rels
	}
	return got, nil
}
