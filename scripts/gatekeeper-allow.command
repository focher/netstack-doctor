#!/usr/bin/env bash
# Allow NetStack Doctor to run on macOS by clearing the Gatekeeper quarantine
# attribute that macOS adds to downloaded, unsigned binaries.
#
# Usage: double-click this file in Finder, or run it from Terminal.
set -euo pipefail

cd "$(dirname "$0")"

# The macOS binary that ships alongside this script.
BIN="netstack-doctor-macos-arm64"

if [[ ! -f "$BIN" ]]; then
  echo "Could not find '$BIN' next to this script."
  echo "Make sure this script is in the same folder as the app."
  read -r -p "Press Return to close." _
  exit 1
fi

echo "Removing Gatekeeper quarantine from '$BIN'…"
xattr -d com.apple.quarantine "$BIN" 2>/dev/null || true
chmod +x "$BIN"

echo
echo "Done. You can now launch NetStack Doctor by running:"
echo "    ./$BIN"
echo
read -r -p "Launch it now? [y/N] " ans
if [[ "${ans:-}" =~ ^[Yy]$ ]]; then
  "./$BIN"
fi
