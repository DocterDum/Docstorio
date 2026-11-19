package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"docstorio/internal/fsutil"
)

// receiveUpload streams the multipart part "file" of r into a new Pending
// in dir (temp file + rename, nothing is held in memory). checkName, when
// not nil, checks the part's file name before anything is written. Parts
// before "file" are skipped; anything after it is not read. The caller must
// Abort the returned Pending.
func receiveUpload(r *http.Request, dir string, checkName func(string) error) (*fsutil.Pending, string, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", badRequest("expected a multipart/form-data body")
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, "", badRequest(`missing multipart part "file"`)
		}
		if err != nil {
			return nil, "", badRequest("malformed multipart body: " + err.Error())
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		name := part.FileName()
		if checkName != nil {
			if err := checkName(name); err != nil {
				return nil, "", fmt.Errorf("%q: %w", name, err)
			}
		}
		p, err := fsutil.NewPending(dir, 0o644)
		if err != nil {
			return nil, "", err
		}
		if _, err := io.Copy(p, part); err != nil {
			p.Abort()
			return nil, "", fmt.Errorf("receiving upload: %w", err)
		}
		return p, name, nil
	}
}
