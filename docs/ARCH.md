# Docstorio - architecture

How the code is organised. `docs/DESIGN.md` says what the system does (it is the contract), `docs/FACTS.md` lists verified external behaviour, and this file says which package does it, through which exported API, and under which locks. When code and this file disagree, fix one of them in the same change.

Module `docstorio`, Go 1.25, standard library only (no `go.sum`). Every package has a `doc.go` stating its job; exported identifiers carry doc comments that are part of the contract. Build only what DESIGN.md describes; simple, readable code beats clever code.

## Package map

| Package | Job | Key exported API |
|---|---|---|
| `cmd/docstorio` | Entry point: wiring, autostart, shutdown | `main` |
| `internal/fsutil` | Temp + rename writes, name rules | `AtomicWrite`, `Pending`, `NewPending`, `ValidName`, `ValidSaveName`, `Join`, `ErrInvalidName`, `TempPrefix` |
| `internal/settings` | Environment, data-folder paths, `manager/settings.json` | `Env`/`LoadEnv`, `Paths`/`NewPaths`, `Settings`, `View`, `Patch`, `Store` (`Open`, `Get`, `Apply`), `ErrInvalid` |
| `internal/jobs` | Busy lock, job logs, token redaction | `Manager` (`TryBegin`, `Run`, `Get`, `Busy`, `Close`), `Job` (`Logf`, `Finish`, `Snapshot`), `Snapshot`, `BusyError`, `Logger`, `Kind*`, `Redact` |
| `internal/mods` | Versions, dependencies, info.json, built-ins, scan, desired state, mod-list.json, planner, enable/disable | `Version`, `ParseVersion`, `Compatible`, `Dependency`, `ParseDependency`, `Info`, `ReadInfo`, `Builtins`, `Mod`, `Scanner`, `State`, `LoadState`, `SaveState`, `RenderModList`, `Planner`, `Plan`, `PlanRequest`, `CheckSet`, `ReleaseSource`, `Release`, `Credentials`, `Manager` |
| `internal/portal` | Mod portal client; implements `mods.ReleaseSource` | `Client` (`Search`, `Refresh`, `Details`, `Thumbnail`, `Releases`, `UpdateReleases`, `Download`), `Config`, `Query`, `Page`, `Result`, `Details`, `UpstreamError` |
| `internal/factorio` | Factorio process, log buffer, RCON, saves, effective settings, seeding, config files | `Supervisor`, `Config`, `Status`, `State`, `CreateOptions`, `LogBuffer`, `RCON`, `SaveInfo`, `ResolveSave`, `Args`, `WriteEffectiveSettings`, `ExtractErrors`, `ParsePlayersOnline`, `DetectVersion`, `Seed`, `ConfigFiles`, `Field`, `JSONError` |
| `internal/api` | HTTP routes, JSON helpers, error mapping | `Server`, `Server.Routes` |
| `web` | Embedded UI (`static/index.html`, `app.js`, `style.css`) | `Static embed.FS` |
| `build/` | `build.sh` (POSIX sh) building the OCI image | - |

`VERSION` holds the manager version; `build/build.sh` bakes it into `DOCSTORIO_VERSION`.

### Import graph

Arrows mean "imports". No cycles.

```
cmd/docstorio -> api, factorio, jobs, mods, portal, settings, web
api           -> factorio, fsutil, jobs, mods, portal, settings
factorio      -> settings, fsutil, jobs (Logger), mods (Version only)
portal        -> mods, fsutil
mods          -> fsutil, jobs (Logger)
settings      -> fsutil
jobs          -> (nothing internal)
fsutil        -> (nothing internal)
```

Seams (the only interfaces and function hooks):

- `mods.ReleaseSource`, implemented by `*portal.Client` and faked in mods tests, so mods never imports portal.
- `factorio.Config.WriteModList func() error`, wired to `mods.Manager.WriteModList`, so the supervisor writes `mod-list.json` before every launch without depending on mods' Manager.
- `factorio.Config.Settings func() settings.Settings`, wired to `settings.Store.Get`, so launch-time values (game port, whitelist, factorio.com login, RCON password) are always current.
- `jobs.Logger` (`Logf`), satisfied by `*jobs.Job`, accepted by long operations in mods and factorio.
- `factorio.Config.Command` overrides the Factorio binary (tests run a fake script).

