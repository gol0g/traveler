package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"traveler/internal/broker"
	binanceBroker "traveler/internal/broker/binance"
	"traveler/internal/notify"
	"traveler/internal/strategy"
)

// BinanceScalpTradeRecord records one completed short scalp trade.
type BinanceScalpTradeRecord struct {
	Symbol     string    `json:"symbol"`
	EntryPrice float64   `json:"entry_price"`
	ExitPrice  float64   `json:"exit_price"`
	Quantity   float64   `json:"quantity"`
	AmountUSDT float64   `json:"amount_usdt"`
	Leverage   int       `json:"leverage"`
	NetPnL     float64   `json:"net_pnl"`
	PnLPct     float64   `json:"pnl_pct"`
	ExitReason string    `json:"exit_reason"`
	EntryTime  time.Time `json:"entry_time"`
	ExitTime   time.Time `json:"exit_time"`
}

// BinanceScalpState persists Binance short scalping state across restarts.
type BinanceScalpState struct {
	ActivePositions map[string]*strategy.ShortScalpPosition `json:"active_positions"`
	DailyStats      BinanceScalpDailyStats                  `json:"daily_stats"`
	TotalStats      BinanceScalpTotalStats                  `json:"total_stats"`
	BarCounter      int                                     `json:"bar_counter"`
	LastScanTime    time.Time                               `json:"last_scan_time"`
	RecentTrades    []BinanceScalpTradeRecord                `json:"recent_trades"`
	FundingEarned   float64                                 `json:"funding_earned"` // Total funding fees received (USDT)
	EarnDeposited   float64                                 `json:"earn_deposited"` // Total deposited to Flexible Earn
	EarnInterest    float64                                 `json:"earn_interest"`  // Total interest earned from Flexible
	LastEarnCheck   time.Time                               `json:"last_earn_check"`
}

// BinanceScalpDailyStats tracks today's performance.
type BinanceScalpDailyStats struct {
	Date       string  `json:"date"`
	Trades     int     `json:"trades"`
	Wins       int     `json:"wins"`
	Losses     int     `json:"losses"`
	GrossPnL   float64 `json:"gross_pnl"`
	NetPnL     float64 `json:"net_pnl"`
	Commission float64 `json:"commission"`
}

// BinanceScalpTotalStats tracks lifetime performance.
type BinanceScalpTotalStats struct {
	TotalTrades     int     `json:"total_trades"`
	TotalWins       int     `json:"total_wins"`
	TotalLosses     int     `json:"total_losses"`
	TotalGrossPnL   float64 `json:"total_gross_pnl"`
	TotalNetPnL     float64 `json:"total_net_pnl"`
	TotalCommission float64 `json:"total_commission"`
	BestTrade       float64 `json:"best_trade"`
	WorstTrade      float64 `json:"worst_trade"`
	StartDate       string  `json:"start_date"`
	WinStreakMax    int     `json:"win_streak_max"`
	LoseStreakMax   int     `json:"lose_streak_max"`
	currentWinStreak  int
	currentLoseStreak int
}

// BinanceScalpDaemon runs the Binance Futures short scalping strategy.
type BinanceScalpDaemon struct {
	scalper *strategy.ShortScalper
	config  strategy.ShortScalpConfig
	broker  *binanceBroker.Client
	dataDir string

	state   *BinanceScalpState
	stateMu sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc

	dailyLossLimit float64 // USDT, e.g. -20

	// Simple Earn Flexible: idle capital management
	earnProductID string  // USDT Flexible product ID
	earnAPY       float64 // current APY for logging

	// Notifications
	notifier *notify.TelegramNotifier
}

// NewBinanceScalpDaemon creates a new Binance short scalping daemon.
func NewBinanceScalpDaemon(cfg strategy.ShortScalpConfig, b *binanceBroker.Client, p strategy.ScalpProvider, dataDir string) *BinanceScalpDaemon {
	ctx, cancel := context.WithCancel(context.Background())
	return &BinanceScalpDaemon{
		scalper:        strategy.NewShortScalper(cfg, p),
		config:         cfg,
		broker:         b,
		dataDir:        dataDir,
		ctx:            ctx,
		cancel:         cancel,
		dailyLossLimit: -20.0, // $20 daily loss limit
		notifier:       notify.NewTelegramNotifier(),
	}
}

