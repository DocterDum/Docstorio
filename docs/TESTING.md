# Testing

Nothing needs a local Go install: unit tests run in the `golang` Docker image,
and the end-to-end test runs a built image.

## Unit tests

From the repository root:

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.25.14 \
  sh -c 'test -z "$(gofmt -l .)" && go vet ./... && go test ./...'
```

The tests use only the standard library, `t.TempDir()` and `httptest`: no
network and no real Factorio (the `factorio` package runs a fake shell script
in place of the server). `build/build.sh` runs `go vet` and `go test` again
inside the image build, so a failing test stops the build.

## Shell scripts

All scripts are POSIX `sh`:

```sh
for f in build/build.sh build/entrypoint.sh test/e2e.sh; do sh -n "$f"; done
docker run --rm -v "$PWD:/mnt:ro" -w /mnt koalaman/shellcheck:stable \
  -s sh build/build.sh build/entrypoint.sh test/e2e.sh
```

## End-to-end test

`test/e2e.sh` runs a built image on a Docker host and drives the HTTP API with
`curl`. It needs `docker`, `curl` and `python3` (for JSON and for building the
test mod zips), port 8080 free on 127.0.0.1, and internet access for the portal steps (they
use the live mod portal, without credentials).

```sh
sh build/build.sh                               # prints the image tag it built
sudo sh test/e2e.sh docstorio:0.1.0-2.1.24
```

It starts the image as container `docstorio-test-e2e` with the data folder
`/tmp/docstorio-test-e2e` and the web UI on `127.0.0.1:8080` only (the game
port is not published), then runs these steps and prints `PASS` or `FAIL` for
each:

| Step | Checks |
|---|---|
| status | manager answers, state `stopped`, versions set, Space Age enabled |
| create world | `POST /api/saves` job finishes, `e2e.zip` listed |
| start, wait running | server reaches `running` on `e2e.zip` |
| console `/version` | RCON answers with the Factorio version |
| logs | log lines are returned, including the `to(InGame)` line |
| saves list + download | `e2e.zip` marked running, download is a zip |
| config files | list of six files, valid JSON saved (sets `restartRequired`), invalid JSON refused with `invalid_json`, line and column |
| server-settings form | seeded name `Docstorio`, partial update keeps other keys |
| mods list | the five built-ins are listed and enabled |
| enable/disable | space-age off and on; disabling `recycler` gives 409 with `quality` and `space-age` as dependents, with `cascade` all three go; enabling space-age brings `recycler` back but not the recommended `quality` |
| portal search | live search finds `RateCalculator` |
| portal plan | plan for `RateCalculator` installs it and `flib` |
| upload 2.1 mod | a crafted mod zip is stored as `mods/e2e-mod_1.0.0.zip`, enabled and compatible |
| restart | the server comes back and the log shows `Loading mod e2e-mod 1.0.0` |
| 2.0 mod | a crafted 2.0 mod is marked incompatible; after a restart the server is `failed` and `lastError` mentions the incompatible version |
| delete + start | deleting the 2.0 mod lets the server start again |
| stop | the server stops and the log shows the save being written |

The script exits non-zero when any step fails and then prints the last
container log lines. The container, the data folder and its temp files are
always removed, also on failure or Ctrl-C.
