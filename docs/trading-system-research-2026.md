# 자동 매매 시스템 리서치 (2026-02-17)

## 1. 알고 트레이딩 프레임워크 현황

### Tier 1: 프로덕션급 플랫폼

**QuantConnect (LEAN Engine)**
- C# 코어 + Python 바인딩, 이벤트 기반, 클라우드 백테스팅
- 30만+ 사용자, 기관 헤지펀드 사용
- 멀티에셋 (주식, 선물, 옵션, 크립토)

**NautilusTrader**
- Rust/Cython 코어 + Python API
- 핵심 혁신: 백테스트와 라이브 트레이딩에 동일 코드 사용 (재구현 불필요)
- Python 접근 가능 프레임워크 중 최고 성능

### Tier 2: 특화/커뮤니티

**Freqtrade** — 크립토 특화, ML 최적화, Telegram 제어, 활발한 개발
**Jesse** — 크립토, GPT 지원 전략 개발
**Backtrader** — 심플, 파이썬, 개발 정체
**StockSharp** — C#, 비주얼 전략 디자이너

### 성공하는 프레임워크의 공통 패턴
1. **이벤트 기반 > 벡터화**: 프로덕션은 무조건 이벤트 기반
2. **전략-실행 동일성**: 백테스트 = 라이브 코드 (Traveler도 이 패턴)
3. **모듈 분리**: Strategy/Broker/Risk/Execution (Traveler의 internal/ 구조와 동일)

---

## 2. 소규모 자본에 유효한 전략 패턴

### Mean Reversion vs Trend Following

| 속성 | Mean Reversion | Trend Following |
|------|---------------|-----------------|
| 승률 | 70-90% | 20-40% |
| 보유기간 | 2-10일 | 수주~수개월 |
| 트레이드당 수익 | 작음 (1-5%) | 큼 (10-50%+) |
| 에퀴티 커브 | 부드럽고 안정 | 울퉁불퉁 |
| 적합 시장 | 횡보, 박스권 | 추세, 방향성 |

**2025-2026 컨센서스**: 둘을 결합하는 것이 최선. 레짐별로 전략 전환 — Traveler가 이미 하고 있는 방식.

### 포지션 사이징

**Fixed Fractional** (기본): 계좌의 1-2% 리스크/트레이드
**ATR 기반**: 변동성에 반비례 → 리스크 균등화
**Half-Kelly**: 매매 이력 충분 시 적용 (Full Kelly는 위험)
**최대 포트폴리오 리스크**: 동시 6-10% 이하

### 보유 기간 컨센서스
- 스캘핑 (초~분): 개인 불가 (코로케이션 필요)
- 데이트레이딩: PDT 규칙, 최소 $25K
- **스윙 (2-10일)**: 개인 알고 트레이더의 스윗스팟
- 포지션 (수주~수개월): 자본 많을 때

---

## 3. 트레일링 스탑 메커니즘

### 87개 스탑 전략 백테스트 결과 (SPY, GLD, AAPL 등 8개 자산)

| 방법 | 수익 확보 | 손실 회피 | 비고 |
|------|----------|----------|------|
| **Keltner Channel Stop** | +211 bps | 59 bps | **최고 성과** |
| Historical Quantile Stop | +375 bps | 113 bps | 수익 확보 최고 |
| MA Crossover | +130 bps | 79 bps | 안정적 |
| Parabolic SAR | baseline | ~30 bps | 무난 |
| ATR Trailing (1x) | 음수 | -60 bps | 너무 공격적 |
| Fixed Loss Stop | 미미 | ~6 bps | 사실상 무의미 |

### ATR 기반 최적 파라미터 (스윙 트레이딩)
- ATR 기간: 14-21일
- 멀티플라이어: 2.0-3.0x (2.5x가 적정)
- Chandelier Exit: HighestHigh(22) - ATR(22) × 3.0

### 실용적 티어드 트레일링 스탑
1. **초기 스탑**: ATR(14) × 2.0 (진입 보호)
2. **T1 도달 후**: Chandelier Exit (ATR×3.0)로 전환
3. **추세 지속 시**: Keltner Channel 하단으로 트레일링
4. **레짐 적응**: 추세장 → 넓게, 횡보장 → 좁게

---

## 4. AI/ML 트레이딩 현실

