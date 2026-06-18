#!/usr/bin/env bash
# Build single self-contained binaries for macOS and Windows.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist
LDFLAGS="-s -w"  # strip symbols for a smaller binary

echo "macOS  arm64..."; GOOS=darwin  GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/netstack-doctor-macos-arm64 .
echo "macOS  amd64..."; GOOS=darwin  GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/netstack-doctor-macos-amd64 .
echo "Windows amd64..."; GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/netstack-doctor.exe .
echo "Windows arm64..."; GOOS=windows GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/netstack-doctor-arm64.exe .

echo "Done:"; ls -lh dist/
