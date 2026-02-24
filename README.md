# Traveler - 멀티마켓 자동 매매 시스템

미국/한국 주식 + 암호화폐 자동 매매, DCA 적립식 투자, 스캘핑을 하나의 Go 바이너리로 운영하는 시스템입니다. systemd 서비스로 24/7 가동.

## 주요 기능

- **멀티마켓**: US 주식, KR 주식, 암호화폐 (Upbit) 동시 운영
- **레짐 기반 전략**: bull/sideways/bear 시장 상태에 따라 자동 전략 전환
- **AI 시그널 필터**: Gemini API로 시그널 검증 및 SL/TP 최적화
- **펀더멘탈 필터**: Yahoo Finance 재무 데이터 기반 부실주 제거
- **Crypto DCA**: Fear & Greed 지수 + EMA50 기반 장기 적립식 투자
- **KR DCA**: RSI 공포 게이지 + KODEX 200 ETF 주간 적립식 매수
- **Crypto Scalping**: RSI(7) 15분봉 평균 회귀 단타 (WR 70%, Sharpe 11.2)
- **Web Dashboard**: 실시간 포지션, 차트, DCA 현황, 포트폴리오 종합 뷰
- **Self-hosted 배포**: 크로스 컴파일, systemd 서비스 7개 자동화

## 시스템 구성

```
┌─────────────────────────────────────────────────────────┐
│                  systemd services (Linux)                │
├─────────────┬─────────────┬─────────────┬───────────────┤
│ traveler-web│traveler-us  │traveler-kr  │traveler-crypto│
│ :8080       │ timer 23:20 │ timer 08:40 │ always-on     │
│ always-on   │ KST         │ KST         │               │
├─────────────┼─────────────┼─────────────┼───────────────┤
│traveler-dca │traveler-scalp│traveler-    │               │
│ always-on   │ always-on    │ kr-dca      │               │
│ daily 09:00 │ 15min cycle  │ weekly Mon  │               │
└─────────────┴─────────────┴─────────────┴───────────────┘
```

## 지원 전략

### Stock Meta Strategy (레짐 기반)
시장 레짐(bull/sideways/bear)을 자동 감지하여 최적 전략 조합 선택:

| 레짐 | US 전략 | KR 전략 |
|------|---------|---------|
| Bull | etf-momentum, tqqq-sma | etf-momentum(kr_timing) |
| Sideways | etf-momentum | etf-momentum |
| Bear | etf-momentum (방어적) | etf-momentum |

### 개별 전략 (9종 등록)
| 전략 | 유형 | 설명 |
|------|------|------|
| pullback | 추세 추종 | MA50 위 + MA20 눌림목 + 반전 신호 |
| breakout | 모멘텀 | 저항선 돌파 + 거래량 급증 |
| mean-reversion | 역추세 | RSI < 30 + 볼린저 하단 이탈 |
| oversold | 역추세 | 과매도 반등 |
| volatility-breakout | 변동성 | 변동성 돌파 |
| rsi-contrarian | 역추세 | RSI 역행 매매 |
| volume-spike | 거래량 | 거래량 급등 포착 |
| crypto-meta | 암호화폐 | BTC 레짐 기반 crypto-trend 전략 |
| crypto-scalp | 스캘핑 | RSI(7) 15분봉 mean-reversion + EMA50 필터 |

### AI 시그널 필터 (Gemini)
- 시그널 통과 여부 판단 + SL/TP 최적화
- R/R 1.5 미만 시그널 최적화 스킵
- 지지/저항 레벨 기반 SL 조정

### 펀더멘탈 필터
- 52주 수익률 < -30% 제외
- D/E > 200% 제외
- Profit Margin < -10% 제외
- 시가총액 < $200M / ₩200B 제외

## 지원 마켓

### 미국 (US)
- NYSE/NASDAQ — KIS 해외주식 API, Yahoo/Finnhub 시세
- ETF: QQQ, SPY, TQQQ, SOXL, VXUS 등

### 한국 (KR)
- KOSPI/KOSDAQ — KIS 국내주식 API
- 6자리 종목코드 (005930=삼성전자), KRW 정수 가격
- ETF: KODEX 200 (069500), KODEX 인버스 (114800)

