# Docstorio

A Factorio 2.1 headless server in one OCI image, with a built-in web manager.
Build the image once, run it as a Proxmox container (or with plain Docker), and
manage the server from a browser on your LAN. All state lives in one data
folder mounted at `/factorio`, so the container can be destroyed and recreated
at any time.

## Features

- **Server:** start, stop and restart, live log, RCON console, online players,
  autostart of the newest save.
- **Mods:** search the mod portal, install with dependencies (recommended and
  optional ones as checkboxes), update, enable/disable (including the built-in
  Space Age mods), delete, upload your own zip.
- **Saves:** list, load, download, upload, delete, create a new world.
- **Config:** a form for `server-settings.json` and a JSON editor for the other
  config files (server settings, map generation, admin/ban/whitelist).
- **Settings:** autostart, game port, whitelist on/off, factorio.com username
  and token.

The web UI has **no login**. Anyone who can reach port 8080 controls the
server, so only expose it on a network you trust (do not forward 8080 to the
internet). Only the game port (34197/udp) needs to be reachable by players.

## Build

Needs Docker 25 or newer and `curl`. Everything else, including the Go build
and tests, runs inside Docker.

```sh
sh build/build.sh                          # latest stable Factorio
FACTORIO_VERSION=2.1.24 sh build/build.sh  # a specific Factorio version
```

Use `sudo sh build/build.sh` if your user may not run docker. The script
downloads the Factorio headless server, checks it against the SHA256 published
by factorio.com, builds the image `docstorio:<version>-<factorioVersion>` and
exports it as an OCI archive. The last two lines it prints are the archive path
and its SHA256:

```
ARCHIVE=/path/to/docstorio/dist/docstorio-0.1.0-factorio-2.1.24.tar
SHA256=...
```

## Run on Proxmox

Needs a Proxmox VE version that can create containers from OCI images (9.1 or newer).

1. **Copy the archive** into the template folder of the `local` storage:

   ```sh
   scp dist/docstorio-0.1.0-factorio-2.1.24.tar root@<pve-host>:/var/lib/vz/template/cache/
   ```

   Or, when the archive is on a web server: *local -> CT Templates ->
   Download from URL*. The web UI's *Upload* button does not accept a plain
   `.tar`.

2. **Prepare the data folder.** Either use a Proxmox volume (created in the
   next step), or a folder on the host as a bind mount. A host folder must be
   owned by `100845:100845` (uid 845 inside an unprivileged container):

   ```sh
   mkdir -p /srv/factorio && chown 100845:100845 /srv/factorio
   ```

3. **Create the container** (unprivileged) from the template, with a mount
   point at `/factorio`. In the web UI: *Create CT*, pick the `.tar` as the
   template, then add a mount point with path `/factorio` under *Resources*.
   Or on the host:

   ```sh
   # with a Proxmox volume (16 GB) for the data:
   pct create 120 local:vztmpl/docstorio-0.1.0-factorio-2.1.24.tar \
     --hostname factorio --unprivileged 1 --cores 2 --memory 4096 \
     --rootfs local-lvm:4 --net0 name=eth0,bridge=vmbr0,ip=dhcp \
     --mp0 local-lvm:16,mp=/factorio

   # or with a bind mount of a host folder:
   #   --mp0 /srv/factorio,mp=/factorio
   ```

4. **Start it** (`pct start 120`) and open `http://<container-ip>:8080`. The
   container's IP is shown in the web UI, or with
   `pvesh get /nodes/<node>/lxc/120/interfaces --output-format json`. With no
   save yet, the dashboard offers *Create world*.

Stop the container with *Shutdown* (`pct shutdown 120 --timeout 130`), which
lets Factorio save. *Stop* (`pct stop`) kills it without saving.

