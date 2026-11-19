#!/bin/sh
# Builds the docstorio image and exports it as an OCI archive in dist/.
#
#   sh build/build.sh                          # latest stable Factorio
#   FACTORIO_VERSION=2.1.24 sh build/build.sh  # exact Factorio version
#
# Needs docker (25 or newer, so that `docker save` writes an OCI layout) and
# curl. Run it as a user that may use docker, or with sudo.
# The last two output lines are ARCHIVE=<path> and SHA256=<hash>.
set -eu

cd "$(dirname "$0")/.."

die() {
	echo "build.sh: $*" >&2
	exit 1
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	else
		shasum -a 256 "$1" | cut -d ' ' -f 1
	fi
}

command -v docker >/dev/null 2>&1 || die "docker not found"
command -v curl >/dev/null 2>&1 || die "curl not found"

# Factorio version: "stable" (default) or an exact x.y.z.
FACTORIO_VERSION="${FACTORIO_VERSION:-stable}"
if [ "$FACTORIO_VERSION" = stable ]; then
	releases=$(curl -fsSL https://factorio.com/api/latest-releases) ||
		die "could not fetch https://factorio.com/api/latest-releases"
	FACTORIO_VERSION=$(printf '%s\n' "$releases" | tr -d '\r\n' |
		sed -n 's/.*"stable"[[:space:]]*:[[:space:]]*{[^}]*"headless"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
	[ -n "$FACTORIO_VERSION" ] || die "no stable headless version in latest-releases"
	echo "Latest stable Factorio: $FACTORIO_VERSION"
fi
printf '%s\n' "$FACTORIO_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' ||
	die "FACTORIO_VERSION must be 'stable' or x.y.z (got '$FACTORIO_VERSION')"

# SHA256 of the headless tarball, as published by factorio.com.
tarball="factorio-headless_linux_$FACTORIO_VERSION.tar.xz"
sums=$(curl -fsSL https://factorio.com/download/sha256sums/) ||
	die "could not fetch https://factorio.com/download/sha256sums/"
FACTORIO_SHA256=$(printf '%s\n' "$sums" | tr -d '\r' | awk -v f="$tarball" '$2 == f { print $1; exit }')
printf '%s\n' "$FACTORIO_SHA256" | grep -Eq '^[0-9a-f]{64}$' ||
	die "no SHA256 for $tarball on factorio.com"
echo "Factorio $FACTORIO_VERSION sha256: $FACTORIO_SHA256"

# Manager version from VERSION.
DOCSTORIO_VERSION=$(tr -d ' \t\r\n' <VERSION)
[ -n "$DOCSTORIO_VERSION" ] || die "VERSION is empty"

tag="docstorio:$DOCSTORIO_VERSION-$FACTORIO_VERSION"
archive="dist/docstorio-$DOCSTORIO_VERSION-factorio-$FACTORIO_VERSION.tar"

echo "Building $tag"
docker build -f build/Dockerfile \
	--build-arg "FACTORIO_VERSION=$FACTORIO_VERSION" \
	--build-arg "FACTORIO_SHA256=$FACTORIO_SHA256" \
	--build-arg "DOCSTORIO_VERSION=$DOCSTORIO_VERSION" \
	-t "$tag" .

echo "Exporting $archive"
mkdir -p dist
rm -f "$archive"
docker save -o "$archive" "$tag"

# Proxmox only recognises the archive as OCI when these entries exist.
entries=$(tar -tf "$archive")
for entry in oci-layout index.json; do
	printf '%s\n' "$entries" | grep -qx "$entry" ||
		die "$archive has no $entry (docker 25 or newer is needed for an OCI archive)"
done

echo "ARCHIVE=$(pwd)/$archive"
echo "SHA256=$(sha256 "$archive")"