### 암호화폐 (Crypto)
- Upbit KRW 마켓 — BTC, ETH, SOL, XRP 등
- 단기 트렌드 + 스캘핑 + 장기 DCA 3중 운영

## 설치

```bash
git clone https://github.com/gol0g/traveler.git
cd traveler
go mod tidy
go build -o traveler ./cmd/traveler
```

### 크로스 컴파일 (Linux ARM64)
```bash
GOOS=linux GOARCH=arm64 go build -o traveler ./cmd/traveler
```

## 빠른 시작

### Web UI
```bash
./traveler --web --port 8080
```
브라우저에서 `http://localhost:8080` 접속:
- **US/KR/Crypto 마켓 토글**: 상단 버튼으로 전환
- **Scanner**: 실시간 멀티 전략 스캔, 개별 종목 차트
- **Positions**: 실계좌 포지션 + TP/SL 모니터링
- **History**: 거래 내역 + 실현 손익
- **Strategy**: 전략 설정 + 펀더멘탈 필터 현황
- **DCA**: Crypto DCA (F&G) / KR DCA (RSI) — 마켓 토글로 전환
- **Scalp**: Crypto 스캘핑 현황
- **Portfolio**: 전체 투자 자산 종합 + FIRE 프로젝션

### Daemon 모드
```bash
# US 주식 데몬
./traveler --daemon --market us

# KR 주식 데몬
./traveler --daemon --market kr

# Crypto 단기 매매 데몬
./traveler --daemon --market crypto --trading-capital 100000

# Crypto DCA (장기 적립식)
./traveler --daemon --dca --dca-amount 10000

# Crypto 스캘핑
./traveler --daemon --scalp --scalp-amount 50000

# KR DCA (KODEX 200 주간 매수)
./traveler --daemon --kr-dca --kr-dca-shares 1
```

### 기본 스캔
```bash
# 미국 Russell 200 종목 멀티 전략 스캔
./traveler --strategy all --universe russell

# 한국 KOSPI 30 종목 스캔
./traveler --strategy all --market kr

# 특정 종목만 스캔
./traveler --strategy pullback --symbols AAPL,MSFT,GOOGL
```

## KIS API 설정

