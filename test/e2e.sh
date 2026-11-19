#!/bin/sh
# End-to-end test of a built docstorio image. Runs on a Docker host:
#
#   sudo sh test/e2e.sh docstorio:<version>-<factorioVersion>
#
# Starts the image as container docstorio-test-e2e with the data folder
# /tmp/docstorio-test-e2e and the web UI on 127.0.0.1:8080 only, drives the
# HTTP API with curl and prints PASS/FAIL per step. Exits non-zero when any
# step fails. The container, data folder and temp files are always removed.
#
# Needs docker, curl and python3 (python3 builds the test mod zips and reads
# JSON). The portal steps use the live mod portal without credentials.
set -eu

IMAGE="${1:?usage: sh test/e2e.sh <image>}"
NAME=docstorio-test-e2e
DATA=/tmp/docstorio-test-e2e
PORT=8080
BASE="http://127.0.0.1:$PORT"

WORK=$(mktemp -d /tmp/docstorio-test-e2e-work.XXXXXX)
PASSED=0
FAILED=0
code=000
why=
FACTORIO_VERSION=

cleanup() {
	docker rm -f "$NAME" >/dev/null 2>&1 || true
	rm -rf "$DATA" "$WORK"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# --- helpers -----------------------------------------------------------------

# req METHOD PATH [curl args...]: sends a request; the response body goes to
# $WORK/body and the status code to $code (000 when no connection).
req() {
	method=$1
	path=$2
	shift 2
	code=$(curl -sS -m 120 -o "$WORK/body" -w '%{http_code}' -X "$method" "$@" "$BASE$path" 2>/dev/null) || code=000
}

# send_json METHOD PATH JSON: sends a request with a JSON body.
send_json() {
	req "$1" "$2" -H 'Content-Type: application/json' --data-binary "$3"
}

body() {
	cat "$WORK/body"
}

# jpy EXPR: evaluates a Python expression over the JSON on stdin (bound to d)
# and prints the result (strings as-is, everything else as JSON).
jpy() {
	python3 -c 'import json, sys
d = json.load(sys.stdin)
v = eval(sys.argv[1])
print(v if isinstance(v, str) else json.dumps(v))' "$1" 2>/dev/null
}

# want_code CODE...: true when $code is one of CODE, else sets $why.
want_code() {
	for c in "$@"; do
		[ "$code" = "$c" ] && return 0
	done
	why="HTTP $code (want $*): $(head -c 400 "$WORK/body" 2>/dev/null)"
	return 1
}

# check EXPR MESSAGE: true when jpy EXPR over the last body prints true.
check() {
	[ "$(body | jpy "$1")" = true ] && return 0
	why="$2: $(head -c 400 "$WORK/body" 2>/dev/null)"
	return 1
}

# step NAME FUNCTION: runs FUNCTION and records PASS or FAIL.
step() {
	why=
	if "$2"; then
		echo "PASS $1"
		PASSED=$((PASSED + 1))
	else
		echo "FAIL $1: $why"
		FAILED=$((FAILED + 1))
	fi
}

# state: prints the current server state ("" when the status call fails).
state() {
	req GET /api/status
	[ "$code" = 200 ] && body | jpy 'd["state"]'
	return 0
}

# wait_state STATE SECONDS: waits until the server state is STATE. Waiting
# for "running" ends early when the server fails.
wait_state() {
	i=0
	while [ "$i" -lt "$2" ]; do
		st=$(state)
		[ "$st" = "$1" ] && return 0
		if [ "$1" = running ] && [ "$st" = failed ]; then
			why="server failed: $(body | jpy 'd["lastError"]')"
			return 1
		fi
		sleep 1
		i=$((i + 1))
	done
	why="state is '$st' after $2 s (want $1)"
	return 1
}

# wait_job ID SECONDS: waits for a job to finish; true when it is done.
wait_job() {
	i=0
	while [ "$i" -lt "$2" ]; do
		req GET "/api/jobs/$1"
		want_code 200 || return 1
		js=$(body | jpy 'd["state"]')
		[ "$js" = "done" ] && return 0
		if [ "$js" = failed ]; then
			why="job failed: $(body | jpy 'd.get("error")')"
			return 1
		fi
		sleep 1
		i=$((i + 1))
	done
	why="job $1 still running after $2 s"
	return 1
}

# log_after N: prints the text of all log lines numbered above N.
log_after() {
	n=$1
	while :; do
		req GET "/api/logs?after=$n"
		[ "$code" = 200 ] || return 0
		[ "$(body | jpy 'len(d["lines"])')" -gt 0 ] 2>/dev/null || return 0
		body | jpy '"\n".join(l["text"] for l in d["lines"])'
		n=$(body | jpy 'd["lines"][-1]["n"]')
	done
}

# log_mark: prints the number of the newest log line.
log_mark() {
	n=0
	while :; do
		req GET "/api/logs?after=$n"
		[ "$code" = 200 ] || break
		next=$(body | jpy 'd["lines"][-1]["n"] if d["lines"] else 0')
		[ "${next:-0}" -gt "$n" ] 2>/dev/null || break
		n=$next
	done
	echo "$n"
}

# wait_log N TEXT SECONDS: waits until a log line above N contains TEXT.
wait_log() {
	i=0
	while [ "$i" -lt "$3" ]; do
		log_after "$1" | grep -qF -- "$2" && return 0
		sleep 1
		i=$((i + 1))
	done
	why="no log line containing '$2'"
	return 1
}

# make_mod NAME FACTORIO_VERSION: writes $WORK/<NAME>.zip, a minimal mod
# (info.json + data.lua in one top-level folder) for that Factorio version.
make_mod() {
	python3 - "$WORK/$1.zip" "$1" "$2" <<'EOF'
import json, sys, zipfile
path, name, factorio_version = sys.argv[1:4]
info = {
    "name": name,
    "version": "1.0.0",
    "title": "Docstorio e2e " + name,
    "author": "docstorio",
    "factorio_version": factorio_version,
    "dependencies": ["base >= " + factorio_version + ".0"],
}
folder = name + "_1.0.0/"
with zipfile.ZipFile(path, "w") as z:
    z.writestr(folder + "info.json", json.dumps(info))
    z.writestr(folder + "data.lua", "-- docstorio e2e test mod\n")
EOF
}

# --- steps -------------------------------------------------------------------

t_status() {
	i=0
	while [ "$i" -lt 60 ]; do
		req GET /api/status
		[ "$code" = 200 ] && break
		sleep 1
		i=$((i + 1))
	done
	want_code 200 || return 1
	check 'd["state"] == "stopped"' "state is not stopped" || return 1
	check 'd["factorioVersion"] != "" and d["managerVersion"] != ""' "versions missing" || return 1
	check 'd["spaceAge"] is True and d["players"] == []' "unexpected spaceAge/players" || return 1
	FACTORIO_VERSION=$(body | jpy 'd["factorioVersion"]')
}

t_create_world() {
	send_json POST /api/saves '{"name":"e2e","useMapGenSettings":false,"useMapSettings":false}'
	want_code 200 201 202 || return 1
	job=$(body | jpy 'd["jobId"]')
	[ -n "$job" ] || {
		why="no jobId"
		return 1
	}
	wait_job "$job" 180 || return 1
	req GET /api/saves
	want_code 200 || return 1
	check '"e2e.zip" in [s["name"] for s in d]' "e2e.zip not listed"
}

t_start() {
	send_json POST /api/server/start '{}'
	want_code 200 || return 1
	check 'd["state"] in ("starting", "running")' "state after start"
}

t_wait_running() {
	wait_state running 120 || return 1
	check 'd["save"] == "e2e.zip"' "wrong save"
}

t_console() {
	i=0
	while [ "$i" -lt 10 ]; do
		send_json POST /api/console '{"command":"/version"}'
		[ "$code" = 200 ] && break
		sleep 1
		i=$((i + 1))
	done
	want_code 200 || return 1
	check "d[\"output\"].strip() == \"$FACTORIO_VERSION\"" "unexpected /version output"
}

t_logs() {
	req GET '/api/logs?after=0'
	want_code 200 || return 1
	check 'len(d["lines"]) > 0 and d["last"] >= d["lines"][-1]["n"]' "no log lines" || return 1
	log_after 0 | grep -qF 'to(InGame)' || {
		why="no InGame line in the log"
		return 1
	}
}

t_saves() {
	req GET /api/saves
	want_code 200 || return 1
	check '[s["running"] for s in d if s["name"] == "e2e.zip"] == [True]' "e2e.zip not marked running" || return 1
	req GET /api/saves/e2e.zip/download
	want_code 200 || return 1
	[ "$(dd if="$WORK/body" bs=2 count=1 2>/dev/null)" = PK ] || {
		why="download is not a zip"
		return 1
	}
}

t_config() {
	req GET /api/config/files
	want_code 200 || return 1
	check '[f["name"] for f in d] == ["server-settings.json", "map-gen-settings.json", "map-settings.json", "server-adminlist.json", "server-banlist.json", "server-whitelist.json"]' "file list" || return 1
	req GET /api/config/files/map-gen-settings.json
	want_code 200 || return 1
	check 'd["content"] != ""' "no map-gen-settings content" || return 1

	send_json PUT /api/config/files/server-adminlist.json '{"content":"[\"e2e-admin\"]"}'
	want_code 204 || return 1
	req GET /api/config/files/server-adminlist.json
	want_code 200 || return 1
	check '"e2e-admin" in json.loads(d["content"]) and d["exists"]' "admin list not saved" || return 1
	req GET /api/status
	check 'd["restartRequired"] is True' "restartRequired not set" || return 1

	send_json PUT /api/config/files/server-banlist.json '{"content":"[\n  \"a\",\n  oops\n]"}'
	want_code 400 || return 1
	check 'd["code"] == "invalid_json" and d["line"] == 3 and d["column"] > 0' "invalid_json with line/column" || return 1
	req GET /api/config/files/server-banlist.json
	check 'json.loads(d["content"]) == []' "invalid content was written"
}

t_server_settings() {
	req GET /api/config/server-settings
	want_code 200 || return 1
	check '{f["key"]: f["value"] for f in d["fields"]}.get("name") == "Docstorio"' "seeded name missing" || return 1
	send_json PUT /api/config/server-settings '{"values":{"description":"e2e test"}}'
	want_code 204 || return 1
	req GET /api/config/server-settings
	want_code 200 || return 1
	check '(lambda v: v.get("description") == "e2e test" and v.get("name") == "Docstorio" and v.get("visibility.public") is False)({f["key"]: f["value"] for f in d["fields"]})' "partial update" || return 1
	req GET /api/config/files/server-settings.json
	check 'json.loads(d["content"])["description"] == "e2e test"' "file not updated"
}

t_mods_list() {
	req GET /api/mods
	want_code 200 || return 1
	check 'all(any(m["name"] == b and m["builtin"] and m["enabled"] for m in d["mods"]) for b in ("base", "elevated-rails", "quality", "recycler", "space-age"))' "built-ins missing or disabled"
}

# enabled_set: prints the sorted names of enabled mods as JSON.
enabled_set() {
	req GET /api/mods
	body | jpy 'sorted(m["name"] for m in d["mods"] if m["enabled"])'
}

t_toggle() {
	all='["base", "elevated-rails", "quality", "recycler", "space-age"]'

	send_json POST /api/mods/space-age/disable '{}'
	want_code 200 || return 1
	check 'd["disabled"] == ["space-age"]' "disable space-age" || return 1
	req GET /api/status
	check 'd["spaceAge"] is False' "status spaceAge still true" || return 1

	send_json POST /api/mods/space-age/enable '{}'
	want_code 200 || return 1
	check '"space-age" in d["enabled"]' "enable space-age" || return 1

	send_json POST /api/mods/recycler/disable '{}'
	want_code 409 || return 1
	check 'd["code"] == "conflict" and sorted(d["dependents"]) == ["quality", "space-age"]' "dependents of recycler" || return 1

	send_json POST /api/mods/recycler/disable '{"cascade":true}'
	want_code 200 || return 1
	check 'sorted(d["disabled"]) == ["quality", "recycler", "space-age"]' "cascade disable" || return 1
	[ "$(enabled_set)" = '["base", "elevated-rails"]' ] || {
		why="enabled after cascade: $(enabled_set)"
		return 1
	}

	# Enabling space-age enables its required dependencies (recycler), not
	# the recommended quality.
	send_json POST /api/mods/space-age/enable '{}'
	want_code 200 || return 1
	check '{"recycler", "space-age"} <= set(d["enabled"])' "enable with dependencies" || return 1
	[ "$(enabled_set)" = '["base", "elevated-rails", "recycler", "space-age"]' ] || {
		why="enabled after enabling space-age: $(enabled_set)"
		return 1
	}
	send_json POST /api/mods/quality/enable '{}'
	want_code 200 || return 1
	[ "$(enabled_set)" = "$all" ] || {
		why="enabled at the end: $(enabled_set)"
		return 1
	}
}

t_portal_search() {
	req GET '/api/portal/search?q=RateCalculator&page=1&pageSize=24&sort=downloads'
	want_code 200 || return 1
	check 'd["total"] > 0 and "RateCalculator" in [r["name"] for r in d["results"]]' "RateCalculator not found"
}

t_portal_plan() {
	send_json POST /api/portal/plan '{"mods":[{"name":"RateCalculator"}],"includeOptional":false,"includeRecommended":true}'
	want_code 200 || return 1
	check '{"RateCalculator", "flib"} <= {i["name"] for i in d["install"]} and d["conflicts"] == []' "plan lacks RateCalculator + flib"
}

t_upload_mod() {
	make_mod e2e-mod 2.1
	req POST /api/mods/upload -F "file=@$WORK/e2e-mod.zip;filename=upload.zip"
	want_code 201 || return 1
	check 'd == {"name": "e2e-mod", "version": "1.0.0"}' "upload response" || return 1
	[ -f "$DATA/mods/e2e-mod_1.0.0.zip" ] || {
		why="mods/e2e-mod_1.0.0.zip not stored"
		return 1
	}
	req GET /api/mods
	check '[(m["enabled"], m["compatible"]) for m in d["mods"] if m["name"] == "e2e-mod"] == [(True, True)]' "e2e-mod not listed as enabled + compatible"
}

t_restart_loads_mod() {
	mark=$(log_mark)
	send_json POST /api/server/restart '{}'
	want_code 200 || return 1
	wait_state running 120 || return 1
	check 'd["restartRequired"] is False' "restartRequired still set" || return 1
	wait_log "$mark" 'Loading mod e2e-mod 1.0.0' 5
}

t_incompatible_mod() {
	make_mod e2e-old 2.0
	req POST /api/mods/upload -F "file=@$WORK/e2e-old.zip"
	want_code 201 || return 1
	req GET /api/mods
	check '[m["compatible"] for m in d["mods"] if m["name"] == "e2e-old"] == [False]' "e2e-old not marked incompatible" || return 1
	send_json POST /api/server/restart '{}'
	want_code 200 || return 1
	wait_state failed 120 || return 1
	check '"Incompatible Factorio version" in d["lastError"] and "2.0" in d["lastError"]' "lastError"
}

t_delete_and_start() {
	req DELETE /api/mods/e2e-old
	want_code 204 || return 1
	[ ! -e "$DATA/mods/e2e-old_1.0.0.zip" ] || {
		why="zip still present"
		return 1
	}
	send_json POST /api/server/start '{}'
	want_code 200 || return 1
	wait_state running 120
}

t_stop() {
	mark=$(log_mark)
	send_json POST /api/server/stop '{}'
	want_code 200 || return 1
	wait_state stopped 150 || return 1
	wait_log "$mark" 'Saving map as /factorio/saves/e2e.zip' 5 || return 1
	[ -s "$DATA/saves/e2e.zip" ] || {
		why="saves/e2e.zip missing"
		return 1
	}
}

# --- run ---------------------------------------------------------------------

docker rm -f "$NAME" >/dev/null 2>&1 || true
rm -rf "$DATA"
mkdir -p "$DATA"
echo "Starting $IMAGE as $NAME"
docker run -d --name "$NAME" -p "127.0.0.1:$PORT:8080" -v "$DATA:/factorio" "$IMAGE" >/dev/null

step "status" t_status
step "create world" t_create_world
step "start" t_start
step "wait running" t_wait_running
step "console /version" t_console
step "logs" t_logs
step "saves list + download" t_saves
step "config files get/put (valid + invalid JSON)" t_config
step "server-settings form get/put" t_server_settings
step "mods list" t_mods_list
step "disable/enable space-age + cascade" t_toggle
step "portal search (live)" t_portal_search
step "portal plan RateCalculator -> flib" t_portal_plan
step "upload 2.1 mod" t_upload_mod
step "restart loads uploaded mod" t_restart_loads_mod
step "2.0 mod makes the server fail" t_incompatible_mod
step "delete 2.0 mod and start again" t_delete_and_start
step "stop writes the save" t_stop

if [ "$FAILED" -gt 0 ]; then
	echo "--- last container log lines ---"
	docker logs --tail 40 "$NAME" 2>&1 || true
fi
echo "$PASSED passed, $FAILED failed"
[ "$FAILED" -eq 0 ]
