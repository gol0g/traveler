package daemon

import (
	"context"
	"fmt"
	"log"
	"os/exec"
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

	// 5. AutoTrader 생성
	traderCfg := trader.Config{
		DryRun:          false, // 실전 모드
		MaxPositions:    d.config.Sizer.MaxPositions,
		MaxPositionPct:  d.config.Sizer.MaxPositionPct,
		TotalCapital:    balance.TotalEquity,
		RiskPerTrade:    d.config.Sizer.RiskPerTrade,
		MonitorInterval: d.config.MonitorInterval,
	}
	d.autoTrader = trader.NewAutoTrader(traderCfg, d.broker, false)

	// 6. 기존 포지션 확인
	positions, err := d.broker.GetPositions(d.ctx)
	if err != nil {
		log.Printf("[DAEMON] Failed to get positions: %v", err)
	} else {
		log.Printf("[DAEMON] Current positions: %d", len(positions))
		for _, p := range positions {
			log.Printf("  - %s: %d shares @ $%.2f (P&L: $%.2f)",
				p.Symbol, p.Quantity, p.AvgCost, p.UnrealizedPnL)
		}
	}

	// 7. P&L 재계산 (재시작 시 미체결 주문 반영)
	d.runMonitorCycle()

	// 8. 메인 루프
	return d.mainLoop()
}

// mainLoop 메인 거래 루프
func (d *Daemon) mainLoop() error {
	log.Println("[DAEMON] Entering main trading loop...")

	scanTicker := time.NewTicker(d.config.ScanInterval)
	monitorTicker := time.NewTicker(d.config.MonitorInterval)
	defer scanTicker.Stop()
	defer monitorTicker.Stop()

	// 초기 스캔
	d.runScanCycle()

	for {
		select {
		case <-d.ctx.Done():
			return d.shutdown("cancelled")

		case <-scanTicker.C:
			// 정기 스캔
			d.runScanCycle()

		case <-monitorTicker.C:
			// 포지션 모니터링
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
	pendingOrders, pendingErr := d.broker.GetPendingOrders(d.ctx)
	if pendingErr != nil {
		log.Printf("[DAEMON] GetPendingOrders error: %v", pendingErr)
	}
	var pendingValue float64
	for _, order := range pendingOrders {
		log.Printf("[DAEMON] Pending order: %s %s x%d @ $%.2f (filled: %d)",
			order.Side, order.Symbol, order.Quantity, order.Price, order.FilledQty)
		if order.Side == broker.OrderSideBuy {
			// 미체결 수량 * 주문가 = 예약 금액
			unfilledQty := order.Quantity - order.FilledQty
			pendingValue += float64(unfilledQty) * order.Price
		}
	}
	if pendingValue > 0 {
		log.Printf("[DAEMON] Total pending order value: $%.2f", pendingValue)
	}

	// 총 자산 = 현금 + 보유 주식 + 미체결 주문 예약금
	totalEquity := balance.TotalEquity + pendingValue

	// 실현 손익 = 총 자산 - 시작 잔고 - 미실현 손익
	state := d.tracker.GetState()
	realizedPnL := totalEquity - state.StartingBalance - unrealizedPnL

	// 트래커 업데이트
	d.tracker.UpdatePnL(realizedPnL, unrealizedPnL, totalEquity)

	// 손절/익절 체크 (autoTrader.monitor가 처리)
}

// adaptiveScan 적응형 스캔
func (d *Daemon) adaptiveScan() ([]strategy.Signal, error) {
	// 전략 가져오기
	strat, err := strategy.Get("pullback", d.provider)
	if err != nil {
		return nil, err
	}

	// 스캔 함수
	scanFunc := func(ctx context.Context, stocks []model.Stock) ([]strategy.Signal, error) {
		var signals []strategy.Signal
		for _, stock := range stocks {
			sig, err := strat.Analyze(ctx, stock)
			if err == nil && sig != nil {
				signals = append(signals, *sig)
			}
		}
		return signals, nil
	}

	// 적응형 스캐너
	adaptiveCfg := trader.DefaultAdaptiveConfig()
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
