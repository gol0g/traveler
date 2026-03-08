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
	"traveler/internal/strategy"
)

// ScalpTradeRecord records one completed scalp trade.
type ScalpTradeRecord struct {
	Symbol     string    `json:"symbol"`
	EntryPrice float64   `json:"entry_price"`
	ExitPrice  float64   `json:"exit_price"`
	Quantity   float64   `json:"quantity"`
	NetPnL     float64   `json:"net_pnl"`
	PnLPct     float64   `json:"pnl_pct"`
	ExitReason string    `json:"exit_reason"`
	EntryTime  time.Time `json:"entry_time"`
	ExitTime   time.Time `json:"exit_time"`
}

// ScalpState persists scalping daemon state across restarts.
type ScalpState struct {
	ActivePositions map[string]*strategy.ScalpPosition `json:"active_positions"`
	DailyStats      ScalpDailyStats                    `json:"daily_stats"`
	TotalStats      ScalpTotalStats                    `json:"total_stats"`
	BarCounter      int                                `json:"bar_counter"`
	LastScanTime    time.Time                          `json:"last_scan_time"`
	RecentTrades    []ScalpTradeRecord                 `json:"recent_trades"` // last 50 trades
}

// ScalpDailyStats tracks today's performance.
type ScalpDailyStats struct {
	Date          string  `json:"date"`
	Trades        int     `json:"trades"`
	Wins          int     `json:"wins"`
	Losses        int     `json:"losses"`
	GrossPnL      float64 `json:"gross_pnl"`
	NetPnL        float64 `json:"net_pnl"`
	Commission    float64 `json:"commission"`
	MaxDrawdown   float64 `json:"max_drawdown"`
	PeakEquity    float64 `json:"peak_equity"`
}

// ScalpTotalStats tracks lifetime performance.
type ScalpTotalStats struct {
	TotalTrades    int     `json:"total_trades"`
	TotalWins      int     `json:"total_wins"`
	TotalLosses    int     `json:"total_losses"`
	TotalGrossPnL  float64 `json:"total_gross_pnl"`
	TotalNetPnL    float64 `json:"total_net_pnl"`
	TotalCommission float64 `json:"total_commission"`
	BestTrade      float64 `json:"best_trade"`
	WorstTrade     float64 `json:"worst_trade"`
	StartDate      string  `json:"start_date"`
	WinStreakMax   int     `json:"win_streak_max"`
	LoseStreakMax  int     `json:"lose_streak_max"`
	currentWinStreak  int
	currentLoseStreak int
}

// ScalpDaemon runs the crypto scalping strategy.
type ScalpDaemon struct {
	scalper  *strategy.CryptoScalper
	config   strategy.ScalpConfig
	broker   broker.Broker
	dataDir  string

	state    *ScalpState
	stateMu  sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc

	dailyLossLimit float64 // KRW, e.g. -30000
}

// NewScalpDaemon creates a new scalping daemon.
func NewScalpDaemon(cfg strategy.ScalpConfig, b broker.Broker, p strategy.ScalpProvider, dataDir string) *ScalpDaemon {
	ctx, cancel := context.WithCancel(context.Background())
	return &ScalpDaemon{
		scalper:        strategy.NewCryptoScalper(cfg, p),
		config:         cfg,
		broker:         b,
		dataDir:        dataDir,
		ctx:            ctx,
		cancel:         cancel,
		dailyLossLimit: -30000, // ₩30,000 daily loss limit
	}
}

