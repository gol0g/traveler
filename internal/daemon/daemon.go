package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"traveler/internal/broker"
	"traveler/internal/provider"
	"traveler/internal/strategy"
	"traveler/internal/symbols"
	"traveler/internal/trader"
	"traveler/pkg/model"
)

// Config 데몬 설정
type Config struct {
	// 마켓 설정
	Market           string        // "us", "kr", or "crypto"
	WaitForMarket    bool          // 마켓 열릴 때까지 대기
	MaxWaitTime      time.Duration // 최대 대기 시간

	// 거래 설정
	Daily            DailyConfig
	Sizer            trader.SizerConfig

	// 스캔 설정
	ScanInterval     time.Duration // 스캔 주기
	MonitorInterval  time.Duration // 모니터링 주기

	// 스캔 옵션
	ForceScan        bool // 이미 매매했더라도 강제 스캔

	// 자본 설정
	TradingCapital   float64 // 자동매매 전용 자본 (0이면 전체 잔고 사용)

	// 종료 설정
	SleepOnExit      bool // 종료시 PC 절전
	DataDir          string
}

// DefaultConfig 기본 설정
func DefaultConfig() Config {
	return Config{
		WaitForMarket:   true,
		MaxWaitTime:     2 * time.Hour,
		Daily:           DefaultDailyConfig(),
		ScanInterval:    30 * time.Minute,
		MonitorInterval: 30 * time.Second,
		SleepOnExit:     true,
	}
}

// Daemon 자동 매매 데몬
type Daemon struct {
	config     Config
	broker     broker.Broker
	provider   provider.Provider
	tracker    *DailyTracker
	autoTrader *trader.AutoTrader
	history    *trader.TradeHistory
	capital    *CapitalTracker // 자동매매 전용 자본 추적

	ctx            context.Context
	cancel         context.CancelFunc
	isRunning      bool
	startedAt      time.Time
	preMarketScan  bool                // 프리마켓 스캔 완료 여부
	preMarketSigs  []strategy.Signal   // 프리마켓에서 찾은 시그널

	// 장중 매매
	intradayScanner *strategy.IntradayScanner
	intradaySymbols []string // 장중 스캔 대상 종목
}

