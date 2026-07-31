#!/usr/bin/env bash
# Build a distributable macOS DMG containing the self-contained app, an alias
# of /Applications to drag it onto, and a one-click Gatekeeper-bypass installer.
#
# The image is built read/write first so Finder can record a window layout into
# it (app on the left, Applications alias on the right — the conventional
# drag-to-install gesture), then converted to the compressed read-only image
# that actually ships.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-1.2.1}"
APP="dist/NetStack Doctor.app"
VOLNAME="NetStack Doctor"
DMG="dist/NetStack-Doctor-${VERSION}-macos-arm64.dmg"
RW_DMG="dist/netstack-doctor-rw.dmg"
STAGE="dist/dmg-stage"

# 1. Build the app bundle.
bash scripts/make-macos-app.sh "$VERSION"

# 2. Stage the DMG contents.
rm -rf "$STAGE" "$DMG" "$RW_DMG"
mkdir -p "$STAGE"
cp -R "$APP" "$STAGE/"
# The drop target for the drag-to-install gesture.
ln -s /Applications "$STAGE/Applications"
cp scripts/dmg-install.command "$STAGE/Install — Bypass Gatekeeper.command"
chmod +x "$STAGE/Install — Bypass Gatekeeper.command"
cp scripts/READ-ME-FIRST.txt "$STAGE/READ ME FIRST.txt" 2>/dev/null || true

# 3. Create a writable image with headroom, so Finder has room to write the
#    .DS_Store holding the layout. HFS+ keeps Finder's layout handling simple.
STAGE_KB="$(du -sk "$STAGE" | awk '{print $1}')"
IMG_MB=$(( STAGE_KB / 1024 + 50 ))
hdiutil create \
  -volname "$VOLNAME" \
  -srcfolder "$STAGE" \
  -fs HFS+ \
  -format UDRW \
  -size "${IMG_MB}m" \
  -ov "$RW_DMG" >/dev/null

# 4. Mount it and let Finder position the icons. Finder scripting needs a GUI
#    session and can fail on headless machines; the layout is cosmetic, so a
#    failure must not fail the build — the Applications alias works regardless.
MOUNT_POINT="$(hdiutil attach "$RW_DMG" -nobrowse -noverify -noautoopen \
  | grep -Eo '/Volumes/.*$' | tail -1)"
if [ -z "$MOUNT_POINT" ]; then
  echo "error: failed to mount $RW_DMG" >&2
  exit 1
fi
# Detach on any exit path, so a failed layout never leaves a volume mounted.
trap 'hdiutil detach "$MOUNT_POINT" >/dev/null 2>&1 || hdiutil detach "$MOUNT_POINT" -force >/dev/null 2>&1 || true' EXIT

if osascript scripts/dmg-layout.applescript "$(basename "$MOUNT_POINT")" >/dev/null 2>&1; then
  echo "Applied DMG window layout."
else
  echo "warning: could not apply Finder window layout; shipping default view" >&2
fi

sync
hdiutil detach "$MOUNT_POINT" >/dev/null 2>&1 \
  || hdiutil detach "$MOUNT_POINT" -force >/dev/null
trap - EXIT

# 5. Convert to the compressed, read-only image that ships.
hdiutil convert "$RW_DMG" -format UDZO -imagekey zlib-level=9 -ov -o "$DMG" >/dev/null

rm -f "$RW_DMG"
rm -rf "$STAGE"
echo "Built: $DMG"
shasum -a 256 "$DMG"
