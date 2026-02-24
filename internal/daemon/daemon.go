package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"traveler/internal/ai"
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

	// AI signal filter
	aiClient *ai.GeminiClient

	windDown chan struct{} // signal to stop intraday loop on shutdown

	// Monitor-only mode: KR low-balance → monitor existing positions, no new scans
	monitorOnly bool
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

// SetAIClient sets the Gemini AI client for signal filtering
func (d *Daemon) SetAIClient(c *ai.GeminiClient) {
	d.aiClient = c
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

	// KR 소액 계좌: KR DCA 데몬이 KODEX 200을 처리
	// → 기존 포지션이 없으면 즉시 종료, 있으면 모니터링만 하고 신규 매수 안 함
	if d.isKR() && balance.TotalEquity < 500000 {
		log.Printf("[DAEMON] KR balance ₩%.0f < ₩500,000 — KR DCA handles KODEX 200", balance.TotalEquity)
		// plans.json에 기존 KR 포지션이 있는지 확인
		checkDir := d.config.DataDir
		if checkDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				checkDir = filepath.Join(home, ".traveler")
			}
		}
		hasKRPositions := false
		if ps, perr := trader.NewPlanStore(checkDir); perr == nil {
			for _, plan := range ps.GetAll() {
				// KR 심볼: 6자리 숫자 (005930, 069500 등)
				if len(plan.Symbol) == 6 && plan.Symbol[0] >= '0' && plan.Symbol[0] <= '9' {
					hasKRPositions = true
					log.Printf("[DAEMON] Open KR position: %s (strategy=%s)", plan.Symbol, plan.Strategy)
				}
			}
		}
		if hasKRPositions {
			log.Printf("[DAEMON] Monitor-only mode: watching existing positions, no new scans")
			d.monitorOnly = true
		} else {
			log.Printf("[DAEMON] No open KR positions. Exiting cleanly.")
			return nil
		}
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
		capState := d.capital.GetState()
		tradingCapital = capState.CurrentCapital + capState.TotalInvested
		log.Printf("[DAEMON] Trading capital: ₩%.0f (cash=₩%.0f, invested=₩%.0f, account=₩%.0f)",
			tradingCapital, capState.CurrentCapital, capState.TotalInvested, balance.TotalEquity)
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
					mon := d.autoTrader.GetMonitor()
					mon.RegisterPositionWithPlan(
						p.Symbol, p.Quantity, plan.EntryPrice,
						plan.StopLoss, plan.Target1, plan.Target2,
						plan.Strategy, plan.MaxHoldDays, plan.EntryTime,
					)
					// Restore trailing stop state
					if plan.UseTrailingStop {
						mon.SetTrailingStop(p.Symbol, true, plan.TrailingATR, plan.TrailingMultiplier)
						if plan.HighestSinceT1 > 0 {
							// Also restore HighestSinceT1 for T1-already-hit positions
							mon.SetHighestSinceT1(p.Symbol, plan.HighestSinceT1)
						}
					}
					// Restore T1 hit state
					if plan.Target1Hit {
						mon.SetTarget1Hit(p.Symbol, true)
					}
					// Restore Intraday flag for force close
					if plan.Strategy == "intraday_orb" || plan.Strategy == "intraday_dip" {
						for _, pos := range mon.GetActivePositions() {
							if pos.Symbol == p.Symbol {
								pos.Intraday = true
							}
						}
					}
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

	// 8.5. 기존 포지션 타겟 재계산 (구조적 레벨 기반)
	d.recalculateTargets(planStore)

	// 9. 전략 무효화 체크 (전일 데이터 기반, 프리마켓에서 가능)
	d.runInvalidationCheck()

	// 10. 프리마켓 스캔 (전일 일봉 데이터 사용, 장 열기 전에 시그널 준비)
	if d.monitorOnly {
		log.Printf("[DAEMON] Monitor-only mode: skipping scan, will only watch existing positions")
	} else {
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
	} // end !monitorOnly

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
					// 실제 체결가 사용 (있으면)
					actualPrice := r.Order.LimitPrice
					if r.Result != nil && r.Result.AvgPrice > 0 {
						actualPrice = r.Result.AvgPrice
					}
					investAmount := r.Order.Quantity * actualPrice
					if r.Order.Amount > 0 {
						investAmount = r.Order.Amount // 시장가 매수: KRW 금액
					}
					d.tracker.RecordTrade(TradeLog{
						Symbol:   r.Order.Symbol,
						Side:     string(r.Order.Side),
						Quantity: r.Order.Quantity,
						Price:    actualPrice,
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
							Price:    actualPrice,
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

	// 장중 매매 초기화 (monitor-only 모드에서는 건너뜀)
	d.windDown = make(chan struct{})
	if !d.monitorOnly {
		d.initIntraday()
	}

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
		shouldStop, reason := d.checkStopConditions()
		if !shouldStop {
			continue
		}

		// Hard stop: 장 마감 또는 기타 사유
		if d.autoTrader != nil {
			d.autoTrader.GetMonitor().ForceCloseIntraday(d.ctx)
		}
		<-intradayDone
		return d.shutdown(reason)
	}
}

