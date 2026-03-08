#!/bin/bash
# update-pi.sh — 바이너리 업데이트 + 전체 서비스 재시작 + 검증
# Usage: ./update-pi.sh [pi-host] [pi-user]
#
# 1. 빌드
# 2. 모든 서비스 중지 (+ nohup 프로세스 kill)
# 3. 바이너리 교체
# 4. 모든 서비스 재시작
# 5. sim 데몬 재시작 (nohup)
# 6. 전체 상태 검증

set -euo pipefail

PI_HOST="${1:-100.78.139.68}"
PI_USER="${2:-junghyun}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
BINARY="${PROJECT_DIR}/traveler-linux-arm64"

# All systemd services
SERVICES=(
    traveler-web
    traveler-arb
    traveler-binance
    traveler-btcfutures
    traveler-crypto
    traveler-dca
    traveler-scalp
    traveler-kr-dca
)

echo "=== TRAVELER PI UPDATE ==="
echo ""

# 1. Build
echo "[1/6] Building for linux/arm64..."
cd "${PROJECT_DIR}"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o "${BINARY}" ./cmd/traveler/
echo "  Built: $(ls -lh "${BINARY}" | awk '{print $5}')"

# 2. Upload
echo "[2/6] Uploading binary..."
scp -q "${BINARY}" "${PI_USER}@${PI_HOST}:/tmp/traveler"

# 3. Stop all + replace + restart + verify (single SSH session)
echo "[3/6] Stopping services, replacing binary, restarting..."
ssh "${PI_USER}@${PI_HOST}" bash <<'REMOTE'
set -e
DATA_DIR="/home/junghyun/.traveler"

# --- Check if market daemons are running (timer-triggered, oneshot) ---
for MARKET_SVC in traveler-kr traveler-us; do
    if systemctl is-active --quiet "$MARKET_SVC" 2>/dev/null; then
        echo "  ⚠ WARNING: $MARKET_SVC is running (market hours). It will be restarted."
    fi
done

# --- Stop all systemd services ---
echo "  Stopping systemd services..."
sudo systemctl stop traveler-web traveler-arb traveler-binance traveler-crypto traveler-dca traveler-scalp traveler-kr-dca 2>/dev/null || true
# Also stop market daemons if running (timer-triggered oneshot)
sudo systemctl stop traveler-kr traveler-us 2>/dev/null || true

# --- Kill any nohup traveler processes (sim-kr, sim-us, etc.) ---
echo "  Killing nohup processes..."
SIM_PIDS=$(sudo pgrep -f 'traveler.*--sim' 2>/dev/null || true)
if [ -n "$SIM_PIDS" ]; then
    sudo kill $SIM_PIDS 2>/dev/null || true
    sleep 1
fi

# --- Verify all stopped ---
REMAINING=$(sudo pgrep -af traveler 2>/dev/null | grep -v pgrep | grep -v sentinel || true)
if [ -n "$REMAINING" ]; then
    echo "  WARNING: still running:"
    echo "$REMAINING"
    echo "  Force killing..."
    sudo pkill -9 -f 'traveler' 2>/dev/null || true
    sleep 1
fi

# --- Replace binary ---
echo "  Replacing binary..."
sudo cp /tmp/traveler /usr/local/bin/traveler
sudo chmod +x /usr/local/bin/traveler

# --- Restart systemd services ---
echo "  Starting systemd services..."
sudo systemctl start traveler-web traveler-arb traveler-binance traveler-crypto traveler-dca traveler-scalp traveler-kr-dca
# Re-trigger market daemons if within market hours (they were stopped above)
for MARKET_SVC in traveler-kr traveler-us; do
    TIMER="${MARKET_SVC}.timer"
    if systemctl is-active --quiet "$TIMER" 2>/dev/null; then
        echo "  Restarting $MARKET_SVC (was interrupted by deploy)..."
        sudo systemctl start --no-block "$MARKET_SVC" 2>/dev/null || true
    fi
done

# --- Restart sim daemons if data dirs exist ---
if [ -d "$DATA_DIR/sim_kr" ]; then
    echo "  Starting sim-kr daemon..."
    nohup /usr/local/bin/traveler --daemon --market kr --sim --sim-capital 50000000 --sleep-on-exit=false --data-dir "$DATA_DIR" >> "$DATA_DIR/sim_kr/daemon.log" 2>&1 &
fi
if [ -d "$DATA_DIR/sim_us" ]; then
    echo "  Starting sim-us daemon..."
    nohup /usr/local/bin/traveler --daemon --market us --sim --sleep-on-exit=false --data-dir "$DATA_DIR" >> "$DATA_DIR/sim_us/daemon.log" 2>&1 &
fi

sleep 2

# --- Verify all services ---
echo ""
echo "=== SERVICE STATUS ==="
FAIL=0
for svc in traveler-web traveler-arb traveler-binance traveler-crypto traveler-dca traveler-scalp traveler-kr-dca; do
    STATUS=$(systemctl is-active "$svc" 2>/dev/null || echo "inactive")
    if [ "$STATUS" = "active" ]; then
        echo "  ✓ $svc"
    else
        echo "  ✗ $svc ($STATUS)"
        FAIL=1
    fi
done

# Check sim processes (they exit after market hours — not a failure)
SIM_KR=$(pgrep -af 'traveler.*sim.*kr' 2>/dev/null | head -1 || true)
SIM_US=$(pgrep -af 'traveler.*sim.*us' 2>/dev/null | head -1 || true)
if [ -d "$DATA_DIR/sim_kr" ]; then
    if [ -n "$SIM_KR" ]; then
        echo "  ✓ sim-kr (nohup)"
    else
        echo "  - sim-kr exited (market closed)"
    fi
fi
if [ -d "$DATA_DIR/sim_us" ]; then
    if [ -n "$SIM_US" ]; then
        echo "  ✓ sim-us (nohup)"
    else
        echo "  - sim-us exited (market closed)"
    fi
fi

# Quick health check
echo ""
echo "=== HEALTH CHECK ==="
WEB_OK=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/api/balance 2>/dev/null || echo "000")
if [ "$WEB_OK" = "200" ]; then
    echo "  ✓ Web API responding (HTTP 200)"
else
    echo "  ✗ Web API not responding (HTTP $WEB_OK)"
    FAIL=1
fi

echo ""
if [ "$FAIL" = "0" ]; then
    echo "=== ALL OK ==="
else
    echo "=== SOME SERVICES FAILED — CHECK ABOVE ==="
    exit 1
fi
REMOTE

echo ""
echo "[6/6] Update complete!"
