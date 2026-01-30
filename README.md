# Traveler - 장초반 하락 → 종장 상승 패턴 탐지기

미국 주식(NYSE/NASDAQ)에서 "장 초반 하락 후 종장 상승" 패턴이 연속으로 나타나는 종목을 찾아내는 Go CLI 프로그램입니다.

## 패턴 정의

```
장 초반 하락 → 종장 상승 패턴:

1. 장 초반 (개장 후 1시간)
   - 시가 대비 최저점이 설정된 임계값 이하로 하락 (기본: -1%)

2. 종장 조건 (둘 중 하나 충족)
   - 종가가 시가 대비 상승 (기본: +0.5% 이상)
   - 또는 종가가 장중 최저점 대비 반등 (기본: +2% 이상)

3. 연속 감지
   - 설정된 N일 연속 위 패턴 충족 시 탐지
```

## 설치

### 요구사항
- Go 1.21 이상

### 빌드

```bash
git clone <repository>
cd traveler

# 의존성 설치
go mod tidy

# 빌드
go build -o traveler ./cmd/traveler

# Windows
go build -o traveler.exe ./cmd/traveler
```

## 사용법

### 기본 실행

```bash
# 기본 설정으로 실행 (3일 연속 패턴, 기본 종목 리스트)
./traveler

# 특정 종목만 검사
./traveler --symbols AAPL,TSLA,NVDA,MSFT,GOOGL

# 5일 연속 패턴 검색
./traveler --days 5

# JSON 형식으로 출력
./traveler --format json
```

### CLI 옵션

| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--days` | 3 | 최소 연속 패턴 일수 |
| `--symbols` | (전체) | 검사할 종목 (쉼표로 구분) |
| `--drop` | -1.0 | 장초반 최소 하락폭 (%) |
| `--rise` | 0.5 | 종가 최소 상승폭 (%) |
| `--rebound` | 2.0 | 장중 최저점 대비 최소 반등폭 (%) |
| `--workers` | 10 | 병렬 처리 워커 수 |
| `--format` | table | 출력 형식 (table, json) |
| `--config` | config.yaml | 설정 파일 경로 |
| `--verbose` | false | 상세 출력 |

### 사용 예시

```bash
# 기본 실행 (3일 연속 패턴)
./traveler

# 5일 연속 패턴, 하락폭 -2% 이상
./traveler --days 5 --drop -2.0

# 특정 종목 JSON 출력
./traveler --symbols AAPL,TSLA,NVDA --format json

# 상세 옵션 지정
./traveler --days 3 \
           --workers 10 \
           --drop -1.0 \
           --rise 0.5 \
           --rebound 2.0
```

### 출력 예시

```
Scanning 7 US stocks for 1-day morning-dip pattern...

Scanning 100% [████████████████████████████████████████] (7/7, 54 it/min)

Found 5 stocks with 1+ day morning-dip pattern:

┌────────┬───────┬──────┬─────────┬──────────┬──────┬────────┐
│ SYMBOL │ NAME  │ DAYS │ AVG DIP │ AVG RISE │ PROB │ SIGNAL │
├────────┼───────┼──────┼─────────┼──────────┼──────┼────────┤
│ GOOGL  │ GOOGL │ 1    │ -3.9%   │ +-0.8%   │ 35%  │ weak   │
│ MSFT   │ MSFT  │ 1    │ -3.6%   │ +-1.5%   │ 32%  │ weak   │
│ AMD    │ AMD   │ 1    │ -3.1%   │ +-1.0%   │ 31%  │ weak   │
│ META   │ META  │ 1    │ -2.9%   │ +0.1%    │ 33%  │ weak   │
│ NVDA   │ NVDA  │ 1    │ -1.7%   │ +0.6%    │ 27%  │ avoid  │
└────────┴───────┴──────┴─────────┴──────────┴──────┴────────┘

--- Technical Analysis Details ---

[GOOGL] GOOGL
  Pattern: 1 consecutive days | Strength: 72 | Consistency: 50
  Trend: neutral (MA5: -1.8%, MA20: +0.0%)
  >> Continuation Probability: 35% [WEAK]

[MSFT] MSFT
  Pattern: 1 consecutive days | Strength: 59 | Consistency: 50
  Trend: neutral (MA5: -0.1%, MA20: +0.0%)
  >> Continuation Probability: 32% [WEAK]

Scanned 7 stocks in 9s
```

## 기술적 분석 (Technical Analysis)

패턴이 감지된 종목에 대해 다음 날에도 패턴이 이어질 확률을 예측합니다.

### 분석 지표

| 지표 | 설명 |
|------|------|
| **RSI(14)** | 과매수/과매도 판단 (30 이하: oversold, 70 이상: overbought) |
| **Volume Ratio** | 당일 거래량 / 20일 평균 거래량 |
| **MA5/MA20** | 5일/20일 이동평균 대비 현재가 위치 |
| **Pattern Strength** | 패턴 강도 (하락폭 + 반등폭 기반) |
| **Consistency** | 연속 패턴의 일관성 (표준편차 기반) |

### 지속 확률 (Continuation Probability)

다음 요소를 종합하여 0-100% 확률 산출:

1. **패턴 강도** (25%): 하락폭과 반등폭이 클수록 높은 점수
2. **일관성** (25%): 연속 패턴이 일정할수록 높은 점수
3. **RSI** (20%): 과매도 상태(RSI < 30)일수록 반등 가능성 높음
4. **거래량** (15%): 평균 대비 거래량 증가 시 높은 점수
5. **연속 일수** (15%): 연속 일수가 길수록 높은 점수

### 추천 등급

| 등급 | 확률 | 의미 |
|------|------|------|
| **strong** | 70%+ | 강력 추천 - 패턴 지속 가능성 높음 |
| **moderate** | 50-70% | 보통 - 관심 종목으로 모니터링 |
| **weak** | 30-50% | 약함 - 추가 분석 필요 |
| **avoid** | 30% 미만 | 회피 - 패턴 지속 가능성 낮음 |

## API 설정

### 데이터 제공자

| API | Rate Limit | 특징 |
|-----|------------|------|
| **Finnhub** | 분당 60회 | 주력 (빠른 스캔) |
| **Alpha Vantage** | 분당 5회 | 보조 |
| **Yahoo Finance** | 비공식 | 폴백 (API 키 불필요) |

### API 키 설정

환경 변수로 설정:

```bash
# Linux/macOS
export FINNHUB_API_KEY="your_finnhub_key"
export ALPHAVANTAGE_API_KEY="your_alphavantage_key"

