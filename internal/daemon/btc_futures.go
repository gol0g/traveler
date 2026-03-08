package daemon

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"traveler/internal/broker"
	binanceBroker "traveler/internal/broker/binance"
	"traveler/internal/strategy"
)

// BTCFuturesTradeRecord records one completed trade with full context.
type BTCFuturesTradeRecord struct {
	Symbol       string    `json:"symbol"`
	Side         string    `json:"side"` // "long"
	EntryPrice   float64   `json:"entry_price"`
	ExitPrice    float64   `json:"exit_price"`
	Quantity     float64   `json:"quantity"`
	AmountUSDT   float64   `json:"amount_usdt"`
	Leverage     int       `json:"leverage"`
	GrossPnL     float64   `json:"gross_pnl"`
	NetPnL       float64   `json:"net_pnl"`
	PnLPct       float64   `json:"pnl_pct"`
	Commission   float64   `json:"commission"`
	ExitReason   string    `json:"exit_reason"`
	EntryTime    time.Time `json:"entry_time"`
	ExitTime     time.Time `json:"exit_time"`
	BarsHeld     int       `json:"bars_held"`
	EntryFunding float64   `json:"entry_funding"`
	EntryRSI     float64   `json:"entry_rsi"`
	EntryATR     float64   `json:"entry_atr"`
	StopLoss     float64   `json:"stop_loss"`
	TakeProfit   float64   `json:"take_profit"`
}

// BTCFuturesState persists state across restarts.
type BTCFuturesState struct {
	ActivePosition *strategy.FundingLongPosition `json:"active_position"`
	DailyStats     BTCFuturesDailyStats          `json:"daily_stats"`
	TotalStats     BTCFuturesTotalStats           `json:"total_stats"`
	BarCounter     int                            `json:"bar_counter"`
	LastScanTime   time.Time                      `json:"last_scan_time"`
	RecentTrades   []BTCFuturesTradeRecord         `json:"recent_trades"`
}

type BTCFuturesDailyStats struct {
	Date       string  `json:"date"`
	Trades     int     `json:"trades"`
	Wins       int     `json:"wins"`
	Losses     int     `json:"losses"`
	NetPnL     float64 `json:"net_pnl"`
	Commission float64 `json:"commission"`
}

type BTCFuturesTotalStats struct {
	TotalTrades     int     `json:"total_trades"`
	TotalWins       int     `json:"total_wins"`
	TotalLosses     int     `json:"total_losses"`
	TotalNetPnL     float64 `json:"total_net_pnl"`
	TotalCommission float64 `json:"total_commission"`
	BestTrade       float64 `json:"best_trade"`
	WorstTrade      float64 `json:"worst_trade"`
	StartDate       string  `json:"start_date"`
}

// BTCFuturesDaemon runs the BTC funding rate long strategy.
type BTCFuturesDaemon struct {
	strategy *strategy.FundingLongStrategy
	config   strategy.FundingLongConfig
	broker   *binanceBroker.Client
	dataDir  string

	state   *BTCFuturesState
	stateMu sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc

	// Data logging
	signalLogFile *os.File
	signalWriter  *csv.Writer
}

