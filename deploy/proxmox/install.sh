#!/usr/bin/env bash
# Idempotent CT-side installer for mill on Debian 13 LXC. Run inside CT 122 as root.
set -euo pipefail

BIN_SRC="${1:-/tmp/mill-linux-amd64}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

if [ ! -f "$BIN_SRC" ]; then
  echo "missing binary at $BIN_SRC" >&2
  exit 1
fi

if ! id -u mill >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin mill
fi

install -d -m 0750 -o root -g mill /etc/mill
install -d -m 0750 -o mill -g mill /var/lib/mill
install -m 0755 -o root -g root "$BIN_SRC" /usr/local/bin/mill
install -m 0644 -o root -g root "$SCRIPT_DIR/mill.service" /etc/systemd/system/mill.service

if [ ! -f /etc/mill/mill.env ]; then
  TOKEN=$(openssl rand -hex 32)
  sed "s/replace-with-openssl-rand-hex-32/$TOKEN/" "$SCRIPT_DIR/mill.env.example" > /etc/mill/mill.env
  chown root:mill /etc/mill/mill.env
  chmod 0640 /etc/mill/mill.env
  echo "created /etc/mill/mill.env with generated MILL_API_TOKEN"
fi

if [ ! -f /var/lib/mill/mill.db ]; then
  su -s /bin/sh mill -c "MILL_DB=/var/lib/mill/mill.db /usr/local/bin/mill init"
fi

systemctl daemon-reload
systemctl enable --now mill.service

if command -v tailscale >/dev/null 2>&1; then
  tailscale serve --bg 127.0.0.1:8080
fi

systemctl status --no-pager mill.service
