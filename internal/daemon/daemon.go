package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
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
	WaitForMarket    bool          // 마켓 열릴 때까지 대기
	MaxWaitTime      time.Duration // 최대 대기 시간

	// 거래 설정
	Daily            DailyConfig
	Sizer            trader.SizerConfig

	// 스캔 설정
	ScanInterval     time.Duration // 스캔 주기
	MonitorInterval  time.Duration // 모니터링 주기

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

	ctx        context.Context
	cancel     context.CancelFunc
	isRunning  bool
}

// NewDaemon 생성자
func NewDaemon(cfg Config, b broker.Broker, p provider.Provider) *Daemon {
	ctx, cancel := context.WithCancel(context.Background())

	// Sizer config는 나중에 잔고 확인 후 설정
	return &Daemon{
		config:   cfg,
		broker:   b,
		provider: p,
		tracker:  NewDailyTracker(cfg.Daily, cfg.DataDir),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Run 데몬 실행
func (d *Daemon) Run() error {
	log.Println("[DAEMON] Starting automated trading daemon...")

	// 화면 켜기 (절전 해제 후 모니터가 꺼져있을 수 있음)
	wakeMonitor()

	d.isRunning = true
	defer func() {
		d.isRunning = false
	}()

	// 1. 마켓 상태 확인
	status := GetMarketStatus(DefaultMarketSchedule())
	log.Printf("[DAEMON] Market status: %s (ET: %s)", status.Reason, status.CurrentTimeET.Format("15:04"))

	if !status.IsOpen {
		if d.config.WaitForMarket {
			log.Printf("[DAEMON] Market closed. Waiting %s for market open...", FormatDuration(status.TimeToOpen))

			if status.TimeToOpen > d.config.MaxWaitTime {
				log.Printf("[DAEMON] Wait time too long (> %s). Exiting.", FormatDuration(d.config.MaxWaitTime))
				return d.shutdown("wait_too_long")
			}

			// 대기
			select {
			case <-time.After(status.TimeToOpen):
				log.Println("[DAEMON] Market should be open now.")
			case <-d.ctx.Done():
				return d.shutdown("cancelled")
			}
		} else {
			log.Println("[DAEMON] Market closed and WaitForMarket=false. Exiting.")
			return d.shutdown("market_closed")
		}
	}

	// 2. 계좌 잔고 확인
	balance, err := d.broker.GetBalance(d.ctx)
	if err != nil {
		log.Printf("[DAEMON] Failed to get balance: %v", err)
		return d.shutdown("balance_error")
	}
	log.Printf("[DAEMON] Account balance: $%.2f", balance.TotalEquity)

	// 3. 일일 트래커 시작
	if err := d.tracker.Start(balance.TotalEquity); err != nil {
		log.Printf("[DAEMON] Failed to start tracker: %v", err)
	}

	// 4. Sizer 설정 (잔고 기반)
	d.config.Sizer = trader.AdjustConfigForBalance(balance.TotalEquity)

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

	// 6. AutoTrader 생성 (PlanStore 포함)
	traderCfg := trader.Config{
		DryRun:          false, // 실전 모드
		MaxPositions:    d.config.Sizer.MaxPositions,
		MaxPositionPct:  d.config.Sizer.MaxPositionPct,
		TotalCapital:    balance.TotalEquity,
		RiskPerTrade:    d.config.Sizer.RiskPerTrade,
		MonitorInterval: d.config.MonitorInterval,
	}
	d.autoTrader = trader.NewAutoTraderWithPlanStore(traderCfg, d.broker, false, planStore)

	// 7. 기존 포지션 확인 및 모니터 등록 (PlanStore에서 원래 플랜 복원)
	positions, err := d.broker.GetPositions(d.ctx)
	if err != nil {
		log.Printf("[DAEMON] Failed to get positions: %v", err)
	} else {
		log.Printf("[DAEMON] Current positions: %d", len(positions))
		for _, p := range positions {
			log.Printf("  - %s: %d shares @ $%.2f (P&L: $%.2f)",
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

			// Fallback: 플랜이 없으면 기술 분석 기반으로 플랜 자동 생성
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

	// 8. P&L 재계산 (재시작 시 미체결 주문 반영)
	d.runMonitorCycle()

	// 9. 메인 루프
	return d.mainLoop()
}

// mainLoop 메인 거래 루프
func (d *Daemon) mainLoop() error {
	log.Println("[DAEMON] Entering main trading loop...")

	monitorTicker := time.NewTicker(d.config.MonitorInterval)
	defer monitorTicker.Stop()

	// 장 시작 시 기존 포지션 전략 무효화 체크 (전일 기준)
	d.runInvalidationCheck()

	// 멀티 전략 스캔 1회 (일봉 기반이라 장중 재스캔 불필요)
	d.runScanCycle()
	log.Println("[DAEMON] Initial scan complete. Switching to monitor-only mode.")

	for {
		select {
		case <-d.ctx.Done():
			return d.shutdown("cancelled")

		case <-monitorTicker.C:
			// 포지션 모니터링만 (30초 간격)
			d.runMonitorCycle()
		}

		// 종료 조건 체크
		if shouldStop, reason := d.checkStopConditions(); shouldStop {
			return d.shutdown(reason)
		}
	}
}

// runScanCycle 스캔 사이클
func (d *Daemon) runScanCycle() {
	log.Println("[DAEMON] Running scan cycle...")

	// 마켓 상태 재확인
	status := GetMarketStatus(DefaultMarketSchedule())
	if !status.IsOpen {
		log.Printf("[DAEMON] Market closed during scan cycle. Status: %s", status.Reason)
		return
	}

	// 적응형 스캔
	signals, err := d.adaptiveScan()
	if err != nil {
		log.Printf("[DAEMON] Scan error: %v", err)
		return
	}

	if len(signals) == 0 {
		log.Println("[DAEMON] No trading signals found.")
		return
	}

	log.Printf("[DAEMON] Found %d signals", len(signals))

	// 매매 실행
	results, err := d.autoTrader.ExecuteSignals(d.ctx, signals)
	if err != nil {
		log.Printf("[DAEMON] Execution error: %v", err)
		return
	}

	// 거래 기록
	for _, r := range results {
		if r.Success {
			orderID := ""
			if r.Result != nil {
				orderID = r.Result.OrderID
			}
			d.tracker.RecordTrade(TradeLog{
				Symbol:   r.Order.Symbol,
				Side:     string(r.Order.Side),
				Quantity: r.Order.Quantity,
				Price:    r.Order.LimitPrice,
				Amount:   float64(r.Order.Quantity) * r.Order.LimitPrice,
				OrderID:  orderID,
				Reason:   "signal",
			})
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
			pendingValue += float64(unfilledQty) * order.Price
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

// adaptiveScan 적응형 멀티 전략 스캔
func (d *Daemon) adaptiveScan() ([]strategy.Signal, error) {
	// 모든 전략 가져오기
	strategies := strategy.GetAll(d.provider)
	log.Printf("[DAEMON] Multi-strategy scan (%d strategies: %v)", len(strategies), strategy.List())

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

	// 스캔 실행
	loader := &daemonStockLoader{provider: d.provider}
	result, err := scanner.Scan(d.ctx, loader)
	if err != nil {
		return nil, err
	}

	// 포지션 사이징 적용
	sizer := trader.NewPositionSizer(d.config.Sizer)
	return sizer.ApplyToSignals(result.Signals), nil
}

// checkStopConditions 종료 조건 체크
func (d *Daemon) checkStopConditions() (bool, string) {
	// 마켓 상태
	status := GetMarketStatus(DefaultMarketSchedule())
	if !status.IsOpen && status.Reason == "after-hours" {
		return true, "market_closed"
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
	if d.config.SleepOnExit {
		// 다음 시장 오픈 시간에 wake timer 등록
		status := GetMarketStatus(DefaultMarketSchedule())
		if status.TimeToOpen > 0 {
			wakeTime := time.Now().Add(status.TimeToOpen).Add(-10 * time.Minute) // 10분 전에 깨우기
			if err := registerWakeTimer(wakeTime); err != nil {
				log.Printf("[DAEMON] Failed to register wake timer: %v", err)
			} else {
				log.Printf("[DAEMON] Wake timer set for %s", wakeTime.Format("2006-01-02 15:04:05"))
			}
		}

		log.Println("[DAEMON] Entering sleep mode...")
		time.Sleep(3 * time.Second) // 로그 출력 대기
		sleepPC()
	}

	return nil
}

// Stop 데몬 중지
func (d *Daemon) Stop() {
	log.Println("[DAEMON] Stop requested...")
	d.cancel()
}

// wakeMonitor 모니터 켜기
func wakeMonitor() {
	if runtime.GOOS != "windows" {
		return
	}

	// Windows API로 모니터 켜기
	user32 := syscall.NewLazyDLL("user32.dll")
	sendMessage := user32.NewProc("SendMessageW")

	// SC_MONITORPOWER = 0xF170, -1 = on
	sendMessage.Call(
		0xFFFF,             // HWND_BROADCAST
		0x0112,             // WM_SYSCOMMAND
		0xF170,             // SC_MONITORPOWER
		uintptr(0xFFFFFFFF), // -1 = monitor on
	)

	log.Println("[DAEMON] Monitor wake signal sent")
}

// sleepPC PC 절전 모드
func sleepPC() {
	switch runtime.GOOS {
	case "windows":
		// Windows 절전 모드
		cmd := exec.Command("rundll32.exe", "powrprof.dll,SetSuspendState", "0", "1", "0")
		if err := cmd.Run(); err != nil {
			log.Printf("[DAEMON] Failed to sleep PC: %v", err)
		}
	case "linux":
		// Linux 절전
		cmd := exec.Command("systemctl", "suspend")
		cmd.Run()
	case "darwin":
		// macOS 절전
		cmd := exec.Command("pmset", "sleepnow")
		cmd.Run()
	}
}

// registerWakeTimer 다음 실행을 위한 wake timer 시간 업데이트
func registerWakeTimer(wakeTime time.Time) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("wake timer only supported on Windows")
	}

	// schtasks /change로 기존 TravelerDaemon task의 시간만 변경
	// 초기 설정은 setup-daemon.ps1로 관리자가 한 번만 실행
	timeStr := wakeTime.Format("15:04")

	cmd := exec.Command("schtasks", "/change", "/tn", "TravelerDaemon", "/st", timeStr)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to update wake timer: %v, output: %s", err, string(output))
	}

	log.Printf("[DAEMON] Wake timer updated: TravelerDaemon at %s", timeStr)
	return nil
}

// daemonStockLoader StockLoader 구현
type daemonStockLoader struct {
	provider provider.Provider
}

func (l *daemonStockLoader) LoadUniverse(ctx context.Context, u symbols.Universe) ([]model.Stock, error) {
	syms := symbols.GetUniverse(u)
	if syms == nil {
		return nil, fmt.Errorf("unknown universe: %s", u)
	}

	stocks := make([]model.Stock, len(syms))
	for i, sym := range syms {
		stocks[i] = model.Stock{Symbol: sym, Name: sym}
	}
	return stocks, nil
}

// generatePlanFromAnalysis 기존 보유 종목에 대해 기술 분석 기반 플랜 자동 생성
func (d *Daemon) generatePlanFromAnalysis(symbol string, avgCost float64, quantity int) *trader.PositionPlan {
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