## Who calls whom

- **main** builds everything once (see Startup), then serves `api.Server.Routes()` with `http.Server`.
- **api** handlers decode the request, take the busy lock where DESIGN "Jobs" requires it, call domain methods and encode the result. api calls:
  - `Settings`: `Get` (status fields, credentials), `Apply` (PUT /api/settings); responses use `Settings.View()`.
  - `Factorio` (`*Supervisor`): `Status`, `Start`, `Stop`, `Restart`, `Console`, `MarkRestartRequired`, `ListSaves`, `OpenSave`, `DeleteSave`, `CommitSaveUpload`, `CreateWorld`.
  - `Config` (`*ConfigFiles`): `List`, `Read`, `Write`, `ServerSettingsFields`, `UpdateServerSettings`.
  - `Logs` (`*LogBuffer`): `Since`.
  - `Mods`: `List`, `InstalledVersions`, `Enabled`, `Enable`, `Disable`, `Delete`, `AddUpload`, `Plan`, `Install`, `Update`.
  - `Portal`: `Search`, `Refresh`, `Details`, `Thumbnail`.
  - `Jobs`: `TryBegin`, `Run`, `Get`, `Busy`.
  - `fsutil`: `NewPending` for upload temp files, `ValidSaveName` for save names.
- **factorio.Supervisor** calls `Config.WriteModList` (-> mods) and `Config.Settings` (-> settings) before every launch, and its own `ResolveSave`, `WriteEffectiveSettings`, `Args`, `RCON`, `LogBuffer`, `ExtractErrors`, `ParsePlayersOnline`.
- **mods.Manager** calls `ReleaseSource` (-> portal) for plans, update checks and downloads, and fsutil for writes.
- **portal.Client** calls the portal over HTTP and fsutil for its cache files and downloads.

Only api and main call into another package's `Manager` / `Supervisor` / `Client`.

## Startup sequence