### 냉정한 평가
- "AI 트레이딩이 일반 투자자에게 측정 가능한 장기 수익 우위를 제공하지 않는다" (CapTrader 2026)
- StockBench: GPT-5, Claude-4 등 LLM 에이전트 대부분 buy-and-hold 못 이김
- 독립 AI 예측 시스템은 과적합 + 알파 소멸 문제

### 실제 작동하는 AI 활용법
1. **감성 분석 필터**: FinBERT(무료) 또는 Alpha Vantage 뉴스 API → 기술적 시그널 확인 레이어
2. **레짐 감지 고도화**: HMM(Hidden Markov Model) → MA 크로스오버보다 정교
3. **RL 포지션 사이징**: 매매 이력 기반 강화학습 (Python 사이드카)
4. **하이브리드 LLM+RL**: PrimoRL — Llama-3.1 감성 추출 + PPO/SAC 의사결정 (Sharpe 1.70)

### 주요 프로젝트

| 프로젝트 | Stars | 설명 |
|----------|-------|------|
| FinRL | 15K+ | 강화학습 트레이딩 (PPO, A2C, SAC) |
| FinGPT | 14K+ | 금융 LLM (Llama2 fine-tune, F1 87.62%) |
| TradingAgents | 4K+ | 멀티 에이전트 LLM (GPT-5, Claude 4 지원) |
| Freqtrade+FreqAI | — | 자체 적응 ML (LightGBM, LSTM, RL) |

### 감성 분석 API

| API | 특징 | 비용 |
|-----|------|------|
| Alpha Vantage News | 내장 감성 점수 (-0.35~+0.35) | 무료 티어 |
| Finnhub | 기업 뉴스 + 소셜 감성 | 무료 티어 |
| FinBERT | HuggingFace, 금융 특화 BERT | 무료 (셀프호스트) |
| FinGPT v3.3 | Llama2-13B fine-tune | ~$300 fine-tune |

---

## 5. 한국 시장 특화

### 브로커 API

**KIS Open API** (현재 사용 중)
- REST 기반, OS 무관, AI 연동 공식 지원
- Rate limit: 300/min domestic

**키움증권 REST API** (2025년 3월 신규)
- OCX 의존성 제거, Windows/macOS/Linux 지원
- 국내 최대 점유율 → 두 번째 브로커 후보

