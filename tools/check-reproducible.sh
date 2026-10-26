#!/usr/bin/env bash
# Builds the image three times and compares the bytes: once as checked out, once with
# group-writable files under umask 002 (what a checkout on a desktop with umask 002
# gives; CI runs with 022). Different bytes mean a release image depends on the machine
# that built it (measured 2026-10-26: file modes, and VCS info in the Go binary).
#   tools/check-reproducible.sh [amd64|arm64]
set -euo pipefail
cd "$(dirname "$0")/.."
ARCH="${1:-amd64}"
OUT="cron-$ARCH.raw"
before=$(mktemp); trap 'rm -f "$before"' EXIT
find raw -perm -g+w > "$before"
chmod -R g-w raw
(umask 022 && ./build.sh "$ARCH" > /dev/null)
a=$(sha256sum "$OUT" | cut -d' ' -f1)
chmod -R g+w raw
(umask 002 && ./build.sh "$ARCH" > /dev/null)
b=$(sha256sum "$OUT" | cut -d' ' -f1)
chmod -R go-rwx raw
(umask 077 && ./build.sh "$ARCH" > /dev/null)
c=$(sha256sum "$OUT" | cut -d' ' -f1)
# put the modes back as they were
chmod -R g-w raw
xargs -r -d '\n' chmod g+w < "$before"
if [ "$a" != "$b" ] || [ "$a" != "$c" ]; then
  echo "not reproducible: $OUT is $a with umask 022, $b with umask 002 and group-writable files, $c with umask 077" >&2
  exit 1
fi
echo "reproducible: $OUT $a (umask 022, 002 and 077)"
