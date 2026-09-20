#!/usr/bin/env bash
# Install Nexus Desktop (tray + daemon + CLI) for Linux.
#
#   curl -fsSL https://central-memory-releases.s3.ap-south-1.amazonaws.com/desktop/latest/install-linux.sh | bash
#
set -euo pipefail

BUCKET_BASE="${NEXUS_BUCKET_BASE:-https://central-memory-releases.s3.ap-south-1.amazonaws.com}"
VERSION="${NEXUS_VERSION:-latest}"
BIN_DIR="${HOME}/.local/share/nexus/bin"
AUTOSTART_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/autostart"
DESKTOP_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/applications"

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)       GOARCH=amd64 ;;
  aarch64|arm64)      GOARCH=arm64 ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac

mkdir -p "$BIN_DIR" "$AUTOSTART_DIR" "$DESKTOP_DIR"

if [ "$VERSION" = "latest" ]; then
  VER="$(curl -fsSL "$BUCKET_BASE/desktop/latest.json" 2>/dev/null | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1 || true)"
  if [ -n "${VER:-}" ]; then
    VERSION="$VER"
  else
    VERSION="0.1.0"
  fi
fi

BASE="$BUCKET_BASE/desktop/$VERSION"
echo "Installing Nexus Desktop $VERSION (linux/$GOARCH) into $BIN_DIR"

download() {
  local name="$1" dest="$2"
  echo "  downloading $name…"
  curl -fsSL "$BASE/$name" -o "$dest"
  chmod +x "$dest"
}

download "nexus-desktop-linux-${GOARCH}" "$BIN_DIR/nexus-desktop"
download "nexus-daemon-linux-${GOARCH}" "$BIN_DIR/nexus-daemon"
download "nexus-linux-${GOARCH}" "$BIN_DIR/nexus"

marker='# nexus-bin'
line="export PATH=\"$BIN_DIR:\$PATH\" $marker"
for rc in "$HOME/.bashrc" "$HOME/.profile" "$HOME/.zshrc"; do
  touch "$rc"
  if ! grep -qF "$marker" "$rc" 2>/dev/null; then
    printf '\n%s\n' "$line" >> "$rc"
  fi
done
export PATH="$BIN_DIR:$PATH"

cat > "$DESKTOP_DIR/nexus-desktop.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Nexus Desktop
Comment=Nexus memory harvest tray
Exec=$BIN_DIR/nexus-desktop
Icon=utilities-terminal
Terminal=false
Categories=Development;Utility;
StartupNotify=false
EOF

cp "$DESKTOP_DIR/nexus-desktop.desktop" "$AUTOSTART_DIR/nexus-desktop.desktop"

# Prefer a detached start so piping curl|bash does not kill the tray.
nohup "$BIN_DIR/nexus-desktop" >/dev/null 2>&1 &

echo ""
echo "Installed."
echo "  Binaries:  $BIN_DIR"
echo "  App menu:  nexus-desktop.desktop"
echo "  Autostart: $AUTOSTART_DIR/nexus-desktop.desktop"
echo "  Quit:      tray menu → Quit Nexus Desktop"
echo ""
