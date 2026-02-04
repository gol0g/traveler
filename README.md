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
│   ├── daemon/                   # 데몬 모드 (완전 자동화)
│   │   ├── daemon.go             # 메인 오케스트레이터
│   │   ├── market.go             # 마켓 시간 관리
│   │   └── tracker.go            # 일일 P&L 추적
│   ├── trader/                   # 자동 매매
│   │   ├── trader.go             # AutoTrader
│   │   ├── executor.go           # 주문 실행
│   │   ├── monitor.go            # 손절/익절 모니터링
│   │   ├── sizer.go              # 포지션 사이징 (수수료 고려)
│   │   ├── adaptive.go           # 적응형 스캔
│   │   └── risk.go               # 리스크 관리
│   ├── strategy/                 # 매매 전략
│   │   ├── registry.go           # 전략 레지스트리
│   │   ├── pullback.go           # 눌림목 전략
│   │   └── indicators.go         # 기술적 지표
│   ├── provider/                 # 시세 데이터 API
│   │   ├── finnhub.go
│   │   ├── yahoo.go
│   │   └── fallback.go           # 멀티 프로바이더
│   ├── symbols/universe.go       # 종목 유니버스
│   ├── web/                      # 웹 UI
│   └── backtest/                 # 백테스트
├── scripts/                      # Windows 자동화 스크립트
│   ├── setup-daemon.ps1          # 초기 설정
│   ├── test-wake-only.ps1        # Wake 테스트
│   └── setup-autologin.bat       # 자동 로그인
├── config.yaml                   # 설정 파일
└── README.md
```

## Daemon 모드 (완전 자동화)

### 개요
PC 절전 → 자동 기상 → 시장 대기 → 자동 매매 → 리포트 → 절전 반복

### 사용법
```bash
# 데몬 모드 시작 (장 열릴 때까지 대기 후 자동 매매)
./traveler --daemon

# 절전 없이 테스트
./traveler --daemon --sleep-on-exit=false

# 일일 목표/손실 한도 설정
./traveler --daemon --daily-target=1.5 --daily-loss-limit=-2.0
```

### Daemon 옵션
| 옵션 | 기본값 | 설명 |
|------|--------|------|
| `--daemon` | false | 데몬 모드 활성화 |
| `--sleep-on-exit` | true | 종료 시 PC 절전 |
| `--daily-target` | 1.0 | 일일 목표 수익률 (%) |
| `--daily-loss-limit` | -2.0 | 일일 최대 손실 (%) |
| `--adaptive` | true | 적응형 스캔 (잔고 기반 유니버스 선택) |

### 동작 흐름
```
1. 시작 → 마켓 상태 확인
2. 마켓 닫힘 → 오픈까지 대기 (최대 2시간)
3. 마켓 오픈 → 적응형 스캔 (30분 주기)
4. 시그널 발견 → 포지션 사이징 → 주문 실행
5. 포지션 모니터링 (30초 주기) → 손절/익절
6. 종료 조건 (마감/목표달성/손실한도) → 리포트 생성
7. 다음 날 Wake timer 등록 → PC 절전
8. 다음 날 자동 기상 → 1번부터 반복
```

### Windows 자동 기상 설정
```powershell
# 관리자 PowerShell에서 초기 설정 (1회만)
.\scripts\setup-daemon.ps1

# 테스트 (2분 후 자동 기상)
.\scripts\test-wake-only.ps1
```

## 수수료 및 리스크 관리

### 수수료 계산
- 매매 시 자동으로 수수료 계산 (기본 0.25% × 2 = 0.5%)
- P&L에서 수수료 차감한 순이익 표시
- 리포트에 총 수수료 표시

### 최소 기대수익률 필터
- 기대수익률 < 1% 시그널 자동 스킵
- 수수료(0.5%) + 마진(0.5%) 보장
- 소액 계좌(<$500)는 1.5% 기준 적용

### 리포트 예시
```
SUMMARY
-------
  Realized P&L:     $100.00
  Unrealized P&L:   $50.00
  Commission:       $12.50 (0.25%)
  Net P&L:          $137.50 (2.75%)
```

## 적응형 스캔

잔고에 따라 자동으로 유니버스와 설정 조정:

| 잔고 | 유니버스 | 리스크/거래 | 최대 포지션 |
|------|----------|-------------|-------------|
| < $500 | test (소형주) | 2% | 3개 |
| < $5,000 | russell (중소형) | 1% | 5개 |
| ≥ $5,000 | nasdaq100 (대형) | 1% | 5개 |

시그널 부족 시 자동으로 다른 유니버스까지 확대 스캔.

## Scripts (Windows 자동화)

| 스크립트 | 설명 |
|----------|------|
| `setup-daemon.ps1` | 초기 Wake timer 설정 (관리자 1회) |
| `test-wake-only.ps1` | Wake timer 테스트 |
| `setup-autologin.bat` | Windows 자동 로그인 설정 |
| `set-autologin.ps1` | 자동 로그인 비밀번호 저장 |

## 향후 계획

- [x] 적응형 자동 스캔 (예수금 기반 유니버스 자동 선택)
- [x] 시그널 품질 평가 및 확대 스캔
- [x] 완전 자동화 모드 (daemon)
- [x] 수수료 계산 및 최소 기대수익률 필터
- [ ] 모니터 자동 켜기 (wake 후)
- [ ] 멀티 전략 동시 실행

## 라이선스

MIT License
