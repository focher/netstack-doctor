#!/usr/bin/env bash
# Build a distributable macOS DMG containing the self-contained app, a
# drag-to-Applications target, and a one-click Gatekeeper-bypass installer.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-1.2.1}"
APP="dist/NetStack Doctor.app"
DMG="dist/NetStack-Doctor-${VERSION}-macos-arm64.dmg"
STAGE="dist/dmg-stage"

# 1. Build the app bundle.
bash scripts/make-macos-app.sh "$VERSION"

# 2. Stage the DMG contents.
rm -rf "$STAGE" "$DMG"
mkdir -p "$STAGE"
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"
cp scripts/dmg-install.command "$STAGE/Install — Bypass Gatekeeper.command"
chmod +x "$STAGE/Install — Bypass Gatekeeper.command"
cp scripts/READ-ME-FIRST.txt "$STAGE/READ ME FIRST.txt" 2>/dev/null || true

# 3. Build a compressed, read-only DMG.
hdiutil create \
  -volname "NetStack Doctor" \
  -srcfolder "$STAGE" \
  -ov -format UDZO \
  "$DMG" >/dev/null

rm -rf "$STAGE"
echo "Built: $DMG"
shasum -a 256 "$DMG"
