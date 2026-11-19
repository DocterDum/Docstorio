package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"

	"docstorio/internal/factorio"
	"docstorio/internal/fsutil"
	"docstorio/internal/jobs"
	"docstorio/internal/mods"
	"docstorio/internal/portal"
	"docstorio/internal/settings"
)

// Error codes (the "code" field): the DESIGN "HTTP API" list, plus
// "internal" for unexpected errors.
const (
	codeBadRequest          = "bad_request"
	codeNotFound            = "not_found"
	codeBusy                = "busy"
	codeServerRunning       = "server_running"
	codeServerNotRunning    = "server_not_running"
	codeConflict            = "conflict"
	codeInvalidJSON         = "invalid_json"
	codeCredentialsRequired = "credentials_required"
	codePortalError         = "portal_error"
	codeInternal            = "internal"
)

// errBadRequest marks a malformed request detected by the api itself (bad
// query value, missing multipart part). writeErr maps it to 400
// bad_request.
var errBadRequest = errors.New("bad request")

// badRequest returns an error wrapping errBadRequest with msg as its text.
func badRequest(msg string) error {
	return fmt.Errorf("%w: %s", errBadRequest, msg)
}

// writeJSON writes v as JSON with status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the client may have gone away; nothing to do
}

// errorBody is the JSON error shape. Extra fields are omitted when empty.
type errorBody struct {
	Error      string   `json:"error"`
	Code       string   `json:"code"`
	JobID      string   `json:"jobId,omitempty"`
	Dependents []string `json:"dependents,omitempty"`
	Line       int      `json:"line,omitempty"`
	Column     int      `json:"column,omitempty"`
}

// writeError writes {"error": msg, "code": code} with status.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: msg, Code: code})
}

// writeErr maps a domain error to status, code and extra fields (see
// docs/ARCH.md "Error codes") and writes it. Unknown errors become 500
// "internal" with their message (redacted).
func writeErr(w http.ResponseWriter, err error) {
	body := errorBody{Error: jobs.Redact(err.Error())}
	status := http.StatusInternalServerError

	var (
		busy       *jobs.BusyError
		dependents *mods.DependentsError
		jsonErr    *factorio.JSONError
		upstream   *portal.UpstreamError
	)
	switch {
	case errors.As(err, &busy):
		status, body.Code, body.JobID = http.StatusConflict, codeBusy, busy.JobID
	case errors.As(err, &dependents):
		status, body.Code, body.Dependents = http.StatusConflict, codeConflict, dependents.Dependents
	case errors.As(err, &jsonErr):
		status, body.Code = http.StatusBadRequest, codeInvalidJSON
		body.Line, body.Column = jsonErr.Line, jsonErr.Column
	case errors.As(err, &upstream):
		status, body.Code = http.StatusBadGateway, codePortalError
	case errors.Is(err, factorio.ErrRunning):
		status, body.Code = http.StatusConflict, codeServerRunning
	case errors.Is(err, factorio.ErrNotRunning):
		status, body.Code = http.StatusConflict, codeServerNotRunning
	case errors.Is(err, factorio.ErrNoSaves),
		errors.Is(err, factorio.ErrSaveExists),
		errors.Is(err, factorio.ErrSaveInUse),
		errors.Is(err, mods.ErrBuiltin),
		errors.Is(err, mods.ErrBaseRequired),
		errors.Is(err, mods.ErrPlanFailed):
		status, body.Code = http.StatusConflict, codeConflict
	case errors.Is(err, mods.ErrCredentials):
		status, body.Code = http.StatusBadRequest, codeCredentialsRequired
	case errors.Is(err, mods.ErrNotFound),
		errors.Is(err, factorio.ErrUnknownConfigFile),
		errors.Is(err, fs.ErrNotExist):
		status, body.Code, body.Error = http.StatusNotFound, codeNotFound, "not found"
	case errors.Is(err, errBadRequest),
		errors.Is(err, fsutil.ErrInvalidName),
		errors.Is(err, mods.ErrInvalidMod),
		errors.Is(err, settings.ErrInvalid),
		errors.Is(err, factorio.ErrBadValue):
		status, body.Code = http.StatusBadRequest, codeBadRequest
	default:
		body.Code = codeInternal
	}
	writeJSON(w, status, body)
}

// decodeJSON decodes r's body into v. On failure (including an empty body)
// it writes 400 bad_request and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return false
	}
	return true
}

// decodeOptionalJSON is decodeJSON for endpoints whose body may be left out:
// an empty body leaves v unchanged.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, codeBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return false
	}
	return true
}

// queryInt returns the query parameter key as an int64, or def when it is
// absent. A value that is not a non-negative integer is errBadRequest.
func queryInt(r *http.Request, key string, def int64) (int64, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, badRequest(key + " must be a non-negative integer")
	}
	return n, nil
}
