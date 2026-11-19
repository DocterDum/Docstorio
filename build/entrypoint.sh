#!/bin/sh
# Container entrypoint (run by tini as PID 1's child).
# As root: create the data folder, give it to PUID:PGID, then drop to that
# user and run the manager.
set -eu

data="${DOCSTORIO_DATA:-/factorio}"
puid="${PUID:-845}"
pgid="${PGID:-845}"

case "$puid$pgid" in
'' | *[!0-9]*)
	echo "entrypoint: PUID and PGID must be numbers (got PUID=$puid PGID=$pgid)" >&2
	exit 1
	;;
esac

if ! mkdir -p "$data"; then
	echo "entrypoint: warning: could not create $data" >&2
fi
if ! chown -R "$puid:$pgid" "$data"; then
	echo "entrypoint: warning: could not chown $data to $puid:$pgid" >&2
fi

exec setpriv --reuid="$puid" --regid="$pgid" --clear-groups /usr/local/bin/docstorio "$@"