// Run starts the scalping daemon main loop.
func (d *ScalpDaemon) Run() error {
	log.Println("[SCALP] Starting crypto scalping daemon...")
	log.Printf("[SCALP] Strategy: RSI(%d) mean-reversion on %d-min candles",
		d.config.RSIPeriod, d.config.CandleInterval)
	log.Printf("[SCALP] Entry: RSI<%.0f, Vol>%.1fx, EMA%d filter",
		d.config.RSIEntry, d.config.VolumeMin, d.config.EMAPeriod)
	log.Printf("[SCALP] Exit: TP=+%.1f%%, SL=-%.1f%%, RSI>%.0f, MaxBars=%d",
		d.config.TakeProfitPct, d.config.StopLossPct, d.config.RSIExit, d.config.MaxHoldBars)
	log.Printf("[SCALP] Order: ₩%.0f/trade, max %d positions, %d pairs",
		d.config.OrderAmountKRW, d.config.MaxPositions, len(d.config.Pairs))

	// Load or init state
	d.loadState()

	// Reset daily stats if new day
	d.checkDayRollover()

	// Save initial status
	d.saveState()
	d.saveStatusJSON()

	// Two loops:
	// 1. Fast loop (1 min): monitor active positions for SL/TP exit
	// 2. Slow loop (15 min aligned): scan for new entries
	monitorTicker := time.NewTicker(1 * time.Minute)
	defer monitorTicker.Stop()

	// Align scan to candle interval boundaries
	scanInterval := time.Duration(d.config.CandleInterval) * time.Minute
	nextScan := d.nextAlignedTime(scanInterval)
	log.Printf("[SCALP] Next scan at %s (in %s)", nextScan.Format("15:04:05"), time.Until(nextScan).Round(time.Second))

	for {
		select {
		case <-d.ctx.Done():
			log.Println("[SCALP] Daemon stopped by signal")
			d.saveState()
			return nil

		case <-monitorTicker.C:
			d.checkDayRollover()

			// Monitor active positions
			d.monitorPositions()

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
					log.Printf("[SCALP] Daily loss limit hit (₩%.0f <= ₩%.0f), skipping scan",
						dailyPnL, d.dailyLossLimit)
				} else {
					d.scanAndExecute()
				}

				nextScan = d.nextAlignedTime(scanInterval)
				log.Printf("[SCALP] Next scan at %s", nextScan.Format("15:04:05"))
				d.saveState()
				d.saveStatusJSON()
			}
		}
	}
}

// Stop gracefully stops the daemon.
func (d *ScalpDaemon) Stop() {
	d.cancel()
}

// scanAndExecute scans for entry signals and executes trades.
func (d *ScalpDaemon) scanAndExecute() {
	d.stateMu.RLock()
	activeCount := len(d.state.ActivePositions)
	positions := make(map[string]*strategy.ScalpPosition, len(d.state.ActivePositions))
	for k, v := range d.state.ActivePositions {
		positions[k] = v
	}
	d.stateMu.RUnlock()

	if activeCount >= d.config.MaxPositions {
		log.Printf("[SCALP] Max positions reached (%d/%d), skipping scan",
			activeCount, d.config.MaxPositions)
		return
	}

	// Note: preBuyQty delta tracking handles position isolation per-daemon.
	// No need to skip DCA-held coins — scalp's sell uses its own tracked quantity.

	result, err := d.scalper.Scan(d.ctx, positions)
	if err != nil {
		log.Printf("[SCALP] Scan error: %v", err)
		return
	}

	log.Printf("[SCALP] Scan: %d pairs, %d signals, %s",
		result.ScannedPairs, len(result.Signals), result.ScanTime.Round(time.Millisecond))

	// Execute signals (strongest first, up to max positions)
	slotsAvailable := d.config.MaxPositions - activeCount
	executed := 0

	for _, sig := range result.Signals {
		if executed >= slotsAvailable {
			break
		}

		if sig.Side != "buy" {
			continue
		}


		err := d.executeBuy(sig)
		if err != nil {
			log.Printf("[SCALP] %s buy failed: %v", sig.Symbol, err)
			continue
		}
		executed++
	}
}

// getOtherDaemonSymbols returns symbols held on the exchange but NOT by our scalp daemon.
// These are positions from the crypto trend daemon or DCA daemon.
func (d *ScalpDaemon) getOtherDaemonSymbols(scalpPositions map[string]*strategy.ScalpPosition) map[string]bool {
	result := make(map[string]bool)

	brokerPositions, err := d.broker.GetPositions(d.ctx)
	if err != nil {
		return result
	}

	for _, p := range brokerPositions {
		// If the broker shows a position that our scalp daemon doesn't own,
		// it must belong to another daemon → skip this symbol
		if _, isOurs := scalpPositions[p.Symbol]; !isOurs && p.Quantity > 0 {
			result[p.Symbol] = true
		}
	}

	if len(result) > 0 {
		symbols := make([]string, 0, len(result))
		for s := range result {
			symbols = append(symbols, s)
		}
		log.Printf("[SCALP] Other daemon positions detected: %v", symbols)
	}

	return result
}

