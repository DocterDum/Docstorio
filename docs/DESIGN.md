# Docstorio - design

Docstorio is a self-built Factorio headless server image with a built-in web manager.
One OCI image contains the Factorio server and the manager; all state lives outside the image.

Facts this design relies on were verified live against Factorio 2.1.24, the mod portal API and Proxmox VE 9.1.9; see `docs/FACTS.md`.

## Goals

- **Easy to deploy image:** `build/build.sh` produces one OCI archive (`.tar`). Put it in Proxmox's template folder, create a container from it, add a mount point at `/factorio`, start it. No other setup.
- **All text:** the repository holds only text (Go, HTML/CSS/JS, POSIX shell, Markdown). No binaries, no build output. Safe to push to GitHub.
- **Cross-platform tooling:** scripts are POSIX `sh`. The image is built entirely inside Docker; nothing depends on a local Go install.
- **Stateless image:** saves, mods, configs and manager settings live in the data folder `/factorio` (a mount point). The container can be destroyed and recreated at any time.
- **Manager:** mod browser (search the mod portal, install with dependencies, update, enable/disable, remove, upload), config editing, server start/stop/restart, live log and RCON console, basic saves management.

## Non-goals

Multiple servers per container, Factorio versions other than the one baked into the image, editing `mod-settings.dat`, TLS, a login (LAN tool; anyone who can reach the page controls the server), deployment automation, a backtracking dependency solver.

## Runtime layout

Image:

| Path | Content |
|---|---|
| `/opt/factorio` | Factorio headless server (read-only, SHA256-verified at build) |
| `/usr/local/bin/docstorio` | Manager binary (Go, static, stdlib only) |
| `/usr/local/bin/entrypoint.sh` | As root: fix data ownership, then `setpriv` to PUID:PGID and `exec docstorio` |
| `/usr/bin/tini` | PID 1: reaps zombies, forwards signals |

Dockerfile: `ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]`, `STOPSIGNAL SIGTERM`, `EXPOSE 8080/tcp 34197/udp`, packages `ca-certificates tini tzdata`.

Data folder `/factorio` (same layout as the earlier plain image, so an existing folder can be copied in):

| Path | Content |
|---|---|
| `saves/` | Save zips |
| `mods/` | Mod zips, `mod-list.json`, `mod-settings.dat` |
| `config/config.ini` | Factorio paths: read-data `/opt/factorio/data`, write-data `/factorio` |
| `config/server-settings.json` | Factorio server settings |
| `config/map-gen-settings.json`, `config/map-settings.json` | Optional, used when creating worlds |
| `config/server-adminlist.json`, `server-banlist.json`, `server-whitelist.json` | Player lists, created as `[]` when missing |
| `manager/settings.json` | Manager settings, incl. factorio.com username + token (file mode 0600) and the generated RCON password |
| `manager/mod-state.json` | Desired enabled/disabled state per mod (see Mods) |
| `manager/cache/` | Mod portal cache |
| `factorio-current.log` etc. | Factorio's own files |

Everything is owned by PUID:PGID (default 845:845; host uid 100845 on an unprivileged Proxmox CT).

## Process model

