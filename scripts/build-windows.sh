#!/bin/bash
# Usage: scripts/build-windows.sh <version> <outdir>
# Works on macOS and Linux (GitHub Actions). Needs Go and NSIS (makensis).
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=$1; mkdir -p "$2"; OUT=$(cd "$2" && pwd)
LDFLAGS="-s -w -H=windowsgui -X main.version=$VERSION"
export PATH="$PATH:$(go env GOPATH)/bin"

command -v rsrc >/dev/null || go install github.com/akavel/rsrc@latest
for arch in amd64 arm64; do rsrc -ico web/icon.ico -arch $arch -o rsrc_windows_$arch.syso >/dev/null; done
trap 'rm -f rsrc_windows_*.syso' EXIT
for arch in amd64 arm64; do
  GOOS=windows GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/OfficeChat-windows-$arch.exe" .
done

# Installer (Program Files, shortcuts, uninstaller, firewall).
if [[ "$(uname)" == "Darwin" ]]; then LOC=en_US.UTF-8; else LOC=C.UTF-8; fi
LANG=$LOC LC_ALL=$LOC makensis -V2 -DVERSION="$VERSION" -DSRC="$OUT" -DICON="$PWD/web/icon-installer.ico" \
  -DOUT="$OUT/OfficeChat-Setup.exe" scripts/installer.nsi

# Portable zip for people without admin rights.
P=$(mktemp -d)/OfficeChat-Windows-Portable; mkdir -p "$P"
cp "$OUT/OfficeChat-windows-amd64.exe" "$P/OfficeChat.exe"
cp scripts/allow-firewall.bat "$P/Allow OfficeChat through firewall.bat"
cp scripts/README-Windows.txt "$P/README.txt"
(cd "$(dirname "$P")" && zip -qr "$OUT/OfficeChat-Windows-Portable.zip" OfficeChat-Windows-Portable)
echo "Windows $VERSION built in $OUT"