// Run starts the Binance short scalping daemon main loop.
func (d *BinanceScalpDaemon) Run() error {
	log.Println("[BSCALP] Starting Binance Futures short scalping daemon...")
	log.Printf("[BSCALP] Strategy: RSI(%d) overbought mean-reversion SHORT on %d-min candles",
		d.config.RSIPeriod, d.config.CandleInterval)
	log.Printf("[BSCALP] Entry: RSI>%.0f, Vol>%.1fx, EMA%d filter (price below)",
		d.config.RSIEntry, d.config.VolumeMin, d.config.EMAPeriod)
	log.Printf("[BSCALP] Exit: TP=+%.1f%%, SL=-%.1f%%, RSI<%.0f, MaxBars=%d",
		d.config.TakeProfitPct, d.config.StopLossPct, d.config.RSIExit, d.config.MaxHoldBars)
	log.Printf("[BSCALP] Order: $%.0f/trade, %dx leverage, max %d positions, %d pairs",
		d.config.OrderAmountUSDT, d.config.Leverage, d.config.MaxPositions, len(d.config.Pairs))

	// Initialize broker (exchange info + leverage)
	if err := d.broker.Init(d.ctx, d.config.Pairs); err != nil {
		return fmt.Errorf("broker init: %w", err)
	}

	// Check balance
	bal, err := d.broker.GetBalance(d.ctx)
	if err != nil {
		log.Printf("[BSCALP] Warning: could not get balance: %v", err)
	} else {
		log.Printf("[BSCALP] Futures balance: $%.2f (available: $%.2f)", bal.TotalEquity, bal.CashBalance)
	}

	// Load or init state
	d.loadState()
	d.checkDayRollover()
	d.saveState()
	d.saveStatusJSON()

	// Initialize Simple Earn Flexible (non-blocking: log warning on failure)
	d.initEarn()

	// Two loops:
	// 1. Fast loop (1 min): monitor active positions for SL/TP exit
	// 2. Slow loop (15 min aligned): scan for new short entries
	monitorTicker := time.NewTicker(1 * time.Minute)
	defer monitorTicker.Stop()

	// Align scan to candle interval boundaries
	scanInterval := time.Duration(d.config.CandleInterval) * time.Minute
	nextScan := d.nextAlignedTime(scanInterval)
	log.Printf("[BSCALP] Next scan at %s (in %s)", nextScan.Format("15:04:05"), time.Until(nextScan).Round(time.Second))

	for {
		select {
		case <-d.ctx.Done():
			log.Println("[BSCALP] Daemon stopped by signal")
			d.saveState()
			return nil

		case <-monitorTicker.C:
			d.checkDayRollover()

			// Monitor active short positions
			d.monitorPositions()

			// Manage idle capital → Flexible Earn (every hour)
			d.manageEarn()

			// Check if it's time for a scan
			if time.Now().After(nextScan) {
				d.stateMu.Lock()
				d.state.BarCounter++
				d.stateMu.Unlock()

				// Check daily loss limit
				d.stateMu.RLock()
				dailyPnL := d.state.DailyStats.NetPnL
				d.stateMu.RUnlock()

				if dailyPnL <= d.dailyLossLimit {
					log.Printf("[BSCALP] Daily loss limit hit ($%.2f <= $%.2f), skipping scan",
						dailyPnL, d.dailyLossLimit)
				} else {
					d.scanAndExecute()
				}

				nextScan = d.nextAlignedTime(scanInterval)
				log.Printf("[BSCALP] Next scan at %s", nextScan.Format("15:04:05"))
				d.saveState()
				d.saveStatusJSON()
			}
		}
	}
}

// Stop gracefully stops the daemon.
func (d *BinanceScalpDaemon) Stop() {
	d.cancel()
}

