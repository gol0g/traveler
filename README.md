# Traveler - 주식 패턴 스캐너

미국 주식(NYSE/NASDAQ)에서 기술적 분석 패턴을 탐지하는 Go CLI 프로그램입니다.

## 지원 전략

### 1. Morning Dip (역추세)
```
장 초반 하락 → 종장 상승 패턴:
- 장 초반 (개장 후 1시간) 시가 대비 하락
- 종가가 시가 대비 상승 또는 장중 최저점 대비 반등
- 데이 트레이딩/스캘핑용
```

### 2. Pullback (추세 추종) - 권장
```
상승 추세 눌림목 매매:
- 주가가 MA50 위에 있음 (상승 추세 확인)
- 주가가 MA20 부근까지 조정
- 거래량이 평균보다 적음 (매도세 약함)
- 반전 신호 (양봉 또는 긴 아래꼬리)
- 스윙 트레이딩용
```

## 설치

```bash
git clone https://github.com/gol0g/traveler.git
cd traveler
go mod tidy
go build -o traveler ./cmd/traveler
```

## 사용법

### 전략 선택

```bash
# 눌림목 전략 (추세 추종) - 권장
./traveler --strategy pullback --symbols AAPL,MSFT,GOOGL,NVDA

# Morning Dip 전략 (역추세)
./traveler --strategy morning-dip --days 3

# 전체 종목 스캔
./traveler --strategy pullback
```

### CLI 옵션

| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--strategy` | morning-dip | 전략 선택 (morning-dip, pullback) |
| `--days` | 1 | 최소 연속 패턴 일수 (morning-dip) |
| `--symbols` | (전체) | 검사할 종목 (쉼표로 구분) |
| `--drop` | -1.0 | 장초반 최소 하락폭 (%) |
| `--rise` | 0.5 | 종가 최소 상승폭 (%) |
| `--rebound` | 2.0 | 장중 최저점 대비 최소 반등폭 (%) |
| `--workers` | 10 | 병렬 처리 워커 수 |
| `--format` | table | 출력 형식 (table, json) |
| `--config` | config.yaml | 설정 파일 경로 |
| `--verbose` | false | 상세 출력 |

### 출력 예시

#### Pullback 전략
```
Scanning 10 stocks for pullback opportunities...

Found 4 pullback opportunities:

┌────────┬───────┬──────────┬──────┬──────────────────────────────────────────────────┐
│ SYMBOL │ NAME  │ STRENGTH │ PROB │                      REASON                      │
├────────┼───────┼──────────┼──────┼──────────────────────────────────────────────────┤
│ AMZN   │ AMZN  │ 59       │ 56%  │ Uptrend pullback to MA20, low volume (0.8x)...   │
│ GOOGL  │ GOOGL │ 63       │ 48%  │ Uptrend pullback to MA20, bullish candle         │
│ NVDA   │ NVDA  │ 59       │ 46%  │ Uptrend pullback to MA20, long lower shadow      │
└────────┴───────┴──────────┴──────┴──────────────────────────────────────────────────┘

--- Pullback Details ---

[AMZN] AMZN
  Uptrend pullback to MA20 (2.9% above MA50), low volume (0.8x), bullish candle
  Close: $239.30 | MA20: $239.08 | MA50: $232.54
  Price vs MA50: +2.9% | Price vs MA20: -0.6%
  RSI(14): 40.3 | Volume: 0.8x avg
  >> Probability: 56% | Strength: 59
```

#### Morning Dip 전략
```
Found 5 stocks with 1+ day morning-dip pattern:

┌────────┬───────┬──────┬─────────┬──────────┬──────┬────────┐
│ SYMBOL │ NAME  │ DAYS │ AVG DIP │ AVG RISE │ PROB │ SIGNAL │
├────────┼───────┼──────┼─────────┼──────────┼──────┼────────┤
│ GOOGL  │ GOOGL │ 1    │ -3.9%   │ +0.8%    │ 35%  │ weak   │
│ MSFT   │ MSFT  │ 1    │ -3.6%   │ +1.5%    │ 32%  │ weak   │
└────────┴───────┴──────┴─────────┴──────────┴──────┴────────┘
```

## 전략 비교

| 특성 | Morning Dip | Pullback |
|------|-------------|----------|
| 유형 | 역추세 (Counter-Trend) | 순추세 (Trend-Following) |
| 용도 | 데이 트레이딩 | 스윙 트레이딩 |
| 위험도 | 높음 (떨어지는 칼날) | 낮음 (검증된 상승세) |
| 필요 데이터 | 분봉 | 일봉 (MA50, MA20) |
| 권장 | 단기 스캘핑 | 중기 포지션 |

## Pullback 전략 상세

### 진입 조건
1. **상승 추세**: 현재가 > MA50 (장기 상승세 확인)
2. **눌림 발생**: 저가가 MA20 부근 터치 (±2%)
3. **약한 매도**: 거래량 < 20일 평균 (매도세 약함)
4. **반전 신호**: 양봉 또는 긴 아래꼬리 (매수세 유입)

### 신뢰도 점수
- **Strength**: 조건 충족 정도 (0-100)
- **Probability**: 성공 확률 추정 (0-100%)

### 보조 지표
- RSI(14): 50 이하면 추가 상승 여력
- Volume: 0.7x 이하면 매도세 매우 약함
- Bouncing: 전일 저가 > 전전일 저가면 반등 중

## 기술적 분석 지표

| 지표 | 설명 |
|------|------|
| **RSI(14)** | 과매수/과매도 (30 이하: oversold, 70 이상: overbought) |
| **MA20/MA50** | 20일/50일 이동평균선 |
| **Volume Ratio** | 당일 거래량 / 20일 평균 |
| **Pattern Strength** | 패턴 강도 (0-100) |

## API 설정

### 지원 API

| API | Rate Limit | 특징 |
|-----|------------|------|
| **Finnhub** | 분당 60회 | 주력 (빠른 스캔) |
| **Alpha Vantage** | 분당 5회 | 보조 |
| **Yahoo Finance** | 비공식 | 폴백 (API 키 불필요) |

### API 키 설정

```bash
# 환경 변수
export FINNHUB_API_KEY="your_key"
export ALPHAVANTAGE_API_KEY="your_key"

# 또는 config.yaml
api:
  finnhub:
    key: "your_key"
```

### API 키 발급
- **Finnhub**: https://finnhub.io/
- **Alpha Vantage**: https://www.alphavantage.co/support/#api-key

## 프로젝트 구조

```
traveler/
├── cmd/traveler/main.go          # CLI 진입점
├── internal/
│   ├── strategy/
│   │   ├── strategy.go           # Strategy 인터페이스
│   │   ├── pullback.go           # 눌림목 전략
│   │   ├── morningdip.go         # Morning Dip 전략
│   │   └── indicators.go         # 기술적 지표 계산
│   ├── analyzer/
│   │   ├── pattern.go            # 패턴 감지 로직
│   │   ├── intraday.go           # 분봉 분석
│   │   └── technical.go          # 기술적 분석
│   ├── provider/
│   │   ├── provider.go           # Provider 인터페이스
│   │   ├── finnhub.go            # Finnhub API
│   │   ├── alphavantage.go       # Alpha Vantage API
│   │   └── yahoo.go              # Yahoo Finance
│   ├── scanner/scanner.go        # 병렬 스캐너
│   ├── symbols/loader.go         # 종목 로더
│   └── ratelimit/limiter.go      # Rate Limiter
├── pkg/model/types.go            # 공통 타입
├── config.yaml.example           # 설정 예시
└── README.md
```

## 테스트

```bash
go test ./... -v
```

## 라이선스

MIT License
