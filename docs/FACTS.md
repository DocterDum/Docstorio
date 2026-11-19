# Verified facts

Observed on 2026-11-19 against Factorio 2.1.24 headless, the live mod portal API and Proxmox VE 9.1.9 (pve-container 6.1.5). Re-check after major upgrades.

## Factorio 2.1.24 engine

- **Mod compatibility is exact `major.minor`.** A mod with `factorio_version` `2.0` or `1.1` fails with `Incompatible Factorio version (current: 2.1, required: 2.0)` and **exit 1** (fatal), whether listed enabled or unlisted. Listed `enabled:false` -> ignored, exit 0. Corrupt/empty zips are skipped (`Mod package read error ...`), exit 0.
- **Mod load errors** (exit 1, mod-list.json not rewritten):
  ```
  Error Util.cpp:81: Failed to load mod "depmod":
  • depmod
      • Missing required dependency missingdep >= 1.0.0
      • Dependency modv21 >= 2.0.0 is not satisfied (active: modv21 1.0.0)
      • Incompatible with modv21
  ```
  A missing optional dependency is not an error.
- **mod-list.json** is rewritten on successful start: unlisted zips added as enabled; entries without a zip silently dropped; built-ins missing from the list re-added as **enabled**; a `version` key pins a version, unknown pinned version falls back to the zip present; with two versions present and no pin, the newest loads. `--sync-mods <save>` writes entries with `version` keys.
- **Built-in mods** (`/opt/factorio/data/*/info.json`): `core` (no deps), `base` 2.1.24, `elevated-rails` (`base >= 2.1.0`), `recycler` (`base >= 2.1.0`), `quality` (`base >= 2.1.0`, `recycler >= 2.1.0`), `space-age` (`base >= 2.1.0`, `elevated-rails >= 2.1.0`, `+ quality >= 2.1.0`, `recycler >= 2.1.0`). Fresh start enables all five.
  - Base-only world requires all four expansion mods `enabled:false`. `quality:true` + `recycler:false` -> exit 1. `base:false` -> `No map settings instance.`, exit 1. `--create` with all four disabled makes a base-only world. A Space Age save loads (InGame) with the expansion disabled.
