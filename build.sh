#!/bin/bash
# Build the cron sysext package for ZimaOS.
#   ./build.sh [amd64|arm64]      (default: amd64)
# Produces cron-<arch>.raw next to this script and prints its sha256.
set -euo pipefail
cd "$(dirname "$0")"

ARCH="${1:-amd64}"
case "$ARCH" in
  amd64) SYSEXT_ARCH=x86-64 ;;
  arm64) SYSEXT_ARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH (amd64|arm64)" >&2; exit 2 ;;
esac
VERSION=$(sed -n 's/^\s*version\s*=\s*"\(.*\)".*/\1/p' cmd/cron/main.go)
[ -n "$VERSION" ] || { echo "version not found in cmd/cron/main.go" >&2; exit 1; }
WEB=raw/usr/share/casaos/www/modules/cron
OUT="cron-${ARCH}.raw"

echo "cron v${VERSION} linux/${ARCH}"

CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags="-s -w" -o raw/usr/bin/cron ./cmd/cron/

# The sysext must refuse to merge on a host of the other architecture.
printf 'ID=_any\nARCHITECTURE=%s\n' "$SYSEXT_ARCH" > raw/usr/lib/extension-release.d/extension-release.cron

# Cache-bust the frontend with the version so a browser never serves a stale app.js after an upgrade.
sed -i -E "s/(styles\.css|app\.js)\?v=[0-9.]+/\1?v=${VERSION}/g" "$WEB/index.html"

mksquashfs raw/ "$OUT" -noappend -comp gzip -quiet
sha256sum "$OUT" | tee "$OUT.sha256"