// NewBTCFuturesDaemon creates a new BTC Futures daemon.
func NewBTCFuturesDaemon(cfg strategy.FundingLongConfig, b *binanceBroker.Client,
	cp strategy.FundingProvider, fp strategy.FundingRateProvider, dataDir string) *BTCFuturesDaemon {

	ctx, cancel := context.WithCancel(context.Background())
	strat := strategy.NewFundingLongStrategy(cfg, cp, fp)
	// Wire OI provider if funding rate provider also supports it
	if oiProv, ok := interface{}(cp).(strategy.OpenInterestProvider); ok {
		strat.SetOIProvider(oiProv)
		log.Println("[BTCF] OI divergence filter enabled")
	}
	return &BTCFuturesDaemon{
		strategy: strat,
		config:   cfg,
		broker:   b,
		dataDir:  dataDir,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Run starts the BTC Futures daemon main loop.
func (d *BTCFuturesDaemon) Run() error {
	log.Println("[BTCF] Starting BTC Futures funding-rate long daemon...")
	log.Printf("[BTCF] Symbol: %s, Leverage: %dx, Amount: $%.0f",
		d.config.Symbol, d.config.Leverage, d.config.OrderAmountUSDT)
	log.Printf("[BTCF] Entry: funding < %.4f%%, RSI > %.0f",
		d.config.FundingThreshold*100, d.config.RSIMin)
	log.Printf("[BTCF] Exit: TP=ATR*%.1f, SL=ATR*%.1f, MaxBars=%d",
		d.config.TPAtrMultiple, d.config.SLAtrMultiple, d.config.MaxHoldBars)

	// Initialize broker
	if err := d.broker.Init(d.ctx, []string{d.config.Symbol}); err != nil {
		return fmt.Errorf("broker init: %w", err)
	}

	bal, err := d.broker.GetBalance(d.ctx)
	if err != nil {
		log.Printf("[BTCF] Warning: could not get balance: %v", err)
	} else {
		log.Printf("[BTCF] Futures balance: $%.2f (available: $%.2f)", bal.TotalEquity, bal.CashBalance)
	}

	// Load state + init logging
	d.loadState()
	d.initSignalLog()
	d.checkDayRollover()
	d.saveState()
	d.saveStatusJSON()

	// Two loops: 1min monitor, 15min scan
	monitorTicker := time.NewTicker(1 * time.Minute)
	defer monitorTicker.Stop()

	scanInterval := time.Duration(d.config.CandleInterval) * time.Minute
	nextScan := d.nextAlignedTime(scanInterval)
	log.Printf("[BTCF] Next scan at %s", nextScan.Format("15:04:05"))

	for {
		select {
		case <-d.ctx.Done():
			log.Println("[BTCF] Daemon stopped")
			d.closeSignalLog()
			d.saveState()
			return nil

		case <-monitorTicker.C:
			d.checkDayRollover()
			d.monitorPosition()

			if time.Now().After(nextScan) {
				d.stateMu.Lock()
				d.state.BarCounter++
				d.stateMu.Unlock()

				d.scanAndExecute()

				nextScan = d.nextAlignedTime(scanInterval)
				d.saveState()
				d.saveStatusJSON()
			}
		}
	}
}

// Stop gracefully stops the daemon.
func (d *BTCFuturesDaemon) Stop() {
	d.cancel()
}

func (d *BTCFuturesDaemon) scanAndExecute() {
	d.stateMu.RLock()
	hasPosition := d.state.ActivePosition != nil
	d.stateMu.RUnlock()

	// Always scan for logging purposes, even if we have a position
	sig, scanData, err := d.strategy.Scan(d.ctx)
	if err != nil {
		log.Printf("[BTCF] Scan error: %v", err)
		return
	}

	d.stateMu.Lock()
	d.state.LastScanTime = time.Now()
	d.stateMu.Unlock()

	// Log every scan result for analysis
	d.logScanResult(scanData)

	if hasPosition {
		if sig != nil {
			log.Printf("[BTCF] Signal detected but already in position (funding=%.4f%%, rsi=%.1f)",
				scanData.FundingRate*100, scanData.RSI7)
		}
		return
	}

	if sig == nil {
		log.Printf("[BTCF] Scan: %s — %s", scanData.Signal, scanData.Reason)
		return
	}

	// Execute long entry
	d.executeLong(sig)
}

func (d *BTCFuturesDaemon) executeLong(sig *strategy.FundingLongSignal) {
	log.Printf("[BTCF] LONG %s: $%.0f (funding=%.4f%%, RSI=%.1f, ATR=%.1f)",
		sig.Symbol, d.config.OrderAmountUSDT, sig.FundingRate*100, sig.RSI, sig.ATR)

	order := broker.Order{
		Symbol: sig.Symbol,
		Side:   broker.OrderSideBuy,
		Type:   broker.OrderTypeMarket,
		Amount: d.config.OrderAmountUSDT,
	}

	result, err := d.broker.PlaceOrder(d.ctx, order)
	if err != nil {
		log.Printf("[BTCF] Order failed: %v", err)
		return
	}

	entryPrice := sig.Price
	if result != nil && result.AvgPrice > 0 {
		entryPrice = result.AvgPrice
	}

	quantity := result.FilledQty
	if quantity <= 0 {
		quantity = d.config.OrderAmountUSDT * float64(d.config.Leverage) / entryPrice
	}

	tp := d.strategy.CalculateTP(entryPrice, sig.ATR)
	sl := d.strategy.CalculateSL(entryPrice, sig.ATR)

	pos := &strategy.FundingLongPosition{
		Symbol:       sig.Symbol,
		EntryPrice:   entryPrice,
		Quantity:     quantity,
		AmountUSDT:   d.config.OrderAmountUSDT,
		Leverage:     d.config.Leverage,
		EntryTime:    time.Now(),
		EntryBar:     d.state.BarCounter,
		StopLoss:     sl,
		TakeProfit:   tp,
		EntryATR:     sig.ATR,
		EntryRSI:     sig.RSI,
		EntryFunding: sig.FundingRate,
	}

	d.stateMu.Lock()
	d.state.ActivePosition = pos
	d.stateMu.Unlock()

	log.Printf("[BTCF] FILLED LONG %s: qty=%.5f @ $%.2f, TP=$%.0f, SL=$%.0f",
		sig.Symbol, quantity, entryPrice, tp, sl)

	d.saveState()
	d.saveStatusJSON()
}

func (d *BTCFuturesDaemon) monitorPosition() {
	d.stateMu.RLock()
	pos := d.state.ActivePosition
	bar := d.state.BarCounter
	d.stateMu.RUnlock()

	if pos == nil {
		return
	}

	shouldExit, reason, currentPrice, scanData := d.strategy.CheckExit(d.ctx, pos, bar)

	// Log monitor data
	d.logScanResult(scanData)

	if !shouldExit {
		return
	}

	d.executeClose(pos, currentPrice, reason)
}

func (d *BTCFuturesDaemon) executeClose(pos *strategy.FundingLongPosition, exitPrice float64, reason string) {
	order := broker.Order{
		Symbol:     pos.Symbol,
		Side:       broker.OrderSideSell,
		Type:       broker.OrderTypeMarket,
		Quantity:   pos.Quantity,
		ReduceOnly: true,
	}

	log.Printf("[BTCF] CLOSE LONG %s: qty=%.5f, reason=%s", pos.Symbol, pos.Quantity, reason)

	_, err := d.broker.PlaceOrder(d.ctx, order)
	if err != nil {
		log.Printf("[BTCF] Close order failed: %v", err)
		return
	}

	// LONG PnL
	grossPnL := (exitPrice - pos.EntryPrice) * pos.Quantity
	entryComm := pos.EntryPrice * pos.Quantity * d.config.CommissionPct / 100.0
	exitComm := exitPrice * pos.Quantity * d.config.CommissionPct / 100.0
	totalComm := entryComm + exitComm
	netPnL := grossPnL - totalComm
	pnlPct := (exitPrice - pos.EntryPrice) / pos.EntryPrice * 100
	barsHeld := d.state.BarCounter - pos.EntryBar

	isWin := netPnL > 0
	holdDur := time.Since(pos.EntryTime).Round(time.Minute)

	log.Printf("[BTCF] CLOSED LONG %s: gross=$%.2f, comm=$%.4f, net=$%.2f (%.2f%%), hold=%s, reason=%s",
		pos.Symbol, grossPnL, totalComm, netPnL, pnlPct, holdDur, reason)

	// Record trade
	trade := BTCFuturesTradeRecord{
		Symbol:       pos.Symbol,
		Side:         "long",
		EntryPrice:   pos.EntryPrice,
		ExitPrice:    exitPrice,
		Quantity:     pos.Quantity,
		AmountUSDT:   pos.AmountUSDT,
		Leverage:     pos.Leverage,
		GrossPnL:     grossPnL,
		NetPnL:       netPnL,
		PnLPct:       pnlPct,
		Commission:   totalComm,
		ExitReason:   reason,
		EntryTime:    pos.EntryTime,
		ExitTime:     time.Now(),
		BarsHeld:     barsHeld,
		EntryFunding: pos.EntryFunding,
		EntryRSI:     pos.EntryRSI,
		EntryATR:     pos.EntryATR,
		StopLoss:     pos.StopLoss,
		TakeProfit:   pos.TakeProfit,
	}

	// Update state
	d.stateMu.Lock()
	d.state.ActivePosition = nil

	d.state.DailyStats.Trades++
	d.state.DailyStats.NetPnL += netPnL
	d.state.DailyStats.Commission += totalComm

	d.state.TotalStats.TotalTrades++
	d.state.TotalStats.TotalNetPnL += netPnL
	d.state.TotalStats.TotalCommission += totalComm

	if isWin {
		d.state.DailyStats.Wins++
		d.state.TotalStats.TotalWins++
	} else {
		d.state.DailyStats.Losses++
		d.state.TotalStats.TotalLosses++
	}

	if netPnL > d.state.TotalStats.BestTrade {
		d.state.TotalStats.BestTrade = netPnL
	}
	if netPnL < d.state.TotalStats.WorstTrade || d.state.TotalStats.TotalTrades == 1 {
		if netPnL < d.state.TotalStats.WorstTrade {
			d.state.TotalStats.WorstTrade = netPnL
		}
	}

	d.state.RecentTrades = append(d.state.RecentTrades, trade)
	if len(d.state.RecentTrades) > 100 {
		d.state.RecentTrades = d.state.RecentTrades[len(d.state.RecentTrades)-100:]
	}
	d.stateMu.Unlock()

	// Log trade to CSV
	d.logTrade(trade)

	d.saveState()
	d.saveStatusJSON()
}

// ========== Data Logging ==========

func (d *BTCFuturesDaemon) signalLogDir() string {
	dir := filepath.Join(d.dataDir, "btc_signals")
	os.MkdirAll(dir, 0755)
	return dir
}

func (d *BTCFuturesDaemon) initSignalLog() {
	date := time.Now().UTC().Format("2006-01-02")
	filename := filepath.Join(d.signalLogDir(), fmt.Sprintf("scan_%s.csv", date))

	isNew := false
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		isNew = true
	}

	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[BTCF] Could not open signal log: %v", err)
		return
	}

	d.signalLogFile = f
	d.signalWriter = csv.NewWriter(f)

	if isNew {
		d.signalWriter.Write([]string{
			"time", "symbol", "price", "funding_rate", "rsi7", "atr14",
			"ema50", "volume", "avg_volume", "oi", "oi_change", "oi_divergence",
			"signal", "reason",
		})
		d.signalWriter.Flush()
	}
}

