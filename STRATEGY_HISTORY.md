# Strategy Decision Log

이 문서는 모든 전략 파라미터 변경의 이력, 근거, 결과를 기록합니다.
**전략 수정 전 반드시 이 파일을 읽고, 과거 실패를 반복하지 않는지 확인해야 합니다.**

---

## 변경 규칙

1. 파라미터 변경 전 백테스트 필수 (최소 90일, 30거래 이상)
2. 한 번에 1~2개 파라미터만 변경 (극단적 스위칭 금지)
3. "거래가 없다"는 이유로 기준을 낮추지 않는다 — 시장 조건이 안 맞는 것
4. 변경 후 이 파일에 기록: 날짜, 변경 내용, 백테스트 결과, 사유

---

## Binance Short Scalp (ETHUSDT, SOLUSDT, XRPUSDT)

### 2026-03-08: MaxPositions 3→4 (백테스트 검증)
- **변경**: MaxPositions 3→4
- **백테스트**: 90일, 8페어, MaxPos 3/4/5/6/8 비교
  - MaxPos=3: 80 trades, WR 75%, Net +23.3%, PF 2.34, MDD 4.1%
  - **MaxPos=4: 84 trades, WR 76%, Net +25.3%, PF 2.46, MDD 4.1%** ← 채택
  - MaxPos=5+: 효과 없음 (RSI>75 동시 시그널 드물어서)
- **사유**: MDD 변화 없이 Net +2%p, PF 0.12 개선. 자본 $341 > $320(4×$80) 충분

### 2026-03-08: 9페어 확장 + RSI 강화
- **변경**: RSI 65→75, RSIExit 40→45, MaxBars 48→32, 3→9 페어
- **추가 페어**: LINKUSDT, DOGEUSDT, ADAUSDT, AVAXUSDT, BNBUSDT (5개 추가 → 총 8페어)
  - 탈락: SUIUSDT (WR 47%, -3.6%), DOTUSDT (PF 1.07), MATICUSDT (Symbol closed, POL 리브랜딩), POLUSDT (PF 1.31)
- **백테스트**: 90일, 3840 combo grid search, 9 pairs
  - `go run ./cmd/backtest-short/ --optimize --days=90 --pairs=ETHUSDT,SOLUSDT,XRPUSDT,BNBUSDT,DOGEUSDT,ADAUSDT,AVAXUSDT,LINKUSDT,MATICUSDT`
  - Risk-adjusted best: 86 trades, WR 77%, Net +30.9%, PF 2.97, MDD 4.1%
- **이전 대비 개선**: WR 64→77%, PF 1.65→2.97, MDD 8.8→4.1%, Net 14.4→30.9%
- **사유**: RSI>75 = 고품질 시그널만 선별, 거래수는 유사(86 vs 75)하면서 품질 대폭 개선

### 2026-03-08: 백테스트 기반 최적화 (3페어)
- **변경**: RSI 50→65, Vol 0.5→1.5, SL 1.5→3.0, MaxBars 32→48
- **백테스트**: 90일, 3840 combo grid search
  - `go run ./cmd/backtest-short/ --optimize --days=90`
  - 결과: 75 trades, WR 64%, Net +14.4%, PF 1.65, MDD 8.8%
- **사유**: 아래 실패 이력을 거친 후 백테스트로 최적값 도출

### 2026-03-08: 실패 — 백테스트 없이 기준 완화 (되돌림)
- **변경**: RSI 55→50, Vol 0.8→0.5 (거래 발생시키려고)
- **결과**: SOL RSI=52.8에서 숏 진입 — 백테스트 기준 수익 안 나는 구간
- **교훈**: "거래가 없다"고 기준을 낮추면 안 됨. 백테스트 없이 감으로 조정한 전형적 실수

### 2026-03-08: 실패 — 미완성 캔들 버그 + 기준 완화
- **변경**: RSI 70→55, Vol 1.5→0.8, strength 30→15
- **결과**: 여전히 0 거래 (미완성 캔들 버그가 진짜 원인이었음)
- **교훈**: 0 거래의 원인을 분석하지 않고 파라미터만 바꾸면 안 됨

### 2026-02 ~ 2026-03-07: 초기 배포 (실패)
- **설정**: RSI>70, Vol>1.5, TP 2%, SL 2.5%, MaxBars 32
- **결과**: 9일간 0 거래
- **원인 1**: Binance klines API 마지막 캔들이 미완성(진행중)이라 거래량이 항상 ~3%
- **원인 2**: 백테스트 없이 Upbit 롱 스캘핑 파라미터를 뒤집어서 만든 설정
- **교훈**: 숏은 롱의 반대가 아님. 별도 백테스트 필요

