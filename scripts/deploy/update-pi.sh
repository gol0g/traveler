#!/bin/bash
# update-pi.sh — 바이너리만 업데이트 (서비스 재시작 포함)
# Usage: ./update-pi.sh [pi-host] [pi-user]

set -euo pipefail

PI_HOST="${1:-raspberrypi.local}"
PI_USER="${2:-pi}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
BINARY="${PROJECT_DIR}/traveler-linux-arm64"

echo "Building for linux/arm64..."
cd "${PROJECT_DIR}"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o "${BINARY}" ./cmd/traveler/
echo "Built: $(ls -lh "${BINARY}" | awk '{print $5}')"

echo "Uploading binary..."
scp "${BINARY}" "${PI_USER}@${PI_HOST}:/tmp/traveler"

echo "Installing and restarting services..."
ssh "${PI_USER}@${PI_HOST}" bash <<'REMOTE'
sudo mv /tmp/traveler /usr/local/bin/traveler
sudo chmod +x /usr/local/bin/traveler

# Restart always-on services
sudo systemctl restart traveler-web.service
sudo systemctl restart traveler-crypto.service

echo "Done. Service status:"
systemctl is-active traveler-web.service    && echo "  web:    active" || echo "  web:    inactive"
systemctl is-active traveler-crypto.service && echo "  crypto: active" || echo "  crypto: inactive"
/usr/local/bin/traveler --help 2>&1 | head -1
REMOTE

echo "Update complete!"