// NewDaemon 생성자
func NewDaemon(cfg Config, b broker.Broker, p provider.Provider) *Daemon {
	ctx, cancel := context.WithCancel(context.Background())

	// Sizer config는 나중에 잔고 확인 후 설정
	tracker := NewDailyTracker(cfg.Daily, cfg.DataDir)
	tracker.SetMarket(cfg.Market) // 파일명 분리: daily_us_*.json vs daily_kr_*.json

	// 마켓 타임존 설정 (US=ET, KR/Crypto=KST)
	if cfg.Market == "kr" || cfg.Market == "crypto" {
		if tz, err := time.LoadLocation("Asia/Seoul"); err == nil {
			tracker.SetTimezone(tz)
		}
	} else {
		if tz, err := time.LoadLocation("America/New_York"); err == nil {
			tracker.SetTimezone(tz)
		}
	}

	return &Daemon{
		config:   cfg,
		broker:   b,
		provider: p,
		tracker:  tracker,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// isKR 한국 시장 모드 여부
func (d *Daemon) isKR() bool {
	return d.config.Market == "kr"
}

// isCrypto 크립토 시장 모드 여부
func (d *Daemon) isCrypto() bool {
	return d.config.Market == "crypto"
}

// getMarketStatus 현재 시장에 맞는 마켓 상태 조회
func (d *Daemon) getMarketStatus() MarketStatus {
	if d.isCrypto() {
		return GetCryptoMarketStatus()
	}
	if d.isKR() {
		return GetKRMarketStatus(KRMarketSchedule())
	}
	return GetMarketStatus(DefaultMarketSchedule())
}

// Run 데몬 실행
func (d *Daemon) Run() error {
	d.startedAt = time.Now()
	log.Println("[DAEMON] Starting automated trading daemon...")

	// 화면 켜기 (절전 해제 후 모니터가 꺼져있을 수 있음)
	// 비동기 실행 — SendMessageW(HWND_BROADCAST)가 블로킹될 수 있음
	go func() {
		done := make(chan struct{})
		go func() {
			wakeMonitor()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			log.Println("[DAEMON] wakeMonitor timed out (5s), skipping")
		}
	}()

	d.isRunning = true
	defer func() {
		d.isRunning = false
	}()

	// 1. 마켓 상태 확인
	status := d.getMarketStatus()
	tzLabel := "ET"
	if d.isKR() || d.isCrypto() {
		tzLabel = "KST"
	}
	log.Printf("[DAEMON] Market status: %s (%s: %s)", status.Reason, tzLabel, status.CurrentTimeET.Format("15:04"))

	if !status.IsOpen {
		if !d.config.WaitForMarket {
			log.Println("[DAEMON] Market closed and WaitForMarket=false. Exiting.")
			return d.shutdown("market_closed")
		}

		if status.TimeToOpen > d.config.MaxWaitTime {
			log.Printf("[DAEMON] Wait time too long (%s > %s). Exiting.",
				FormatDuration(status.TimeToOpen), FormatDuration(d.config.MaxWaitTime))
			return d.shutdown("wait_too_long")
		}

		log.Printf("[DAEMON] Market opens in %s. Setting up and pre-scanning...", FormatDuration(status.TimeToOpen))
	}

	// 2. 계좌 잔고 확인 (프리마켓에서도 가능)
	balance, err := d.broker.GetBalance(d.ctx)
	if err != nil {
		log.Printf("[DAEMON] Failed to get balance: %v", err)
		return d.shutdown("balance_error")
	}
	if d.isKR() || d.isCrypto() {
		log.Printf("[DAEMON] Account balance: ₩%.0f", balance.TotalEquity)
	} else {
		log.Printf("[DAEMON] Account balance: $%.2f", balance.TotalEquity)
	}

	// 자동매매 전용 자본 결정
	tradingCapital := balance.TotalEquity
	if d.config.TradingCapital > 0 {
		// CapitalTracker로 자본 추적 (저장된 상태가 있으면 복원)
		dataDir := d.config.DataDir
		if dataDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				dataDir = filepath.Join(home, ".traveler")
			}
		}
		d.capital = NewCapitalTracker(dataDir, d.config.TradingCapital)
		tradingCapital = d.capital.GetCurrentCapital()
		log.Printf("[DAEMON] Trading capital: ₩%.0f (account: ₩%.0f)", tradingCapital, balance.TotalEquity)
	}

	// 3. 일일 트래커 시작
	if err := d.tracker.Start(tradingCapital); err != nil {
		log.Printf("[DAEMON] Failed to start tracker: %v", err)
	}

	// 4. Sizer 설정 (자동매매 자본 기반)
	if d.isCrypto() {
		d.config.Sizer = trader.AdjustConfigForCryptoBalance(tradingCapital)
	} else if d.isKR() {
		d.config.Sizer = trader.AdjustConfigForKRBalance(tradingCapital)
	} else {
		d.config.Sizer = trader.AdjustConfigForBalance(tradingCapital)
	}

	// 5. PlanStore 초기화 (~/.traveler/ 고정 경로)
	dataDir := d.config.DataDir
	if dataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dataDir = filepath.Join(home, ".traveler")
		} else {
			dataDir = "."
		}
	}
	planStore, err := trader.NewPlanStore(dataDir)
	if err != nil {
		log.Printf("[DAEMON] Warning: could not init plan store: %v", err)
	}

	// 6. TradeHistory 초기화
	history, err := trader.NewTradeHistory(dataDir)
	if err != nil {
		log.Printf("[DAEMON] Warning: could not init trade history: %v", err)
	} else {
		d.history = history
	}

	// 7. AutoTrader 생성 (PlanStore 포함)
	traderCfg := trader.Config{
		DryRun:          false, // 실전 모드
		MaxPositions:    d.config.Sizer.MaxPositions,
		MaxPositionPct:  d.config.Sizer.MaxPositionPct,
		TotalCapital:    tradingCapital,
		RiskPerTrade:    d.config.Sizer.RiskPerTrade,
		MonitorInterval: d.config.MonitorInterval,
	}
	d.autoTrader = trader.NewAutoTraderWithPlanStore(traderCfg, d.broker, d.isCrypto(), planStore)

	// Monitor에 TradeHistory 연결
	if d.history != nil {
		d.autoTrader.GetMonitor().SetTradeHistory(d.history, d.config.Market)
	}

	// 자본 추적 콜백 등록
	if d.capital != nil {
		d.autoTrader.GetMonitor().SetOnSell(func(investedAmount, sellAmount float64) {
			d.capital.RecordSell(investedAmount, sellAmount)
		})
	}

	// 8. 기존 포지션 확인 및 모니터 등록
	// 크립토: PlanStore에 플랜이 있는(=데몬이 진입한) 포지션만 모니터 등록
	//         수동 매수한 기존 포지션은 건드리지 않음
	// 주식(US/KR): 기존처럼 플랜 없으면 자동 생성
	positions, err := d.broker.GetPositions(d.ctx)
	if err != nil {
		log.Printf("[DAEMON] Failed to get positions: %v", err)
	} else {
		log.Printf("[DAEMON] Current positions: %d", len(positions))
		for _, p := range positions {
			log.Printf("  - %s: %.0f shares @ $%.2f (P&L: $%.2f)",
				p.Symbol, p.Quantity, p.AvgCost, p.UnrealizedPnL)

			// PlanStore에서 원래 플랜 복원
			if planStore != nil {
				if plan := planStore.Get(p.Symbol); plan != nil {
					log.Printf("  → Restored plan: strategy=%s, stop=$%.2f, T1=$%.2f, T2=$%.2f, maxDays=%d",
						plan.Strategy, plan.StopLoss, plan.Target1, plan.Target2, plan.MaxHoldDays)
					d.autoTrader.GetMonitor().RegisterPositionWithPlan(
						p.Symbol, p.Quantity, plan.EntryPrice,
						plan.StopLoss, plan.Target1, plan.Target2,
						plan.Strategy, plan.MaxHoldDays, plan.EntryTime,
					)
					continue
				}
			}

			// 크립토: 플랜 없는 기존 포지션은 건드리지 않음 (수동 매수분 보호)
			if d.isCrypto() {
				log.Printf("  → Skipping (no plan, manual position)")
				continue
			}

			// 주식(US/KR): 플랜 없으면 기술 분석 기반으로 플랜 자동 생성
			plan := d.generatePlanFromAnalysis(p.Symbol, p.AvgCost, p.Quantity)
			if plan != nil {
				log.Printf("  → Generated plan: strategy=%s, stop=$%.2f, T1=$%.2f, T2=$%.2f, maxDays=%d",
					plan.Strategy, plan.StopLoss, plan.Target1, plan.Target2, plan.MaxHoldDays)
				d.autoTrader.GetMonitor().RegisterPositionWithPlan(
					p.Symbol, p.Quantity, plan.EntryPrice,
					plan.StopLoss, plan.Target1, plan.Target2,
					plan.Strategy, plan.MaxHoldDays, plan.EntryTime,
				)
				if planStore != nil {
					planStore.Save(plan)
				}
			} else {
				// 캔들 조회 실패 시 최소 fallback
				log.Printf("  → Analysis failed, using fallback 2%%/4%%")
				stopLoss := p.AvgCost * 0.98
				target1 := p.AvgCost * 1.02
				target2 := p.AvgCost * 1.04
				d.autoTrader.GetMonitor().RegisterPosition(
					p.Symbol, p.Quantity, p.AvgCost, stopLoss, target1, target2,
				)
			}
		}
	}

	// 9. 전략 무효화 체크 (전일 데이터 기반, 프리마켓에서 가능)
	d.runInvalidationCheck()

	// 10. 프리마켓 스캔 (전일 일봉 데이터 사용, 장 열기 전에 시그널 준비)
	state := d.tracker.GetState()
	if !d.config.ForceScan && state.TradeCount > 0 {
		log.Printf("[DAEMON] Already traded today (%d trades). Skipping scan.", state.TradeCount)
	} else {
		if d.config.ForceScan {
			log.Printf("[DAEMON] Force scan enabled (existing trades: %d). Running scan...", state.TradeCount)
		}
		scanStart := time.Now()
		scanResult, err := d.adaptiveScan()
		if err != nil {
			log.Printf("[DAEMON] Scan error: %v", err)
		} else {
			scanResult.ScanTime = time.Since(scanStart)
			d.saveScanResultForWeb(scanResult)
			d.preMarketSigs = scanResult.Signals
			log.Printf("[DAEMON] Scan complete: %d signals found in %s",
				len(d.preMarketSigs), scanResult.ScanTime.Round(time.Second))
		}
	}

	// 11. 장 열릴 때까지 대기 (프리마켓이면)
	if !status.IsOpen {
		// 스캔 완료 후 남은 대기 시간 재계산
		remaining := d.getMarketStatus()
		if !remaining.IsOpen && remaining.TimeToOpen > 0 {
			log.Printf("[DAEMON] Scan done. Waiting %s for market open...", FormatDuration(remaining.TimeToOpen))
			select {
			case <-time.After(remaining.TimeToOpen):
				log.Println("[DAEMON] Market should be open now.")
			case <-d.ctx.Done():
				return d.shutdown("cancelled")
			}
		}
	}

	// 12. 장 열림 → 프리마켓 시그널 즉시 실행
	if len(d.preMarketSigs) > 0 {
		log.Printf("[DAEMON] Executing %d pre-scanned signals...", len(d.preMarketSigs))
		results, err := d.autoTrader.ExecuteSignals(d.ctx, d.preMarketSigs)
		if err != nil {
			log.Printf("[DAEMON] Execution error: %v", err)
		} else {
			for _, r := range results {
				if r.Success {
					orderID := ""
					if r.Result != nil {
						orderID = r.Result.OrderID
					}
					investAmount := r.Order.Quantity * r.Order.LimitPrice
					if r.Order.Amount > 0 {
						investAmount = r.Order.Amount // 시장가 매수: KRW 금액
					}
					d.tracker.RecordTrade(TradeLog{
						Symbol:   r.Order.Symbol,
						Side:     string(r.Order.Side),
						Quantity: r.Order.Quantity,
						Price:    r.Order.LimitPrice,
						Amount:   investAmount,
						OrderID:  orderID,
						Reason:   "signal",
					})
					if d.capital != nil {
						d.capital.RecordBuy(investAmount)
					}
					if d.history != nil {
						d.history.Append(trader.TradeRecord{
							Market:   d.config.Market,
							Symbol:   r.Order.Symbol,
							Side:     "buy",
							Quantity: r.Order.Quantity,
							Price:    r.Order.LimitPrice,
							Strategy: r.Signal.Strategy,
							Reason:   "signal",
						})
					}
				}
			}
		}
		d.preMarketSigs = nil
	}

	// 13. P&L 재계산
	d.runMonitorCycle()

	// 14. 모니터 루프
	return d.mainLoop()
}