### 실패 패턴 (반복 금지)
```
기준 완화 → 손실 → 기준 강화 → 0거래 → 기준 완화 → 손실 (반복)
```
이 패턴이 감지되면 즉시 중단하고 백테스트를 돌린다.

---

## BTC Futures Funding Long (BTCUSDT)

### 2026-03-08: 백테스트 기반 완화
- **변경**: Funding -0.01%→-0.005%, RSI min 40→35, TP ATR*2.0→2.5
- **백테스트**: 180일
  - `go run ./cmd/backtest-futures/ 180`
  - 결과: 49 trades, WR 49%, Net +9.19%, PF 1.55, Sharpe 1.31, MDD 4.07%
- **사유**: 이전 설정(-0.01%)은 180일간 14거래만 발생, 너무 보수적

### 2026-02: 초기 배포
- **설정**: Funding < -0.01%, RSI > 40, TP ATR*2.0, SL ATR*1.5
- **백테스트**: 360일, 14 trades, WR 64%, +8.25%, PF 3.22
- **문제**: 거래 빈도 너무 낮음 (180일 기준 ~14건)

---

## Crypto Scalping - Upbit Long (KRW pairs)

### 2026-03-08: 미완성 캔들 버그 수정 (critical)
- **버그**: Upbit API도 현재 진행 중인 미완성 캔들을 마지막에 포함
- **영향**: 부분 거래량으로 RSI/Volume 왜곡 → 잘못된 진입/미진입
- **수정**: `completedCandles = candles[:len(candles)-1]` (Binance와 동일)
- **실거래 성과**: 5건, WR 20%, Net -803원 (백테스트 WR 70% 대비 심각한 괴리)
- **이 버그가 괴리의 주요 원인일 가능성 높음** — 수정 후 모니터링 필요

### 2026-03-08: RSI Exit 조정
- **변경**: RSIExit 60→65
- **근거**: 더 오래 보유해서 큰 움직임 잡기 (90일 백테스트 기반)
- **백테스트**: 175 trades, WR 70%, PF 2.13, MDD 5.4%

### 확정 파라미터 (변경 시 백테스트 필수)
- RSI(7) < 30 entry, > 65 exit
- TP +2%, SL -2.5%, MaxBars 32
- EMA50 필터 ON (제거 시 -31% — 절대 제거 금지)
- Vol > 1.5x

---

## KR Intraday Dip

### 2026-03-08: 백테스트 후 DipMinDrop 조정
- **변경**: DipMinDrop -3.0%→-4.0%
- **백테스트**: 90일, Binance 5분봉 (KR 분봉 대체)
  - `go run ./cmd/backtest-dip/ 90`
  - Drop -3%: 47건, WR 36%, Net -4.6%, PF 0.90 → 손실
  - Drop -3% (KR 수수료 0.25%): Net **-24.3%** → 대참사
  - **Drop -4%: 15건, WR 53%, Net +12.3%, PF 2.11** → 채택
  - Drop -4% (KR 수수료 0.25%): Net +6.0% → 거래 적지만 수익
- **사유**: 실거래 6건 WR 33% Net -7,482원. -3% 낙폭은 knife catching.
- SL 1.5%, TP 3.0%은 백테스트에서도 최선이므로 유지

### 2026-03-08: TP/SL 조정 (백테스트 없이 — 이후 검증됨)
- **변경**: SL 2.0→1.5%, TP 2.5→3.0% (RR 1.25→2.0)
- **결과**: 위 백테스트에서 SL 1.5/TP 3.0이 최선 확인됨

### 실거래 성과
- 6건: 2W/4L, WR 33%, Net -7,482원
- 승리 평균 +1,620원, 패배 평균 -2,680원
- 손실 원인: -3% 낙폭이 너무 약함, 추가 하락 확률 높음

---

## US Intraday ORB

### 현재 상태
- 3건: 1W/2L, WR 33%, Net -1원
- ORBEnabled: false (비활성화 상태) — 영향 없음

---

## 전략별 백테스트 명령어

```bash
# Short Scalp: 90일 파라미터 최적화
go run ./cmd/backtest-short/ --optimize --days=90

# Short Scalp: 기본 3전략 비교
go run ./cmd/backtest-short/ --days=90

# BTC Futures: 180일 파라미터 그리드 서치
go run ./cmd/backtest-futures/ 180

# DipBuy: 90일 파라미터 스윕
go run ./cmd/backtest-dip/ 90
```
