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

## 전략 status와 파라미터의 단일 권위

**`strategy_manifest.yaml`** (파이: `~/strategy_manifest.yaml`)이 모든 전략의 status·Governor 한도·검증된 파라미터의 유일한 권위다. 코드 수정 전 이 파일을 읽고 status를 검증할 것. manifest에 없는 전략의 주문은 governance가 차단한다.

## 배포

- 빌드: `GOOS=linux GOARCH=arm64 go build -o traveler-linux-arm64 ./cmd/traveler/`
- 배포: `bash scripts/deploy/update-pi.sh` (전 서비스 stop → binary 교체 → 전 서비스 start)
- Pi: `junghyun@100.78.139.68` (Tailscale), binary `/usr/local/bin/traveler`, 데이터 `~/.traveler/`
- 서비스 11종: traveler-web(:8080), battle-etf, binance, btc-futures, collector, crypto, datacollector, dca, leverage, macro, portfolio
- 타이머: traveler-us.timer (23:20 KST), traveler-kr.timer (08:40 KST), macro.timer (07:00 KST)

## API 주의사항

- Binance/Upbit klines API 마지막 캔들은 미완성 → 분석 시 `candles[:len-1]` 사용
- MATICUSDT 상장 폐지 (POL 리브랜딩) — Symbol is closed 에러 발생
- KIS 토큰 발급은 1분당 1회 제한(EGW00133), 조회는 초당 건수 제한(EGW00215) → 한 사이클에서 토큰 재요청 금지
- Upbit RSI 스캘프: **EMA50 필터 절대 제거 금지** (제거 시 -31%)

## 2026-07 디스크 사고 관련

- 2026-03-12 이후 작성된 Go 소스는 로컬·파이 모두 소실(바이너리만 배포하는 방식이었음). 현재 파이에서 도는 것은 7/6 빌드 바이너리다.
- 소스 재작성 시 참조: GitHub 3/12판(`Desktop/traveler-github`), 파이 백업(`pi_backup_2026-07-20`), strategy_manifest.yaml
