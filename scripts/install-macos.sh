#!/usr/bin/env bash
# Install Nexus for macOS (Apple silicon) and the nexus CLI.
#
#   curl -fsSL https://central-memory-releases.s3.ap-south-1.amazonaws.com/desktop/latest/install-macos.sh | bash
#
# Installs Nexus.app from the release's DMG into /Applications (or
# ~/Applications when /Applications isn't writable) and opens it. Nexus then
# updates itself. Files downloaded by curl aren't quarantined, so there's no
# Gatekeeper prompt on this path. The CLI goes to ~/.local/share/nexus/bin.
#
# Replaces the retired Fyne "Nexus Desktop" (nexus-desktop + nexus-daemon and
# its LaunchAgent) if an earlier version of this script installed it: the
# app hosts capture itself, like the Windows app.
set -euo pipefail

BUCKET_BASE="${NEXUS_BUCKET_BASE:-https://central-memory-releases.s3.ap-south-1.amazonaws.com}"
VERSION="${NEXUS_VERSION:-latest}"
BIN_DIR="${HOME}/.local/share/nexus/bin"
OLD_PLIST="${HOME}/Library/LaunchAgents/dev.pratyushes.nexus-desktop.plist"

arch="$(uname -m)"
case "$arch" in
  arm64|aarch64) GOARCH=arm64 ;;
  x86_64|amd64)  GOARCH=amd64 ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac

if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "$BUCKET_BASE/desktop/latest.json" 2>/dev/null | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1 || true)"
  if [ -z "$VERSION" ]; then echo "Could not read the latest Nexus version from $BUCKET_BASE." >&2; exit 1; fi
fi
BASE="$BUCKET_BASE/desktop/$VERSION"
TMP="$(mktemp -d)"
MOUNT=""
cleanup() { if [ -n "$MOUNT" ]; then hdiutil detach "$MOUNT" -quiet 2>/dev/null || true; fi; rm -rf "$TMP"; }
trap cleanup EXIT

# The retired Fyne app.
if [ -f "$OLD_PLIST" ]; then
  echo "Removing the old Nexus Desktop (Fyne) login item"
  launchctl unload "$OLD_PLIST" 2>/dev/null || true
  rm -f "$OLD_PLIST"
fi
pkill -f "$BIN_DIR/nexus-desktop" 2>/dev/null || true
rm -f "$BIN_DIR/nexus-desktop" "$BIN_DIR/nexus-daemon"

# The CLI (both architectures).
mkdir -p "$BIN_DIR"
echo "Installing the nexus CLI $VERSION (darwin/$GOARCH) into $BIN_DIR"
curl -fsSL "$BASE/nexus-darwin-${GOARCH}" -o "$BIN_DIR/nexus"
chmod +x "$BIN_DIR/nexus"
marker='# nexus-bin'
for rc in "$HOME/.zprofile" "$HOME/.zshrc" "$HOME/.bash_profile" "$HOME/.bashrc"; do
  if [ -f "$rc" ] || [ "$rc" = "$HOME/.zprofile" ]; then
    touch "$rc"
    grep -qF "$marker" "$rc" 2>/dev/null || printf '\nexport PATH="%s:$PATH" %s\n' "$BIN_DIR" "$marker" >> "$rc"
  fi
done

# The app (Apple silicon).
if [ "$GOARCH" != "arm64" ]; then
  echo ""
  echo "The Nexus app for Mac needs Apple silicon for now; an Intel build is coming soon."
  echo "The nexus CLI is installed: $BIN_DIR/nexus"
  exit 0
fi
DMG_NAME="Nexus-${VERSION}-osx-arm64.dmg"
echo "Downloading $DMG_NAME"
if ! curl -fsSL "$BASE/$DMG_NAME" -o "$TMP/Nexus.dmg"; then
  echo ""
  echo "Nexus for macOS isn't in release $VERSION yet: the Mac app is coming soon."
  echo "The nexus CLI is installed: $BIN_DIR/nexus"
  exit 0
fi
if curl -fsSL "$BASE/SHA256SUMS-macos.txt" -o "$TMP/SHA256SUMS-macos.txt" 2>/dev/null; then
  want="$(awk -v n="$DMG_NAME" '$2==n {print $1}' "$TMP/SHA256SUMS-macos.txt")"
  got="$(shasum -a 256 "$TMP/Nexus.dmg" | awk '{print $1}')"
  if [ -n "$want" ] && [ "$want" != "$got" ]; then echo "Checksum mismatch for $DMG_NAME" >&2; exit 1; fi
fi
MOUNT="$TMP/mnt"
mkdir -p "$MOUNT"
hdiutil attach "$TMP/Nexus.dmg" -nobrowse -readonly -noverify -mountpoint "$MOUNT" -quiet
DEST_DIR=/Applications
if ! [ -w "$DEST_DIR" ]; then DEST_DIR="$HOME/Applications"; mkdir -p "$DEST_DIR"; fi
# Quit a running copy first (only if running: telling a closed app to quit opens it).
if pgrep -f '/Nexus\.app/Contents/MacOS/Nexus( |$)' >/dev/null; then
  osascript -e 'tell application id "com.nexus.desktop" to quit' >/dev/null 2>&1 || true
  sleep 3
fi
rm -rf "$DEST_DIR/Nexus.app"
ditto "$MOUNT/Nexus.app" "$DEST_DIR/Nexus.app"
hdiutil detach "$MOUNT" -quiet || true
MOUNT=""
open "$DEST_DIR/Nexus.app"

echo ""
echo "Installed Nexus $VERSION."
echo "  App:  $DEST_DIR/Nexus.app (updates itself)"
echo "  CLI:  $BIN_DIR/nexus"
echo ""
