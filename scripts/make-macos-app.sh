#!/usr/bin/env bash
# Build the standalone macOS application bundle (Apple Silicon).
# Produces dist/NetStack Doctor.app — a self-contained, native-window app
# (WKWebView) that needs no browser and no external runtime.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-1.2.0}"
APP="dist/NetStack Doctor.app"

echo "Building GUI binary (CGO, arm64)..."
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w" -o dist/netstack-doctor-macos-arm64 .

echo "Assembling $APP ..."
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp dist/netstack-doctor-macos-arm64 "$APP/Contents/MacOS/netstack-doctor"
cp assets/NetStackDoctor.icns "$APP/Contents/Resources/AppIcon.icns"

cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>NetStack Doctor</string>
  <key>CFBundleDisplayName</key><string>NetStack Doctor</string>
  <key>CFBundleIdentifier</key><string>com.proclaimadvisors.netstackdoctor</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleExecutable</key><string>netstack-doctor</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict>
</plist>
EOF

# Ad-hoc signature so the app launches; replace with a Developer ID to notarize.
codesign --force --deep --sign - "$APP" >/dev/null 2>&1 || true

echo "Done: $APP"
