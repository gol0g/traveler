# Traveler - 주식 패턴 스캐너 & 자동 매매

미국 주식(NYSE/NASDAQ)에서 기술적 분석 패턴을 탐지하고, 한국투자증권 API를 통해 자동 매매하는 Go CLI 프로그램입니다.

## 주요 기능

- **패턴 스캔**: 눌림목(Pullback), Morning Dip 전략 지원
- **자동 매매**: 한국투자증권(KIS) API 연동
- **포트폴리오 관리**: 계좌 잔고 기반 포지션 사이징
- **리스크 관리**: 자동 손절/익절 모니터링
- **Web UI**: 브라우저 기반 스캔 및 차트 분석

## 지원 전략

### 1. Pullback (추세 추종) - 권장
```
상승 추세 눌림목 매매:
- 주가가 MA50 위에 있음 (상승 추세 확인)
- 주가가 MA20 부근까지 조정
- 거래량이 평균보다 적음 (매도세 약함)
- 반전 신호 (양봉 또는 긴 아래꼬리)
- 스윙 트레이딩용
```

### 2. Morning Dip (역추세)
```
장 초반 하락 → 종장 상승 패턴:
- 장 초반 (개장 후 1시간) 시가 대비 하락
- 종가가 시가 대비 상승 또는 장중 최저점 대비 반등
- 데이 트레이딩/스캘핑용
```

## 설치

```bash
git clone https://github.com/gol0g/traveler.git
cd traveler
go mod tidy
go build -o traveler ./cmd/traveler
```

## 빠른 시작

### 기본 스캔
```bash
# Russell 200 종목 눌림목 스캔
./traveler --strategy pullback --universe russell

# 특정 종목만 스캔
./traveler --strategy pullback --symbols AAPL,MSFT,GOOGL
```

### 자동 매매 (KIS API 연동)
```bash
# Dry-run 모드 (실제 주문 안함)
./traveler --strategy pullback --universe russell --auto-trade --dry-run

# 실전 매매 (CONFIRM 입력 필요)
./traveler --strategy pullback --universe russell --auto-trade --dry-run=false
```

## 한국투자증권(KIS) API 설정