1. `settings.LoadEnv(os.Getenv)`, `settings.NewPaths(env.DataDir, env.FactorioDir)`.
2. `paths.EnsureDirs()`: saves, mods, config, manager, manager/cache.
3. `factorio.Seed(paths)`: missing `config.ini`, `server-settings.json` (from Factorio's example, `public: false`, name "Docstorio", empty description), `[]` player lists.
4. `settings.Open(paths)`: loads or creates `manager/settings.json` (0600), generates the RCON password when empty.
5. Factorio version: `mods.ParseVersion(env.FactorioVersion)`, fallback `factorio.DetectVersion`.
6. `factorio.NewLogBuffer(jobs.Redact)`; manager events go there via `Eventf` (which also writes stderr).
7. `jobs.New(jobs.Redact)`.
8. `portal.New(...)` (no I/O), `mods.New(...)` (built-ins, state load or seed from an existing `mod-list.json`, scan).
9. `factorio.NewSupervisor(...)` with `WriteModList = modManager.WriteModList`, `Settings = store.Get`; `factorio.NewConfigFiles(paths)`.
10. `api.Server{...}.Routes()` on `net.Listen(env.HTTPAddr)`.
11. Background: `Jobs.TryBegin(KindAutostart)` -> `Supervisor.Autostart()` (starts the newest save when autostart is on and a save exists) -> `Finish`.
12. Wait for SIGTERM/SIGINT (tini forwards it), then `Supervisor.Shutdown` (SIGINT to Factorio, up to 120 s, then SIGKILL), `http.Server.Shutdown`, `Jobs.Close`; exit 0. Shutdown ignores the busy lock.

An error in steps 2-10 aborts startup with exit 1.

## Launch sequence (inside `Supervisor.Start`)

1. Refuse with `ErrRunning` in `starting` / `running` / `stopping`.
2. `Config.WriteModList()` -> `mods/mod-list.json` with an explicit entry for every built-in and installed mod.
3. `ResolveSave(saves, save)`: the requested save if given (the one-shot "load this save" request from start/restart), otherwise the newest `.zip` by mtime (`ErrNoSaves`, `fs.ErrNotExist`).
4. `WriteEffectiveSettings(config/server-settings.json -> manager/server-settings.effective.json, username, token)` (0600).
5. Spawn with `Args(...)` (DESIGN command line, RCON on `RCONAddr`), stdout+stderr line by line into the LogBuffer, clear `RestartRequired`, state `starting`.
6. Ready line (`ReadyMarker`) -> `running`; poll `/players online` every `PlayersInterval` into `Status.Players`.
7. Exit: after a stop request -> `stopped`; non-zero otherwise -> `failed` with `LastError = ExtractErrors(this run's output)`. No automatic restarts.

## Operations in api

- **Start / Stop / Restart**: `TryBegin(KindStart|KindStop|KindRestart)`, `Supervisor.Start|Stop|Restart(save)`, `Finish`, respond with the status body.
- **Create world** `POST /api/saves`: append `.zip` when missing, `fsutil.ValidSaveName`; refuse with `ErrRunning` unless stopped or failed, and with `ErrSaveExists` when the name is taken (both checked before the job starts so the caller gets a direct answer; `CreateWorld` checks them again); `Jobs.Run(KindCreateWorld)` -> `Supervisor.CreateWorld(ctx, job, opts)` (which writes `mod-list.json` first, so the world uses the current mod state); respond `{jobId}`.
- **Enable / disable / delete / upload mod**: `TryBegin(KindMod...)`, `Mods.Enable|Disable|Delete|AddUpload`, `Finish`, `Supervisor.MarkRestartRequired()`.
- **Install / update**: credentials `mods.Credentials{Username, Token}` from `Settings.Get()`; `creds.Empty()` -> 400 `credentials_required` before any job starts. Then `Jobs.Run(KindModInstall|KindModUpdate)` -> `Mods.Install|Update(ctx, job, ...)` -> `(changed, err)`; `Supervisor.MarkRestartRequired()` whenever `changed` (also when a later step failed). Respond `{jobId}`.
- **Uploads** (saves, mods): `r.MultipartReader()`, stream the `file` part into `fsutil.NewPending(dir, 0644)`, `defer p.Abort()`, then `Supervisor.CommitSaveUpload(p, partFileName, overwrite)` or `Mods.AddUpload(p)` (named `<name>_<version>.zip` from info.json). A mod upload takes the busy lock only after the file is received, so a slow upload never holds it. Save upload and delete do not take the busy lock (DESIGN "Jobs" does not list them).
- **Config writes**: `ConfigFiles.Write` / `UpdateServerSettings`, then `Supervisor.MarkRestartRequired()`. No busy lock.
- **Settings**: `Settings.Apply(patch)`; when it reports `restartRequired`, `Supervisor.MarkRestartRequired()`. No busy lock.
- **Status**: `Supervisor.Status()` plus `gamePort` and `autostart` from `Settings.Get()`, `factorioVersion`, `managerVersion` (`Env.ManagerVersion`), `spaceAge` (`Mods.Enabled("space-age")`), `busyJobId` (`Jobs.Busy()`).

## Locking rules

A package never calls into another package while holding its own mutex, and no mutex is held while waiting for a process, socket or network request (except `RCON.mu`, whose purpose is to serialise one request/reply). Lock order is therefore irrelevant.

| Lock | Protects | Notes |
|---|---|---|
| jobs busy lock (`Manager.busy`) | start, stop, restart, autostart, world creation, mod install / update / upload / delete / enable / disable | Not a mutex: `TryBegin` / `Run` fail fast with `*BusyError` (409 `busy` with `jobId`); `Finish` releases. Reads, config and settings changes, save upload/delete and shutdown do not take it. |
| `jobs.Manager.mu` | job map, busy pointer, pruning | never held while a job function runs |
| `jobs.Job.mu` | lines, state, error of one job | |
| `settings.Store.mu` | in-memory settings and writes of settings.json | |
| `mods.Manager.mu` | desired state (and mod-state.json writes), built-ins, scans | not held during `ReleaseSource` calls; zip changes are serialised by the busy lock |
| `mods.Scanner.mu` | info.json cache | |
| `portal.Client.mu` | list, details, updates and thumbnail caches | not held during HTTP requests |
| `portal.Client.listMu` | list downloads | held during the list HTTP request by design, so concurrent searches fetch the list once; never held while calling other packages |
| `portal.Client.thumbsMu` | writes of `thumbs.json` | |
| `factorio.Supervisor.mu` | `Status`, process handle, stop flag | not held while waiting for exit, RCON or log lines, or while calling `Config.WriteModList` |
| `factorio.LogBuffer.mu` | ring | leaf lock |
| `factorio.RCON.mu` | the connection; one command at a time | |
| `factorio.ConfigFiles.mu` | read-modify-write of config files | |

Status polling (every 2 s) and log polling (every 1 s) never take the busy lock and never block on a long operation.

## Conventions

- **JSON**: field names as in DESIGN (camelCase). Lists are never `null`: domain types return non-nil empty slices. Optional values are pointers (`null`).
- **Times**: `time.Time`, RFC 3339. `cacheAge` is whole seconds.
- **Names from requests**: `{name}` path values come from `r.PathValue` (mod names may contain spaces). Save names must pass `fsutil.ValidSaveName`; any other file name taken from a request goes through `fsutil.ValidName` / `fsutil.Join`. Mod zips are never extracted (`mods.ReadInfo`).
- **Writes**: every manager-written file goes through `fsutil.AtomicWrite` or `fsutil.Pending` (temp + rename). Temp files start with `fsutil.TempPrefix` and are skipped by directory listings.
- **Redaction**: job lines, log-buffer lines and error messages pass through `jobs.Redact` (`token=` values). The token and RCON password never appear in API responses (`settings.View`).
- **Errors**: domain packages return sentinel or typed errors (below); only api turns them into HTTP statuses (`writeErr` in `internal/api/json.go`). Wrap with `fmt.Errorf("...: %w", err)` so `errors.Is/As` keep working.

## Error codes

Body: `{"error": "<message>", "code": "<code>"}` plus the extra fields listed.

| Code | Status | Raised by | Extra fields |
|---|---|---|---|
| `bad_request` | 400 | malformed body or query, `fsutil.ErrInvalidName`, `mods.ErrInvalidMod`, `settings.ErrInvalid`, `factorio.ErrBadValue` | |
| `invalid_json` | 400 | `*factorio.JSONError` (config file content) | `line`, `column` |
| `credentials_required` | 400 | no factorio.com username/token before install/update; `mods.ErrCredentials` | |
| `not_found` | 404 | unknown `/api/` route or method, `mods.ErrNotFound`, `factorio.ErrUnknownConfigFile`, `fs.ErrNotExist`, unknown job id, no thumbnail | |
| `busy` | 409 | `*jobs.BusyError` | `jobId` |
| `server_running` | 409 | `factorio.ErrRunning` | |
| `server_not_running` | 409 | `factorio.ErrNotRunning` | |
| `conflict` | 409 | `*mods.DependentsError`, `mods.ErrBuiltin`, `mods.ErrBaseRequired`, `mods.ErrPlanFailed`, `factorio.ErrSaveExists`, `factorio.ErrSaveInUse`, `factorio.ErrNoSaves` | `dependents` (DependentsError) |
| `portal_error` | 502 | `*portal.UpstreamError` | |
| `internal` | 500 | anything unmapped (redacted message) | |

## Tests

- Each package tests itself with `testing`, `httptest` and `t.TempDir()`; no network, no real Factorio. `factorio` tests use `Config.Command` with a fake shell script written into `t.TempDir()` at test time (the repository holds no binaries).
- `internal/api/routes_test.go` asserts that every DESIGN endpoint is registered.
- Compile and test inside `golang:1.25` (the dev helper or `build/`), never with a local Go.
