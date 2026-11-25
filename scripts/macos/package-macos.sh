#!/usr/bin/env bash
# Package Nexus.app (from scripts/macos/build-app.sh) for distribution:
# Velopack update feed + portable zip, and the branded DMG.
#
#   scripts/macos/package-macos.sh --app dist/Nexus.app --version 0.4.1 --channel osx \
#       [--out dist/macos] [--bundle-version 123] [--previous-feed https://.../desktop/velopack]
#
# Output (<out>):
#   releases/                     Velopack feed for <channel>: releases.<channel>.json,
#                                 assets.<channel>.json, NexusMac-<ver>-full.nupkg (+ delta)
#   Nexus-<ver>-osx-arm64.dmg     the download: drag Nexus to Applications
#   Nexus-<ver>-osx-arm64.zip     the same app, zipped (portable)
#   SHA256SUMS-macos.txt
#
# Pack id is NexusMac and channels are osx / osx-beta, so the macOS feed can
# live in the same S3 folder as Windows (NexusDesktop, win / win-beta)
# without a file name in common.
#
# Signing. Without a Developer ID the app is signed ad hoc: vpk signs the
# bundle after adding UpdateMac (with the hardened runtime and
# desktop/uno/macos/Nexus.entitlements, exactly as a Developer ID build will
# be), through scripts/macos/adhoc-codesign, which only drops the timestamp
# an ad-hoc signature can't have. To sign and notarize, set (CI secrets):
#   MACOS_SIGN_IDENTITY   "Developer ID Application: Name (TEAMID)", in the keychain
#   MACOS_NOTARY_PROFILE  a `xcrun notarytool store-credentials` profile name
#   MACOS_KEYCHAIN        optional keychain path holding both
# Nothing else changes: the DMG is then signed, notarized and stapled too.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
APP=""
VERSION=""
CHANNEL="osx"
OUT="$ROOT/dist/macos"
BUNDLE_VERSION=""
PREVIOUS_FEED=""
while [ $# -gt 0 ]; do
  case "$1" in
    --app) APP="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --channel) CHANNEL="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --bundle-version) BUNDLE_VERSION="$2"; shift 2 ;;
    --previous-feed) PREVIOUS_FEED="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
test -d "$APP" || { echo "--app must be a built Nexus.app" >&2; exit 2; }
test -n "$VERSION" || { echo "--version is required" >&2; exit 2; }
case "$CHANNEL" in osx|osx-beta|osx-ci) ;; *) echo "channel must be osx, osx-beta or osx-ci" >&2; exit 2 ;; esac
command -v vpk >/dev/null || { echo "vpk not found: dotnet tool install --global vpk --version 1.2.161" >&2; exit 2; }

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
RELEASES="$OUT/releases"
STAGE="$OUT/stage"
rm -rf "$STAGE" "$OUT/dmgroot"
mkdir -p "$RELEASES" "$STAGE"
SHORT="${VERSION%%-*}"
# vpk runs on the .NET 8 runtime; let it use a newer one when that's all there is.
export DOTNET_ROLL_FORWARD=Major

echo "--- stage Nexus.app $VERSION"
ditto "$APP" "$STAGE/Nexus.app"
PL="$STAGE/Nexus.app/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $SHORT" "$PL"
if [ -n "$BUNDLE_VERSION" ]; then /usr/libexec/PlistBuddy -c "Set :CFBundleVersion $BUNDLE_VERSION" "$PL"; fi
/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' -c 'Print :CFBundleVersion' "$PL"
EXE=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$PL")

if [ -n "$PREVIOUS_FEED" ]; then
  echo "--- previous release (for a delta package)"
  vpk download http --url "$PREVIOUS_FEED" --channel "$CHANNEL" --outputDir "$RELEASES" \
    || echo "::warning::No previous $CHANNEL release at $PREVIOUS_FEED; building without a delta."
fi

echo "--- vpk pack ($CHANNEL)"
SIGN_ARGS=()
if [ -n "${MACOS_SIGN_IDENTITY:-}" ]; then
  SIGN_ARGS+=(--signAppIdentity "$MACOS_SIGN_IDENTITY")
  if [ -n "${MACOS_NOTARY_PROFILE:-}" ]; then SIGN_ARGS+=(--notaryProfile "$MACOS_NOTARY_PROFILE"); fi
  if [ -n "${MACOS_KEYCHAIN:-}" ]; then SIGN_ARGS+=(--keychain "$MACOS_KEYCHAIN"); fi
  echo "signing as: $MACOS_SIGN_IDENTITY (notarize: ${MACOS_NOTARY_PROFILE:+yes}${MACOS_NOTARY_PROFILE:-no})"