// scanAndExecute scans for short entry signals and executes trades.
func (d *BinanceScalpDaemon) scanAndExecute() {
	d.stateMu.RLock()
	activeCount := len(d.state.ActivePositions)
	positions := make(map[string]*strategy.ShortScalpPosition, len(d.state.ActivePositions))
	for k, v := range d.state.ActivePositions {
		positions[k] = v
	}
	d.stateMu.RUnlock()

	if activeCount >= d.config.MaxPositions {
		log.Printf("[BSCALP] Max positions reached (%d/%d), skipping scan",
			activeCount, d.config.MaxPositions)
		return
	}

	result, err := d.scalper.Scan(d.ctx, positions)
	if err != nil {
		log.Printf("[BSCALP] Scan error: %v", err)
		return
	}

	d.stateMu.Lock()
	d.state.LastScanTime = time.Now()
	d.stateMu.Unlock()

	log.Printf("[BSCALP] Scan: %d pairs, %d signals, %s",
		result.ScannedPairs, len(result.Signals), result.ScanTime.Round(time.Millisecond))

	// Execute signals (strongest first, up to max positions)
	slotsAvailable := d.config.MaxPositions - activeCount
	executed := 0

	for _, sig := range result.Signals {
		if executed >= slotsAvailable {
			break
		}

		err := d.executeShort(sig)
		if err != nil {
			log.Printf("[BSCALP] %s short failed: %v", sig.Symbol, err)
			continue
		}
		executed++
	}
}

// calculateOrderAmountUSDT determines the order size based on available Futures balance and signal strength.
// Uses balance-proportional sizing for compounding, scaled by signal confidence.
// Strength multiplier: <30 → 0.7x, 30-60 → 1.0x, >60 → 1.5x
func (d *BinanceScalpDaemon) calculateOrderAmountUSDT(strength float64) float64 {
	const minOrderUSDT = 20.0   // Binance Futures minimum
	const maxOrderUSDT = 500.0  // Safety cap

	bal, err := d.broker.GetBalance(d.ctx)
	if err != nil || bal.CashBalance <= 0 {
		log.Printf("[BSCALP] Balance check failed, using default $%.0f", d.config.OrderAmountUSDT)
		return d.config.OrderAmountUSDT
	}

	// Available USDT divided by remaining slots
	d.stateMu.RLock()
	activeCount := len(d.state.ActivePositions)
	d.stateMu.RUnlock()

	remainingSlots := d.config.MaxPositions - activeCount
	if remainingSlots <= 0 {
		return d.config.OrderAmountUSDT
	}

	orderAmount := bal.CashBalance / float64(remainingSlots)

	// Signal strength multiplier: bet more on high-conviction signals
	// Short scalp strength range is typically 15-80 (threshold 15)
	strengthMult := 1.0
	if strength >= 60 {
		strengthMult = 1.5
	} else if strength < 30 {
		strengthMult = 0.7
	}
	orderAmount *= strengthMult

	// Apply floor and cap
	if orderAmount < minOrderUSDT {
		orderAmount = minOrderUSDT
	}
	if orderAmount > maxOrderUSDT {
		orderAmount = maxOrderUSDT
	}

	log.Printf("[BSCALP] Dynamic sizing: balance=$%.2f, slots=%d, strength=%.0f (×%.1f), order=$%.2f",
		bal.CashBalance, remainingSlots, strength, strengthMult, orderAmount)
	return orderAmount
}