The container runs in UTC. To set a timezone, append a line such as
`lxc.environment.runtime: TZ=Europe/Berlin` to `/etc/pve/lxc/120.conf` and
restart the container (Proxmox overwrites `--env` given to `pct create` with
the image's values, so set it in the config file).

## Run with plain Docker

`build/build.sh` leaves the image in the local Docker. On another host, load
the archive first with `docker load -i docstorio-<v>-factorio-<fv>.tar`.

```sh
docker run -d --name factorio --restart unless-stopped --stop-timeout 130 \
  -p 8080:8080 -p 34197:34197/udp \
  -v /srv/factorio:/factorio \
  docstorio:0.1.0-2.1.24
```

`--stop-timeout 130` gives Factorio time to save on `docker stop` (Docker's
default is 10 seconds). The data folder is given to uid/gid 845 on every start;
set `-e PUID=... -e PGID=...` to use other ids.

## Data folder

Everything the server and the manager keep lives in `/factorio`:

| Path | Content |
|---|---|
| `saves/` | Save zips |
| `mods/` | Mod zips, `mod-list.json`, `mod-settings.dat` |
| `config/config.ini` | Factorio paths (created when missing) |
| `config/server-settings.json` | Factorio server settings (created when missing) |
| `config/map-gen-settings.json`, `config/map-settings.json` | Optional, used when creating worlds |
| `config/server-adminlist.json`, `server-banlist.json`, `server-whitelist.json` | Player lists (created as `[]` when missing) |
| `manager/settings.json` | Manager settings, including the factorio.com username and token (mode 0600) |
| `manager/mod-state.json` | Which mods are enabled |
| `manager/cache/` | Mod portal cache |
| `factorio-current.log` etc. | Factorio's own files |

## Moving an existing Factorio data folder in

The layout is the same as a plain Factorio server's data folder (for example
the `/factorio` folder of the common Factorio Docker images):

1. Stop the old server so that it saves.
2. Copy `saves/`, `mods/` (zips, `mod-list.json`, `mod-settings.dat`) and the
   files in `config/` into the new data folder. Leave out `config/config.ini`
   unless it already says `read-data=/opt/factorio/data` and
   `write-data=/factorio`; a missing one is created.
3. Start the container. Ownership is fixed on start. On the first run the
   enabled/disabled state of the mods is taken from the existing
   `mod-list.json`, and the newest save is started when autostart is on.

Mods made for Factorio 2.0 or older do not load in 2.1; the Mods tab marks them
as incompatible and shows updates where the portal has a 2.1 release.

## Updating

The image holds no state, so updating means replacing the container and
keeping the data folder:

1. Build a new image (`sh build/build.sh`) and copy the new `.tar` to
   `/var/lib/vz/template/cache/`.
2. Shut the old container down (`pct shutdown <id> --timeout 130`).
3. Create a new container from the new template and give it the data:
   - **Bind mount:** create it with the same `--mp0 /srv/factorio,mp=/factorio`,
     then destroy the old container (`pct destroy <old-id>`); the host folder is
     not touched.
   - **Proxmox volume:** create the new container without a mount point, move
     the volume over with
     `pct move-volume <old-id> mp0 --target-vmid <new-id> --target-volume mp0`,
     then destroy the old container. Do not destroy the old container first:
     `pct destroy` deletes its volumes.
4. Start the new container.

With plain Docker: `docker rm -f factorio` and run the new tag with the same
`-v` option.

## factorio.com token (mod downloads)

Searching the mod portal works without an account, but downloading mods needs
your factorio.com username and token. Find the token on your profile at
<https://factorio.com/profile>, then enter both on the manager's *Settings*
tab. The token is stored in `manager/settings.json` (mode 0600), is never shown
again by the UI and is not written to logs. Uploading mod zips yourself needs
no token.

## Development

See `docs/DESIGN.md` (what it does), `docs/ARCH.md` (how the code is
organised), `docs/FACTS.md` (verified Factorio, portal and Proxmox behaviour)
and `docs/TESTING.md` (unit and end-to-end tests).
