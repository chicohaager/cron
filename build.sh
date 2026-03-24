#!/bin/bash
set -euo pipefail

# Build zima-cron raw package for ZimaOS deployment
# Usage: ./build.sh [arch]
#   arch: amd64 (default), arm64

ARCH="${1:-amd64}"
VERSION=$(grep 'version\s*=' cmd/zima-cron/main.go | head -1 | sed 's/.*"\(.*\)".*/\1/')

echo "Building zima-cron v${VERSION} for linux/${ARCH}..."

# 1. Cross-compile Go binary
CGO_ENABLED=0 GOOS=linux GOARCH="${ARCH}" go build \
    -ldflags="-s -w" \
    -o raw/usr/bin/zima-cron \
    ./cmd/zima-cron/

echo "Binary built: raw/usr/bin/zima-cron ($(du -h raw/usr/bin/zima-cron | cut -f1))"

# 2. Sync frontend files to raw package
cp app.js    raw/usr/share/casaos/www/modules/zima_cron/app.js
cp index.html raw/usr/share/casaos/www/modules/zima_cron/index.html
cp styles.css raw/usr/share/casaos/www/modules/zima_cron/styles.css

echo "Frontend files synced to raw package"

# 3. Create .raw sysext package
mksquashfs raw/ zima_cron.raw -noappend -comp gzip -quiet

echo "Done. Package: zima_cron.raw ($(du -h zima_cron.raw | cut -f1))"
