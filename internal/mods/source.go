package mods

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNotFound reports a mod that does not exist on the portal (or is not
	// installed, for Manager methods taking a name). API: 404 not_found.
	ErrNotFound = errors.New("mod not found")
	// ErrCredentials reports missing or rejected factorio.com credentials.
	// API: 400 credentials_required (before a job) or a failed job.
	ErrCredentials = errors.New("factorio.com username/token missing or wrong")
)

// Release is one release of a mod on the portal.
type Release struct {
	Name            string    // mod name
	Version         string    // e.g. "1.2.3"
	FactorioVersion string    // info_json.factorio_version, not normalised ("2.1", "2.00")
	Dependencies    []string  // info_json.dependencies; nil when the source did not provide them (namelist)
	DownloadURL     string    // portal-relative, e.g. "/download/<name>/<id>"
	SHA1            string    // lowercase hex
	ReleasedAt      time.Time // released_at
}

// Credentials are the factorio.com username and token for downloads.
type Credentials struct {
	Username string
	Token    string
}

// Empty reports whether either field is missing.
func (c Credentials) Empty() bool { return c.Username == "" || c.Token == "" }

// ReleaseSource is what the planner and Manager need from the mod portal.
// *portal.Client implements it; tests use a fake. Implementations must be
// safe for concurrent use and may cache.
type ReleaseSource interface {
	// Releases returns every release of name, oldest first, with
	// Dependencies filled (portal: /api/mods/{name}/full, cached 15 min).
	// Unknown mod: an error wrapping ErrNotFound.
	Releases(ctx context.Context, name string) ([]Release, error)

	// UpdateReleases returns all releases (Dependencies nil) for the given
	// names (portal: namelist, chunked, cached 15 min). Names unknown to the
	// portal are absent from the map.
	UpdateReleases(ctx context.Context, names []string) (map[string][]Release, error)

	// Download fetches rel with creds and writes it to
	// destDir/<rel.Name>_<rel.Version>.zip via temp + rename, returning that
	// path (DESIGN "Mod portal" Download). Rejected or missing credentials:
	// an error wrapping ErrCredentials. It does not touch other files in
	// destDir.
	Download(ctx context.Context, rel Release, creds Credentials, destDir string) (string, error)
}
