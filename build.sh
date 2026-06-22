#!/usr/bin/env bash
# Build NetStack Doctor — a standalone, native-window desktop app (no browser).
#
# The GUI uses a native webview (WKWebView on macOS, WebView2 on Windows) via
# cgo, so each platform's GUI binary must be built ON that platform (cgo cannot
# cross-compile the native webview). macOS is built here; Windows is built by
# the GitHub Actions workflow (.github/workflows/release.yml) on a Windows runner.
#
# Usage:
#   ./build.sh                 macOS .app bundle (default)
#   ./build.sh dmg             distributable .dmg (app + Gatekeeper-bypass installer)
#   ./build.sh package         dmg + checksums for release
#   ./build.sh headless        portable server-only binary (no GUI, any OS via cross-compile)
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist
VERSION="1.2.1"

case "${1:-app}" in
  headless)
    echo "Building headless (server-only) binaries — no cgo, cross-compiles freely..."
    CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -tags headless -ldflags "-s -w" -o dist/netstack-doctor-headless-macos-arm64 .
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags headless -ldflags "-s -w" -o dist/netstack-doctor-headless-windows-amd64.exe .
    CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -tags headless -ldflags "-s -w" -o dist/netstack-doctor-headless-linux-amd64 .
    ;;
  dmg)
    bash scripts/make-dmg.sh "$VERSION"
    ;;
  package)
    bash scripts/make-dmg.sh "$VERSION"
    ( cd dist && shasum -a 256 *.dmg > SHA256SUMS.txt 2>/dev/null || true )
    ;;
  *)
    bash scripts/make-macos-app.sh "$VERSION"
    ;;
esac

echo "Done:"; ls -lh dist/