### 1. API 키 발급
1. [한국투자증권 OpenAPI](https://apiportal.koreainvestment.com/) 접속
2. 앱 등록 → App Key, App Secret 발급
3. 해외주식 + 국내주식 거래 권한 신청

### 2. 환경 변수 (.env)
```bash
# 해외주식
KIS_APP_KEY="your_key"
KIS_APP_SECRET="your_secret"
KIS_ACCOUNT_NO="12345678-01"

# 국내주식 (별도 AppKey인 경우)
KIS_KR_APP_KEY="your_kr_key"
KIS_KR_APP_SECRET="your_kr_secret"
KIS_KR_ACCOUNT_NO="12345678-01"

# Upbit (암호화폐)
UPBIT_ACCESS_KEY="your_key"
UPBIT_SECRET_KEY="your_secret"

# Gemini AI (시그널 필터)
GEMINI_API_KEY="your_key"

# Finnhub (US 시세)
FINNHUB_API_KEY="your_key"
```

## CLI 옵션

### 기본 옵션
| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--strategy` | pullback | 전략 (pullback, breakout, mean-reversion, all) |
| `--market` | us | 시장 (us, kr, crypto) |
| `--universe` | (없음) | 종목 유니버스 선택 |
| `--capital` | 100000 | 계좌 자금 (auto-trade시 실제 잔고 사용) |
| `--symbols` | (전체) | 검사할 종목 (쉼표 구분) |
| `--format` | table | 출력 형식 (table, json) |
| `--workers` | 10 | 병렬 처리 워커 수 |
| `--data-dir` | ~/.traveler | 데이터 디렉토리 |
| `--verbose` | false | 상세 출력 |

### 자동 매매 옵션
| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--auto-trade` | false | 자동 매매 활성화 |
| `--dry-run` | true | 시뮬레이션 모드 |
| `--market-order` | false | 시장가 주문 |
| `--monitor` | false | 포지션 모니터링만 |
| `--adaptive` | false | 적응형 스캔 (잔고 기반 유니버스) |
| `--trading-capital` | 0 | 매매 전용 자본 (0=전체 잔고) |
| `--force-scan` | false | 강제 스캔 |

### Daemon 옵션
| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--daemon` | false | 데몬 모드 |
| `--sleep-on-exit` | true | 종료 시 PC 절전 (Windows) |
| `--daily-target` | 1.0 | (비활성) 개별 TP/SL로 대체 |
| `--daily-loss-limit` | -2.0 | (비활성) 개별 TP/SL로 대체 |
| `--sim` | false | 시뮬레이션 모드 (가상 자본) |
| `--sim-capital` | 0 | 가상 자본 (US: $100K, KR: ₩5000만) |

### DCA / Scalp 옵션
| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--dca` | false | Crypto DCA 모드 |
| `--dca-amount` | 10000 | DCA 1회 투입액 (KRW) |
| `--scalp` | false | Crypto 스캘핑 모드 |
| `--scalp-amount` | 50000 | 스캘핑 1회 주문액 (KRW) |
| `--kr-dca` | false | KR DCA 모드 (KODEX 200) |
| `--kr-dca-shares` | 1 | KR DCA 기본 매수 주수 |

### 백테스트 / Web 옵션
| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--backtest` | false | 백테스트 모드 |
| `--backtest-days` | 365 | 백테스트 기간 (일) |
| `--web` | false | 웹 UI 서버 |
| `--port` | 8080 | 웹 서버 포트 |

## Universe 옵션

### 미국 (US)
| Universe | 종목 수 | 설명 |
|----------|---------|------|
| `test` | 10 | 테스트용 (AAPL, MSFT 등) |
| `dow30` | 30 | 다우존스 30 |
| `nasdaq100` | 100 | 나스닥 100 |
| `sp500` | 100 | S&P 500 상위 100 |
| `midcap` | 100 | S&P MidCap 400 상위 100 |
| `russell` | 200 | Russell 2000 상위 200 |
| `us-etf` | 5 | US ETF (QQQ, SPY, TQQQ, SOXL, VXUS) |

### 한국 (KR)
| Universe | 종목 수 | 설명 |
|----------|---------|------|
| `kospi30` | 30 | KOSPI 시총 상위 30 |
| `kospi200` | 100 | KOSPI 200 주요 종목 |
| `kosdaq30` | 30 | KOSDAQ 시총 상위 30 |
| `kr-etf` | 2 | KR ETF (KODEX 200, KODEX 인버스) |

### 암호화폐
| Universe | 종목 수 | 설명 |
|----------|---------|------|
| `crypto-top10` | 10 | Upbit KRW 시총 상위 10 |
| `crypto-top30` | 30 | Upbit KRW 시총 상위 30 |

## 적응형 스캔

잔고에 따라 자동으로 유니버스, 리스크, 포지션 한도 조정:

### 미국 (USD)
| 잔고 | 우선 유니버스 | 확대 유니버스 |
|------|-------------|-------------|
| < $500 | russell (저가주) | midcap → sp500, nasdaq100 |
| < $5,000 | russell + midcap | sp500, nasdaq100 |
| < $25,000 | nasdaq100 + sp500 | midcap, russell |
| ≥ $25,000 | 전체 동일 우선순위 | - |

### 한국 (KRW)
| 잔고 | 유니버스 | 리스크/거래 | 종목당 최대 | 최대 포지션 |
|------|----------|------------|------------|------------|
| < ₩50만 | kr-etf (ETF 전용) | 5% | 100% | 2개 |
| < ₩500만 | kosdaq30 + kospi30 → kospi200 | 2% | 25% | 5개 |
| < ₩5000만 | kospi30 + kosdaq30 → kospi200 | 1.5% | 25% | 5개 |
| ≥ ₩5000만 | 전체 동일 우선순위 | 1% | 20% | 5개 |

시그널 부족 시 자동으로 낮은 우선순위 유니버스까지 확대 스캔.

## DCA 시스템

### Crypto DCA (Fear & Greed 기반)
- **대상**: BTC 40%, ETH 20%, SOL 15%, XRP 10%, Cash 15%
- **공포 지수 기반 매수량**: ExtFear 1.5x, Fear 1.0x, Neutral 0.75x, Greed 0.5x, ExtGreed 0.25x + 5% 매도
- **EMA50 오버레이**: Price < EMA50 → 2.0x, Price > EMA50 → 0.5x
- **리밸런싱**: 주간, 15% 이탈 시 자동 조정
- **CLI**: `./traveler --daemon --dca --dca-amount 10000`

### KR DCA (RSI 공포 게이지)
- **대상**: KODEX 200 (069500) 단일 종목
- **RSI 기반 매수량**: RSI < 25 → 3주, 25-35 → 2주, 35-50 → 1주, 50-65 → 스킵, > 65 → 10% 매도
- **EMA50 보너스**: Price < EMA50 일 때 +1주 추가
- **스케줄**: 매주 월요일 09:30 KST
- **CLI**: `./traveler --daemon --kr-dca --kr-dca-shares 1`

## Daemon 동작 흐름

### 주식 데몬 (US/KR)
```
1. 시작 → 마켓 상태 확인 (US: ET, KR: KST)
2. 프리마켓 → 적응형 멀티 전략 프리스캔 + AI 필터
3. 마켓 오픈 → 시그널 실행 (포지션 사이징 → 주문)
4. 모니터 모드 전환 → TP/SL/MaxHold 감시 (30초 주기)
5. 인트라데이 스캔 (5분 주기, 레짐에 따라 ORB 등)
6. 마감 → 인트라데이 포지션 청산, 리포트 생성, 종료
```

### KR 데몬 특수 모드
- **잔고 < ₩50만**: KR DCA가 KODEX 200을 관리하므로 자동으로 monitor-only 모드 전환
- **monitor-only**: 기존 포지션 TP/SL/MaxHold만 감시, 신규 스캔 없음

### 마켓 시간
| 마켓 | 장 시간 | 시간대 |
|------|---------|--------|
| US | 09:30 ~ 16:00 | Eastern Time |
| KR | 09:00 ~ 15:30 | KST |
| Crypto | 24/7 | - |

## 수수료

| 마켓 | 편도 수수료 | 왕복 수수료 |
|------|-----------|-----------|
| US (해외주식) | 0.25% | 0.50% |
| KR (국내주식) | 0.25% | 0.50% |
| Crypto (Upbit) | 0.05% | 0.10% |

- P&L은 수수료 포함 순손익 (grossPnL - buyComm - sellComm)
- 최소 기대수익률 필터: 수수료 + 마진 보장

## API 연동

| API | 용도 | Rate Limit |
|-----|------|------------|
| KIS 해외주식 | US 주식 매매 | - |
| KIS 국내주식 | KR 주식 시세 + 매매 | 분당 300회 |
| Upbit | 암호화폐 시세 + 매매 | - |
| Yahoo Finance | US 주식 시세 + 펀더멘탈 | 비공식 |
| Finnhub | US 주식 시세 | 분당 60회 |
| Gemini | AI 시그널 필터 | - |
| alternative.me | Fear & Greed 지수 | 1시간 캐시 |

## 프로젝트 구조

```
traveler/
├── cmd/
│   ├── traveler/main.go         # CLI 진입점 (cobra)
│   ├── backtest-stock/          # 주식 백테스터 (optimize 지원)
│   └── backtest-scalp/          # 스캘핑 백테스터
├── internal/
│   ├── ai/                      # Gemini AI 시그널 필터
│   ├── broker/
│   │   ├── broker.go            # Broker 인터페이스
│   │   ├── kis/                 # KIS API (해외/국내 듀얼)
│   │   ├── upbit/               # Upbit 거래소 API
│   │   └── sim/                 # 시뮬레이션 브로커
│   ├── daemon/
│   │   ├── daemon.go            # 메인 오케스트레이터 (monitor-only)
│   │   ├── market.go            # US/KR 마켓 시간 + 휴일
│   │   ├── tracker.go           # 일일 P&L 추적
│   │   ├── dca.go               # Crypto DCA 데몬
│   │   ├── scalp.go             # Crypto 스캘핑 데몬
│   │   └── kr_dca.go            # KR DCA 데몬
│   ├── dca/
│   │   ├── engine.go            # Crypto DCA 엔진 (F&G + EMA50)
│   │   ├── state.go             # DCA 상태 영속화
│   │   └── kr_engine.go         # KR DCA 엔진 (RSI + KODEX 200)
│   ├── trader/
│   │   ├── trader.go            # AutoTrader
│   │   ├── executor.go          # 주문 실행 (체결가 조회)
│   │   ├── monitor.go           # TP/SL/MaxHold/Trailing 모니터링
│   │   ├── sizer.go             # 포지션 사이징 (수수료 고려)
│   │   ├── adaptive.go          # 적응형 스캔 (US/KR/Crypto 티어)
│   │   ├── planstore.go         # 매매 계획 영속화
│   │   ├── history.go           # 거래 내역 기록
│   │   └── risk.go              # 리스크 관리
│   ├── strategy/
│   │   ├── registry.go          # 전략 레지스트리 (9종)
│   │   ├── stock_meta.go        # 레짐 기반 전략 선택
│   │   ├── pullback.go          # 눌림목 전략
│   │   ├── breakout.go          # 돌파 전략
│   │   ├── meanreversion.go     # 평균 회귀 전략
│   │   ├── etf_momentum.go      # ETF 모멘텀 전략
│   │   ├── crypto_trend.go      # 암호화폐 트렌드
│   │   ├── crypto_scalp.go      # 암호화폐 스캘핑 (RSI 15분봉)
│   │   ├── intraday.go          # 인트라데이 전략
│   │   └── indicators.go        # 기술적 지표 (RSI, EMA, BB 등)
│   ├── provider/
│   │   ├── yahoo.go             # Yahoo Finance (US 시세)
│   │   ├── finnhub.go           # Finnhub (US 시세)
│   │   ├── kis.go               # KIS 국내주식 시세
│   │   ├── upbit.go             # Upbit 암호화폐 시세
│   │   ├── fundamentals.go      # Yahoo 펀더멘탈 데이터
│   │   ├── feargreed.go         # Fear & Greed API
│   │   ├── caching.go           # 캐싱 프로바이더
│   │   └── fallback.go          # 멀티 프로바이더 폴백
│   ├── symbols/
│   │   ├── universe.go          # US 유니버스 + 헬퍼
│   │   └── kr_universe.go       # KR 유니버스 (KOSPI/KOSDAQ)
│   ├── config/config.go         # 설정 관리
│   └── web/
│       ├── server.go            # HTTP 서버 (embed static)
│       ├── handlers.go          # API 핸들러 (포트폴리오 포함)
│       └── static/              # 웹 UI (HTML/CSS/JS)
├── scripts/                     # 유틸리티 스크립트
└── README.md
```

## 배포 (systemd)

단일 바이너리로 크로스 컴파일 후 Linux 서버에 systemd 서비스로 배포:

```bash
GOOS=linux GOARCH=arm64 go build -o traveler ./cmd/traveler
```

### 서비스 구성
| 서비스 | 유형 | 스케줄 | 설명 |
|--------|------|--------|------|
| `traveler-web` | always-on | :8080 | Web UI |
| `traveler-crypto` | always-on | 24/7 | Crypto 단기 매매 |
| `traveler-dca` | always-on | daily 09:00 | Crypto DCA |
| `traveler-scalp` | always-on | 15min cycle | Crypto 스캘핑 |
| `traveler-kr-dca` | always-on | weekly Mon | KR DCA (KODEX 200) |
| `traveler-us.timer` | oneshot | 23:20 KST | US 주식 데몬 |
| `traveler-kr.timer` | oneshot | 08:40 KST | KR 주식 데몬 |

### 로그 확인
```bash
# always-on 서비스
journalctl -u traveler-crypto -f

# oneshot 서비스 (파일 로그)
tail -f ~/.traveler/daemon_us.log
tail -f ~/.traveler/daemon_kr.log
```

## 데이터 파일

| 파일 | 용도 |
|------|------|
| `plans.json` | 활성 매매 계획 (TP/SL/MaxHold) |
| `trade_history.json` | 거래 내역 (전 마켓) |
| `dca_state.json` | Crypto DCA 상태 |
| `dca_status.json` | Crypto DCA 웹 표시용 |
| `scalp_state.json` | 스캘핑 상태 |
| `scalp_status.json` | 스캘핑 웹 표시용 |
| `kr_dca_state.json` | KR DCA 상태 |
| `kr_dca_status.json` | KR DCA 웹 표시용 |
| `last_scan_{us\|kr}.json` | 최근 스캔 결과 |
| `report_YYYY-MM-DD.txt` | 일일 매매 리포트 |
| `.kis_token_*.json` | KIS API 토큰 캐시 (AppKey별) |

## 라이선스

Private repository. All rights reserved.