// executeShort places a market SELL order to open a short position.
func (d *BinanceScalpDaemon) executeShort(sig strategy.ShortScalpSignal) error {
	// Dynamic position sizing: balance-proportional + strength-scaled
	orderAmount := d.calculateOrderAmountUSDT(sig.Strength)

	log.Printf("[BSCALP] SHORT %s: $%.2f (RSI=%.1f, Vol=%.1fx, strength=%.0f)",
		sig.Symbol, orderAmount, sig.RSI, sig.VolumeRatio, sig.Strength)

	// Check if Futures balance is sufficient; recall from Earn if needed
	bal, err := d.broker.GetBalance(d.ctx)
	if err == nil && bal.CashBalance < orderAmount {
		deficit := orderAmount - bal.CashBalance + 5 // +$5 buffer
		recovered := d.recallFromEarn(deficit)
		if recovered > 0 {
			log.Printf("[BSCALP] Recalled $%.2f from Earn for trade", recovered)
		}
	}

	// Open short: SELL to open
	order := broker.Order{
		Symbol: sig.Symbol,
		Side:   broker.OrderSideSell,
		Type:   broker.OrderTypeMarket,
		Amount: orderAmount,
	}

	result, err := d.broker.PlaceOrder(d.ctx, order)
	if err != nil {
		return fmt.Errorf("place short order: %w", err)
	}

	entryPrice := sig.Price
	if result != nil && result.AvgPrice > 0 {
		entryPrice = result.AvgPrice
	}

	quantity := result.FilledQty
	if quantity <= 0 {
		quantity = orderAmount * float64(d.config.Leverage) / entryPrice
	}

	pos := &strategy.ShortScalpPosition{
		Symbol:     sig.Symbol,
		EntryPrice: entryPrice,
		Quantity:   quantity,
		AmountUSDT: orderAmount,
		Leverage:   d.config.Leverage,
		EntryTime:  time.Now(),
		EntryBar:   d.state.BarCounter,
		StopLoss:   d.scalper.CalculateStopLoss(entryPrice),
		TakeProfit: d.scalper.CalculateTakeProfit(entryPrice),
		Strategy:   "rsi-overbought-short",
		RSIAtEntry: sig.RSI,
	}

	d.stateMu.Lock()
	d.state.ActivePositions[sig.Symbol] = pos
	d.stateMu.Unlock()

	log.Printf("[BSCALP] FILLED SHORT %s: qty=%.4f @ $%.4f, SL=$%.4f, TP=$%.4f",
		sig.Symbol, quantity, entryPrice, pos.StopLoss, pos.TakeProfit)

	// Notify
	d.notifier.TradeAlert(d.ctx, "B-Short", sig.Symbol, "SHORT", orderAmount, "$", 0,
		fmt.Sprintf("RSI=%.0f, Str=%.0f", sig.RSI, sig.Strength))

	// Entry commission
	commission := orderAmount * float64(d.config.Leverage) * d.config.CommissionPct / 100.0
	d.stateMu.Lock()
	d.state.DailyStats.Commission += commission
	d.state.TotalStats.TotalCommission += commission
	d.stateMu.Unlock()

	return nil
}

// monitorPositions checks all active short positions for exit conditions.
func (d *BinanceScalpDaemon) monitorPositions() {
	d.stateMu.RLock()
	if len(d.state.ActivePositions) == 0 {
		d.stateMu.RUnlock()
		return
	}
	toCheck := make(map[string]*strategy.ShortScalpPosition, len(d.state.ActivePositions))
	for k, v := range d.state.ActivePositions {
		toCheck[k] = v
	}
	barCounter := d.state.BarCounter
	d.stateMu.RUnlock()

	for symbol, pos := range toCheck {
		shouldExit, reason, currentPrice := d.scalper.CheckExit(d.ctx, pos, barCounter)
		if !shouldExit {
			continue
		}

		err := d.executeCoverShort(pos, currentPrice, reason)
		if err != nil {
			log.Printf("[BSCALP] %s cover failed: %v", symbol, err)
		}
	}
}

