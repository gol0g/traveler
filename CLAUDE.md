# Traveler Project Rules

## 전략 수정 시 필수 절차

1. **STRATEGY_HISTORY.md를 먼저 읽어라.** 과거 실패 이력을 확인하고, 동일한 실수를 반복하는 변경은 하지 마라.
2. **백테스트 없이 파라미터를 변경하지 마라.** 감으로 조정 금지. 반드시 `cmd/backtest-*` 도구를 실행하고 결과를 근거로 변경.
3. **"거래가 없다"는 이유로 기준을 낮추지 마라.** 거래가 없으면 시장 조건이 안 맞는 것이다. 백테스트 검증된 값을 유지해라.
4. **변경 후 STRATEGY_HISTORY.md에 기록해라.** 날짜, 변경 내용, 백테스트 결과, 사유를 남겨라.
5. **한 번에 1~2개 파라미터만 변경해라.** 극단적 스위칭(예: RSI 70→50) 금지. 5~10% 범위 내 미세 조정.

## 금지된 패턴

```
기준 완화 → 손실 → 기준 강화 → 0거래 → 기준 완화 (반복)
```
이 패턴이 감지되면 즉시 중단. 백테스트 그리드 서치로 최적값을 찾아라.

## 배포

- 빌드: `GOOS=linux GOARCH=arm64 go build -o traveler-linux-arm64 ./cmd/traveler/`
- 배포: `bash scripts/deploy/update-pi.sh` (전 서비스 stop → binary 교체 → 전 서비스 start)
- 서비스: traveler-web, traveler-arb, traveler-binance, traveler-crypto, traveler-dca, traveler-scalp, traveler-kr-dca
- 타이머: traveler-us.timer (23:20 KST), traveler-kr.timer (08:40 KST)
- Pi: `junghyun@100.78.139.68` (Tailscale), binary `/usr/local/bin/traveler`

## API 주의사항

- Binance/Upbit klines API 마지막 캔들은 미완성 → 분석 시 `candles[:len-1]` 사용
- MATICUSDT 상장 폐지 (POL 리브랜딩) — Symbol is closed 에러 발생

## 현재 전략 파라미터 (2026-03-08)

### US Stock (breakout-bull)
- Bull: ETF momentum + TQQQ/SMA + breakout
- Sideways/Bear: ETF momentum + oversold
- 백테스트: Sharpe 0.60, +2.2%

### KR Stock (extended-hold)
- Bull: ETF momentum + breakout (보유 20일)
- Sideways: ETF momentum + mean-reversion + oversold
- Bear: ETF momentum + oversold
- 백테스트: Sharpe 2.05, +23.6%

### Upbit RSI Scalp (9페어)
- RSI(7)<30 entry, >65 exit, TP +2%, SL -2.5%, MaxBars 32
- EMA50 필터 절대 제거 금지 (제거 시 -31%)
- 페어: ETH, LINK, SOL, AVAX, SUI, XRP, ADA, DOGE, TRX

### Binance Short Scalp (8페어)
- RSI(7)>75 entry, <45 exit, TP 3%, SL 3%, MaxBars 32
- EMA50 below 필터, $80/건 × 2x 레버리지, 최대 4포지션
- 페어: ETH, SOL, XRP, LINK, DOGE, ADA, AVAX, BNB
- 백테스트: 86건, WR 77%, PF 2.97, MDD 4.1%

### BTC Futures Funding Long
- 펀딩비 < -0.005%, RSI > 35, TP ATR×2.5, SL ATR×1.5

### Funding Arb (BTCUSDT)
- Spot long + Futures short, 펀딩비 > 0.01% 진입, 음수 시 청산
