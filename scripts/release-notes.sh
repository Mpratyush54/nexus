#!/usr/bin/env bash
# Prints the GitHub release body for a desktop release, from the files that
# are actually in the release folder (release-desktop.yml):
#
#   scripts/release-notes.sh <version> <dist-dir> > release-body.md
#
# Downloads first, by platform, so nobody picks an internal file by mistake.
set -euo pipefail
VER="$1"
DIST="$2"
has() { compgen -G "$DIST/$1" > /dev/null; }

echo "## Download Nexus $VER"
echo
if has "Nexus-Setup.exe"; then
  echo "- **Windows**: download **Nexus-Setup.exe** (Windows 10 and 11, x64). Nexus updates itself after that."
elif has "Nexus-*-win-x64-Setup.exe"; then
  echo "- **Windows**: download **$(basename "$(ls "$DIST"/Nexus-*-win-x64-Setup.exe | head -1)")** (Windows 10 and 11, x64). Nexus updates itself after that."
fi
if has "Nexus.dmg"; then
  echo "- **macOS**: download **Nexus.dmg** (Apple silicon, macOS 12 or later), open it and drag Nexus to Applications. Nexus updates itself after that."
  echo "  This is an unsigned beta, so macOS asks once: **right-click Nexus → Open**. On macOS 15 and later, open System Settings → Privacy & Security and click **Open Anyway**."
fi
echo
CLI=$(cd "$DIST" && ls nexus-windows-* nexus-darwin-* nexus-linux-* 2>/dev/null | sort | sed 's/^/`/; s/$/`/' | paste -sd ' ' - || true)
DAEMON=$(cd "$DIST" && ls nexus-daemon-* 2>/dev/null | sort | sed 's/^/`/; s/$/`/' | paste -sd ' ' - || true)
if [ -n "$CLI$DAEMON" ]; then
  echo "### Command-line tools"
  echo
  [ -n "$CLI" ] && echo "- The \`nexus\` CLI: $CLI"
  [ -n "$DAEMON" ] && echo "- The capture daemon for servers without the app (macOS, Linux): $DAEMON"
  INSTALLS=$(cd "$DIST" && ls install-*.ps1 install-*.sh 2>/dev/null | sed 's/^/`/; s/$/`/' | paste -sd ' ' - || true)
  [ -n "$INSTALLS" ] && echo "- Install scripts: $INSTALLS"
  echo
fi
echo "### Other files"
echo
echo "\`SHA256SUMS*.txt\` hold the checksums. Versioned installers (\`Nexus-$VER-…\`), portable zips, \`nexus-desktop-windows-amd64.zip\` and \`latest.json\` are for existing installs and the updater; you don't need them to install Nexus."
