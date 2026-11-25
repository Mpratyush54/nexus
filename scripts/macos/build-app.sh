#!/usr/bin/env bash
# Build Nexus.app for macOS (Apple silicon): the Uno head (desktop/uno) as a
# self-contained osx-arm64 bundle, with the universal Go core
# (libnexuscore.dylib) and the AppKit helper (libnexusmac.dylib), our
# Info.plist keys merged into Uno's, and an ad-hoc signature.
#
#   scripts/macos/build-app.sh [--version 0.4.1-beta.2] [--build 123] [--out dist]
#
# Output: <out>/Nexus.app and <out>/publish.log. Used by
# .github/workflows/desktop-macos.yml (checks) and release-desktop.yml
# (releases), which then run scripts/macos/package-macos.sh. Distribution
# signing happens there (vpk), not here: the ad-hoc signature only makes the
# bundle valid and runnable on Apple silicon.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/dist"
VERSION=""
BUILD="${GITHUB_RUN_NUMBER:-1}"
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --build) BUILD="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
APP="$OUT/Nexus.app"
CSPROJ="$ROOT/desktop/uno/Nexus.Uno.csproj"
if [ -z "$VERSION" ]; then
  VERSION=$(sed -n 's:.*<Version>\(.*\)</Version>.*:\1:p' "$CSPROJ" | head -1)
fi
SHORT="${VERSION%%-*}"
echo "Nexus.app $VERSION (CFBundleShortVersionString $SHORT, CFBundleVersion $BUILD)"

echo "--- libnexuscore.dylib (arm64 + x86_64, universal)"
(
  cd "$ROOT"
  CGO_ENABLED=1 GOARCH=arm64 CC="clang -arch arm64" \
    go build -tags nexuscorelib -trimpath -buildmode=c-shared -o "$OUT/arm64/libnexuscore.dylib" ./cmd/nexuscore
  CGO_ENABLED=1 GOARCH=amd64 CC="clang -arch x86_64" \
    go build -tags nexuscorelib -trimpath -buildmode=c-shared -o "$OUT/amd64/libnexuscore.dylib" ./cmd/nexuscore
)
lipo -create -output "$OUT/libnexuscore.dylib" "$OUT/arm64/libnexuscore.dylib" "$OUT/amd64/libnexuscore.dylib"
lipo -info "$OUT/libnexuscore.dylib"

# The macOS integration helper: menu bar status item, app menu, Dock reopen,
# nexus:// URL events, App Translocation lookup (desktop/uno/macos/native/NexusMac.m).
echo "--- libnexusmac.dylib (arm64 + x86_64, universal)"
clang -arch arm64 -arch x86_64 -mmacosx-version-min=12.0 -fobjc-arc -Wall -Wextra -Wno-unused-parameter \
  -dynamiclib -framework AppKit -framework CoreServices \
  -install_name @rpath/libnexusmac.dylib -o "$OUT/libnexusmac.dylib" "$ROOT/desktop/uno/macos/native/NexusMac.m"
lipo -info "$OUT/libnexusmac.dylib"
nm -gU "$OUT/libnexusmac.dylib" | grep ' _nx_mac_' | sort -u

# Uno's own bundler lays the .app out the way codesign wants it (managed
# assemblies in Contents/Resources) and signs it; CodesignKey=- is ad hoc.
echo "--- dotnet publish (osx-arm64, self-contained)"
(
  cd "$ROOT/desktop/uno"
  dotnet publish Nexus.Uno.csproj -c Release -f net10.0-desktop -r osx-arm64 \
    --self-contained true -p:TargetFrameworks=net10.0-desktop -p:PackageFormat=app -p:CodesignKey=- \
    -p:Version="$VERSION" -p:InformationalVersion="$VERSION" \
    > "$OUT/publish.log" 2>&1 || { tail -80 "$OUT/publish.log"; exit 1; }
)
grep -E 'warning NU|error|->' "$OUT/publish.log" | tail -20 || true
built=$(find "$ROOT/desktop/uno/bin/Release/net10.0-desktop/osx-arm64/publish" -maxdepth 1 -name '*.app' | head -1)
test -n "$built" || { echo "no .app produced"; exit 1; }
rm -rf "$APP"
cp -R "$built" "$APP"
find "$APP" -type f | wc -l

echo "--- Info.plist"
PL="$APP/Contents/Info.plist"
sed -e "s/__SHORT_VERSION__/${SHORT}/" -e "s/__BUILD_VERSION__/${BUILD}/" \
  "$ROOT/desktop/uno/macos/Info.plist" > "$OUT/nexus.plist"
# Our keys win over Uno's defaults, except the executable name Uno chose.
exe=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$PL")
for key in CFBundleIdentifier CFBundleName CFBundleDisplayName CFBundleShortVersionString CFBundleVersion \
           LSMinimumSystemVersion LSApplicationCategoryType NSHighResolutionCapable \
           NSDocumentsFolderUsageDescription NSDesktopFolderUsageDescription NSDownloadsFolderUsageDescription CFBundleURLTypes; do
  /usr/libexec/PlistBuddy -c "Delete :$key" "$PL" 2>/dev/null || true
done
/usr/libexec/PlistBuddy -c "Merge $OUT/nexus.plist" "$PL"
/usr/libexec/PlistBuddy -c "Set :CFBundleExecutable $exe" "$PL"
plutil -lint "$PL"
# The Dock icon: Uno's packager names it icon.icns (from Resizetizer).
icon=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIconFile' "$PL")
test -f "$APP/Contents/Resources/${icon%.icns}.icns" || { echo "CFBundleIconFile $icon is missing"; exit 1; }

# The core goes where the other native libraries (SkiaSharp) are, which is
# where [LibraryImport("nexuscore")] probes.
skia=$(find "$APP" -name 'libSkiaSharp.dylib' | head -1)
dest=$(dirname "$skia")
cp "$OUT/libnexuscore.dylib" "$dest/libnexuscore.dylib"
cp "$OUT/libnexusmac.dylib" "$dest/libnexusmac.dylib"
echo "core and mac helper -> ${dest#$APP/}"

echo "--- ad-hoc codesign"
for lib in libnexuscore.dylib libnexusmac.dylib; do
  codesign --force --sign - --timestamp=none "$dest/$lib"
done
# The outer signature seals Info.plist and the new libraries.
codesign --force --sign - --timestamp=none "$APP"
codesign --verify --strict --verbose=2 "$APP"
du -sh "$APP"