// runMonitorCycle 모니터링 사이클
func (d *Daemon) runMonitorCycle() {
	// 개별 종목 손절/익절 체크
	if d.autoTrader != nil {
		d.autoTrader.GetMonitor().CheckPositions(d.ctx)
	}

	// P&L 계산: CapitalTracker 모드 vs 전체 계좌 모드
	if d.capital != nil {
		// CapitalTracker 모드: 데몬이 관리하는 포지션만 PnL 추적
		// (수동 매수한 BTC/ETH 등은 제외)
		daemonSymbols := make(map[string]bool)
		if d.autoTrader != nil {
			for _, pos := range d.autoTrader.GetMonitor().GetActivePositions() {
				daemonSymbols[pos.Symbol] = true
			}
		}

		positions, err := d.broker.GetPositions(d.ctx)
		if err != nil {
			return
		}

		var unrealizedPnL float64
		for _, p := range positions {
			if daemonSymbols[p.Symbol] {
				unrealizedPnL += p.UnrealizedPnL
			}
		}

		capState := d.capital.GetState()
		daemonEquity := capState.CurrentCapital + capState.TotalInvested + unrealizedPnL
		d.tracker.UpdatePnL(capState.RealizedPnL, unrealizedPnL, daemonEquity)
		return
	}

	// 전체 계좌 모드 (US/KR 주식)
	// GetBalance()는 positions + buying power를 동시에 반환 — 한 번만 호출
	balance, err := d.broker.GetBalance(d.ctx)
	if err != nil {
		return
	}

	var unrealizedPnL float64
	for _, p := range balance.Positions {
		unrealizedPnL += p.UnrealizedPnL
	}

	// TotalEquity = 보유 포지션 평가액 + BuyingPower(가용 현금)
	// 주의: pendingValue를 더하지 않음. KIS API의 BuyingPower는 미체결 주문
	// 예약금을 차감한 값이지만, 주문 직후에는 API 업데이트 지연으로 차감이
	// 안 됐을 수 있음. pendingValue를 더하면 이중 계산 → 허위 PnL 발생
	// (Bug #009: SMCI $30.43 주문 → 19.5% 허위 PnL → 22초 만에 강제청산)
	totalEquity := balance.TotalEquity
	state := d.tracker.GetState()
	realizedPnL := totalEquity - state.StartingBalance - unrealizedPnL
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
	AIFiltered           int
	AIRejections         []ai.AIRejection
	ScanTime             time.Duration

	// Market regime info
	Regime           string
	ActiveStrategies []string
	BenchmarkPrice   float64
	BenchmarkMA20    float64
	BenchmarkMA50    float64
	BenchmarkRSI     float64
}

