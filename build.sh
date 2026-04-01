#!/bin/bash
set -euo pipefail

# Build cron raw package for ZimaOS deployment
# Usage: ./build.sh [arch]
#   arch: amd64 (default), arm64

ARCH="${1:-amd64}"
VERSION=$(grep 'version\s*=' cmd/cron/main.go | head -1 | sed 's/.*"\(.*\)".*/\1/')

echo "Building cron v${VERSION} for linux/${ARCH}..."

# 1. Cross-compile Go binary
CGO_ENABLED=0 GOOS=linux GOARCH="${ARCH}" go build \
    -ldflags="-s -w" \
    -o raw/usr/bin/cron \
    ./cmd/cron/

echo "Binary built: raw/usr/bin/cron ($(du -h raw/usr/bin/cron | cut -f1))"

# 2. Sync frontend files to raw package
cp app.js    raw/usr/share/casaos/www/modules/cron/app.js
cp index.html raw/usr/share/casaos/www/modules/cron/index.html
cp styles.css raw/usr/share/casaos/www/modules/cron/styles.css

echo "Frontend files synced to raw package"

# 3. Create .raw sysext package
mksquashfs raw/ cron.raw -noappend -comp gzip -quiet

echo "Done. Package: cron.raw ($(du -h cron.raw | cut -f1))"
