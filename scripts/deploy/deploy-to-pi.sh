#!/bin/bash
# deploy-to-pi.sh — Traveler 라즈베리파이 배포 스크립트
# Usage: ./deploy-to-pi.sh [pi-host] [pi-user]
#
# 사전 요구:
#   1. SSH key 등록 (ssh-copy-id pi@<host>)
#   2. Pi에 ~/.traveler/.env 파일 준비 (API 키 등)

set -euo pipefail

PI_HOST="${1:-raspberrypi.local}"
PI_USER="${2:-pi}"
PI_DEST="/usr/local/bin/traveler"
PI_DATA="/home/${PI_USER}/.traveler"
SYSTEMD_DIR="/etc/systemd/system"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
BINARY="${PROJECT_DIR}/traveler-linux-arm64"

echo "============================================"
echo " Traveler Pi Deployment"
echo "============================================"
echo " Host:    ${PI_USER}@${PI_HOST}"
echo " Binary:  ${BINARY}"
echo ""

# 1. Cross-compile
echo "[1/5] Building for linux/arm64..."
cd "${PROJECT_DIR}"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o "${BINARY}" ./cmd/traveler/
echo "  Built: $(ls -lh "${BINARY}" | awk '{print $5}')"

# 2. Transfer binary
echo "[2/5] Uploading binary..."
scp "${BINARY}" "${PI_USER}@${PI_HOST}:/tmp/traveler"
ssh "${PI_USER}@${PI_HOST}" "sudo mv /tmp/traveler ${PI_DEST} && sudo chmod +x ${PI_DEST}"
echo "  Installed to ${PI_DEST}"

# 3. Create data directory
echo "[3/5] Setting up data directory..."
ssh "${PI_USER}@${PI_HOST}" "mkdir -p ${PI_DATA}"

# 4. Install systemd services
echo "[4/5] Installing systemd services..."
SERVICES=(
    "traveler-web.service"
    "traveler-crypto.service"
    "traveler-us.service"
    "traveler-us.timer"
    "traveler-kr.service"
    "traveler-kr.timer"
)

for svc in "${SERVICES[@]}"; do
    scp "${SCRIPT_DIR}/systemd/${svc}" "${PI_USER}@${PI_HOST}:/tmp/${svc}"
    ssh "${PI_USER}@${PI_HOST}" "sudo mv /tmp/${svc} ${SYSTEMD_DIR}/${svc}"
    echo "  Installed ${svc}"
done

# Install logrotate config
scp "${SCRIPT_DIR}/systemd/traveler-logrotate.conf" "${PI_USER}@${PI_HOST}:/tmp/traveler-logrotate"
ssh "${PI_USER}@${PI_HOST}" "sudo mv /tmp/traveler-logrotate /etc/logrotate.d/traveler"
echo "  Installed logrotate config"

# 5. Enable and start services
echo "[5/5] Enabling services..."
ssh "${PI_USER}@${PI_HOST}" bash <<'REMOTE'
sudo systemctl daemon-reload

# Always-on services
sudo systemctl enable --now traveler-web.service
sudo systemctl enable --now traveler-crypto.service

# Timer-based services
sudo systemctl enable --now traveler-us.timer
sudo systemctl enable --now traveler-kr.timer

echo ""
echo "Service Status:"
systemctl is-active traveler-web.service    && echo "  traveler-web:    active" || echo "  traveler-web:    inactive"
systemctl is-active traveler-crypto.service && echo "  traveler-crypto: active" || echo "  traveler-crypto: inactive"
systemctl is-active traveler-us.timer       && echo "  traveler-us:     timer enabled" || echo "  traveler-us:     timer disabled"
systemctl is-active traveler-kr.timer       && echo "  traveler-kr:     timer enabled" || echo "  traveler-kr:     timer disabled"
REMOTE

echo ""
echo "============================================"
echo " Deployment complete!"
echo " Web UI: http://${PI_HOST}:8080"
echo "============================================"