// adaptiveScan 적응형 멀티 전략 스캔
func (d *Daemon) adaptiveScan() (*daemonScanResult, error) {
	var strategies []strategy.Strategy
	var regimeInfo strategy.RegimeInfo
	var activeStrats []string

	// Capital tier 결정
	tradingCap := d.config.Sizer.TotalCapital
	capitalTier := strategy.GetCapitalTier(d.config.Market, tradingCap)
	log.Printf("[DAEMON] Capital tier: %s (capital=%.0f, market=%s)", capitalTier, tradingCap, d.config.Market)

	if d.isCrypto() {
		// 크립토: 레짐 인식 메타전략 — capital tier에 따라 BTC-only 또는 full
		meta := strategy.NewCryptoMetaStrategy(d.provider, tradingCap)
		strategies = []strategy.Strategy{meta}
		regimeInfo = meta.GetRegimeInfo(d.ctx)
		switch regimeInfo.Regime {
		case strategy.RegimeBull:
			activeStrats = []string{"volatility-breakout"}
		case strategy.RegimeSideways:
			activeStrats = []string{"range-trading"}
		case strategy.RegimeBear:
			activeStrats = []string{"(none)"}
		}
		if capitalTier == "btc-only" {
			activeStrats = []string{"crypto-trend"}
		}
	} else {
		// 주식 (US/KR): 레짐 인식 메타전략 — capital tier에 따라 ETF 또는 개별주
		metaCfg := strategy.DefaultStockMetaConfig(d.config.Market, tradingCap)
		meta := strategy.NewStockMetaStrategy(metaCfg, d.provider)
		strategies = []strategy.Strategy{meta}
		regimeInfo = meta.GetRegimeInfo(d.ctx)
		activeStrats = meta.GetActiveStrategyNames(d.ctx)
		log.Printf("[DAEMON] Stock meta strategy: %s (bull=%v, sideways=%v, bear=%v)",
			metaCfg.Name, metaCfg.Bull, metaCfg.Sideways, metaCfg.Bear)
	}
	log.Printf("[DAEMON] Regime: %s (benchmark=%s, price=%.2f, MA20=%.2f, RSI=%.1f), active=%v",
		regimeInfo.Regime, regimeInfo.Symbol, regimeInfo.Price, regimeInfo.MA20, regimeInfo.RSI14, activeStrats)
	stratNames := make([]string, len(strategies))
	for i, s := range strategies {
		stratNames[i] = s.Name()
	}
	log.Printf("[DAEMON] Multi-strategy scan (%d strategies: %v)", len(strategies), stratNames)

	// 스캔 함수: 메타전략이 레짐 감지 + 전략 선택 + 시그널 선택을 모두 처리
	scanFunc := func(ctx context.Context, stocks []model.Stock) ([]strategy.Signal, error) {
		var signals []strategy.Signal
		total := len(stocks)
		for i, stock := range stocks {
			if (i+1)%20 == 0 || i == total-1 {
				log.Printf("[DAEMON] Scan progress: %d/%d", i+1, total)
			}

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

	// 마켓별 유니버스 티어 — capital tier에 따라 ETF 또는 기존 유니버스
	if capitalTier == "etf" || capitalTier == "btc-only" {
		if d.isCrypto() {
			// BTC-only: crypto-top10에서 BTC만 스캔
			scanner.SetTierFunc(func(balance float64) []trader.UniverseTier {
				return trader.GetCryptoUniverseTiers(balance)
			})
		} else if d.isKR() {
			scanner.SetTierFunc(func(balance float64) []trader.UniverseTier {
				return trader.GetKRETFTiers(balance)
			})
		} else {
			scanner.SetTierFunc(func(balance float64) []trader.UniverseTier {
				return trader.GetUSETFTiers(balance)
			})
		}
	} else if d.isCrypto() {
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

	// AI signal filter + SL/TP optimization (after fundamentals, before sizing)
	var aiFiltered int
	var aiRejections []ai.AIRejection
	if d.aiClient != nil && len(result.Signals) > 0 {
		before := len(result.Signals)
		regime := string(regimeInfo.Regime)
		result.Signals, aiRejections = d.aiClient.FilterSignals(d.ctx, result.Signals, regime, d.config.Market)
		aiFiltered = before - len(result.Signals)

		if len(result.Signals) > 0 {
			result.Signals = d.aiClient.OptimizeGuides(d.ctx, result.Signals, regime, d.config.Market)
		}
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
		AIFiltered:           aiFiltered,
		AIRejections:         aiRejections,
		Regime:           string(regimeInfo.Regime),
		ActiveStrategies: activeStrats,
		BenchmarkPrice:   regimeInfo.Price,
		BenchmarkMA20:    regimeInfo.MA20,
		BenchmarkMA50:    regimeInfo.MA50,
		BenchmarkRSI:     regimeInfo.RSI14,
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
		"ai_filtered":          sr.AIFiltered,
		"ai_rejections":        sr.AIRejections,
		"regime":               sr.Regime,
		"active_strategies":     sr.ActiveStrategies,
		"benchmark_price":       sr.BenchmarkPrice,
		"benchmark_ma20":        sr.BenchmarkMA20,
		"benchmark_ma50":        sr.BenchmarkMA50,
		"benchmark_rsi":         sr.BenchmarkRSI,
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
		// US: nasdaq100 + SP500 + midcap (자본이 크면 더 많은 종목 커버)
		for _, u := range []symbols.Universe{symbols.UniverseNasdaq100, symbols.UniverseSP500, symbols.UniverseMidCap} {
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

	// 자본에 따라 종목 수 조정
	maxSymbols := 50
	if capital >= 10000 {
		maxSymbols = 80
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

		if len(affordable) >= maxSymbols {
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

	// 크립토: 하락장이면 인트라데이 ORB 비활성화 (상방 돌파 전략은 bear에서 역효과)
	if d.isCrypto() {
		rd := strategy.NewRegimeDetector(d.provider)
		regime := rd.Detect(d.ctx)
		if regime == strategy.RegimeBear {
			log.Println("[INTRADAY] Regime=bear, skipping intraday ORB (upside breakout ineffective in bear market)")
			return
		}
		log.Printf("[INTRADAY] Regime=%s, proceeding with intraday ORB", regime)
	}

	// 1단계: 종목 초기화 (전일 OHLC 설정 — 구조적 타겟용)
	log.Printf("[INTRADAY] Initializing %d symbols with previous close...", len(d.intradaySymbols))
	for _, sym := range d.intradaySymbols {
		candles, err := d.provider.GetDailyCandles(d.ctx, sym, 2)
		if err != nil || len(candles) == 0 {
			continue
		}
		prev := candles[len(candles)-1]
		d.intradayScanner.InitSymbol(sym, prev.Close, prev.High, prev.Low)
	}

	// 2단계: OR 수집 (ORB가 활성화된 경우만)
	cfg := strategy.DefaultIntradayConfig()
	if cfg.ORBEnabled {
		log.Println("[INTRADAY] Starting Opening Range collection...")
		d.collectOpeningRange()
		d.intradayScanner.FinalizeOR()
		orbSymbols := d.intradayScanner.GetORBSymbols()
		log.Printf("[INTRADAY] OR collection done. %d symbols with valid ORB range", len(orbSymbols))
	} else {
		log.Println("[INTRADAY] ORB disabled, skipping OR collection")
	}

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
		case <-d.windDown:
			log.Println("[INTRADAY] Wind-down signal received, stopping new orders")
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
			// 실제 체결가 사용 (있으면)
			actualPrice := r.Order.LimitPrice
			if r.Result != nil && r.Result.AvgPrice > 0 {
				actualPrice = r.Result.AvgPrice
			}
			investAmount := r.Order.Quantity * actualPrice
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
					Price:    actualPrice,
					Strategy: r.Signal.Strategy,
					Reason:   "intraday_signal",
				})
			}
		}
	}
}

// recalculateTargets 기존 포지션의 T1/T2를 구조적 레벨로 재계산
func (d *Daemon) recalculateTargets(planStore *trader.PlanStore) {
	if planStore == nil {
		return
	}
	plans := planStore.GetAll()
	if len(plans) == 0 {
		return
	}

	log.Printf("[DAEMON] Recalculating targets for %d positions...", len(plans))
	mon := d.autoTrader.GetMonitor()

	for _, plan := range plans {
		if plan.Target1Hit {
			continue // T1 이미 도달한 포지션은 건드리지 않음
		}

		candles, err := d.provider.GetDailyCandles(d.ctx, plan.Symbol, 70)
		if err != nil || len(candles) < 20 {
			log.Printf("[RECALC] %s: insufficient data, keeping old targets", plan.Symbol)
			continue
		}

		ind := strategy.CalculateIndicators(candles)
		oldT1, oldT2 := plan.Target1, plan.Target2
		entry := plan.EntryPrice

		switch {
		case plan.Strategy == "mean-reversion" || plan.Strategy == "rsi-contrarian" || plan.Strategy == "range-trading":
			// 이미 구조적 (MA20, BB상단) — skip
			continue

		case plan.Strategy == "pullback" || plan.Strategy == "oversold":
			riskPerShare := entry - plan.StopLoss
			if riskPerShare <= 0 {
				riskPerShare = entry * 0.02
			}

			// 구조적 손절: 스윙로우 기반 업데이트
			structuralStop := strategy.FindNearestSupport(candles, entry, 30, 2)
			if structuralStop > 0 {
				newStop := structuralStop - ind.ATR14*0.25
				if newStop > plan.StopLoss && newStop < entry*0.99 {
					plan.StopLoss = newStop
					riskPerShare = entry - newStop
				}
			}

			// T1: 가장 가까운 로컬 저항 (최소 1R)
			nearestRes := strategy.FindNearestResistance(candles, entry*1.005, 40, 2)
			if nearestRes > 0 && (nearestRes-entry) >= riskPerShare {
				plan.Target1 = nearestRes
			} else {
				swingHigh, _ := strategy.FindSwingHigh(candles, 20)
				if swingHigh > entry && (swingHigh-entry) >= riskPerShare {
					plan.Target1 = swingHigh
				} else {
					plan.Target1 = entry + riskPerShare*1.5
				}
			}

			// T2: 피보나치 1.272 확장
			swingLow, _ := strategy.FindSwingLow(candles, 20)
			plan.Target2 = 0
			if swingLow > 0 && plan.Target1 > swingLow {
				plan.Target2 = strategy.FibonacciExtension(swingLow, plan.Target1, 0.272)
			}
			if plan.Target2 <= plan.Target1 || plan.Target2 <= 0 {
				plan.Target2 = entry + ind.ATR14*3.0
			}
			if plan.Target2 <= plan.Target1 {
				plan.Target2 = entry + riskPerShare*2.5
			}

		case plan.Strategy == "breakout" || plan.Strategy == "volume-spike":
			riskPerShare := entry - plan.StopLoss
			if riskPerShare <= 0 {
				riskPerShare = entry * 0.02
			}
			breakoutLevel := plan.BreakoutLevel
			if breakoutLevel <= 0 {
				breakoutLevel = strategy.CalculateHighestHigh(candles, 20)
			}

			// 구조적 손절
			structuralStop := strategy.FindNearestSupport(candles, breakoutLevel, 30, 2)
			if structuralStop > 0 {
				newStop := structuralStop - ind.ATR14*0.25
				if newStop > plan.StopLoss && newStop < entry*0.99 {
					plan.StopLoss = newStop
					riskPerShare = entry - newStop
				}
			}

			// T1: 측정이동 50% 또는 가장 가까운 저항
			consolidationLow := strategy.CalculateLowestLow(candles, 20)
			measuredMove := 0.0
			if consolidationLow > 0 && breakoutLevel > consolidationLow {
				measuredMove = breakoutLevel - consolidationLow
			}
			plan.Target1 = 0
			if measuredMove > 0 {
				plan.Target1 = breakoutLevel + measuredMove*0.5
			}
			nearestRes := strategy.FindNearestResistance(candles, entry*1.005, 60, 2)
			if nearestRes > 0 && (nearestRes-entry) >= riskPerShare*1.5 {
				if plan.Target1 <= 0 || nearestRes < plan.Target1 {
					plan.Target1 = nearestRes
				}
			}
			if plan.Target1 <= 0 || (plan.Target1-entry) < riskPerShare*1.5 {
				plan.Target1 = entry + riskPerShare*1.5
			}

			// T2: 측정이동 100% 또는 피보나치 1.618
			plan.Target2 = 0
			if measuredMove > 0 {
				plan.Target2 = breakoutLevel + measuredMove
			}
			if plan.Target2 <= plan.Target1 && consolidationLow > 0 && breakoutLevel > consolidationLow {
				plan.Target2 = strategy.FibonacciExtension(consolidationLow, breakoutLevel, 0.618)
			}
			if plan.Target2 <= plan.Target1 || plan.Target2 <= 0 {
				plan.Target2 = entry + ind.ATR14*3.0
			}
			if plan.Target2 <= plan.Target1 {
				plan.Target2 = entry + riskPerShare*3.0
			}

		case plan.Strategy == "intraday_orb" || plan.Strategy == "intraday_dip":
			// ORB/DipBuy: 전일 고점 기반
			if len(candles) >= 1 {
				prev := candles[len(candles)-1]
				riskPerShare := entry - plan.StopLoss
				if riskPerShare <= 0 {
					continue
				}
				rangeWidth := riskPerShare * 2 // OR range ≈ 2× risk (stop = OR mid)

				rangeT1 := entry + rangeWidth
				plan.Target1 = rangeT1
				if prev.High > entry && prev.High < rangeT1 {
					plan.Target1 = prev.High
				}
				// 구조적 T1이 수수료를 못 커버하면 재계산 건너뜀 (기존 타겟 유지)
				if (plan.Target1-entry)/entry < 0.01 {
					plan.Target1 = oldT1
					plan.Target2 = oldT2
					continue
				}

				rangeT2 := entry + rangeWidth*1.5
				plan.Target2 = rangeT2
				if prev.High > plan.Target1 {
					plan.Target2 = prev.High
				}
				if plan.Target2 <= plan.Target1 {
					plan.Target2 = rangeT2
				}
			}

		default:
			continue
		}

		if plan.Target1 != oldT1 || plan.Target2 != oldT2 {
			planStore.Save(plan)
			mon.UpdateTargets(plan.Symbol, plan.Target1, plan.Target2)
			log.Printf("[RECALC] %s (%s): T1 $%.2f→$%.2f, T2 $%.2f→$%.2f",
				plan.Symbol, plan.Strategy, oldT1, plan.Target1, oldT2, plan.Target2)
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
		// Measured move T1, Fib 1.618 T2
		swingLow, _ := strategy.FindSwingLow(candles, 20)
		if swingLow > 0 && breakoutLevel > swingLow {
			target1 = breakoutLevel + (breakoutLevel - swingLow)
			target2 = strategy.FibonacciExtension(swingLow, breakoutLevel, 0.618)
		}
		if target1 <= 0 || (target1-avgCost) < riskPerShare*0.5 {
			target1 = avgCost + riskPerShare*1.5
		}
		if target2 <= target1 {
			target2 = avgCost + riskPerShare*3.0
		}

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
		// Swing high T1, Fib 1.272 T2
		swingHigh, _ := strategy.FindSwingHigh(candles, 20)
		swingLow, _ := strategy.FindSwingLow(candles, 20)
		target1 = swingHigh
		if target1 <= 0 || (target1-avgCost) < riskPerShare*0.5 {
			target1 = avgCost + riskPerShare*1.5
		}
		if swingLow > 0 && swingHigh > swingLow {
			target2 = strategy.FibonacciExtension(swingLow, swingHigh, 0.272)
		}
		if target2 <= target1 {
			target2 = avgCost + riskPerShare*2.5
		}
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
