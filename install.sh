#!/usr/bin/env bash
set -euo pipefail

REPO="animesao/dck-wings"
BIN="/usr/local/bin/dck-wings"
CONFIG_DIR="/etc/dck-wings"
DATA_DIR="/var/lib/dck-wings"
LOG_DIR="/var/log/dck-wings"
SERVICE="/etc/systemd/system/dck-wings.service"

echo "==> dck-wings installer"
echo ""

if [ "$(id -u)" -ne 0 ]; then
  echo "Error: must run as root (sudo)"
  exit 1
fi

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  GOARCH="amd64" ;;
  aarch64) GOARCH="arm64" ;;
  armv7l)  GOARCH="arm" ;;
  *)
    echo "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

# Check for dck
if ! command -v dck &>/dev/null; then
  echo "Warning: 'dck' not found in PATH."
  echo "Install dck first: echo 'deb [trusted=yes] https://animesao.github.io/dck/apt ./' > /etc/apt/sources.list.d/dck.list && apt update && apt install dck"
  echo ""
fi

# Download dck-wings from GitHub releases
LATEST=$(curl -sfL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
if [ -z "$LATEST" ]; then
  echo "Error: could not fetch latest release"
  exit 1
fi

echo "Downloading dck-wings $LATEST for $GOARCH..."
URL="https://github.com/$REPO/releases/download/$LATEST/dck-wings-linux-$GOARCH"
TMP="/tmp/dck-wings"
curl -sfL "$URL" -o "$TMP"
chmod +x "$TMP"

echo "Installing binary to $BIN..."
mv "$TMP" "$BIN"

# Create directories
mkdir -p "$CONFIG_DIR" "$DATA_DIR" "$LOG_DIR"

# Config
if [ ! -f "$CONFIG_DIR/config.toml" ]; then
  echo "Creating default config at $CONFIG_DIR/config.toml..."
  API_KEY=$(openssl rand -hex 32)
  cat > "$CONFIG_DIR/config.toml" <<EOF
# dck-wings configuration
port = 8080
host = "0.0.0.0"
api_key = "$API_KEY"
dck_bin = "/usr/local/bin/dck"
data_dir = "$DATA_DIR"
log_dir = "$LOG_DIR"
EOF
  echo "  API key: $API_KEY"
  echo "  Save this key — you'll need it in dck-panel!"
fi

# Systemd service
if [ ! -f "$SERVICE" ]; then
  echo "Creating systemd service..."
  cat > "$SERVICE" <<UNIT
[Unit]
Description=dck-wings - Container management agent
After=network.target

[Service]
Type=simple
ExecStart=$BIN -config $CONFIG_DIR/config.toml
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT
fi

echo ""
echo "==> Installation complete!"
echo ""
echo "Next steps:"
echo "  1. Review config:  nano $CONFIG_DIR/config.toml"
echo "  2. Start service:  systemctl enable --now dck-wings"
echo "  3. Check status:   systemctl status dck-wings"
echo "  4. View logs:      journalctl -u dck-wings -f"
echo ""
echo "If you changed the API key, add it to dck-panel's .env:"
echo "  DECK_WINGS_API_KEY=<your-key>"
echo "  DECK_WINGS_URL=http://<vds-ip>:8080"