// mainLoop 메인 거래 루프 (스캔/무효화는 Run()에서 완료, 여기서는 모니터링 + 장중 매매)
func (d *Daemon) mainLoop() error {
	log.Println("[DAEMON] Switching to monitor + intraday mode.")

	monitorTicker := time.NewTicker(d.config.MonitorInterval)
	defer monitorTicker.Stop()

	// 장중 매매 초기화
	d.initIntraday()

	// 장중 스캔 루프 (별도 고루틴)
	intradayDone := make(chan struct{})
	go func() {
		defer close(intradayDone)
		d.runIntradayLoop()
	}()

	for {
		select {
		case <-d.ctx.Done():
			<-intradayDone
			return d.shutdown("cancelled")

		case <-monitorTicker.C:
			// 포지션 모니터링 (30초 간격)
			d.runMonitorCycle()
		}

		// 종료 조건 체크
		if shouldStop, reason := d.checkStopConditions(); shouldStop {
			// 장중 포지션 강제 청산
			if d.autoTrader != nil {
				d.autoTrader.GetMonitor().ForceCloseIntraday(d.ctx)
			}
			<-intradayDone
			return d.shutdown(reason)
		}
	}
}

// runMonitorCycle 모니터링 사이클
func (d *Daemon) runMonitorCycle() {
	// 개별 종목 손절/익절 체크
	if d.autoTrader != nil {
		d.autoTrader.GetMonitor().CheckPositions(d.ctx)
	}

	// 현재 포지션 조회
	positions, err := d.broker.GetPositions(d.ctx)
	if err != nil {
		return
	}

	// P&L 계산
	var unrealizedPnL float64
	for _, p := range positions {
		unrealizedPnL += p.UnrealizedPnL
	}

	// 잔고 조회
	balance, err := d.broker.GetBalance(d.ctx)
	if err != nil {
		return
	}

	// 미체결 주문 조회 (예약금은 손실이 아님)
	pendingOrders, _ := d.broker.GetPendingOrders(d.ctx)
	var pendingValue float64
	for _, order := range pendingOrders {
		if order.Side == broker.OrderSideBuy {
			unfilledQty := order.Quantity - order.FilledQty
			pendingValue += unfilledQty * order.Price
		}
	}

	// 총 자산 = 현금 + 보유 주식 + 미체결 주문 예약금
	totalEquity := balance.TotalEquity + pendingValue

	// 실현 손익 = 총 자산 - 시작 잔고 - 미실현 손익
	state := d.tracker.GetState()
	realizedPnL := totalEquity - state.StartingBalance - unrealizedPnL

	// 트래커 업데이트
	d.tracker.UpdatePnL(realizedPnL, unrealizedPnL, totalEquity)
}

