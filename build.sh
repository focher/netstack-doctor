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
#   ./build.sh package         macOS .app + tar.gz + checksums for release
#   ./build.sh headless        portable server-only binary (no GUI, any OS via cross-compile)
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist
VERSION="1.2.0"

case "${1:-app}" in
  headless)
    echo "Building headless (server-only) binaries — no cgo, cross-compiles freely..."
    CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -tags headless -ldflags "-s -w" -o dist/netstack-doctor-headless-macos-arm64 .
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags headless -ldflags "-s -w" -o dist/netstack-doctor-headless-windows-amd64.exe .
    CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -tags headless -ldflags "-s -w" -o dist/netstack-doctor-headless-linux-amd64 .
    ;;
  package)
    bash scripts/make-macos-app.sh "$VERSION"
    cp scripts/gatekeeper-allow.command dist/
    cp README.md dist/
    chmod +x dist/gatekeeper-allow.command
    ( cd dist
      tar -czf "netstack-doctor-macos-arm64.app.tar.gz" "NetStack Doctor.app" gatekeeper-allow.command README.md
      shasum -a 256 *.app.tar.gz > SHA256SUMS.txt 2>/dev/null || true
    )
    ;;
  *)
    bash scripts/make-macos-app.sh "$VERSION"
    ;;
esac

echo "Done:"; ls -lh dist/
