#!/usr/bin/env bash
# Build the standalone Linux GUI application (WebKitGTK via cgo) and package it
# as a .deb with a desktop-menu entry, plus a plain tarball for other distros.
#
# Like the macOS and Windows builds this must run on the target platform: cgo
# cannot cross-compile the native webview.
#
# Build deps:  golang, gcc, pkg-config, libgtk-3-dev, and libwebkit2gtk-4.0-dev
#              (or 4.1 — see the shim below).
# Usage:       bash scripts/make-linux-app.sh [version]
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-1.2.1}"
ARCH="$(dpkg --print-architecture 2>/dev/null || echo amd64)"
PKG="netstack-doctor"
STAGE="dist/linux-pkg"

command -v pkg-config >/dev/null || { echo "error: pkg-config is required" >&2; exit 1; }

# webview_go pins the module name "webkit2gtk-4.0", but Ubuntu 24.04 and other
# current distros ship only 4.1. The two are compatible for the APIs webview
# uses, so when 4.0 is absent, generate a .pc file that forwards to 4.1 rather
# than requiring an EOL library.
if ! pkg-config --exists webkit2gtk-4.0; then
  if pkg-config --exists webkit2gtk-4.1; then
    echo "webkit2gtk-4.0 not found; shimming it onto the installed 4.1"
    SHIM="$(mktemp -d)"
    trap 'rm -rf "$SHIM"' EXIT
    cat > "$SHIM/webkit2gtk-4.0.pc" <<EOF
Name: webkit2gtk-4.0
Description: Shim forwarding the 4.0 module name onto the installed 4.1
Version: $(pkg-config --modversion webkit2gtk-4.1)
Requires: webkit2gtk-4.1
EOF
    export PKG_CONFIG_PATH="$SHIM${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
  else
    echo "error: neither webkit2gtk-4.0 nor webkit2gtk-4.1 is installed." >&2
    echo "       Debian/Ubuntu: apt install libgtk-3-dev libwebkit2gtk-4.1-dev" >&2
    echo "       Fedora:        dnf install gtk3-devel webkit2gtk4.1-devel" >&2
    exit 1
  fi
fi

mkdir -p dist
echo "Building GUI binary (CGO, ${ARCH})..."
CGO_ENABLED=1 go build -ldflags "-s -w" -o "dist/${PKG}" .

# ---- staging tree, laid out as it will be installed ----
rm -rf "$STAGE"
install -Dm755 "dist/${PKG}" "$STAGE/usr/bin/${PKG}"

install -Dm644 /dev/stdin "$STAGE/usr/share/applications/${PKG}.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=NetStack Doctor
GenericName=Network Diagnostics
Comment=Diagnose all seven OSI network layers
Exec=${PKG}
Icon=${PKG}
Terminal=false
Categories=Network;System;Utility;
Keywords=network;diagnostics;osi;dns;tls;ping;
EOF

for size in 16 32 48 64 128 256 512; do
  src="assets/png/icon-${size}.png"
  [ -f "$src" ] || continue
  install -Dm644 "$src" "$STAGE/usr/share/icons/hicolor/${size}x${size}/apps/${PKG}.png"
done

# ---- .deb ----
if command -v dpkg-deb >/dev/null; then
  install -Dm644 /dev/stdin "$STAGE/DEBIAN/control" <<EOF
Package: ${PKG}
Version: ${VERSION}
Section: net
Priority: optional
Architecture: ${ARCH}
Maintainer: Proclaim Advisors <noreply@proclaimadvisors.com>
Depends: libc6, libgtk-3-0, libwebkit2gtk-4.1-0 | libwebkit2gtk-4.0-37
Description: Cross-platform OSI-layer network diagnostics
 NetStack Doctor tests all seven OSI layers and reports each probe with
 verbose logs, in a native window. It can also interpret the results with
 a local LLM, entirely on this machine.
EOF
  DEB="dist/${PKG}_${VERSION}_${ARCH}.deb"
  dpkg-deb --build --root-owner-group "$STAGE" "$DEB" >/dev/null
  echo "Built: $DEB"
else
  echo "note: dpkg-deb not found, skipping .deb" >&2
fi

# ---- portable tarball for non-Debian distros ----
TAR="dist/${PKG}-${VERSION}-linux-${ARCH}.tar.gz"
tar -czf "$TAR" -C "$STAGE" usr
echo "Built: $TAR"

rm -rf "$STAGE"
ls -lh dist/