// executeCoverShort places a market BUY order with reduceOnly to close a short position.
func (d *BinanceScalpDaemon) executeCoverShort(pos *strategy.ShortScalpPosition, exitPrice float64, reason string) error {
	order := broker.Order{
		Symbol:     pos.Symbol,
		Side:       broker.OrderSideBuy,
		Type:       broker.OrderTypeMarket,
		Quantity:   pos.Quantity,
		ReduceOnly: true, // Close short position
	}

	log.Printf("[BSCALP] COVER %s: qty=%.4f, reason=%s", pos.Symbol, pos.Quantity, reason)

	_, err := d.broker.PlaceOrder(d.ctx, order)
	if err != nil {
		return fmt.Errorf("place cover order: %w", err)
	}

	// SHORT PnL: profit when price drops (진입+청산 수수료 모두 차감)
	grossPnL := (pos.EntryPrice - exitPrice) * pos.Quantity
	entryCommission := pos.EntryPrice * pos.Quantity * d.config.CommissionPct / 100.0
	exitCommission := exitPrice * pos.Quantity * d.config.CommissionPct / 100.0
	totalCommission := entryCommission + exitCommission
	netPnL := grossPnL - totalCommission

	isWin := netPnL > 0
	pnlPct := (pos.EntryPrice - exitPrice) / pos.EntryPrice * 100
	holdDuration := time.Since(pos.EntryTime).Round(time.Minute)

	log.Printf("[BSCALP] CLOSED SHORT %s: pnl=$%.2f (%.2f%%), comm=$%.4f, net=$%.2f, hold=%s, reason=%s",
		pos.Symbol, grossPnL, pnlPct, totalCommission, netPnL, holdDuration, reason)

	// Notify
	d.notifier.TradeAlert(d.ctx, "B-Short", pos.Symbol, "COVER", pos.AmountUSDT, "$", netPnL,
		fmt.Sprintf("%s (%.1f%%, %s)", reason, pnlPct, holdDuration))

	// Update stats
	d.stateMu.Lock()
	delete(d.state.ActivePositions, pos.Symbol)

	d.state.DailyStats.Trades++
	d.state.DailyStats.GrossPnL += grossPnL
	d.state.DailyStats.NetPnL += netPnL
	d.state.DailyStats.Commission += totalCommission

	d.state.TotalStats.TotalTrades++
	d.state.TotalStats.TotalGrossPnL += grossPnL
	d.state.TotalStats.TotalNetPnL += netPnL
	d.state.TotalStats.TotalCommission += totalCommission

	if isWin {
		d.state.DailyStats.Wins++
		d.state.TotalStats.TotalWins++
		d.state.TotalStats.currentWinStreak++
		d.state.TotalStats.currentLoseStreak = 0
		if d.state.TotalStats.currentWinStreak > d.state.TotalStats.WinStreakMax {
			d.state.TotalStats.WinStreakMax = d.state.TotalStats.currentWinStreak
		}
	} else {
		d.state.DailyStats.Losses++
		d.state.TotalStats.TotalLosses++
		d.state.TotalStats.currentLoseStreak++
		d.state.TotalStats.currentWinStreak = 0
		if d.state.TotalStats.currentLoseStreak > d.state.TotalStats.LoseStreakMax {
			d.state.TotalStats.LoseStreakMax = d.state.TotalStats.currentLoseStreak
		}
	}

	if netPnL > d.state.TotalStats.BestTrade {
		d.state.TotalStats.BestTrade = netPnL
	}
	if netPnL < d.state.TotalStats.WorstTrade {
		d.state.TotalStats.WorstTrade = netPnL
	}

	// Record trade history (keep last 50)
	d.state.RecentTrades = append(d.state.RecentTrades, BinanceScalpTradeRecord{
		Symbol:     pos.Symbol,
		EntryPrice: pos.EntryPrice,
		ExitPrice:  exitPrice,
		Quantity:   pos.Quantity,
		AmountUSDT: pos.AmountUSDT,
		Leverage:   pos.Leverage,
		NetPnL:     netPnL,
		PnLPct:     pnlPct,
		ExitReason: reason,
		EntryTime:  pos.EntryTime,
		ExitTime:   time.Now(),
	})
	if len(d.state.RecentTrades) > 50 {
		d.state.RecentTrades = d.state.RecentTrades[len(d.state.RecentTrades)-50:]
	}
	d.stateMu.Unlock()

	// Slot opened — recall capital from Earn if any
	if d.earnProductID != "" {
		if earnBal, err := d.broker.EarnGetPosition(d.ctx, "USDT"); err == nil && earnBal >= 10 {
			log.Printf("[BSCALP] Slot opened, recalling $%.2f from Earn", earnBal)
			d.recallFromEarn(earnBal)
		}
	}

	d.saveState()
	d.saveStatusJSON()
	return nil
}

