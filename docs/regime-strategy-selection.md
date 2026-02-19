# 레짐별 전략 선택 시스템 (StockMetaStrategy)

**날짜**: 2026-02-16
**범위**: US/KR 주식 매매 시스템 전체 (데몬 + 백테스터 + 웹 UI)

---

## 변경 배경

### 문제
기존 시스템은 4개 전략(pullback, breakout, mean-reversion, oversold)을 시장 상황(레짐)에 관계없이
동일하게 실행. 레짐과 무관한 전략 적용으로 불필요한 손실 발생.

**백테스트 증거 (120일, baseline 구성):**
- US: +3.1%, Sharpe 0.87 — mean-reversion이 PF 0.44로 부진하며 -$83 손실
- KR: -1.3%, Sharpe -0.03 — +93% 랠리에서 시스템이 오히려 손실

### 해결 방향
크립토에 이미 구현된 `CryptoMetaStrategy` 패턴을 US/KR에 적용:
- 레짐(bull/sideways/bear) 감지 → 해당 레짐에 최적인 전략만 실행
- 8가지 레짐-전략 구성을 백테스트로 비교 → 최적 구성 자동 선택

---

## 아키텍처

### 핵심 컴포넌트

```
StockMetaStrategy (internal/strategy/stock_meta.go)
├── RegimeDetector — SPY/069500 기반 시장 레짐 감지
├── Bull strategies — 상승장에서 활성화되는 전략 목록
├── Sideways strategies — 횡보장에서 활성화되는 전략 목록
└── Bear strategies — 하락장에서 활성화되는 전략 목록
```

### 레짐 감지 기준 (regime.go)
- **Bull**: Price > MA20 AND > MA50 AND RSI14 > 45 AND MA20 slope > 0
- **Bear**: Price < MA20 AND < MA50 AND RSI14 < 40
- **Sideways**: 기본값 (bull/bear 아닌 모든 상태)

### Analyze() 흐름
1. `RegimeDetector.Detect()` → 현재 레짐 판별
2. 해당 레짐의 전략 목록 선택
3. 모든 전략 실행 → `score = Probability × Strength / 100` 최고 시그널 선택
4. 시그널에 레짐 정보 주입 (`Details["regime"]`)
5. 전략명에 레짐 suffix 추가: `"breakout(bull)"`

---

## 최적화 결과

### 테스트 구성 (8개)

| # | Config | Bull | Sideways | Bear |
|---|--------|------|----------|------|
| 1 | baseline | 전체 4개 | 전체 4개 | 전체 4개 |
| 2 | regime-split | breakout, pullback | meanrev, pullback, oversold | oversold |
| 3 | trend-focus | breakout, pullback | meanrev, oversold | — |
| 4 | breakout-bull | breakout | meanrev, pullback, oversold | oversold |
| 5 | no-bear | breakout, pullback | meanrev, pullback, oversold | — |
| 6 | aggressive | breakout, pullback, oversold | breakout, meanrev | oversold |
| 7 | conservative | pullback, meanrev, oversold | meanrev, oversold | — |
| 8 | extended-hold | #2 + breakout maxHold=20 | #2 동일 | #2 동일 |

### 종합 스코어
`Score = SharpeRatio × sqrt(TotalTrades) × (1 - MaxDrawdown/100)`

### US 결과 (nasdaq100+sp500, 120일)

```
★ breakout-bull    +5.2%  60.7% win  PF 1.53  MDD -5.8%  Sharpe 1.18  89 trades  Score 10.5  ← BEST
  baseline         +3.1%  55.8% win  PF 1.54  MDD -4.6%  Sharpe 0.87  113 trades Score 8.8
  regime-split     +4.0%  58.9% win  PF 1.49  MDD -5.8%  Sharpe 0.93  90 trades  Score 8.0
  aggressive       +2.6%  57.0% win  PF 1.43  MDD -5.0%  Sharpe 0.72  107 trades Score 7.0
  conservative    -10.2%  44.6% win  PF 0.80  MDD -10.4% Sharpe -3.16 130 trades Score 0.0
```

### KR 결과 (kospi30+kosdaq30+kospi200, 120일)