func (d *BTCFuturesDaemon) closeSignalLog() {
	if d.signalWriter != nil {
		d.signalWriter.Flush()
	}
	if d.signalLogFile != nil {
		d.signalLogFile.Close()
	}
}

func (d *BTCFuturesDaemon) logScanResult(sr *strategy.FundingScanResult) {
	if sr == nil || d.signalWriter == nil {
		return
	}

	// Rotate log file at day boundary
	date := time.Now().UTC().Format("2006-01-02")
	expected := filepath.Join(d.signalLogDir(), fmt.Sprintf("scan_%s.csv", date))
	if d.signalLogFile != nil && d.signalLogFile.Name() != expected {
		d.closeSignalLog()
		d.initSignalLog()
	}

	d.signalWriter.Write([]string{
		sr.Time.Format("2006-01-02T15:04:05Z"),
		sr.Symbol,
		fmt.Sprintf("%.2f", sr.Price),
		fmt.Sprintf("%.6f", sr.FundingRate),
		fmt.Sprintf("%.2f", sr.RSI7),
		fmt.Sprintf("%.2f", sr.ATR14),
		fmt.Sprintf("%.2f", sr.EMA50),
		fmt.Sprintf("%.0f", sr.Volume),
		fmt.Sprintf("%.0f", sr.AvgVolume),
		fmt.Sprintf("%.2f", sr.OI),
		fmt.Sprintf("%.2f", sr.OIChange),
		sr.OIDivergence,
		sr.Signal,
		sr.Reason,
	})
	d.signalWriter.Flush()
}