// daemonScanResult 데몬 스캔 결과 (웹 저장용 메타데이터 포함)
type daemonScanResult struct {
	Signals              []strategy.Signal
	ScannedCount         int
	UniversesUsed        []string
	Decision             string
	Expansions           int
	AvgProb              float64
	FundamentalsFiltered int
	ScanTime             time.Duration
}

// adaptiveScan 적응형 멀티 전략 스캔
func (d *Daemon) adaptiveScan() (*daemonScanResult, error) {
	var strategies []strategy.Strategy

	if d.isCrypto() {
		// 크립토: 레짐 인식 메타전략 (Bull→VolatilityBreakout, Sideways→RangeTrading, Bear→skip)
		meta := strategy.NewCryptoMetaStrategy(d.provider)
		strategies = []strategy.Strategy{meta}
	} else {
		// 주식 전략 (US/KR)
		pullbackCfg := strategy.DefaultPullbackConfig()
		if d.isKR() {
			pullbackCfg.MarketRegimeSymbol = "069500" // KODEX 200 (KOSPI 추종 ETF)
		} else {
			pullbackCfg.MarketRegimeSymbol = "SPY"
		}
		pullback := strategy.NewPullbackStrategy(pullbackCfg, d.provider)

		breakoutCfg := strategy.DefaultBreakoutConfig()
		breakout := strategy.NewBreakoutStrategy(breakoutCfg, d.provider)

		meanRevCfg := strategy.DefaultMeanReversionConfig()
		meanRev := strategy.NewMeanReversionStrategy(meanRevCfg, d.provider)

		oversoldCfg := strategy.DefaultOversoldConfig()
		if d.isKR() {
			oversoldCfg.MarketRegimeSymbol = "069500"
		} else {
			oversoldCfg.MarketRegimeSymbol = "SPY"
		}
		oversold := strategy.NewOversoldStrategy(oversoldCfg, d.provider)

		strategies = []strategy.Strategy{pullback, meanRev, breakout, oversold}
	}
	stratNames := make([]string, len(strategies))
	for i, s := range strategies {
		stratNames[i] = s.Name()
	}
	log.Printf("[DAEMON] Multi-strategy scan (%d strategies: %v)", len(strategies), stratNames)

	// 스캔 함수: 종목별로 모든 전략 실행, 가장 강한 신호만 유지
	scanFunc := func(ctx context.Context, stocks []model.Stock) ([]strategy.Signal, error) {
		var signals []strategy.Signal
		total := len(stocks)
		for i, stock := range stocks {
			if (i+1)%20 == 0 || i == total-1 {
				log.Printf("[DAEMON] Scan progress: %d/%d", i+1, total)
			}

			// 모든 전략으로 분석, 가장 강한 신호 유지
			var best *strategy.Signal
			for _, strat := range strategies {
				sig, err := strat.Analyze(ctx, stock)
				if err == nil && sig != nil {
					if best == nil || sig.Strength > best.Strength {
						best = sig
					}
				}
			}
			if best != nil {
				signals = append(signals, *best)
			}
		}
		return signals, nil
	}

	// 적응형 스캐너
	adaptiveCfg := trader.DefaultAdaptiveConfig()
	adaptiveCfg.Verbose = true
	scanner := trader.NewAdaptiveScanner(adaptiveCfg, d.config.Sizer, scanFunc)

	// 마켓별 유니버스 티어
	if d.isCrypto() {
		scanner.SetTierFunc(func(balance float64) []trader.UniverseTier {
			return trader.GetCryptoUniverseTiers(balance)
		})
	} else if d.isKR() {
		scanner.SetTierFunc(func(balance float64) []trader.UniverseTier {
			return trader.GetKRUniverseTiers(balance)
		})
	}

	// 펀더멘탈 필터를 스캐너에 주입 (품질 평가 전에 적용) — 크립토는 사용 안 함
	var fundamentalsFiltered int
	if !d.isCrypto() {
		fundDataDir := d.config.DataDir
		if fundDataDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				fundDataDir = filepath.Join(home, ".traveler")
			}
		}
		if fundDataDir != "" {
			var kosdaqSet map[string]bool
			if d.isKR() {
				kosdaqSet = make(map[string]bool)
				for _, s := range symbols.Kosdaq30Symbols {
					kosdaqSet[s] = true
				}
			}
			checker := provider.NewFundamentalsChecker(fundDataDir, kosdaqSet)
			if err := checker.Init(d.ctx); err != nil {
				log.Printf("[DAEMON] Fundamentals checker init failed (skipping): %v", err)
			} else {
				scanner.SetFilterFunc(func(ctx context.Context, signals []strategy.Signal) []strategy.Signal {
					syms := make([]string, 0, len(signals))
					for _, sig := range signals {
						syms = append(syms, sig.Stock.Symbol)
					}
					rejected := checker.FilterSymbols(ctx, syms)
					if len(rejected) == 0 {
						return signals
					}
					var filtered []strategy.Signal
					for _, sig := range signals {
						if _, rej := rejected[sig.Stock.Symbol]; !rej {
							filtered = append(filtered, sig)
						}
					}
					fundamentalsFiltered += len(signals) - len(filtered)
					log.Printf("[DAEMON] Fundamentals filter: %d → %d signals", len(signals), len(filtered))
					return filtered
				})
			}
		}
	}

	// 스캔 실행
	loader := &daemonStockLoader{provider: d.provider, korean: d.isKR(), crypto: d.isCrypto()}
	result, err := scanner.Scan(d.ctx, loader)
	if err != nil {
		return nil, err
	}

	// 포지션 사이징 적용
	sizer := trader.NewPositionSizer(d.config.Sizer)
	sized := sizer.ApplyToSignals(result.Signals)

	return &daemonScanResult{
		Signals:              sized,
		ScannedCount:         result.ScannedCount,
		UniversesUsed:        result.UniversesUsed,
		Decision:             result.Decision,
		Expansions:           result.Expansions,
		AvgProb:              result.Quality.AvgProb,
		FundamentalsFiltered: fundamentalsFiltered,
	}, nil
}

