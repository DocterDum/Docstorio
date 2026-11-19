// Package fsutil holds the small file helpers the other packages share:
//
//   - AtomicWrite and Pending: every file the manager writes goes through a
//     temp file in the destination directory followed by a rename
//     (DESIGN "File handling").
//   - Name rules: ValidName, ValidSaveName and Join, so names taken from
//     requests always resolve inside their directory (no '/', no "..").
//
// fsutil imports nothing from this module. It never logs.
package fsutil
