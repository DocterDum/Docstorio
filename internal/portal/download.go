package portal

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"docstorio/internal/fsutil"
	"docstorio/internal/mods"
)

// Download implements mods.ReleaseSource: GET {portal}{download_url}
// ?username=U&token=T without following redirects; a redirect (302 to
// /login) or 403 wraps mods.ErrCredentials. The body must start with "PK"
// and match the sha1. The file is written to destDir/<name>_<version>.zip
// via temp + rename; other files in destDir are not touched. Errors never
// contain the token.
func (c *Client) Download(ctx context.Context, rel mods.Release, creds mods.Credentials, destDir string) (string, error) {
	const op = "download"
	what := rel.Name + " " + rel.Version
	if creds.Empty() {
		return "", fmt.Errorf("download %s: %w", what, mods.ErrCredentials)
	}
	fileName := rel.Name + "_" + rel.Version + ".zip"
	if err := fsutil.ValidName(fileName); err != nil {
		return "", fmt.Errorf("download %s: %w", what, err)
	}
	if !strings.HasPrefix(rel.DownloadURL, "/") || strings.Contains(rel.DownloadURL, "?") {
		return "", &UpstreamError{Op: op, Err: fmt.Errorf("bad download_url for %s", what)}
	}
	if rel.SHA1 == "" {
		return "", &UpstreamError{Op: op, Err: fmt.Errorf("no sha1 for %s", what)}
	}

	ctx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	defer cancel()
	q := url.Values{"username": {creds.Username}, "token": {creds.Token}}
	req, err := c.newRequest(ctx, c.cfg.BaseURL+rel.DownloadURL+"?"+q.Encode())
	if err != nil {
		return "", &UpstreamError{Op: op, Err: transportErr(err)}
	}
	client := *c.cfg.HTTP
	client.Timeout = 0 // DownloadTimeout on ctx instead
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", &UpstreamError{Op: op, Err: transportErr(err)}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden,
		resp.StatusCode >= 300 && resp.StatusCode < 400:
		return "", fmt.Errorf("download %s: %w", what, mods.ErrCredentials)
	case resp.StatusCode != http.StatusOK:
		return "", &UpstreamError{Op: op, Status: resp.StatusCode}
	}

	body := bufio.NewReader(resp.Body)
	if magic, _ := body.Peek(2); string(magic) != "PK" {
		return "", &UpstreamError{Op: op, Err: fmt.Errorf("%s is not a zip file", what)}
	}

	p, err := fsutil.NewPending(destDir, 0o644)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", what, err)
	}
	defer p.Abort()
	sum := sha1.New()
	if _, err := io.Copy(io.MultiWriter(p, sum), body); err != nil {
		return "", fmt.Errorf("download %s: %w", what, transportErr(err))
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != strings.ToLower(rel.SHA1) {
		return "", &UpstreamError{Op: op, Err: fmt.Errorf("%s: sha1 mismatch (got %s, want %s)", what, got, rel.SHA1)}
	}
	if err := p.Commit(fileName); err != nil {
		return "", fmt.Errorf("download %s: %w", what, err)
	}
	return filepath.Join(destDir, fileName), nil
}