// checkStopConditions 종료 조건 체크
func (d *Daemon) checkStopConditions() (bool, string) {
	// 크립토는 마켓 종료 없음 — 일일 한도만 체크
	if !d.isCrypto() {
		status := d.getMarketStatus()
		if !status.IsOpen && status.Reason == "after-hours" {
			return true, "market_closed"
		}
	}

	// 일일 목표/한도
	check := d.tracker.CheckTargets()
	if check.ShouldStop {
		return true, check.Reason
	}

	return false, ""
}

// shutdown 종료 처리
func (d *Daemon) shutdown(reason string) error {
	log.Printf("[DAEMON] Shutting down. Reason: %s", reason)

	// 날짜 보장
	d.tracker.EnsureDate()

	// 상태 저장
	d.tracker.SetStatus(reason)

	// 리포트 생성
	reportPath, err := d.tracker.SaveReport()
	if err != nil {
		log.Printf("[DAEMON] Failed to save report: %v", err)
	} else {
		log.Printf("[DAEMON] Report saved: %s", reportPath)
	}

	// 리포트 출력
	fmt.Println(d.tracker.GenerateReport())

	// PC 절전
	// wake timer는 영구 예약 작업(TravelerDaemon, TravelerDaemonKR)이 관리함
	// setup-daemon.ps1에서 WakeToRun 설정 완료 → 여기서 별도 등록 불필요
	// 크립토는 24/7이므로 절전 안 함
	if d.config.SleepOnExit && !d.isCrypto() {
		// 어떤 시장이든 장중이면 절전하지 않음
		usOpen := IsMarketOpen()
		krOpen := GetKRMarketStatus(KRMarketSchedule()).IsOpen
		if usOpen || krOpen {
			openMarket := "US"
			if krOpen {
				openMarket = "KR"
			}
			log.Printf("[DAEMON] %s market still open. Skipping sleep.", openMarket)
		} else {
			runtime := time.Since(d.startedAt)
			idle := getUserIdleSeconds()
			if runtime < 5*time.Minute {
				// 데몬이 5분 미만 실행: 절전 해제 직후이므로 idle 타이머 신뢰 불가
				// (Windows resume 시 GetLastInputInfo가 리셋됨)
				// 장중이 아닌 이상 PC를 켜둘 이유 없음 → 바로 절전
				log.Printf("[DAEMON] Short run (%s), idle unreliable (%ds). Entering sleep mode...",
					FormatDuration(runtime), idle)
				time.Sleep(3 * time.Second)
				sleepPC()
			} else if idle < 300 { // 5분 이내 활동 있으면 사용 중
				log.Printf("[DAEMON] User active (idle %ds < 300s). Skipping sleep.", idle)
			} else {
				log.Printf("[DAEMON] User idle %ds. Entering sleep mode...", idle)
				time.Sleep(3 * time.Second)
				sleepPC()
			}
		}
	}

	return nil
}

// Stop 데몬 중지
func (d *Daemon) Stop() {
	log.Println("[DAEMON] Stop requested...")
	d.cancel()
}