// --- Simple Earn Flexible: idle capital management ---

// initEarn fetches the USDT Flexible Earn product ID on startup.
func (d *BinanceScalpDaemon) initEarn() {
	productID, apy, err := d.broker.EarnGetProductID(d.ctx, "USDT")
	if err != nil {
		log.Printf("[BSCALP] Earn init failed (will retry): %v", err)
		return
	}
	d.earnProductID = productID
	d.earnAPY = apy
	log.Printf("[BSCALP] Earn initialized: USDT Flexible productId=%s, APY=%.2f%%", productID, apy)

	// Check current earn position
	earnBal, err := d.broker.EarnGetPosition(d.ctx, "USDT")
	if err != nil {
		log.Printf("[BSCALP] Earn position check failed: %v", err)
	} else if earnBal > 0 {
		log.Printf("[BSCALP] Current Earn balance: $%.2f", earnBal)
	}
}

// manageEarn sweeps idle USDT from Futures → Spot → Flexible Earn (every hour).
// Reserve = max positions × $100 (enough for dynamic sizing with small balance).
// Only moves excess above reserve.
func (d *BinanceScalpDaemon) manageEarn() {
	if d.earnProductID == "" {
		return // Earn not initialized
	}

	d.stateMu.RLock()
	lastCheck := d.state.LastEarnCheck
	d.stateMu.RUnlock()

	// Only check every hour
	if time.Since(lastCheck) < 1*time.Hour {
		return
	}

	d.stateMu.Lock()
	d.state.LastEarnCheck = time.Now()
	d.stateMu.Unlock()

	// Get Futures available balance
	bal, err := d.broker.GetBalance(d.ctx)
	if err != nil {
		return
	}

	d.stateMu.RLock()
	activeCount := len(d.state.ActivePositions)
	d.stateMu.RUnlock()

	remainingSlots := d.config.MaxPositions - activeCount

	// Only sweep when ALL position slots are full.
	// With balance-proportional sizing (available / remainingSlots), the entire
	// available balance is the trading reserve. Sweeping with open slots would
	// reduce order sizes and weaken the compounding effect.
	// When all slots are full, no new trades can be placed → excess is truly idle.
	if remainingSlots > 0 {
		return
	}

	// All slots full: keep small buffer for margin/fees, sweep the rest
	reserve := 20.0 // $20 margin buffer
	excess := bal.CashBalance - reserve
	const minSweep = 10.0

	if excess >= minSweep {
		// Transfer Futures → Spot → Flexible Earn
		log.Printf("[BSCALP] Earn sweep: available=$%.2f, reserve=$%.2f, sweeping $%.2f to Flexible",
			bal.CashBalance, reserve, excess)

		if err := d.broker.Transfer(d.ctx, "USDT", excess, "UMFUTURE_MAIN"); err != nil {
			log.Printf("[BSCALP] Earn: Futures→Spot transfer failed: %v", err)
			return
		}

		if err := d.broker.EarnSubscribe(d.ctx, d.earnProductID, excess); err != nil {
			log.Printf("[BSCALP] Earn: subscribe failed, returning to Futures: %v", err)
			// Try to return to Futures
			d.broker.Transfer(d.ctx, "USDT", excess, "MAIN_UMFUTURE")
			return
		}

		d.stateMu.Lock()
		d.state.EarnDeposited += excess
		d.stateMu.Unlock()

		log.Printf("[BSCALP] Earn: $%.2f deposited to Flexible Earn (APY %.2f%%)", excess, d.earnAPY)
		d.saveState()
	}
}