### 한국 퀀트 커뮤니티
- WikiDocs: [KIS 자동매매](https://wikidocs.net/book/7845), [비트코인 자동매매](https://wikidocs.net/book/1665)
- GitHub: quantylab, hyunyulhenry/quant_py
- YouTube: 조코딩 (Upbit 변동성 돌파 튜토리얼)

### Upbit 크립토 전략 현황
1. **변동성 돌파** — 압도적 1위 (Traveler에 이미 구현)
2. **변동성 돌파 + MA 필터** — 20일 MA 위에서만 진입
3. **변동성 돌파 + AI** — GPT/Prophet 결합
4. **RSI + MACD 조합** — 과매도 + 크로스 확인

### 레짐 감지 고도화

**HMM (Hidden Markov Model)** — 업계 표준
- 2-3 상태: 저변동/추세, 고변동/횡보, 위기
- 입력: 일간 수익률 + 실현 변동성
- 슬라이딩 윈도우 ~2700일, 매일 재학습

**ADX 기반 2차원 레짐** — 실용적 개선
- 추세 강도(ADX > 25) × 변동성(ATR 백분위)
- 4가지 레짐: 추세+저변동, 추세+고변동, 횡보+저변동, 횡보+고변동

**Wasserstein Distance Clustering** — 최신 연구
- 최적 수송 이론 기반 수익률 분포 클러스터링
- 비정규 분포 처리에 HMM보다 우수

### 멀티 타임프레임

| 역할 | 스윙 트레이딩 | 제공 정보 |
|------|-------------|----------|
| 상위 (추세) | 주봉 | 주요 추세, S/R 레벨 |
| 중간 (시그널) | 일봉 | 진입/퇴출 시그널 |
| 하위 (타이밍) | 4시간/1시간 | 정밀 진입, 스탑 배치 |

- 2개 이상 타임프레임 일치 시 승률 58% vs 불일치 39%
- 최대 3개 타임프레임 (분석 마비 방지)

---

## 6. 핵심 평가 지표

### 필수 지표

| 지표 | 공식 | Good | Excellent |
|------|------|------|-----------|
| Sharpe | (Return-Rf)/StdDev | >1.0 | >2.0 |
| Sortino | (Return-Rf)/DownsideStdDev | >1.0 | >2.0 |
| Max Drawdown | (Trough-Peak)/Peak | <15% | <10% |
| Calmar | AnnualReturn/MaxDrawdown | >1.0 | >3.0 |
| Profit Factor | GrossProfit/GrossLoss | >1.5 | >2.0 |

### 고급 지표

| 지표 | 용도 |
|------|------|
| Omega Ratio | 전체 수익률 분포 (비정규 분포에 Sharpe보다 우수) |
| Recovery Factor | Net Profit / abs(MaxDD) — 회복력 |
| MDD Duration | 최대 손실 회복 기간 (깊이보다 중요할 수 있음) |
| Tail Ratio | 95th/abs(5th) 백분위 — 수익 비대칭성 |
| Deflated Sharpe | 다중 테스트 편향 보정 |

### 과적합 경고 신호
- Profit Factor > 4.0
- Sharpe > 3.0 (백테스트)
- Win Rate > 90%
- 아웃오브샘플 성과 급격 저하

---

## Sources

### 프레임워크/전략
- [Algorithmic Trading Software 2026 - Gainify](https://www.gainify.io/blog/algorithmic-trading-software)
- [NautilusTrader](https://github.com/nautechsystems/nautilus_trader)
- [best-of-algorithmic-trading](https://github.com/merovinh/best-of-algorithmic-trading)
- [Mean Reversion vs Trend Following - QuantifiedStrategies](https://www.quantifiedstrategies.com/mean-reversion-vs-trend-following/)

### 트레일링 스탑
- [87 Stop Loss Strategies Tested - Paper to Profit](https://papertoprofit.substack.com/p/i-tested-87-different-stop-loss-strategies)
- [5 ATR Stop-Loss Strategies - LuxAlgo](https://www.luxalgo.com/blog/5-atr-stop-loss-strategies-for-risk-control/)
- [Chandelier Exit - StockCharts](https://chartschool.stockcharts.com/table-of-contents/technical-indicators-and-overlays/technical-overlays/chandelier-exit)

### AI/ML 트레이딩
- [PrimoRL: LLM + DRL Framework](https://www.mdpi.com/2504-2289/9/12/317)
- [StockBench: Can LLM Agents Trade?](https://arxiv.org/html/2510.02209v1)
- [FinRL](https://github.com/AI4Finance-Foundation/FinRL)
- [FinGPT](https://github.com/AI4Finance-Foundation/FinGPT)
- [TradingAgents](https://github.com/TauricResearch/TradingAgents)
- [AI Trading Hype vs Reality - CapTrader](https://www.captrader.com/en/blog/ai-trading/)

### 한국 시장
- [KIS Developers](https://apiportal.koreainvestment.com/intro)
- [키움 REST API](https://openapi.kiwoom.com/)
- [DQN on Korean Exchange](https://www.koreascience.kr/article/JAKO202514739604943.page)
- [pyupbit-autotrade](https://github.com/youtube-jocoding/pyupbit-autotrade)

### 레짐 감지
- [QuantStart: HMM Regime Detection](https://www.quantstart.com/articles/market-regime-detection-using-hidden-markov-models-in-qstrader/)
- [Wasserstein Clustering (arXiv:2110.11848)](https://arxiv.org/abs/2110.11848)
- [Regime-Adaptive Trading Python - QuantInsti](https://blog.quantinsti.com/regime-adaptive-trading-python/)

### 평가 지표
- [Top 7 Backtesting Metrics - LuxAlgo](https://www.luxalgo.com/blog/top-7-metrics-for-backtesting-results/)
- [Sharpe, Sortino, Calmar - Medium](https://medium.com/@mburakbedir/beyond-returns-a-deep-dive-into-risk-adjusted-metrics-with-sharpe-sortino-calmer-and-modigliani-9653a2341f51)
- [Omega Ratio - PyQuant News](https://www.pyquantnews.com/the-pyquant-newsletter/capture-your-tail-risk-with-the-omega-ratio)