// saveScanResultForWeb 데몬 스캔 결과를 웹 UI에서 읽을 수 있는 JSON으로 저장
func (d *Daemon) saveScanResultForWeb(sr *daemonScanResult) {
	dataDir := d.config.DataDir
	if dataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dataDir = filepath.Join(home, ".traveler")
		} else {
			return
		}
	}

	// 웹 ScanResponse와 동일한 JSON 구조
	type signalWithChart struct {
		strategy.Signal
		Candles      []model.Candle            `json:"candles,omitempty"`
		Fundamentals *provider.FundamentalsData `json:"fundamentals,omitempty"`
	}

	// 펀더멘탈 데이터 로드 (캐시에서) — 크립토는 사용 안 함
	var fundChecker *provider.FundamentalsChecker
	if !d.isCrypto() {
		var kosdaqSet map[string]bool
		if d.isKR() {
			kosdaqSet = make(map[string]bool)
			for _, s := range symbols.Kosdaq30Symbols {
				kosdaqSet[s] = true
			}
		}
		fundChecker = provider.NewFundamentalsChecker(dataDir, kosdaqSet)
		if err := fundChecker.Init(context.Background()); err != nil {
			log.Printf("[DAEMON] Fundamentals checker init for web save: %v", err)
			fundChecker = nil
		}
	}

	sigs := make([]signalWithChart, len(sr.Signals))
	var totalInvest, totalRisk float64
	for i, sig := range sr.Signals {
		// 차트 데이터 로드 (최근 100일)
		candles, _ := d.provider.GetDailyCandles(d.ctx, sig.Stock.Symbol, 100)
		sigs[i] = signalWithChart{Signal: sig, Candles: candles}
		// 펀더멘탈 데이터 첨부
		if fundChecker != nil {
			if fd, err := fundChecker.Check(context.Background(), sig.Stock.Symbol); err == nil {
				sigs[i].Fundamentals = fd
			}
		}
		if sig.Guide != nil {
			totalInvest += sig.Guide.InvestAmount
			totalRisk += sig.Guide.RiskAmount
		}
	}

	strategyName := "multi"
	if d.isCrypto() {
		strategyName = "multi-crypto"
	} else if d.isKR() {
		strategyName = "multi-kr"
	}

	resp := map[string]interface{}{
		"strategy":              strategyName,
		"total_scanned":         sr.ScannedCount,
		"signals_found":         len(sigs),
		"signals":               sigs,
		"scan_time":             sr.ScanTime.Round(time.Second).String(),
		"capital":               d.config.Sizer.TotalCapital,
		"total_invest":          totalInvest,
		"total_risk":            totalRisk,
		"universes_used":        sr.UniversesUsed,
		"decision":              sr.Decision,
		"expansions":            sr.Expansions,
		"avg_prob":              sr.AvgProb,
		"fundamentals_filtered": sr.FundamentalsFiltered,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("[DAEMON] Failed to marshal scan result: %v", err)
		return
	}

	market := "us"
	if d.isCrypto() {
		market = "crypto"
	} else if d.isKR() {
		market = "kr"
	}
	path := filepath.Join(dataDir, fmt.Sprintf("last_scan_%s.json", market))
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("[DAEMON] Failed to save scan result: %v", err)
	} else {
		log.Printf("[DAEMON] Scan result saved to %s (%d signals)", filepath.Base(path), len(sigs))
	}
}

// daemonStockLoader StockLoader 구현
type daemonStockLoader struct {
	provider provider.Provider
	korean   bool
	crypto   bool
}

func (l *daemonStockLoader) LoadUniverse(ctx context.Context, u symbols.Universe) ([]model.Stock, error) {
	syms := symbols.GetUniverse(u)
	if syms == nil {
		return nil, fmt.Errorf("unknown universe: %s", u)
	}

	stocks := make([]model.Stock, len(syms))
	for i, sym := range syms {
		name := sym
		if l.korean {
			name = symbols.GetKRSymbolName(sym)
		} else if l.crypto {
			name = symbols.GetCryptoSymbolName(sym)
		}
		stocks[i] = model.Stock{Symbol: sym, Name: name}
	}
	return stocks, nil
}

// ========================================
// 장중 매매 (Intraday Trading)
// ========================================

// initIntraday 장중 매매 초기화
func (d *Daemon) initIntraday() {
	cfg := strategy.DefaultIntradayConfig()

	// 잔고 조회
	balance, err := d.broker.GetBalance(d.ctx)
	if err != nil {
		log.Printf("[INTRADAY] Failed to get balance, skipping intraday: %v", err)
		return
	}
	capital := balance.TotalEquity
	if d.config.TradingCapital > 0 {
		capital = d.config.TradingCapital
	}

	d.intradayScanner = strategy.NewIntradayScanner(cfg, capital)

	// 장중 스캔 대상 종목 선정: 자본으로 살 수 있는 저가 종목
	d.intradaySymbols = d.selectIntradayUniverse(capital)

	if len(d.intradaySymbols) == 0 {
		log.Println("[INTRADAY] No affordable symbols for intraday trading")
		return
	}

	log.Printf("[INTRADAY] Initialized: %d symbols, capital=%.2f", len(d.intradaySymbols), capital)
}

// selectIntradayUniverse 장중 매매 대상 종목 선정
func (d *Daemon) selectIntradayUniverse(capital float64) []string {
	// 최대 매수 가능 가격 (자본의 50% 이하인 종목)
	maxPrice := capital * 0.5

	// 유니버스 로드
	var allSymbols []string
	if d.isCrypto() {
		allSymbols = append(allSymbols, symbols.GetUniverse(symbols.UniverseCryptoTop30)...)
	} else if d.isKR() {
		for _, u := range []symbols.Universe{symbols.UniverseKospi30, symbols.UniverseKosdaq30} {
			allSymbols = append(allSymbols, symbols.GetUniverse(u)...)
		}
	} else {
		// US: 가격 필터링 필요 — 상위 100종목에서 저가만
		for _, u := range []symbols.Universe{symbols.UniverseNasdaq100, symbols.UniverseSP500} {
			allSymbols = append(allSymbols, symbols.GetUniverse(u)...)
		}
	}

	// 중복 제거
	seen := make(map[string]bool)
	var unique []string
	for _, s := range allSymbols {
		if !seen[s] {
			seen[s] = true
			unique = append(unique, s)
		}
	}

	// 가격 필터링 (현재가 조회)
	var affordable []string
	for _, sym := range unique {
		price, err := d.broker.GetQuote(d.ctx, sym)
		if err != nil || price <= 0 {
			continue
		}
		if price <= maxPrice {
			affordable = append(affordable, sym)
		}

		// 최대 50종목
		if len(affordable) >= 50 {
			break
		}

		// API 호출 간격
		time.Sleep(100 * time.Millisecond)
	}

	return affordable
}

