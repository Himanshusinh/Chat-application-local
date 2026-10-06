#!/bin/bash
# Usage: scripts/build-mac.sh <version> <outdir>   (macOS only; needs Go + Xcode CLT)
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=$1; mkdir -p "$2"; OUT=$(cd "$2" && pwd)
export MACOSX_DEPLOYMENT_TARGET=13.0
export CGO_CFLAGS="-O2 -mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0"
LDFLAGS="-s -w -X main.version=$VERSION"
WORK=$(mktemp -d)
CGO_ENABLED=1 GOARCH=arm64 CC="clang -arch arm64" go build -trimpath -ldflags "$LDFLAGS" -o "$WORK/oc-arm64" .
CGO_ENABLED=1 GOARCH=amd64 CC="clang -arch x86_64" go build -trimpath -ldflags "$LDFLAGS" -o "$WORK/oc-amd64" .

APP="$WORK/OfficeChat.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
lipo -create -output "$APP/Contents/MacOS/OfficeChat" "$WORK/oc-arm64" "$WORK/oc-amd64"
sed "s/__VERSION__/$VERSION/g" scripts/Info.plist > "$APP/Contents/Info.plist"
ICONSET="$WORK/OfficeChat.iconset"; mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -z $s $s web/icon.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
  sips -z $((s*2)) $((s*2)) web/icon.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/OfficeChat.icns"
# Ad-hoc signature (required on Apple Silicon). A Developer ID certificate
# would remove the first-launch warning.
codesign --force --deep --sign - "$APP"

# Zip used by the auto-updater, and the DMG people install from.
(cd "$WORK" && ditto -c -k --keepParent OfficeChat.app "$OUT/OfficeChat-mac.zip")
DMG="$WORK/dmg"; mkdir -p "$DMG"; cp -R "$APP" "$DMG/"; ln -s /Applications "$DMG/Applications"
hdiutil create -quiet -volname "OfficeChat" -srcfolder "$DMG" -ov -format UDZO "$OUT/OfficeChat.dmg"
rm -rf "$OUT/OfficeChat.app"; cp -R "$APP" "$OUT/OfficeChat.app"
echo "macOS $VERSION built in $OUT"