// recallFromEarn redeems USDT from Flexible Earn back to Futures when needed for trading.
// Returns the amount recovered (may be 0 if nothing in Earn).
func (d *BinanceScalpDaemon) recallFromEarn(needed float64) float64 {
	if d.earnProductID == "" {
		return 0
	}

	earnBal, err := d.broker.EarnGetPosition(d.ctx, "USDT")
	if err != nil || earnBal < 1 {
		return 0
	}

	redeemAmount := needed
	if redeemAmount > earnBal {
		redeemAmount = earnBal
	}

	log.Printf("[BSCALP] Earn recall: need $%.2f, earn has $%.2f, redeeming $%.2f",
		needed, earnBal, redeemAmount)

	// Track interest: difference between earn balance and deposited amount
	d.stateMu.RLock()
	deposited := d.state.EarnDeposited
	d.stateMu.RUnlock()
	if earnBal > deposited {
		interest := earnBal - deposited
		d.stateMu.Lock()
		d.state.EarnInterest += interest
		d.state.EarnDeposited = earnBal - interest // reset baseline
		d.stateMu.Unlock()
		log.Printf("[BSCALP] Earn interest accrued: $%.4f", interest)
	}

	if err := d.broker.EarnRedeem(d.ctx, d.earnProductID, redeemAmount); err != nil {
		log.Printf("[BSCALP] Earn: redeem failed: %v", err)
		return 0
	}

	// Wait briefly for redemption to settle
	time.Sleep(2 * time.Second)

	// Transfer Spot → Futures
	if err := d.broker.Transfer(d.ctx, "USDT", redeemAmount, "MAIN_UMFUTURE"); err != nil {
		log.Printf("[BSCALP] Earn: Spot→Futures transfer failed: %v", err)
		return 0
	}

	d.stateMu.Lock()
	d.state.EarnDeposited -= redeemAmount
	if d.state.EarnDeposited < 0 {
		d.state.EarnDeposited = 0
	}
	d.stateMu.Unlock()

	log.Printf("[BSCALP] Earn: $%.2f returned to Futures", redeemAmount)
	d.saveState()
	return redeemAmount
}

// checkDayRollover resets daily stats at midnight UTC (Binance uses UTC).
func (d *BinanceScalpDaemon) checkDayRollover() {
	today := time.Now().UTC().Format("2006-01-02")

	d.stateMu.Lock()
	defer d.stateMu.Unlock()

	if d.state.DailyStats.Date != today {
		if d.state.DailyStats.Date != "" && d.state.DailyStats.Trades > 0 {
			log.Printf("[BSCALP] Day rollover: %s -> %s (trades=%d, net=$%.2f, winRate=%.0f%%)",
				d.state.DailyStats.Date, today,
				d.state.DailyStats.Trades, d.state.DailyStats.NetPnL,
				binanceScalpWinRate(d.state.DailyStats.Wins, d.state.DailyStats.Trades))
		}
		d.state.DailyStats = BinanceScalpDailyStats{Date: today}
	}
}

// nextAlignedTime returns the next time aligned to the given interval.
func (d *BinanceScalpDaemon) nextAlignedTime(interval time.Duration) time.Time {
	now := time.Now()
	next := now.Truncate(interval).Add(interval).Add(30 * time.Second)
	return next
}

// State persistence

func (d *BinanceScalpDaemon) statePath() string {
	return filepath.Join(d.dataDir, "binance_state.json")
}

func (d *BinanceScalpDaemon) statusPath() string {
	return filepath.Join(d.dataDir, "binance_status.json")
}

func (d *BinanceScalpDaemon) loadState() {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()

	d.state = &BinanceScalpState{
		ActivePositions: make(map[string]*strategy.ShortScalpPosition),
	}

	data, err := os.ReadFile(d.statePath())
	if err != nil {
		today := time.Now().UTC().Format("2006-01-02")
		d.state.DailyStats.Date = today
		d.state.TotalStats.StartDate = today
		log.Printf("[BSCALP] No saved state, starting fresh")
		return
	}

	if err := json.Unmarshal(data, d.state); err != nil {
		log.Printf("[BSCALP] Failed to parse state: %v, starting fresh", err)
		d.state.ActivePositions = make(map[string]*strategy.ShortScalpPosition)
		return
	}

	if d.state.ActivePositions == nil {
		d.state.ActivePositions = make(map[string]*strategy.ShortScalpPosition)
	}

	log.Printf("[BSCALP] Restored state: %d positions, %d total trades, net=$%.2f, funding=$%.2f",
		len(d.state.ActivePositions), d.state.TotalStats.TotalTrades,
		d.state.TotalStats.TotalNetPnL, d.state.FundingEarned)
}