// runIntradayLoop 장중 매매 루프
func (d *Daemon) runIntradayLoop() {
	if d.intradayScanner == nil || len(d.intradaySymbols) == 0 {
		log.Println("[INTRADAY] Scanner not initialized, skipping intraday loop")
		return
	}

	// 1단계: 종목 초기화 (전일 종가 설정)
	log.Printf("[INTRADAY] Initializing %d symbols with previous close...", len(d.intradaySymbols))
	for _, sym := range d.intradaySymbols {
		price, err := d.broker.GetQuote(d.ctx, sym)
		if err != nil || price <= 0 {
			continue
		}
		d.intradayScanner.InitSymbol(sym, price)
	}

	// 2단계: OR 수집 (장 시작 후 30분간 매 1분마다 가격 수집)
	log.Println("[INTRADAY] Starting Opening Range collection...")
	d.collectOpeningRange()

	// OR 완료
	d.intradayScanner.FinalizeOR()
	orbSymbols := d.intradayScanner.GetORBSymbols()
	log.Printf("[INTRADAY] OR collection done. %d symbols with valid ORB range", len(orbSymbols))

	// 3단계: 장중 스캔 루프 (5분 간격)
	scanInterval := strategy.DefaultIntradayConfig().ScanInterval
	forceCloseMin := strategy.DefaultIntradayConfig().ForceCloseMin

	log.Printf("[INTRADAY] Starting scan loop (interval=%s, force_close=%dmin before close)", scanInterval, forceCloseMin)

	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			// 장 마감 임박 체크
			status := d.getMarketStatus()
			if !status.IsOpen {
				log.Println("[INTRADAY] Market closed, stopping intraday loop")
				return
			}

			// 강제 청산 시간 체크 (마감 N분 전) — 크립토는 24시간이므로 해당 없음
			if !d.isCrypto() && status.TimeToClose <= time.Duration(forceCloseMin)*time.Minute {
				log.Printf("[INTRADAY] %s before close, force closing intraday positions",
					FormatDuration(status.TimeToClose))
				if d.autoTrader != nil {
					d.autoTrader.GetMonitor().ForceCloseIntraday(d.ctx)
				}
				return
			}

			// 일일 손실 한도 체크
			if d.intradayScanner.IsLossLimitHit() {
				log.Println("[INTRADAY] Daily loss limit hit, stopping intraday scan")
				return
			}

			// 가격 업데이트 + 스캔
			d.updateIntradayPrices()
			d.executeIntradaySignals()
		}
	}
}

// collectOpeningRange OR 수집 (첫 30분)
func (d *Daemon) collectOpeningRange() {
	cfg := strategy.DefaultIntradayConfig()
	endTime := time.Now().Add(time.Duration(cfg.ORBCollectMin) * time.Minute)
	pollInterval := 1 * time.Minute

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	polls := 0
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			if now.After(endTime) {
				return
			}

			// 모든 종목 가격 조회
			for _, sym := range d.intradaySymbols {
				price, err := d.broker.GetQuote(d.ctx, sym)
				if err != nil || price <= 0 {
					continue
				}
				d.intradayScanner.RecordPrice(sym, price, now)
				time.Sleep(150 * time.Millisecond) // API 간격 (429 방지)
			}
			polls++
			log.Printf("[INTRADAY] OR poll %d/%d complete (%d symbols)",
				polls, cfg.ORBCollectMin, d.intradayScanner.SymbolCount())
		}
	}
}

// updateIntradayPrices 장중 가격 업데이트
func (d *Daemon) updateIntradayPrices() {
	now := time.Now()
	for _, sym := range d.intradaySymbols {
		price, err := d.broker.GetQuote(d.ctx, sym)
		if err != nil || price <= 0 {
			continue
		}
		d.intradayScanner.RecordPrice(sym, price, now)
		time.Sleep(150 * time.Millisecond)
	}
}