```
★ extended-hold    +6.2%  55.9% win  PF 1.34  MDD -14.2% Sharpe 0.76  170 trades Score 8.5  ← BEST
  aggressive       +3.5%  56.2% win  PF 1.28  MDD -12.3% Sharpe 0.53  169 trades Score 6.1
  breakout-bull    +4.2%  55.7% win  PF 1.29  MDD -12.3% Sharpe 0.54  140 trades Score 5.6
  baseline         -1.3%  54.4% win  PF 1.18  MDD -14.2% Sharpe -0.03 169 trades Score 0.0
  conservative     -4.0%  57.6% win  PF 1.17  MDD -7.9%  Sharpe -0.46 139 trades Score 0.0
```

---

## 적용된 최적 구성

### US: breakout-bull
- **Bull**: `[breakout]` — 상승장에서 모멘텀 돌파 전략에 집중
- **Sideways**: `[mean-reversion, pullback, oversold]` — 횡보장에서 다양화
- **Bear**: `[oversold]` — 하락장에서 극단적 과매도 반등만
- **개선**: Return +3.1%→+5.2%, Sharpe 0.87→1.18, Win Rate 55.8%→60.7%

### KR: extended-hold
- **Bull**: `[breakout, pullback]` — 상승장에서 돌파+눌림 두 가지
- **Sideways**: `[mean-reversion, pullback, oversold]` — 횡보장 다양화
- **Bear**: `[oversold]` — 하락장에서 과매도만
- **MaxHoldOverride**: breakout 보유 기간 15→20일 (KR 추세 연장 활용)
- **개선**: Return -1.3%→+6.2%, Sharpe -0.03→0.76

---

## 변경된 파일

### 신규
| 파일 | 설명 |
|------|------|
| `internal/strategy/stock_meta.go` | StockMetaStrategy — 레짐별 전략 선택 메타전략 |
| `internal/backtest/stock_optimize.go` | 최적화 하네스 — 8개 구성 비교 테스트 |

### 수정
| 파일 | 변경 내용 |
|------|-----------|
| `internal/daemon/daemon.go` | `adaptiveScan()`: 4개 개별 전략 → StockMetaStrategy 1개, 수동 RegimeDetector/regime inject 제거 |
| `internal/backtest/stock_sim.go` | 수동 RegimeDetector 제거, meta strategy에 레짐 위임, max_hold_override 지원 |
| `internal/web/handlers.go` | `createMarketAwareStrategies()`: 4개 개별 전략 → StockMetaStrategy, 개별 종목 분석도 동기화 |
| `cmd/backtest-stock/main.go` | `--optimize` 플래그 추가, StockMetaStrategy 사용 |

---

## 사용법

### 백테스트 (단일 실행)
```bash
go run ./cmd/backtest-stock/ --market us --days 120          # US 최적 구성
go run ./cmd/backtest-stock/ --market kr --days 120          # KR 최적 구성
go run ./cmd/backtest-stock/ --market us --days 120 --verbose  # 개별 트레이드 출력
```

### 백테스트 (최적화 비교)
```bash
go run ./cmd/backtest-stock/ --market us --days 120 --optimize  # US 8개 구성 비교
go run ./cmd/backtest-stock/ --market kr --days 120 --optimize  # KR 8개 구성 비교
```

### 데몬
변경 사항 없이 기존과 동일하게 실행. `DefaultStockMetaConfig()`가 자동으로 최적 구성 적용.

---

## 핵심 교훈

1. **레짐 무시는 비용이 크다**: baseline(전체 전략)은 KR에서 -1.3%였지만, 레짐별 전략 선택으로 +6.2%
2. **하락장에서 매매 축소가 핵심**: bear에서 oversold만 남기는 것이 모든 최적 구성의 공통점
3. **시장별 최적 구성은 다르다**: US는 bull에서 breakout 단독이 최적, KR은 breakout+pullback 조합이 최적
4. **보유 기간 연장이 KR에서 유효**: KR 시장의 추세 지속성이 US보다 길어 breakout hold 20일이 효과적
5. **conservative(방어적)은 최악**: 지나친 방어는 기회 손실로 이어짐 (US -10.2%, KR -4.0%)