func (d *BTCFuturesDaemon) logTrade(trade BTCFuturesTradeRecord) {
	filename := filepath.Join(d.signalLogDir(), "trades.csv")

	isNew := false
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		isNew = true
	}

	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[BTCF] Could not write trade log: %v", err)
		return
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if isNew {
		w.Write([]string{
			"entry_time", "exit_time", "symbol", "side",
			"entry_price", "exit_price", "quantity", "amount_usdt",
			"leverage", "gross_pnl", "net_pnl", "pnl_pct", "commission",
			"exit_reason", "bars_held",
			"entry_funding", "entry_rsi", "entry_atr", "stop_loss", "take_profit",
		})
	}

	w.Write([]string{
		trade.EntryTime.Format("2006-01-02T15:04:05Z"),
		trade.ExitTime.Format("2006-01-02T15:04:05Z"),
		trade.Symbol, trade.Side,
		fmt.Sprintf("%.2f", trade.EntryPrice),
		fmt.Sprintf("%.2f", trade.ExitPrice),
		fmt.Sprintf("%.5f", trade.Quantity),
		fmt.Sprintf("%.2f", trade.AmountUSDT),
		fmt.Sprintf("%d", trade.Leverage),
		fmt.Sprintf("%.4f", trade.GrossPnL),
		fmt.Sprintf("%.4f", trade.NetPnL),
		fmt.Sprintf("%.4f", trade.PnLPct),
		fmt.Sprintf("%.4f", trade.Commission),
		trade.ExitReason,
		fmt.Sprintf("%d", trade.BarsHeld),
		fmt.Sprintf("%.6f", trade.EntryFunding),
		fmt.Sprintf("%.2f", trade.EntryRSI),
		fmt.Sprintf("%.2f", trade.EntryATR),
		fmt.Sprintf("%.2f", trade.StopLoss),
		fmt.Sprintf("%.2f", trade.TakeProfit),
	})
}

