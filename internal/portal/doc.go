// Package portal is the client for the Factorio mod portal (DESIGN "Mod
// portal", FACTS "Mod portal API"):
//
//   - Search: the full mod list for the running major.minor
//     (GET {portal}/api/mods?page_size=max&version=X.Y, gzip), cached on disk
//     in manager/cache/mods-X.Y.json, refreshed when searched and older than
//     6 h or via Refresh; matching, sorting and paging run locally.
//   - Details: GET /api/mods/{name}/full, cached in memory 15 min.
//   - Thumbnail: GET /api/mods/{name} -> thumbnail, cached on disk in
//     manager/cache/thumbs.json.
//   - mods.ReleaseSource: Releases (from the Details cache), UpdateReleases
//     (namelist, chunked, cached 15 min) and Download (redirects not
//     followed, "PK" check, sha1 check, temp + rename).
//
// Every request sends User-Agent "docstorio/<version>". The token is never
// put into an error or log message.
//
// portal imports mods and fsutil.
package portal