# Windows (PowerShell)
$env:FINNHUB_API_KEY="your_finnhub_key"
$env:ALPHAVANTAGE_API_KEY="your_alphavantage_key"

# Windows (CMD)
set FINNHUB_API_KEY=your_finnhub_key
set ALPHAVANTAGE_API_KEY=your_alphavantage_key
```

또는 `config.yaml` 파일에서 설정:

```yaml
api:
  finnhub:
    key: "your_finnhub_key"
    rate_limit: 60
  alphavantage:
    key: "your_alphavantage_key"
    rate_limit: 5
```

### API 키 발급

- **Finnhub**: https://finnhub.io/ (무료 가입)
- **Alpha Vantage**: https://www.alphavantage.co/support/#api-key (무료 발급)

> API 키 없이도 Yahoo Finance 폴백으로 동작하지만, 안정성과 속도를 위해 Finnhub 키 설정을 권장합니다.

## 설정 파일

`config.yaml`:

```yaml
api:
  finnhub:
    key: ""  # 환경 변수 FINNHUB_API_KEY로 대체 가능
    rate_limit: 60
  alphavantage:
    key: ""  # 환경 변수 ALPHAVANTAGE_API_KEY로 대체 가능
    rate_limit: 5

scanner:
  workers: 10
  timeout: 30m

pattern:
  consecutive_days: 3
  morning_drop_threshold: -1.0  # 장초반 최소 하락폭 (%)
  close_rise_threshold: 0.5     # 종가 최소 상승폭 (%)
  rebound_threshold: 2.0        # 장중 최저점 대비 최소 반등폭 (%)
  morning_window: 60            # 장초반 판단 시간 (분)
  closing_window: 60            # 종장 판단 시간 (분)
```

## 프로젝트 구조

```
traveler/
├── cmd/
│   └── traveler/
│       └── main.go           # CLI 진입점 (cobra)
├── internal/
│   ├── config/
│   │   └── config.go         # 설정 관리 (YAML)
│   ├── provider/
│   │   ├── provider.go       # Provider 인터페이스 + Fallback
│   │   ├── finnhub.go        # Finnhub API 구현
│   │   ├── alphavantage.go   # Alpha Vantage API 구현
│   │   └── yahoo.go          # Yahoo Finance 구현 (폴백)
│   ├── analyzer/
│   │   ├── pattern.go        # 패턴 감지 로직
│   │   ├── pattern_test.go   # 패턴 테스트
│   │   ├── intraday.go       # 분봉 분석
│   │   └── technical.go      # 기술적 분석 (RSI, MA, 지속확률)
│   ├── scanner/
│   │   └── scanner.go        # 병렬 스캐너 (Worker Pool)
│   ├── symbols/
│   │   └── loader.go         # NYSE/NASDAQ 종목 리스트 로더
│   └── ratelimit/
│       ├── limiter.go        # Rate Limiter (Token Bucket)
│       └── limiter_test.go   # Rate Limiter 테스트
├── pkg/
│   └── model/
│       └── types.go          # 공통 타입 정의
├── config.yaml               # 설정 파일
├── go.mod
├── go.sum
└── README.md
```

## 아키텍처

### 병렬 처리

```
┌─────────────────────────────────────────────────────────┐
│                    Main Scanner                          │
├─────────────────────────────────────────────────────────┤
│  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐    │
│  │ Worker1 │  │ Worker2 │  │ Worker3 │  │ Worker4 │    │
│  └────┬────┘  └────┬────┘  └────┬────┘  └────┬────┘    │
│       │            │            │            │          │
│       └────────────┴─────┬──────┴────────────┘          │
│                          │                               │
│                  ┌───────▼───────┐                      │
│                  │  Rate Limiter │ (API별 독립 관리)     │
│                  └───────┬───────┘                      │
│                          │                               │
│     ┌────────────────────┼────────────────────┐         │
│     │                    │                    │         │
│  ┌──▼───┐           ┌────▼────┐          ┌───▼───┐     │
│  │Finnhub│          │AlphaVant│          │ Yahoo │     │
│  │60/min │          │ 5/min   │          │Fallback│    │
│  └───────┘          └─────────┘          └───────┘     │
└─────────────────────────────────────────────────────────┘
```

### Rate Limiting

- Token Bucket 알고리즘 사용
- 각 API별 독립적 limiter 관리
- 429 응답 시 Exponential Backoff 적용

## 테스트

```bash
# 전체 테스트 실행
go test ./... -v

# 특정 패키지 테스트
go test ./internal/analyzer -v
go test ./internal/ratelimit -v
```

## 주요 의존성

- `github.com/spf13/cobra` - CLI 프레임워크
- `github.com/olekukonko/tablewriter` - 테이블 출력
- `github.com/schollz/progressbar/v3` - 진행률 표시
- `golang.org/x/time/rate` - Rate Limiting
- `gopkg.in/yaml.v3` - YAML 파싱

## 라이선스

MIT License
