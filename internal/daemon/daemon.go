package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

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
	Market           string        // "us" or "kr"
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

	ctx        context.Context
	cancel     context.CancelFunc
	isRunning  bool
}

// NewDaemon 생성자
func NewDaemon(cfg Config, b broker.Broker, p provider.Provider) *Daemon {
	ctx, cancel := context.WithCancel(context.Background())

	// Sizer config는 나중에 잔고 확인 후 설정
	tracker := NewDailyTracker(cfg.Daily, cfg.DataDir)

	// 마켓 타임존 설정 (US=ET, KR=KST)
	if cfg.Market == "kr" {
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

// getMarketStatus 현재 시장에 맞는 마켓 상태 조회
func (d *Daemon) getMarketStatus() MarketStatus {
	if d.isKR() {
		return GetKRMarketStatus(KRMarketSchedule())
	}
	return GetMarketStatus(DefaultMarketSchedule())
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
	status := d.getMarketStatus()
	tzLabel := "ET"
	if d.isKR() {
		tzLabel = "KST"
	}
	log.Printf("[DAEMON] Market status: %s (%s: %s)", status.Reason, tzLabel, status.CurrentTimeET.Format("15:04"))

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
	if d.isKR() {
		log.Printf("[DAEMON] Account balance: ₩%.0f", balance.TotalEquity)
	} else {
		log.Printf("[DAEMON] Account balance: $%.2f", balance.TotalEquity)
	}

	// 3. 일일 트래커 시작
	if err := d.tracker.Start(balance.TotalEquity); err != nil {
		log.Printf("[DAEMON] Failed to start tracker: %v", err)
	}

	// 4. Sizer 설정 (잔고 기반)
	if d.isKR() {
		d.config.Sizer = trader.AdjustConfigForKRBalance(balance.TotalEquity)
	} else {
		d.config.Sizer = trader.AdjustConfigForBalance(balance.TotalEquity)
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
		TotalCapital:    balance.TotalEquity,
		RiskPerTrade:    d.config.Sizer.RiskPerTrade,
		MonitorInterval: d.config.MonitorInterval,
	}
	d.autoTrader = trader.NewAutoTraderWithPlanStore(traderCfg, d.broker, false, planStore)

	// Monitor에 TradeHistory 연결
	if d.history != nil {
		d.autoTrader.GetMonitor().SetTradeHistory(d.history, d.config.Market)
	}

	// 8. 기존 포지션 확인 및 모니터 등록 (PlanStore에서 원래 플랜 복원)
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

	// 9. P&L 재계산 (재시작 시 미체결 주문 반영)
	d.runMonitorCycle()

	// 10. 메인 루프
	return d.mainLoop()
}

// mainLoop 메인 거래 루프
func (d *Daemon) mainLoop() error {
	log.Println("[DAEMON] Entering main trading loop...")

	monitorTicker := time.NewTicker(d.config.MonitorInterval)
	defer monitorTicker.Stop()

	// 장 시작 시 기존 포지션 전략 무효화 체크 (전일 기준)
	d.runInvalidationCheck()

	// 오늘 이미 스캔+매매했으면 스캔 생략 (재시작 시 중복 매수 방지)
	// ForceScan이면 무조건 1회 스캔 실행
	state := d.tracker.GetState()
	if d.config.ForceScan {
		log.Printf("[DAEMON] Force scan enabled (existing trades: %d). Running scan...", state.TradeCount)
		d.runScanCycle()
		log.Println("[DAEMON] Force scan complete.")
	} else if state.TradeCount > 0 {
		log.Printf("[DAEMON] Already traded today (%d trades). Skipping scan.", state.TradeCount)
	} else {
		d.runScanCycle()
		log.Println("[DAEMON] Initial scan complete.")
	}
	log.Println("[DAEMON] Switching to monitor-only mode.")

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
	status := d.getMarketStatus()
	if !status.IsOpen {
		log.Printf("[DAEMON] Market closed during scan cycle. Status: %s", status.Reason)
		return
	}

	// 적응형 스캔
	scanStart := time.Now()
	scanResult, err := d.adaptiveScan()
	if err != nil {
		log.Printf("[DAEMON] Scan error: %v", err)
		return
	}
	scanResult.ScanTime = time.Since(scanStart)

	// 스캔 결과를 웹 UI용 JSON으로 저장
	d.saveScanResultForWeb(scanResult)

	signals := scanResult.Signals
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

			// TradeHistory에도 매수 기록
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

	// KR 모드: 한국 유니버스 티어 사용
	if d.isKR() {
		scanner.SetTierFunc(func(balance float64) []trader.UniverseTier {
			return trader.GetKRUniverseTiers(balance)
		})
	}

	// 스캔 실행
	loader := &daemonStockLoader{provider: d.provider, korean: d.isKR()}
	result, err := scanner.Scan(d.ctx, loader)
	if err != nil {
		return nil, err
	}

	// 펀더멘털 필터
	var fundamentalsFiltered int
	if len(result.Signals) > 0 {
		dataDir := d.config.DataDir
		if dataDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				dataDir = filepath.Join(home, ".traveler")
			}
		}
		if dataDir != "" {
			var kosdaqSet map[string]bool
			if d.isKR() {
				kosdaqSet = make(map[string]bool)
				for _, s := range symbols.Kosdaq30Symbols {
					kosdaqSet[s] = true
				}
			}
			checker := provider.NewFundamentalsChecker(dataDir, kosdaqSet)
			if err := checker.Init(d.ctx); err != nil {
				log.Printf("[DAEMON] Fundamentals checker init failed (skipping): %v", err)
			} else {
				syms := make([]string, 0, len(result.Signals))
				for _, sig := range result.Signals {
					syms = append(syms, sig.Stock.Symbol)
				}
				rejected := checker.FilterSymbols(d.ctx, syms)
				if len(rejected) > 0 {
					before := len(result.Signals)
					var filtered []strategy.Signal
					for _, sig := range result.Signals {
						if _, rej := rejected[sig.Stock.Symbol]; !rej {
							filtered = append(filtered, sig)
						}
					}
					result.Signals = filtered
					fundamentalsFiltered = before - len(filtered)
					log.Printf("[DAEMON] Fundamentals filter: %d → %d signals", before, len(result.Signals))
				}
			}
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
	}, nil
}

// checkStopConditions 종료 조건 체크
func (d *Daemon) checkStopConditions() (bool, string) {
	// 마켓 상태
	status := d.getMarketStatus()
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
	// wake timer는 영구 예약 작업(TravelerDaemon, TravelerDaemonKR)이 관리함
	// setup-daemon.ps1에서 WakeToRun 설정 완료 → 여기서 별도 등록 불필요
	if d.config.SleepOnExit {
		idle := getUserIdleSeconds()
		if idle < 300 { // 5분 이내 활동 있으면 사용 중
			log.Printf("[DAEMON] User active (idle %ds < 300s). Skipping sleep.", idle)
		} else {
			log.Printf("[DAEMON] User idle %ds. Entering sleep mode...", idle)
			time.Sleep(3 * time.Second)
			sleepPC()
		}
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

// sleepPC PC 절전 모드 (wake timer를 존중하는 방식)
func sleepPC() {
	switch runtime.GOOS {
	case "windows":
		// PowerShell SetSuspendState: force=false, disableWakeEvent=false
		// → 예약 작업의 WakeToRun이 정상 작동함
		cmd := exec.Command("powershell", "-Command",
			"Add-Type -Assembly System.Windows.Forms; [System.Windows.Forms.Application]::SetSuspendState('Suspend', $false, $false)")
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

// getUserIdleSeconds 사용자 마지막 입력 이후 경과 시간 (초)
func getUserIdleSeconds() int {
	if runtime.GOOS != "windows" {
		return 9999 // 비 Windows: 항상 유휴 상태로 간주
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getLastInputInfo := user32.NewProc("GetLastInputInfo")
	getTickCount := kernel32.NewProc("GetTickCount")

	// LASTINPUTINFO: cbSize(4) + dwTime(4) = 8 bytes
	type lastInputInfo struct {
		cbSize uint32
		dwTime uint32
	}
	var info lastInputInfo
	info.cbSize = 8
	ret, _, _ := getLastInputInfo.Call(uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return 9999 // 실패 시 유휴로 간주
	}
	tick, _, _ := getTickCount.Call()
	idleMs := uint32(tick) - info.dwTime
	return int(idleMs / 1000)
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
		Candles []model.Candle `json:"candles,omitempty"`
	}

	sigs := make([]signalWithChart, len(sr.Signals))
	var totalInvest, totalRisk float64
	for i, sig := range sr.Signals {
		// 차트 데이터 로드 (최근 100일)
		candles, _ := d.provider.GetDailyCandles(d.ctx, sig.Stock.Symbol, 100)
		sigs[i] = signalWithChart{Signal: sig, Candles: candles}
		if sig.Guide != nil {
			totalInvest += sig.Guide.InvestAmount
			totalRisk += sig.Guide.RiskAmount
		}
	}

	strategyName := "multi"
	if d.isKR() {
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
	if d.isKR() {
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
		}
		stocks[i] = model.Stock{Symbol: sym, Name: name}
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
