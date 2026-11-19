// Package api is the HTTP layer: it registers every endpoint of DESIGN
// "HTTP API" on a Go 1.22+ pattern mux, decodes requests, takes the jobs
// busy lock where DESIGN "Jobs" requires it, calls the domain packages and
// encodes JSON responses. It holds no domain logic of its own.
//
// Conventions:
//
//   - Every response body is JSON except save downloads, static files and
//     204/302 responses.
//   - Errors are {"error": "<message>", "code": "<code>"} written by
//     writeError / writeErr; "jobId", "dependents", "line" and "column" are
//     added for busy, conflict and invalid_json.
//
// api imports every internal package; nothing imports api except main.
package api