- **Ready:** log line contains `changing state from(CreatingGame) to(InGame)` (the `updateTick(N)` prefix varies). Preceded by `Hosting game at IP ADDR:({0.0.0.0:34197})`; followed by `Starting RCON interface at IP ADDR:({127.0.0.1:27015})`. About 0.5-1.5 s after start.
- **Startup failures** (exit 1): missing save `MultiplayerManager failed: "File /factorio/saves/x.zip does not exist."` then `to(InitializationFailed)`; corrupt save `Opening zip ... failed: I/O error`; `--start-server-load-latest` with no saves `No latest save file found in /factorio/saves.`
- **Shutdown:** SIGINT and SIGTERM both log `Received SIGINT|SIGTERM, shutting down`, **save the loaded save** (`Saving map as /factorio/saves/x.zip`), then `Goodbye`, exit 0 within about 0.6 s for a small map.
- **RCON** (`--rcon-bind 127.0.0.1:27015 --rcon-password P`): auth OK = reply id=1 type 2; bad password = id -1. Exactly one type-0 reply per command, same id. **Do not send an empty sentinel packet**: Factorio logs `RCON received an invalid message type 0`. Replies: `/players online` -> `Online players (0):\n` (with players: one indented name per line, `(online)` suffix - format with players unverified); `/server-save` -> `Saving the map\n` and returns **before** the save; wait for log `Saving finished`. `/version` -> `2.1.24\n`. Unknown command -> `Unknown command "x". Type /h ...`. Chat text -> empty reply, log `[CHAT] <server>: text`. First `/c` per run -> empty reply + log warning about achievements; repeat to run.
- **Create:** `--create /abs/path.zip` (`.zip` appended if missing, overwrites silently, relative paths resolve against cwd `/`), `--map-gen-settings FILE`, `--map-settings FILE`, `--map-gen-seed N` (uint32; overrides the file's seed), `--preset NAME`. Prints `Creating new map ...`, `Done.`, exit 0 in about 2 s.
- **Flags:** `-c/--config`, `--mod-directory`, `--start-server FILE`, `--start-server-load-latest`, `--port N`, `--bind ADDR[:PORT]`, `--rcon-port`, `--rcon-bind ADDR:PORT`, `--rcon-password`, `--server-settings`, `--use-server-whitelist`, `--server-whitelist`, `--server-banlist`, `--server-adminlist`, `--use-authserver-bans`, `--console-log`, `--sync-mods`, `--no-log-rotation`. Unknown option -> exit 1.
- Factorio needs no writable `/opt/factorio` or `/tmp` when write-data is `/factorio`. It rotates `factorio-current.log` -> `factorio-previous.log` on start and writes `player-data.json`, `temp/`, `.lock` to write-data.
- With stdin not a terminal it logs `Got EOF on stdin; closing` (harmless).

## Mod portal API (anonymous)

- `GET /api/mods?page_size=max&version=2.1` -> `{"pagination": null, "results": [...]}`, 6 582 mods, 4.2 MB raw / 1.2 MB gzip. `latest_release` = newest release **for that version**; deprecated mods are excluded. Invalid version -> 400 `{"message": "Invalid factorio version"}`. Without `version`, `latest_release` is the newest across all Factorio versions (wrong for us).
- Per mod: `name`, `title`, `owner`, `summary`, `downloads_count`, `category`, `score`, `latest_release` (`download_url`, `file_name`, `info_json: {factorio_version}`, `released_at`, `version`, `sha1`, optional `feature_flags`), optional `requires_space_age`, `last_highlighted_at`. **No `thumbnail`.** `latest_release` can be null (21 mods in the unfiltered list).
- `namelist=a,b&page_size=max` -> per mod `releases[]` (all releases, `info_json` has only `factorio_version`); `version` is ignored; unknown names omitted.
- No server-side search (`q`, `tag`, `owner` ignored). `sort=updated_at&sort_order=desc` works on paginated calls only.
- `GET /api/mods/{name}/full` -> `description` (markdown), `homepage`, `source_url` (may be missing/null), `thumbnail` (relative, e.g. `/assets/<hash>.thumb.png`, or null), `images[]`, `tags[]`, `license`, `created_at`, `updated_at`, `downloads_count`, `category`, `changelog`, `releases[]` oldest -> newest with `info_json.factorio_version` and `info_json.dependencies`. Unknown -> 404 `{"message":"Mod not found"}`. `GET /api/mods/{name}` = same without dependencies.
- Thumbnails are served from `https://assets-mod.factorio.com` + `thumbnail` (144x144 PNG); `https://mods.factorio.com` + path is 404.
- `factorio_version` strings are not normalised in old releases (`2.00`, `01.01`, `0.14.0`): compare numerically.
- Dependency strings seen: `base >= 2.1.22`, `base>=1.1.100`, `base >= 2.1`, `Krastorio2`, `Krastorio2Assets = 2.0.3`, `! space-age`, `! Power Armor MK3`, `? elevated-rails`, `?alien-biomes-hr-terrain >= 0.3.1`, `(?) Flow Control >= 3.0.5`, `(?)IndustrialRevolution >= 0.3.1`, `(?) metal-and-stars  >= 0.1.10`, `~ Krastorio2 >= 1.2.7`, `+ ChangeInserterDropLane >= 1.3.0`. Only `>=` and `=` observed; support all of `< <= = >= >`. `+` = optional but recommended, enabled by default (changelog 2.1.7).
- Downloads without credentials: 302 to `/login?next=...`; empty or wrong credentials: 403 HTML. Do not follow redirects.
- Caching: `Cache-Control: max-age≈420` (list) / 900 (`/full`), no ETag, Cloudflare. No rate-limit headers observed.

## Proxmox VE 9.1.9 OCI containers

- `pct create <id> local:vztmpl/<file>.tar ...` detects OCI when the tar contains `oci-layout`, `index.json`, `blobs/` (`docker save` output works) and prints `Detected OCI archive`, `Auto-Enabling host-managed network for network device net0.`
- Copied from the image automatically: `entrypoint` (Entrypoint+Cmd), env -> `lxc.environment.runtime:` lines, `lxc.signal.halt` from `StopSignal` (default SIGTERM when absent), `cmode: console`, `ostype` detected. `User` -> `lxc.init.uid/gid`, `WorkingDir` -> `lxc.init.cwd`.
- `--env`, `--entrypoint`, `--cmode` passed to `pct create` are **silently overwritten** by the image. `pct set --env` **replaces all** variables and cannot carry more than one (NUL separator). Workaround: append `lxc.environment.runtime: KEY=value` to `/etc/pve/lxc/<id>.conf` after create.
- Explicit `--net0 ...,hwaddr=XX` is kept; `host-managed=1` is added only if not set. With host-managed the **host** runs `dhclient` for the CT. IP: `pvesh get /nodes/<node>/lxc/<id>/interfaces --output-format json`.
- Bind mount `--mp0 /host/path,mp=/factorio` works; files created by uid 845 inside appear as `100845:100845` on the host.
- `pct shutdown <id> --timeout N` (default 60) sends `lxc.signal.halt` to PID 1. `pct stop` is a hard kill (no save).
- `pct destroy <id>` (with or without `--purge`) removes the CT and its volumes; **bind-mount source folders survive**. `/var/lib/lxc/<id>/` is left behind (harmless).
- Only `local` (`/var/lib/vz`) has `vztmpl` content. `pveam list local` shows `.tar` OCI templates; `pveam remove local:vztmpl/<file>.tar` deletes one. The web UI upload dialog rejects plain `.tar` (only `.tar.gz/.xz/.zst`); copy into `/var/lib/vz/template/cache/` instead.
- The container clock is UTC unless `TZ` is set. The slim image has no `ps`.
