package portal

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"docstorio/internal/mods"
)

// Cache lifetimes (DESIGN).
const (
	ListMaxAge    = 6 * time.Hour
	DetailsMaxAge = 15 * time.Minute
	UpdatesMaxAge = 15 * time.Minute
)

// Timeouts for portal requests.
const (
	// RequestTimeout bounds every API request of the default client.
	RequestTimeout = 60 * time.Second
	// DownloadTimeout bounds one mod download (mods can be large).
	DownloadTimeout = 30 * time.Minute
)

// UpstreamError reports a portal request that failed (network error,
// unexpected status, bad body). API: 502 portal_error. A 404 for a mod is
// not an UpstreamError but mods.ErrNotFound.
type UpstreamError struct {
	Op     string // e.g. "list", "details", "download"
	Status int    // HTTP status, 0 for transport errors
	Err    error  // underlying error, may be nil
}

func (e *UpstreamError) Error() string {
	if e.Status != 0 {
		if e.Err != nil {
			return fmt.Sprintf("mod portal %s: HTTP %d: %v", e.Op, e.Status, e.Err)
		}
		return fmt.Sprintf("mod portal %s: HTTP %d", e.Op, e.Status)
	}
	return fmt.Sprintf("mod portal %s: %v", e.Op, e.Err)
}

func (e *UpstreamError) Unwrap() error { return e.Err }

// Config configures a Client.
type Config struct {
	BaseURL   string // DOCSTORIO_PORTAL_URL, no trailing '/'
	AssetsURL string // DOCSTORIO_ASSETS_URL, no trailing '/'
	CacheDir  string // manager/cache
	Factorio  mods.Version
	// Version is the manager version for the User-Agent
	// "docstorio/<Version>"; "" means "dev".
	Version string
	// HTTP is the client to use; nil means a client with a RequestTimeout
	// timeout. Download clones it with CheckRedirect returning
	// http.ErrUseLastResponse and no client timeout (it uses
	// DownloadTimeout on the request context instead).
	HTTP *http.Client
	// Now is the clock (time.Now if nil).
	Now func() time.Time
}

// Client talks to the portal. Safe for concurrent use: mu protects the
// caches and is not held during HTTP requests. listMu serialises list
// downloads (so concurrent searches do not fetch the list twice) and
// thumbsMu serialises writes of thumbs.json.
type Client struct {
	cfg Config

	listMu   sync.Mutex
	thumbsMu sync.Mutex

	mu           sync.Mutex
	list         *modList
	details      map[string]detailsEntry
	updates      map[string]updateEntry
	thumbs       map[string]string // name -> relative thumbnail path, "" = none
	thumbsLoaded bool
}

// New returns a Client. It does no I/O.
func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	cfg.AssetsURL = strings.TrimRight(cfg.AssetsURL, "/")
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: RequestTimeout}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	return &Client{
		cfg:     cfg,
		details: make(map[string]detailsEntry),
		updates: make(map[string]updateEntry),
		thumbs:  make(map[string]string),
	}
}

var _ mods.ReleaseSource = (*Client)(nil)

// portalRelease is a release as the portal sends it (list latest_release,
// namelist releases[], /full releases[]).
type portalRelease struct {
	DownloadURL string `json:"download_url"`
	InfoJSON    struct {
		FactorioVersion string   `json:"factorio_version"`
		Dependencies    []string `json:"dependencies,omitempty"`
	} `json:"info_json"`
	ReleasedAt time.Time `json:"released_at"`
	Version    string    `json:"version"`
	SHA1       string    `json:"sha1"`
}

// toRelease converts a portal release of mod name. Dependencies stay nil
// when the portal did not send them.
func (r portalRelease) toRelease(name string) mods.Release {
	return mods.Release{
		Name:            name,
		Version:         r.Version,
		FactorioVersion: r.InfoJSON.FactorioVersion,
		Dependencies:    r.InfoJSON.Dependencies,
		DownloadURL:     r.DownloadURL,
		SHA1:            strings.ToLower(r.SHA1),
		ReleasedAt:      r.ReleasedAt,
	}
}

// modPath returns the escaped portal path /api/mods/{name}.
func modPath(name string) string {
	return "/api/mods/" + url.PathEscape(name)
}

// newRequest builds a GET request with the User-Agent set.
func (c *Client) newRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "docstorio/"+c.cfg.Version)
	return req, nil
}

// getJSON fetches {portal}{pathQuery} with gzip and decodes the JSON body
// into v. Any failure is an *UpstreamError (Status 404 for a missing mod).
func (c *Client) getJSON(ctx context.Context, op, pathQuery string, v any) error {
	req, err := c.newRequest(ctx, c.cfg.BaseURL+pathQuery)
	if err != nil {
		return &UpstreamError{Op: op, Err: err}
	}
	req.Header.Set("Accept", "application/json")
	// Setting Accept-Encoding ourselves turns off Go's transparent
	// decompression, so the body is decoded below.
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return &UpstreamError{Op: op, Err: transportErr(err)}
	}
	defer resp.Body.Close()

	var body io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return &UpstreamError{Op: op, Status: resp.StatusCode, Err: err}
		}
		defer gz.Close()
		body = gz
	}
	if resp.StatusCode != http.StatusOK {
		return &UpstreamError{Op: op, Status: resp.StatusCode, Err: portalMessage(body)}
	}
	if err := json.NewDecoder(body).Decode(v); err != nil {
		return &UpstreamError{Op: op, Err: fmt.Errorf("bad response: %w", err)}
	}
	return nil
}

// portalMessage returns the portal's {"message": "..."} from an error body,
// or nil.
func portalMessage(body io.Reader) error {
	var m struct {
		Message string `json:"message"`
	}
	data, _ := io.ReadAll(io.LimitReader(body, 4096))
	if json.Unmarshal(data, &m) != nil || m.Message == "" {
		return nil
	}
	return errors.New(m.Message)
}

// transportErr strips the request URL from a client error, because a
// download URL carries the factorio.com token.
func transportErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// isStatus reports whether err is an *UpstreamError with the given status.
func isStatus(err error, status int) bool {
	var ue *UpstreamError
	return errors.As(err, &ue) && ue.Status == status
}