else
  SIGN_ARGS+=(--signAppIdentity -)
  export PATH="$ROOT/scripts/macos/adhoc-codesign:$PATH"
  chmod +x "$ROOT/scripts/macos/adhoc-codesign/codesign"
  echo "no Developer ID: signing ad hoc (unsigned beta; Gatekeeper asks on first open)"
fi
vpk pack \
  --packId NexusMac \
  --packTitle Nexus \
  --packAuthors Nexus \
  --packVersion "$VERSION" \
  --packDir "$STAGE/Nexus.app" \
  --mainExe "$EXE" \
  --runtime osx-arm64 \
  --channel "$CHANNEL" \
  --outputDir "$RELEASES" \
  --noInst \
  --signEntitlements "$ROOT/desktop/uno/macos/Nexus.entitlements" \
  "${SIGN_ARGS[@]}"
ls -la "$RELEASES"

PORTABLE=$(ls "$RELEASES"/*-Portable.zip | head -1)
test -f "$PORTABLE" || { echo "vpk produced no portable zip" >&2; exit 1; }
ZIP="$OUT/Nexus-$VERSION-osx-arm64.zip"
cp -f "$PORTABLE" "$ZIP"

# The DMG carries the app exactly as Velopack packed it (UpdateMac and
# sq.version inside), so a dragged copy is an installed, self-updating one.
echo "--- app as installed"
mkdir -p "$OUT/dmgroot"
ditto -x -k "$PORTABLE" "$OUT/dmgroot"
INSTALLED="$OUT/dmgroot/Nexus.app"
test -d "$INSTALLED" || { echo "no Nexus.app in $PORTABLE" >&2; ls "$OUT/dmgroot"; exit 1; }
ls -la "$INSTALLED/Contents/MacOS/UpdateMac" "$INSTALLED/Contents/Resources/sq.version"
codesign --verify --deep --strict --verbose=2 "$INSTALLED"
codesign -dv --entitlements - "$INSTALLED" 2>&1 | grep -E 'Identifier|Signature|flags|TeamIdentifier|allow-jit' || true

echo "--- DMG background"
BG="$OUT/dmg-background"
mkdir -p "$BG"
swift "$ROOT/scripts/macos/dmg-background.swift" "$ROOT/desktop/windows/Assets/Nexus.png" "$BG"
tiffutil -cathidpicheck "$BG/background.png" "$BG/background@2x.png" -out "$BG/background.tiff"

echo "--- DMG"
if ! command -v dmgbuild >/dev/null; then
  python3 -m venv "$OUT/.venv"
  "$OUT/.venv/bin/pip" install --quiet "dmgbuild>=1.6,<2"
  DMGBUILD="$OUT/.venv/bin/dmgbuild"
else
  DMGBUILD=dmgbuild
fi
DMG="$OUT/Nexus-$VERSION-osx-arm64.dmg"
rm -f "$DMG"
VOLICON=$(find "$INSTALLED/Contents/Resources" -maxdepth 1 -name '*.icns' | head -1)
"$DMGBUILD" -s "$ROOT/scripts/macos/dmg-settings.py" \
  -D app="$INSTALLED" -D background="$BG/background.tiff" -D icon="$VOLICON" \
  "Nexus" "$DMG"
if [ -n "${MACOS_SIGN_IDENTITY:-}" ]; then
  codesign --force --sign "$MACOS_SIGN_IDENTITY" --timestamp ${MACOS_KEYCHAIN:+--keychain "$MACOS_KEYCHAIN"} "$DMG"
  if [ -n "${MACOS_NOTARY_PROFILE:-}" ]; then
    xcrun notarytool submit "$DMG" --keychain-profile "$MACOS_NOTARY_PROFILE" ${MACOS_KEYCHAIN:+--keychain "$MACOS_KEYCHAIN"} --wait
    xcrun stapler staple "$DMG"
  fi
fi
hdiutil verify "$DMG"

(cd "$OUT" && shasum -a 256 "$(basename "$DMG")" "$(basename "$ZIP")" > SHA256SUMS-macos.txt && cat SHA256SUMS-macos.txt)
ls -la "$OUT"
