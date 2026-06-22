#!/bin/bash
# One-click installer that works around macOS Gatekeeper for this unsigned
# (not notarized) app. It copies NetStack Doctor.app to /Applications, strips
# the quarantine attribute that triggers the "damaged / cannot be opened"
# error, and launches the app.
#
# HOW TO RUN: right-click this file in the DMG and choose "Open" (then "Open"
# again). Double-clicking may be blocked by Gatekeeper the first time.

set -e
HERE="$(cd "$(dirname "$0")" && pwd)"
APP_NAME="NetStack Doctor.app"
SRC="$HERE/$APP_NAME"
DEST="/Applications/$APP_NAME"

echo "Installing $APP_NAME …"
if [ ! -d "$SRC" ]; then
  echo "ERROR: could not find $APP_NAME next to this installer."
  read -r -p "Press Return to close." _
  exit 1
fi

# Copy into /Applications (replace any older copy).
rm -rf "$DEST" 2>/dev/null || true
cp -R "$SRC" "/Applications/"

# Remove the quarantine flag so Gatekeeper allows the unsigned app to run.
xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true

echo "Done. Launching NetStack Doctor…"
open "$DEST"
echo "You can now also launch it any time from your Applications folder."
sleep 1