- `tini` -> `entrypoint.sh` (root): `mkdir -p /factorio`, `chown -R PUID:PGID /factorio` (warn, don't fail), `exec setpriv --reuid=$PUID --regid=$PGID --clear-groups /usr/local/bin/docstorio`.
- `docstorio`:
  - seeds missing files: `config/config.ini`, `config/server-settings.json` (Factorio's example with `"public": false`, `"name": "Docstorio"`, empty description), `[]` list files,
  - serves the web UI and API on `:8080`,
  - supervises one Factorio child process,
  - on SIGTERM/SIGINT: stops Factorio (which saves) and exits 0.
- **Launch:**
  ```
  factorio -c /factorio/config/config.ini --mod-directory /factorio/mods
    --start-server <absolute save path>
    --server-settings <effective settings file>
    --port <gamePort>
    --rcon-bind 127.0.0.1:27015 --rcon-password <generated>
    --server-adminlist /factorio/config/server-adminlist.json
    --server-banlist /factorio/config/server-banlist.json
    [--use-server-whitelist --server-whitelist /factorio/config/server-whitelist.json]   (when whitelistEnabled)
  ```
  Save: a one-shot "load this save" request if pending, otherwise the newest `.zip` in `saves/` by mtime.
  Effective settings file: `manager/server-settings.effective.json` (0600) = `config/server-settings.json` plus the stored factorio.com username/token (needed only for public listing).
  Before launch the manager writes `mods/mod-list.json` from `manager/mod-state.json`.
- **Ready:** log line containing `changing state from(CreatingGame) to(InGame)` -> `running`.
- **Stop:** SIGINT to Factorio (it saves the loaded save itself), wait up to 120 s, then SIGKILL.
- **States:** `stopped`, `starting`, `running`, `stopping`, `failed` (exited non-zero; `lastError` = the `Error` lines plus following bullet lines from that run's output). No automatic restarts.
- **Autostart** (setting, default true): when the manager starts and a save exists, start Factorio. With no save, the UI shows "Create world".
- Factorio output (stdout+stderr) goes to a ring buffer of the last 5000 lines with increasing line numbers `n`; manager events are added prefixed `[docstorio]`.
- **RCON** (players list, console): one connection behind a mutex, reconnect on failure, one reply per command (no sentinel packet). The first `/c` per run only warns in the log (achievements); the UI says to repeat it.

## Environment variables

| Var | Default | Meaning |
|---|---|---|
| `DOCSTORIO_DATA` | `/factorio` | Data folder |
| `DOCSTORIO_FACTORIO_DIR` | `/opt/factorio` | Factorio install |
| `DOCSTORIO_HTTP_ADDR` | `:8080` | Web UI listen address |
| `DOCSTORIO_PORTAL_URL` | `https://mods.factorio.com` | Mod portal (overridable for tests) |
| `DOCSTORIO_ASSETS_URL` | `https://assets-mod.factorio.com` | Thumbnail host |
| `FACTORIO_VERSION`, `DOCSTORIO_VERSION` | set at build | Baked versions |
| `PUID` / `PGID` | 845 | Runtime user/group (entrypoint) |
| `TZ` | UTC | Timezone |

## File handling

- Names from requests are resolved inside their directory only (no `/`, no `..`); saves must end in `.zip`.
- **Mod zips are never extracted.** `info.json` is read in memory: exactly one top-level folder, `<folder>/info.json`, read through a 1 MiB limit; zips with more than 50 000 entries are rejected. `name` must match `^[A-Za-z0-9_\- ]{1,100}$` and `version` `^\d+\.\d+\.\d+$`; the file is stored as `mods/<name>_<version>.zip` regardless of the uploaded file name. Parsed results are cached by (file name, size, mtime).
- Manager-written files use write-to-temp + rename.
- The factorio.com token is never written to logs (redact `token=`).

## Mods

### Built-ins
Directories in `/opt/factorio/data` with an `info.json`, except `core`. In 2.1.24: `base`, `elevated-rails`, `quality`, `recycler`, `space-age`. They are installed at their `info.json` version, take part in dependency checks, and are listed with enable toggles like any mod; they cannot be downloaded or deleted. `base` cannot be disabled. A base-game world needs `space-age`, `quality`, `elevated-rails` and `recycler` all disabled; the normal dependency rules below handle the combinations (`quality` needs `recycler`, `space-age` needs the others).

### Desired state
`manager/mod-state.json`: `{"mods": {"<name>": {"enabled": bool}}}`. Factorio rewrites `mod-list.json` on a successful start (adds unlisted zips as enabled, drops entries without zips, re-adds missing built-ins as enabled), so the manager keeps its own state and writes the full `mod-list.json` before every launch, with an explicit entry for every built-in and installed mod. A zip without an entry defaults to enabled. On first run, the state is seeded from an existing `mod-list.json` if present.

### Dependency grammar
`^\s*(\(\?\)|[!?~+])?\s*(.+?)\s*(?:(<=|>=|<|>|=)\s*(\S+))?\s*$` (names may contain spaces; whitespace optional).

| Prefix | Meaning | Planner |
|---|---|---|
| none, `~` | Required | must be installed + enabled, constraint satisfied |
| `?` | Optional | installed only when "include optional" is ticked |
| `(?)` | Hidden optional | never installed |
| `+` | Optional, recommended (Factorio 2.1.7+) | proposed by default (ticked) |
| `!` | Incompatible | conflict if present and enabled, in both directions |

Optional constraints are still checked when the optional mod is present and enabled. Versions compare numerically per component; missing components count as 0.

### Compatibility
A release is compatible when its `factorio_version`, parsed as numbers, equals the running Factorio's `major.minor` (2.1 rejects 2.0 mods with a fatal error).

### Planner
Input: requested `[{name, version?}]`, `includeOptional`, `includeRecommended` (default true). Recursively: built-in -> must be enabled (enable it); installed and satisfying -> enable if disabled; otherwise the newest compatible release satisfying all collected constraints. Then the resulting enabled set is checked (incompatibilities both ways, all constraints). Output: `{install: [{name, version, reason}], enable: [names], alreadySatisfied: [names], conflicts: [text]}`. Conflicts are reported, not solved.

### Enable/disable
Enabling a mod enables its required dependencies. Disabling a mod that other enabled mods require returns 409 listing them, unless `cascade: true`.

## Mod portal

- **Search:** `GET {portal}/api/mods?page_size=max&version=<major.minor>` (gzip; about 6 600 mods for 2.1; `latest_release` is the newest release for that version; deprecated mods excluded). Cached to `manager/cache/mods-<major.minor>.json`, refreshed when searched and older than 6 h, or via a refresh button. Local search over name, title, owner, summary; sort by downloads (default), updated or name.
- **Details:** `GET {portal}/api/mods/{name}/full`, cached in memory 15 min.
- **Thumbnails:** `GET /api/portal/thumb/{name}` looks up `thumbnail` via `GET {portal}/api/mods/{name}` (cached in `manager/cache/thumbs.json`) and redirects to `{assets}{thumbnail}`; 404 when none.
- **Updates:** `GET {portal}/api/mods?namelist=<installed>&page_size=max` (chunked) gives all releases; the newest compatible release newer than installed = update available. Cached 15 min.
- **Download:** `{portal}{download_url}?username=U&token=T`, redirects not followed (302 to `/login` or 403 = "factorio.com username/token missing or wrong"). Body must start with `PK`; sha1 must match; written via temp + rename; older versions of that mod are deleted afterwards.

## Jobs

One busy lock covers start, stop, restart, world creation and mod changes; a second request while busy gets 409 `{"error", "code": "busy", "jobId"}`. Install, update and world creation return `{"jobId"}`; `GET /api/jobs/{id}?after=<n>` -> `{id, kind, state: running|done|failed, lines: [{n, text}], last, error?}`. Mod and config changes are allowed while the server runs and set `restartRequired`.

## HTTP API

JSON. Errors: `{"error": "message", "code": "machine_code"}` (`bad_request`, `not_found`, `busy`, `server_running`, `server_not_running`, `conflict`, `invalid_json`, `credentials_required`, `portal_error`).

### Server
- `GET /api/status` -> `{"state", "save", "startedAt", "lastError", "players": [names], "gamePort", "factorioVersion", "managerVersion", "spaceAge": bool, "autostart", "restartRequired", "busyJobId"}`
- `POST /api/server/start` `{"save"?}` / `POST /api/server/stop` / `POST /api/server/restart` `{"save"?}` -> status
- `GET /api/logs?after=<n>` -> `{"lines": [{n, text}], "last"}` (max 1000 per call)
- `POST /api/console` `{"command"}` -> `{"output"}` (409 when not running)

### Saves
- `GET /api/saves` -> `[{"name", "size", "modified", "running": bool, "next": bool}]` newest first
- `POST /api/saves/upload` multipart `file` -> 201 (409 if the name exists, unless `?overwrite=1`; never the running save)
- `GET /api/saves/{name}/download`
- `DELETE /api/saves/{name}` -> 204 (409 for the running save)
- `POST /api/saves` `{"name", "seed"?, "useMapGenSettings": bool, "useMapSettings": bool}` -> `{jobId}` (server must be stopped; `factorio --create` with the current mod state)

### Config
- `GET /api/config/files` -> `[{"name", "exists"}]`: server-settings.json, map-gen-settings.json, map-settings.json, server-adminlist.json, server-banlist.json, server-whitelist.json
- `GET /api/config/files/{name}` -> `{"name", "content", "exists"}` (missing -> Factorio's example where one exists, else `[]`)
- `PUT /api/config/files/{name}` `{"content"}` -> 204; must be valid JSON (400 `invalid_json` with line/column); sets `restartRequired`
- `GET /api/config/server-settings` -> `{"fields": [{"key", "type": "string|int|bool|enum|password", "value", "options"?, "help"}]}` (derived from Factorio's example file; `_comment_*` keys give help text)
- `PUT /api/config/server-settings` `{"values": {key: value}}` -> 204 (partial update, keeps other keys)

### Mods
- `GET /api/mods` -> `{"mods": [{"name", "title", "version", "enabled", "builtin", "factorioVersion", "compatible", "dependencies": [str], "missingDependencies": [str], "updateAvailable": str|null}]}`
- `POST /api/mods/{name}/enable` -> `{"enabled": [names]}`; `POST /api/mods/{name}/disable` `{"cascade"?}` -> `{"disabled": [names]}` or 409 `conflict` with `dependents`
- `DELETE /api/mods/{name}` -> 204
- `POST /api/mods/upload` multipart `file` -> 201 `{"name", "version"}`
- `POST /api/mods/update` `{"names"?: []}` (empty = all) -> `{jobId}`

### Portal
- `GET /api/portal/search?q=&page=1&pageSize=24&sort=downloads|updated|name` -> `{"total", "cacheAge", "results": [{"name", "title", "owner", "summary", "downloads", "latestVersion", "updated", "category", "requiresSpaceAge", "installed": str|null}]}`
- `POST /api/portal/refresh` -> 202
- `GET /api/portal/mods/{name}` -> `{"name", "title", "owner", "summary", "description", "homepage", "sourceUrl", "releases": [{"version", "factorioVersion", "released", "compatible", "dependencies"}], "installed": str|null}`
- `GET /api/portal/thumb/{name}` -> 302 / 404
- `POST /api/portal/plan` `{"mods": [{"name", "version"?}], "includeOptional", "includeRecommended"}` -> plan
- `POST /api/portal/install` same body -> `{jobId}`; 400 `credentials_required` when no username/token

### Settings
- `GET /api/settings` -> `{"autostart", "gamePort", "whitelistEnabled", "factorioUsername", "factorioTokenSet"}`
- `PUT /api/settings` partial; `factorioToken` is write-only (`""` clears)

## UI

Single page, vanilla HTML/CSS/JS embedded in the binary, no build step, no CDN. Dark, clean, works on a phone. Dynamic text is set with `textContent`. Polls `/api/status` every 2 s and `/api/logs` every 1 s on the Console tab. Toasts for results, confirm for deletes.

- **Dashboard:** state, Start/Stop and Restart buttons, game address `ip:port` with copy, online players, save in use, `lastError` when failed, "changes pending - restart" banner, "Create world" when there are no saves.
- **Mods:** Installed (filter, enable toggles, update badges + Update all, delete, upload zip) and Browse (search as you type, sort, cards with thumbnails, details with description and releases, Install -> plan with recommended/optional checkboxes -> progress).
- **Saves:** list, "Load this" (restart with that save), download, delete, upload, Create world (name, seed, use map-gen/map settings).
- **Config:** server-settings form; raw JSON editor per file with validation.
- **Console:** live log, RCON command input with history.
- **Settings:** autostart, game port, whitelist on/off, factorio.com username + token.

## Build and run

- `build/build.sh` (POSIX sh, needs Docker): `FACTORIO_VERSION` = `stable` (default, latest stable) or an exact version; verifies the SHA256 from factorio.com; builds `docstorio:<version>-<factorioVersion>`; exports `dist/docstorio-<version>-factorio-<factorioVersion>.tar` (OCI archive) and prints its sha256.
- **Proxmox:** copy the `.tar` into `/var/lib/vz/template/cache/` (or use "Download from URL"); Create CT from it (unprivileged); add a mount point at `/factorio` (Proxmox volume, or a bind mount from a host folder owned by 100845:100845); start; open `http://<ct-ip>:8080`.
- **Plain Docker:** `docker run -d -p 8080:8080 -p 34197:34197/udp -v <dir>:/factorio docstorio:<tag>`.