// calculateOrderAmount determines the order size based on available balance and signal strength.
// Uses balance-proportional sizing for compounding, scaled by signal confidence.
// Strength multiplier: <50 → 0.7x, 50-70 → 1.0x, >70 → 1.5x
func (d *ScalpDaemon) calculateOrderAmount(strength float64) float64 {
	const minOrderKRW = 30000.0  // Upbit minimum + buffer
	const maxOrderKRW = 500000.0 // Safety cap

	bal, err := d.broker.GetBalance(d.ctx)
	if err != nil || bal.CashBalance <= 0 {
		log.Printf("[SCALP] Balance check failed, using default ₩%.0f", d.config.OrderAmountKRW)
		return d.config.OrderAmountKRW
	}

	// Available KRW divided by remaining slots
	d.stateMu.RLock()
	activeCount := len(d.state.ActivePositions)
	d.stateMu.RUnlock()

	remainingSlots := d.config.MaxPositions - activeCount
	if remainingSlots <= 0 {
		return d.config.OrderAmountKRW
	}

	orderAmount := bal.CashBalance / float64(remainingSlots)

	// Signal strength multiplier: bet more on high-conviction signals
	strengthMult := 1.0
	if strength >= 70 {
		strengthMult = 1.5
	} else if strength < 50 {
		strengthMult = 0.7
	}
	orderAmount *= strengthMult

	// Apply floor and cap
	if orderAmount < minOrderKRW {
		orderAmount = minOrderKRW
	}
	if orderAmount > maxOrderKRW {
		orderAmount = maxOrderKRW
	}

	log.Printf("[SCALP] Dynamic sizing: balance=₩%.0f, slots=%d, strength=%.0f (×%.1f), order=₩%.0f",
		bal.CashBalance, remainingSlots, strength, strengthMult, orderAmount)
	return orderAmount
}

// executeBuy places a market buy order and registers the position.
func (d *ScalpDaemon) executeBuy(sig strategy.ScalpSignal) error {
	// Record pre-buy quantity to isolate our purchase from other daemons' holdings
	var preBuyQty float64
	prePositions, err := d.broker.GetPositions(d.ctx)
	if err == nil {
		for _, p := range prePositions {
			if p.Symbol == sig.Symbol {
				preBuyQty = p.Quantity
				break
			}
		}
	}

	// Dynamic position sizing: balance-proportional + strength-scaled
	orderAmount := d.calculateOrderAmount(sig.Strength)

	order := broker.Order{
		Symbol: sig.Symbol,
		Side:   broker.OrderSideBuy,
		Type:   broker.OrderTypeMarket,
		Amount: orderAmount, // KRW market buy (dynamic)
	}

	log.Printf("[SCALP] BUY %s: ₩%.0f (RSI=%.1f, Vol=%.1fx, strength=%.0f, preBuyQty=%.8f)",
		sig.Symbol, orderAmount, sig.RSI, sig.VolumeRatio, sig.Strength, preBuyQty)

	result, err := d.broker.PlaceOrder(d.ctx, order)
	if err != nil {
		return fmt.Errorf("place order: %w", err)
	}

	// Get actual fill price
	entryPrice := sig.Price
	if result != nil && result.AvgPrice > 0 {
		entryPrice = result.AvgPrice
	}

	// Calculate quantity from order amount (fallback)
	quantity := orderAmount / entryPrice

	// Wait briefly then check actual position to get precise fill
	time.Sleep(2 * time.Second)
	positions, err := d.broker.GetPositions(d.ctx)
	if err == nil {
		for _, p := range positions {
			if p.Symbol == sig.Symbol && p.Quantity > 0 {
				// Only use the DELTA (our purchase), not total position
				// This prevents counting other daemons' (crypto/DCA) holdings
				ourQty := p.Quantity - preBuyQty
				if ourQty > 0 {
					quantity = ourQty
					// Estimate our fill price from amount/quantity
					entryPrice = orderAmount / ourQty
				}
				break
			}
		}
	}

	pos := &strategy.ScalpPosition{
		Symbol:     sig.Symbol,
		EntryPrice: entryPrice,
		Quantity:   quantity,
		AmountKRW:  orderAmount,
		EntryTime:  time.Now(),
		EntryBar:   d.state.BarCounter,
		StopLoss:   d.scalper.CalculateStopLoss(entryPrice),
		TakeProfit: d.scalper.CalculateTakeProfit(entryPrice),
		Strategy:   "rsi-mean-revert",
		RSIAtEntry: sig.RSI,
	}

	d.stateMu.Lock()
	d.state.ActivePositions[sig.Symbol] = pos
	d.stateMu.Unlock()

	log.Printf("[SCALP] FILLED %s: qty=%.8f @ ₩%.2f, SL=₩%.2f, TP=₩%.2f",
		sig.Symbol, quantity, entryPrice, pos.StopLoss, pos.TakeProfit)

	commission := orderAmount * d.config.CommissionPct / 100.0
	d.stateMu.Lock()
	d.state.DailyStats.Commission += commission
	d.state.TotalStats.TotalCommission += commission
	d.stateMu.Unlock()

	return nil
}

