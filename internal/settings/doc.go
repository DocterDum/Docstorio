// Package settings owns the manager's own configuration:
//
//   - Env: the DOCSTORIO_* environment variables (read once at startup).
//   - Paths: every absolute path in the data folder and the Factorio
//     install, derived from DOCSTORIO_DATA and DOCSTORIO_FACTORIO_DIR. No
//     other package builds data-folder paths by hand.
//   - Settings: manager/settings.json (mode 0600; it holds the factorio.com
//     token and the generated RCON password), held by a Store with a mutex
//     and written with fsutil.AtomicWrite.
//
// settings imports only fsutil.
package settings
