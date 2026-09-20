#!/usr/bin/env bash
# Install Nexus Desktop (tray + daemon + CLI) for macOS.
#
#   curl -fsSL https://central-memory-releases.s3.ap-south-1.amazonaws.com/desktop/latest/install-macos.sh | bash
#
set -euo pipefail

BUCKET_BASE="${NEXUS_BUCKET_BASE:-https://central-memory-releases.s3.ap-south-1.amazonaws.com}"
VERSION="${NEXUS_VERSION:-latest}"
BIN_DIR="${HOME}/.local/share/nexus/bin"
LAUNCH_AGENTS="${HOME}/Library/LaunchAgents"
PLIST="${LAUNCH_AGENTS}/dev.pratyushes.nexus-desktop.plist"

arch="$(uname -m)"
case "$arch" in
  arm64|aarch64) GOARCH=arm64 ;;
  x86_64|amd64)  GOARCH=amd64 ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac

mkdir -p "$BIN_DIR" "$LAUNCH_AGENTS"

if [ "$VERSION" = "latest" ]; then
  VER="$(curl -fsSL "$BUCKET_BASE/desktop/latest.json" 2>/dev/null | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1 || true)"
  if [ -n "${VER:-}" ]; then
    VERSION="$VER"
  else
    VERSION="0.1.0"
  fi
fi

BASE="$BUCKET_BASE/desktop/$VERSION"
echo "Installing Nexus Desktop $VERSION (darwin/$GOARCH) into $BIN_DIR"

download() {
  local name="$1" dest="$2"
  local url="$BASE/$name"
  echo "  downloading $name…"
  curl -fsSL "$url" -o "$dest"
  chmod +x "$dest"
}

download "nexus-desktop-darwin-${GOARCH}" "$BIN_DIR/nexus-desktop"
download "nexus-daemon-darwin-${GOARCH}" "$BIN_DIR/nexus-daemon"
download "nexus-darwin-${GOARCH}" "$BIN_DIR/nexus"

# PATH for this user (zsh/bash)
ensure_path() {
  local marker='# nexus-bin'
  local line="export PATH=\"$BIN_DIR:\$PATH\" $marker"
  for rc in "$HOME/.zprofile" "$HOME/.zshrc" "$HOME/.bash_profile" "$HOME/.bashrc"; do
    if [ -f "$rc" ] || [ "$rc" = "$HOME/.zprofile" ]; then
      touch "$rc"
      if ! grep -qF "$marker" "$rc" 2>/dev/null; then
        printf '\n%s\n' "$line" >> "$rc"
      fi
    fi
  done
  export PATH="$BIN_DIR:$PATH"
}
ensure_path

# LaunchAgent — start tray at login
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>dev.pratyushes.nexus-desktop</string>
  <key>ProgramArguments</key>
  <array>
    <string>$BIN_DIR/nexus-desktop</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <false/>
  <key>WorkingDirectory</key>
  <string>$BIN_DIR</string>
  <key>StandardOutPath</key>
  <string>$HOME/Library/Logs/nexus-desktop.log</string>
  <key>StandardErrorPath</key>
  <string>$HOME/Library/Logs/nexus-desktop.log</string>
</dict>
</plist>
EOF

launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST" 2>/dev/null || true
nohup "$BIN_DIR/nexus-desktop" >/dev/null 2>&1 &

echo ""
echo "Installed."
echo "  Binaries: $BIN_DIR"
echo "  Login:    LaunchAgent $PLIST"
echo "  Quit:     menu bar → Quit Nexus Desktop"
echo ""