// monitorPositions checks all active positions for exit conditions.
func (d *ScalpDaemon) monitorPositions() {
	d.stateMu.RLock()
	if len(d.state.ActivePositions) == 0 {
		d.stateMu.RUnlock()
		return
	}
	// Copy positions to avoid holding lock during API calls
	toCheck := make(map[string]*strategy.ScalpPosition, len(d.state.ActivePositions))
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

		err := d.executeSell(pos, currentPrice, reason)
		if err != nil {
			log.Printf("[SCALP] %s sell failed: %v", symbol, err)
		}
	}
}

// executeSell places a market sell order and records the result.
func (d *ScalpDaemon) executeSell(pos *strategy.ScalpPosition, exitPrice float64, reason string) error {
	order := broker.Order{
		Symbol:   pos.Symbol,
		Side:     broker.OrderSideSell,
		Type:     broker.OrderTypeMarket,
		Quantity: pos.Quantity,
	}

	log.Printf("[SCALP] SELL %s: qty=%.8f, reason=%s", pos.Symbol, pos.Quantity, reason)

	_, err := d.broker.PlaceOrder(d.ctx, order)
	if err != nil {
		return fmt.Errorf("place sell: %w", err)
	}

	// Calculate PnL (매수+매도 수수료 모두 차감)
	grossPnL := (exitPrice - pos.EntryPrice) * pos.Quantity
	buyCommission := pos.EntryPrice * pos.Quantity * d.config.CommissionPct / 100.0
	sellCommission := exitPrice * pos.Quantity * d.config.CommissionPct / 100.0
	totalCommission := buyCommission + sellCommission
	netPnL := grossPnL - totalCommission

	isWin := netPnL > 0
	pnlPct := (exitPrice - pos.EntryPrice) / pos.EntryPrice * 100
	holdDuration := time.Since(pos.EntryTime).Round(time.Minute)

	log.Printf("[SCALP] CLOSED %s: pnl=₩%.0f (%.2f%%), comm=₩%.0f, net=₩%.0f, hold=%s, reason=%s",
		pos.Symbol, grossPnL, pnlPct, totalCommission, netPnL, holdDuration, reason)

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
	d.state.RecentTrades = append(d.state.RecentTrades, ScalpTradeRecord{
		Symbol:     pos.Symbol,
		EntryPrice: pos.EntryPrice,
		ExitPrice:  exitPrice,
		Quantity:   pos.Quantity,
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

	d.saveState()
	d.saveStatusJSON()
	return nil
}

// checkDayRollover resets daily stats at midnight KST.
func (d *ScalpDaemon) checkDayRollover() {
	loc, _ := time.LoadLocation("Asia/Seoul")
	today := time.Now().In(loc).Format("2006-01-02")

	d.stateMu.Lock()
	defer d.stateMu.Unlock()

	if d.state.DailyStats.Date != today {
		if d.state.DailyStats.Date != "" && d.state.DailyStats.Trades > 0 {
			log.Printf("[SCALP] Day rollover: %s → %s (trades=%d, net=₩%.0f, winRate=%.0f%%)",
				d.state.DailyStats.Date, today,
				d.state.DailyStats.Trades, d.state.DailyStats.NetPnL,
				scalpWinRate(d.state.DailyStats.Wins, d.state.DailyStats.Trades))
		}
		d.state.DailyStats = ScalpDailyStats{Date: today}
	}
}

// nextAlignedTime returns the next time aligned to the given interval.
func (d *ScalpDaemon) nextAlignedTime(interval time.Duration) time.Time {
	now := time.Now()
	// Add 30 seconds buffer to ensure candle is closed
	next := now.Truncate(interval).Add(interval).Add(30 * time.Second)
	return next
}

// State persistence

func (d *ScalpDaemon) statePath() string {
	return filepath.Join(d.dataDir, "scalp_state.json")
}

func (d *ScalpDaemon) statusPath() string {
	return filepath.Join(d.dataDir, "scalp_status.json")
}

func (d *ScalpDaemon) loadState() {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()

	d.state = &ScalpState{
		ActivePositions: make(map[string]*strategy.ScalpPosition),
	}

	data, err := os.ReadFile(d.statePath())
	if err != nil {
		loc, _ := time.LoadLocation("Asia/Seoul")
		today := time.Now().In(loc).Format("2006-01-02")
		d.state.DailyStats.Date = today
		d.state.TotalStats.StartDate = today
		log.Printf("[SCALP] No saved state, starting fresh")
		return
	}

	if err := json.Unmarshal(data, d.state); err != nil {
		log.Printf("[SCALP] Failed to parse state: %v, starting fresh", err)
		d.state.ActivePositions = make(map[string]*strategy.ScalpPosition)
		return
	}

	if d.state.ActivePositions == nil {
		d.state.ActivePositions = make(map[string]*strategy.ScalpPosition)
	}

	log.Printf("[SCALP] Restored state: %d positions, %d total trades, net=₩%.0f",
		len(d.state.ActivePositions), d.state.TotalStats.TotalTrades, d.state.TotalStats.TotalNetPnL)
}

func (d *ScalpDaemon) saveState() {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()

	data, err := json.MarshalIndent(d.state, "", "  ")
	if err != nil {
		log.Printf("[SCALP] Failed to marshal state: %v", err)
		return
	}

	if err := os.WriteFile(d.statePath(), data, 0644); err != nil {
		log.Printf("[SCALP] Failed to save state: %v", err)
	}
}

// saveStatusJSON writes a web-readable status file.
func (d *ScalpDaemon) saveStatusJSON() {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()

	totalTrades := d.state.TotalStats.TotalTrades
	wr := scalpWinRate(d.state.TotalStats.TotalWins, totalTrades)

	status := map[string]interface{}{
		"strategy":      "rsi-mean-reversion-scalp",
		"candle_min":    d.config.CandleInterval,
		"pairs":         d.config.Pairs,
		"order_amount":  d.config.OrderAmountKRW,
		"max_positions": d.config.MaxPositions,
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
			"trades":         totalTrades,
			"wins":           d.state.TotalStats.TotalWins,
			"losses":         d.state.TotalStats.TotalLosses,
			"win_rate":       wr,
			"gross_pnl":      d.state.TotalStats.TotalGrossPnL,
			"net_pnl":        d.state.TotalStats.TotalNetPnL,
			"commission":     d.state.TotalStats.TotalCommission,
			"best_trade":     d.state.TotalStats.BestTrade,
			"worst_trade":    d.state.TotalStats.WorstTrade,
			"start_date":     d.state.TotalStats.StartDate,
			"win_streak_max": d.state.TotalStats.WinStreakMax,
			"lose_streak_max": d.state.TotalStats.LoseStreakMax,
		},
		"bar_counter":   d.state.BarCounter,
		"last_scan":     d.state.LastScanTime,
		"updated_at":    time.Now(),
		"recent_trades": d.state.RecentTrades,
	}

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		log.Printf("[SCALP] Failed to marshal status: %v", err)
		return
	}

	if err := os.WriteFile(d.statusPath(), data, 0644); err != nil {
		log.Printf("[SCALP] Failed to save status: %v", err)
	}
}

func scalpWinRate(wins, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(wins) / float64(total) * 100
}