// ========== State Persistence ==========

func (d *BTCFuturesDaemon) checkDayRollover() {
	today := time.Now().UTC().Format("2006-01-02")
	d.stateMu.Lock()
	defer d.stateMu.Unlock()

	if d.state.DailyStats.Date != today {
		if d.state.DailyStats.Date != "" && d.state.DailyStats.Trades > 0 {
			log.Printf("[BTCF] Day rollover: %s (trades=%d, net=$%.2f)",
				d.state.DailyStats.Date, d.state.DailyStats.Trades, d.state.DailyStats.NetPnL)
		}
		d.state.DailyStats = BTCFuturesDailyStats{Date: today}
	}
}

func (d *BTCFuturesDaemon) nextAlignedTime(interval time.Duration) time.Time {
	now := time.Now()
	return now.Truncate(interval).Add(interval).Add(30 * time.Second)
}

func (d *BTCFuturesDaemon) statePath() string {
	return filepath.Join(d.dataDir, "btc_futures_state.json")
}

func (d *BTCFuturesDaemon) statusPath() string {
	return filepath.Join(d.dataDir, "btc_futures_status.json")
}

func (d *BTCFuturesDaemon) loadState() {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()

	d.state = &BTCFuturesState{}

	data, err := os.ReadFile(d.statePath())
	if err != nil {
		today := time.Now().UTC().Format("2006-01-02")
		d.state.DailyStats.Date = today
		d.state.TotalStats.StartDate = today
		log.Printf("[BTCF] No saved state, starting fresh")
		return
	}

	if err := json.Unmarshal(data, d.state); err != nil {
		log.Printf("[BTCF] Failed to parse state: %v", err)
		return
	}

	if d.state.ActivePosition != nil {
		log.Printf("[BTCF] Restored active position: %s @ $%.2f",
			d.state.ActivePosition.Symbol, d.state.ActivePosition.EntryPrice)
	}
	log.Printf("[BTCF] Restored: %d total trades, net=$%.2f",
		d.state.TotalStats.TotalTrades, d.state.TotalStats.TotalNetPnL)
}

func (d *BTCFuturesDaemon) saveState() {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()

	data, err := json.MarshalIndent(d.state, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(d.statePath(), data, 0644)
}

func (d *BTCFuturesDaemon) saveStatusJSON() {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()

	totalTrades := d.state.TotalStats.TotalTrades
	wr := 0.0
	if totalTrades > 0 {
		wr = float64(d.state.TotalStats.TotalWins) / float64(totalTrades) * 100
	}

	status := map[string]interface{}{
		"strategy":      "funding-rate-long",
		"exchange":      "binance-futures",
		"symbol":        d.config.Symbol,
		"candle_min":    d.config.CandleInterval,
		"order_amount":  d.config.OrderAmountUSDT,
		"leverage":      d.config.Leverage,
		"funding_thresh": d.config.FundingThreshold * 100,
		"rsi_min":       d.config.RSIMin,
		"tp_atr_mult":   d.config.TPAtrMultiple,
		"sl_atr_mult":   d.config.SLAtrMultiple,
		"active_position": d.state.ActivePosition,
		"daily": d.state.DailyStats,
		"total": map[string]interface{}{
			"trades":     totalTrades,
			"wins":       d.state.TotalStats.TotalWins,
			"losses":     d.state.TotalStats.TotalLosses,
			"win_rate":   wr,
			"net_pnl":    d.state.TotalStats.TotalNetPnL,
			"commission": d.state.TotalStats.TotalCommission,
			"best_trade": d.state.TotalStats.BestTrade,
			"worst_trade": d.state.TotalStats.WorstTrade,
			"start_date": d.state.TotalStats.StartDate,
		},
		"bar_counter":   d.state.BarCounter,
		"last_scan":     d.state.LastScanTime,
		"updated_at":    time.Now(),
		"recent_trades": d.state.RecentTrades,
	}

	data, _ := json.MarshalIndent(status, "", "  ")
	os.WriteFile(d.statusPath(), data, 0644)
}