### 1. API 키 발급
1. [한국투자증권 OpenAPI](https://apiportal.koreainvestment.com/) 접속
2. 앱 등록 → App Key, App Secret 발급
3. 해외주식 거래 권한 신청

### 2. 설정 파일 (config.yaml)
```yaml
kis:
  app_key: "YOUR_APP_KEY"
  app_secret: "YOUR_APP_SECRET"
  account_no: "12345678-01"  # 계좌번호

trader:
  dry_run: true           # true: 시뮬레이션, false: 실전
  max_positions: 5        # 최대 동시 포지션
  max_position_pct: 0.2   # 종목당 최대 20%
  risk_per_trade: 0.01    # 거래당 리스크 1%
  monitor_interval: 30    # 모니터링 주기 (초)
```

### 3. 환경 변수 (선택)
```bash
export KIS_APP_KEY="your_key"
export KIS_APP_SECRET="your_secret"
export KIS_ACCOUNT_NO="12345678-01"
```

## 자동 매매 기능

### 주요 특징
- **실계좌 잔고 조회**: API로 실제 예수금 확인
- **토큰 캐시**: 24시간 토큰 캐싱 (API 호출 최소화)
- **잔고 기반 필터링**: 매수 가능한 종목만 추천
- **포지션 사이징**: 리스크 기반 자동 수량 계산
- **손절/익절 모니터링**: 자동 청산

### 매매 플로우
```
1. KIS API 토큰 확인 (캐시 또는 신규 발급)
2. 계좌 잔고 조회
3. 유니버스 스캔 → 시그널 수집
4. 잔고 기준 필터링 (가격 > 잔고 20% 제외)
5. 포지션 사이징 계산
6. 주문 실행 (dry-run이면 시뮬레이션)
7. 포지션 모니터링 시작
```

### 안전장치
| 장치 | 설명 |
|------|------|
| dry_run | 기본 true, 실제 주문 안함 |
| CONFIRM 입력 | 실전 매매시 확인 필수 |
| 최대 포지션 | 동시 5개 제한 |
| 종목당 최대 | 자본의 20% 제한 |
| 토큰 캐시 | 반복 발급으로 인한 API 정지 방지 |

## CLI 옵션

### 기본 옵션
| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--strategy` | morning-dip | 전략 선택 (morning-dip, pullback) |
| `--universe` | (없음) | 종목 유니버스 선택 |
| `--capital` | 100000 | 계좌 자금 (USD) - auto-trade시 실제 잔고 사용 |
| `--symbols` | (전체) | 검사할 종목 (쉼표로 구분) |
| `--format` | table | 출력 형식 (table, json) |
| `--workers` | 10 | 병렬 처리 워커 수 |
| `--verbose` | false | 상세 출력 |

### 자동 매매 옵션
| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--auto-trade` | false | 자동 매매 모드 활성화 |
| `--dry-run` | true | 시뮬레이션 모드 |
| `--market-order` | false | 시장가 주문 (기본: 지정가) |
| `--monitor` | false | 포지션 모니터링만 실행 |

### Web UI 옵션
| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--web` | false | 웹 UI 모드 |
| `--port` | 8080 | 웹 서버 포트 |

### 백테스트 옵션
| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--backtest` | false | 백테스트 모드 |
| `--backtest-days` | 365 | 백테스트 기간 (일) |

## Universe 옵션

| Universe | 종목 수 | 설명 |
|----------|---------|------|
| `test` | 10 | 테스트용 (AAPL, MSFT 등) |
| `dow30` | 30 | 다우존스 30 |
| `nasdaq100` | 100 | 나스닥 100 |
| `sp500` | 100 | S&P 500 상위 100 |
| `midcap` | 100 | S&P MidCap 400 상위 100 |
| `russell` | 200 | Russell 2000 상위 200 |

## Web UI

```bash
# 웹 서버 시작
./traveler --web

# 포트 지정
./traveler --web --port 3000
```

브라우저에서 `http://localhost:8080` 접속:
- **Run Scan**: 실시간 스캔 실행
- **Load Report**: 저장된 JSON 리포트 불러오기
- **Detail**: 개별 종목 차트 및 매매 가이드

## 사용 예시

### 1. 스캔만 (추천 종목 확인)
```bash
./traveler --strategy pullback --universe russell
```

### 2. 자동 매매 시뮬레이션
```bash
./traveler --strategy pullback --universe russell --auto-trade --dry-run
```
출력:
```
[KIS] Using cached token (expires: 2026-02-04 01:20:43)
KIS Account Balance: $204.74
Loading russell universe (200 stocks)...
Scanning 200 stocks for pullback opportunities...

Found 5 pullback opportunities:
┌───┬────────┬────────┬────────┬────────┬─────────┬────────┐
│ # │ SYMBOL │ PRICE  │ SHARES │ AMOUNT │ ALLOC % │ RISK $ │
├───┼────────┼────────┼────────┼────────┼─────────┼────────┤
│ 1 │ AMPH   │ $27.11 │ 1      │ $27.11 │ 13.2%   │ $0.74  │
│ 2 │ DIN    │ $35.27 │ 1      │ $35.27 │ 17.2%   │ $0.71  │
...
```

### 3. 실전 매매
```bash
./traveler --strategy pullback --universe russell --auto-trade --dry-run=false
```
"CONFIRM" 입력 필요

### 4. 포지션 모니터링
```bash
./traveler --monitor
```

## 리포트 저장

스캔 완료 시 자동으로 리포트 파일 생성:
- `report_YYYY-MM-DD_HHMMSS.json` - 웹 UI에서 로드 가능
- `report_YYYY-MM-DD_HHMMSS.txt` - 텍스트 요약

## API 설정 (시세 데이터)

### 지원 API
| API | Rate Limit | 특징 |
|-----|------------|------|
| **Finnhub** | 분당 60회 | 주력 (빠른 스캔) |
| **Alpha Vantage** | 분당 5회 | 보조 |
| **Yahoo Finance** | 비공식 | 폴백 (API 키 불필요) |

### API 키 설정
```bash
export FINNHUB_API_KEY="your_key"
export ALPHAVANTAGE_API_KEY="your_key"
```

## 프로젝트 구조

```
traveler/
├── cmd/traveler/main.go          # CLI 진입점
├── internal/
│   ├── broker/                   # 브로커 API
│   │   ├── broker.go             # Broker 인터페이스
│   │   └── kis/                  # 한국투자증권 API
│   │       ├── client.go         # HTTP 클라이언트
│   │       ├── auth.go           # OAuth 토큰 관리 (24h 캐시)
│   │       └── types.go          # 요청/응답 타입
│   ├── trader/                   # 자동 매매
│   │   ├── trader.go             # AutoTrader
│   │   ├── executor.go           # 주문 실행
│   │   ├── monitor.go            # 손절/익절 모니터링
│   │   └── risk.go               # 리스크 관리
│   ├── strategy/                 # 매매 전략
│   │   ├── pullback.go           # 눌림목 전략
│   │   └── indicators.go         # 기술적 지표
│   ├── provider/                 # 시세 데이터 API
│   │   ├── finnhub.go
│   │   ├── yahoo.go
│   │   └── fallback.go           # 멀티 프로바이더
│   ├── symbols/universe.go       # 종목 유니버스
│   ├── web/                      # 웹 UI
│   └── backtest/                 # 백테스트
├── config.yaml                   # 설정 파일
└── README.md
```

## 향후 계획

- [ ] 적응형 자동 스캔 (예수금 기반 유니버스 자동 선택)
- [ ] 시그널 품질 평가 및 확대 스캔
- [ ] 완전 자동화 모드 (`--auto` 플래그)

## 라이선스

MIT License
