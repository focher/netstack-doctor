#!/usr/bin/env bash
# Build single self-contained binaries for macOS (Apple Silicon) and Windows,
# then package release archives. macOS Intel is intentionally not built.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist
LDFLAGS="-s -w"  # strip symbols for a smaller binary

echo "macOS  arm64..."; GOOS=darwin  GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/netstack-doctor-macos-arm64 .
echo "Windows amd64..."; GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/netstack-doctor.exe .
echo "Windows arm64..."; GOOS=windows GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/netstack-doctor-arm64.exe .

# Package only when explicitly requested: ./build.sh package
if [[ "${1:-}" == "package" ]]; then
  echo "Packaging release archives..."
  cp README.md dist/
  cp scripts/gatekeeper-allow.command dist/
  chmod +x dist/gatekeeper-allow.command
  ( cd dist
    # macOS (arm64 only) — bundle the Gatekeeper-allow helper alongside the binary.
    tar -czf netstack-doctor-macos-arm64.tar.gz netstack-doctor-macos-arm64 gatekeeper-allow.command README.md
    # Windows
    zip -q netstack-doctor-windows-amd64.zip netstack-doctor.exe README.md
    zip -q netstack-doctor-windows-arm64.zip netstack-doctor-arm64.exe README.md
    shasum -a 256 *.zip *.tar.gz > SHA256SUMS.txt
  )
fi

echo "Done:"; ls -lh dist/
