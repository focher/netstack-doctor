#!/usr/bin/env bash
# Allow NetStack Doctor to run on macOS by clearing the Gatekeeper quarantine
# attribute that macOS adds to downloaded, unsigned apps.
#
# Usage: double-click this file in Finder, or run it from Terminal. Keep it in
# the same folder as "NetStack Doctor.app".
set -euo pipefail
cd "$(dirname "$0")"

APP="NetStack Doctor.app"

if [[ ! -d "$APP" ]]; then
  echo "Could not find '$APP' next to this script."
  echo "Make sure this script is in the same folder as the app."
  read -r -p "Press Return to close." _
  exit 1
fi

echo "Removing Gatekeeper quarantine from '$APP'…"
xattr -dr com.apple.quarantine "$APP" 2>/dev/null || true

echo
echo "Done. Launching NetStack Doctor…"
open "$APP"