// executeIntradaySignals 장중 시그널 실행
func (d *Daemon) executeIntradaySignals() {
	signals := d.intradayScanner.Scan()
	if len(signals) == 0 {
		return
	}

	log.Printf("[INTRADAY] Found %d intraday signals", len(signals))

	// 포지션 사이징 적용
	sizer := trader.NewPositionSizer(d.config.Sizer)
	sized := sizer.ApplyToSignals(signals)

	if len(sized) == 0 {
		// 디버깅: 탈락 원인 로그
		for _, sig := range signals {
			result := sizer.CalculateSize(&sig)
			if result.Skipped {
				rr := 0.0
				price := 0.0
				if sig.Guide != nil {
					rr = sig.Guide.RiskRewardRatio
					price = sig.Guide.EntryPrice
				}
				log.Printf("[INTRADAY] %s skipped: %s (price=%.0f, R/R=%.2f)",
					sig.Stock.Symbol, result.SkipReason, price, rr)
			}
		}
		return
	}

	// AutoTrader로 실행
	results, err := d.autoTrader.ExecuteSignals(d.ctx, sized)
	if err != nil {
		log.Printf("[INTRADAY] Execution error: %v", err)
		return
	}

	for _, r := range results {
		if r.Success {
			sym := r.Order.Symbol
			investAmount := r.Order.Quantity * r.Order.LimitPrice
			if r.Order.Amount > 0 {
				investAmount = r.Order.Amount // 시장가 매수: KRW 금액
			}
			log.Printf("[INTRADAY] Executed %s: BUY ₩%.0f", sym, investAmount)

			// 장중 포지션 표시
			if d.autoTrader != nil {
				monitor := d.autoTrader.GetMonitor()
				// Monitor에 Intraday 플래그 설정
				for _, pos := range monitor.GetActivePositions() {
					if pos.Symbol == sym && (pos.Strategy == "intraday_orb" || pos.Strategy == "intraday_dip") {
						pos.Intraday = true
					}
				}
			}

			// 중복 진입 방지
			d.intradayScanner.MarkExecuted(sym, r.Signal.Strategy)

			// 자본 추적
			if d.capital != nil {
				d.capital.RecordBuy(investAmount)
			}

			// 매매 기록
			if d.history != nil {
				d.history.Append(trader.TradeRecord{
					Market:   d.config.Market,
					Symbol:   sym,
					Side:     "buy",
					Quantity: r.Order.Quantity,
					Price:    r.Order.LimitPrice,
					Strategy: r.Signal.Strategy,
					Reason:   "intraday_signal",
				})
			}
		}
	}
}

// generatePlanFromAnalysis 기존 보유 종목에 대해 기술 분석 기반 플랜 자동 생성
func (d *Daemon) generatePlanFromAnalysis(symbol string, avgCost float64, quantity float64) *trader.PositionPlan {
	candles, err := d.provider.GetDailyCandles(d.ctx, symbol, 50)
	if err != nil || len(candles) < 20 {
		log.Printf("[DAEMON] Cannot generate plan for %s: insufficient candle data", symbol)
		return nil
	}

	ind := strategy.CalculateIndicators(candles)
	lastClose := candles[len(candles)-1].Close

	// 전략 추정: 현재 기술적 상태를 기반으로 가장 적합한 전략 배정
	strategyName := inferStrategy(lastClose, avgCost, ind)

	// R 기반 손절/익절 계산
	var stopLoss, target1, target2 float64

	switch strategyName {
	case "mean-reversion":
		// 손절: 최근 저점 아래 or 진입가 -3%
		recentLow := candles[len(candles)-1].Low
		for i := len(candles) - 5; i < len(candles); i++ {
			if i >= 0 && candles[i].Low < recentLow {
				recentLow = candles[i].Low
			}
		}
		stopLoss = recentLow * 0.99
		if stopLoss > avgCost*0.97 {
			stopLoss = avgCost * 0.97
		}
		// 타겟: MA20(평균 회귀), BB상단
		target1 = ind.MA20
		target2 = ind.BBUpper
		if target1 <= avgCost {
			target1 = avgCost * 1.02
		}
		if target2 <= target1 {
			target2 = avgCost * 1.04
		}

	case "breakout":
		// 손절: 돌파 레벨 아래 or 진입가 -3%
		breakoutLevel := strategy.CalculateHighestHigh(candles, 20)
		stopLoss = breakoutLevel * 0.97
		if stopLoss > avgCost*0.97 {
			stopLoss = avgCost * 0.97
		}
		riskPerShare := avgCost - stopLoss
		target1 = avgCost + riskPerShare*1.5
		target2 = avgCost + riskPerShare*3.0

	default: // pullback
		// 손절: MA20 아래 or 진입가 -3%
		stopLoss = ind.MA20 * 0.98
		if stopLoss > avgCost*0.97 {
			stopLoss = avgCost * 0.97
		}
		riskPerShare := avgCost - stopLoss
		if riskPerShare <= 0 {
			riskPerShare = avgCost * 0.02
		}
		target1 = avgCost + riskPerShare*1.5
		target2 = avgCost + riskPerShare*2.5
	}

	maxDays := trader.GetMaxHoldDays(strategyName)

	plan := &trader.PositionPlan{
		Symbol:      symbol,
		Strategy:    strategyName,
		EntryPrice:  avgCost,
		Quantity:    quantity,
		StopLoss:    stopLoss,
		Target1:     target1,
		Target2:     target2,
		Target1Hit:  false,
		EntryTime:   time.Now(), // 기존 종목은 진입 시점 불명 → 지금부터 카운트
		MaxHoldDays: maxDays,
	}

	// Breakout: 돌파 레벨 저장
	if strategyName == "breakout" {
		plan.BreakoutLevel = strategy.CalculateHighestHigh(candles, 20)
	}

	return plan
}

// inferStrategy 현재 기술적 상태로 전략 추정
func inferStrategy(lastClose, avgCost float64, ind *strategy.Indicators) string {
	// RSI < 40이고 BB 하단 근처 → mean-reversion
	if ind.RSI14 < 40 && ind.BBLower > 0 && lastClose < ind.MA20 {
		return "mean-reversion"
	}

	// 20일 고점 근처이고 MA50 위 → breakout
	if ind.MA50 > 0 && lastClose > ind.MA50 && lastClose > ind.MA20*1.02 {
		return "breakout"
	}

	// 기본: pullback
	return "pullback"
}