func (d *BinanceScalpDaemon) saveState() {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()

	data, err := json.MarshalIndent(d.state, "", "  ")
	if err != nil {
		log.Printf("[BSCALP] Failed to marshal state: %v", err)
		return
	}

	if err := os.WriteFile(d.statePath(), data, 0644); err != nil {
		log.Printf("[BSCALP] Failed to save state: %v", err)
	}
}

// saveStatusJSON writes a web-readable status file.
func (d *BinanceScalpDaemon) saveStatusJSON() {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()

	totalTrades := d.state.TotalStats.TotalTrades
	wr := binanceScalpWinRate(d.state.TotalStats.TotalWins, totalTrades)

	// Fetch current balance for web display
	var balanceUSDT, availableUSDT, earnBalUSDT float64
	if bal, err := d.broker.GetBalance(d.ctx); err == nil {
		balanceUSDT = bal.TotalEquity
		availableUSDT = bal.CashBalance
	}
	if d.earnProductID != "" {
		earnBalUSDT, _ = d.broker.EarnGetPosition(d.ctx, "USDT")
	}

	status := map[string]interface{}{
		"strategy":       "rsi-overbought-short",
		"exchange":       "binance-futures",
		"candle_min":     d.config.CandleInterval,
		"pairs":          d.config.Pairs,
		"order_amount":   d.config.OrderAmountUSDT,
		"leverage":       d.config.Leverage,
		"max_positions":  d.config.MaxPositions,
		"balance_usdt":   balanceUSDT,
		"available_usdt": availableUSDT,
		"active_positions": d.state.ActivePositions,
		"daily": map[string]interface{}{
			"date":       d.state.DailyStats.Date,
			"trades":     d.state.DailyStats.Trades,
			"wins":       d.state.DailyStats.Wins,
			"losses":     d.state.DailyStats.Losses,
			"gross_pnl":  d.state.DailyStats.GrossPnL,
			"net_pnl":    d.state.DailyStats.NetPnL,
			"commission": d.state.DailyStats.Commission,
		},
		"total": map[string]interface{}{
			"trades":          totalTrades,
			"wins":            d.state.TotalStats.TotalWins,
			"losses":          d.state.TotalStats.TotalLosses,
			"win_rate":        wr,
			"gross_pnl":       d.state.TotalStats.TotalGrossPnL,
			"net_pnl":         d.state.TotalStats.TotalNetPnL,
			"commission":      d.state.TotalStats.TotalCommission,
			"best_trade":      d.state.TotalStats.BestTrade,
			"worst_trade":     d.state.TotalStats.WorstTrade,
			"start_date":      d.state.TotalStats.StartDate,
			"win_streak_max":  d.state.TotalStats.WinStreakMax,
			"lose_streak_max": d.state.TotalStats.LoseStreakMax,
		},
		"funding_earned":  d.state.FundingEarned,
		"earn_balance":    earnBalUSDT,
		"earn_deposited":  d.state.EarnDeposited,
		"earn_interest":   d.state.EarnInterest,
		"bar_counter":    d.state.BarCounter,
		"last_scan":      d.state.LastScanTime,
		"updated_at":     time.Now(),
		"recent_trades":  d.state.RecentTrades,
	}

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		log.Printf("[BSCALP] Failed to marshal status: %v", err)
		return
	}

	if err := os.WriteFile(d.statusPath(), data, 0644); err != nil {
		log.Printf("[BSCALP] Failed to save status: %v", err)
	}
}

func binanceScalpWinRate(wins, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(wins) / float64(total) * 100
}
