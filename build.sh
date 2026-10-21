#!/bin/bash
# Build the cron sysext package for ZimaOS.
#   ./build.sh [amd64|arm64]      (default: amd64)
# Produces cron-<arch>.raw next to this script and prints its sha256.
set -euo pipefail
cd "$(dirname "$0")"

ARCH="${1:-amd64}"
case "$ARCH" in
  amd64|arm64) ;;
  *) echo "unsupported arch: $ARCH (amd64|arm64)" >&2; exit 2 ;;
esac

# Packaging is lintux-modkit's sysext/build-sysext.sh (extension-release, -all-root, reproducible
# timestamps, unit lint). CI checks lintux-modkit out and sets LINTUX_MODKIT.
MODKIT="${LINTUX_MODKIT:-../lintux-modkit}"
[ -f "$MODKIT/sysext/build-sysext.sh" ] || { echo "lintux-modkit not found at $MODKIT (set LINTUX_MODKIT)" >&2; exit 1; }
VERSION=$(sed -n 's/^\s*version\s*=\s*"\(.*\)".*/\1/p' cmd/cron/main.go)
[ -n "$VERSION" ] || { echo "version not found in cmd/cron/main.go" >&2; exit 1; }
WEB=raw/usr/share/casaos/www/modules/cron
OUT="cron-${ARCH}.raw"

echo "cron v${VERSION} linux/${ARCH}"

CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags="-s -w" -o raw/usr/bin/cron ./cmd/cron/

# Cache-bust the frontend with the version so a browser never serves a stale app.js after an upgrade.
sed -i -E "s/(styles\.css|app\.js|i18n\.js)\?v=[0-9A-Za-z.-]+/\1?v=${VERSION}/g" "$WEB/index.html"

# Writes extension-release (ARCHITECTURE refuses a merge on the other architecture,
# SYSEXT_VERSION_ID carries the version) into a copy; raw/ stays as it is.
sh "$MODKIT/sysext/build-sysext.sh" cron raw "$ARCH" . "$VERSION"
# An older build-sysext.sh ignores the version argument: refuse an image without it.
RELEASE=$(unsquashfs -cat "$OUT" usr/lib/extension-release.d/extension-release.cron)
grep -qx "SYSEXT_VERSION_ID=$VERSION" <<<"$RELEASE" \
  || { echo "$OUT has no SYSEXT_VERSION_ID=$VERSION - lintux-modkit too old?" >&2; exit 1; }
cat "$OUT.sha256"
