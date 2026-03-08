package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"traveler/internal/broker"
	binanceBroker "traveler/internal/broker/binance"
	"traveler/internal/strategy"
)

// ArbTradeRecord records one completed arb trade.
type ArbTradeRecord struct {
	Symbol           string    `json:"symbol"`
	CapitalUsed      float64   `json:"capital_used"`
	SpotEntryPrice   float64   `json:"spot_entry_price"`
	FuturesEntry     float64   `json:"futures_entry"`
	SpotExitPrice    float64   `json:"spot_exit_price"`
	FuturesExitPrice float64   `json:"futures_exit_price"`
	FundingCollected float64   `json:"funding_collected"`
	TotalCommission  float64   `json:"total_commission"`
	NetPnL           float64   `json:"net_pnl"`
	HoldDuration     string    `json:"hold_duration"`
	FundingPeriods   int       `json:"funding_periods"`
	OpenedAt         time.Time `json:"opened_at"`
	ClosedAt         time.Time `json:"closed_at"`
	ExitReason       string    `json:"exit_reason"`
}

// ArbState persists arb daemon state across restarts.
type ArbState struct {
	ActivePositions  map[string]*strategy.ArbPosition `json:"active_positions"`
	TotalStats       ArbTotalStats                    `json:"total_stats"`
	RecentTrades     []ArbTradeRecord                 `json:"recent_trades"`
	LastCheckTime    time.Time                        `json:"last_check_time"`
	LastFundingRates map[string]float64               `json:"last_funding_rates"`
}

// ArbTotalStats tracks lifetime performance.
type ArbTotalStats struct {
	TotalTrades     int     `json:"total_trades"`
	TotalFunding    float64 `json:"total_funding"`
	TotalCommission float64 `json:"total_commission"`
	TotalNetPnL     float64 `json:"total_net_pnl"`
	TotalBasisPnL   float64 `json:"total_basis_pnl"`
	BestTrade       float64 `json:"best_trade"`
	WorstTrade      float64 `json:"worst_trade"`
	StartDate       string  `json:"start_date"`
}

// BinanceArbDaemon runs the Binance Funding Rate Arbitrage strategy.
type BinanceArbDaemon struct {
	config  strategy.FundingArbConfig
	broker  *binanceBroker.Client
	dataDir string

	state   *ArbState
	stateMu sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc
}

// NewBinanceArbDaemon creates a new funding rate arbitrage daemon.
func NewBinanceArbDaemon(cfg strategy.FundingArbConfig, b *binanceBroker.Client, dataDir string) *BinanceArbDaemon {
	ctx, cancel := context.WithCancel(context.Background())
	return &BinanceArbDaemon{
		config:  cfg,
		broker:  b,
		dataDir: dataDir,
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Run starts the arb daemon main loop.
func (d *BinanceArbDaemon) Run() error {
	log.Println("[ARB] Starting Binance Funding Rate Arbitrage daemon...")
	log.Printf("[ARB] Min funding rate: %.4f%%, Max capital: $%.0f, Pairs: %v",
		d.config.MinFundingRate*100, d.config.MaxCapitalUSDT, d.config.Pairs)

	// Init broker (exchange info + leverage 1x for arb pairs)
	if err := d.broker.Init(d.ctx, d.config.Pairs); err != nil {
		return fmt.Errorf("broker init: %w", err)
	}
	// Override leverage to 1x for arb pairs
	for _, sym := range d.config.Pairs {
		if err := d.broker.SetLeverage(d.ctx, sym, 1); err != nil {
			log.Printf("[ARB] Warning: set leverage 1x %s: %v", sym, err)
		}
	}

	// Check balance
	bal, err := d.broker.GetBalance(d.ctx)
	if err != nil {
		log.Printf("[ARB] Warning: could not get balance: %v", err)
	} else {
		log.Printf("[ARB] Futures balance: $%.2f (available: $%.2f)", bal.TotalEquity, bal.CashBalance)
	}

	d.loadState()
	d.saveState()
	d.saveStatusJSON()

	checkInterval := time.Duration(d.config.CheckIntervalMin) * time.Minute
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// Check immediately on start
	nextCheck := time.Now()

	for {
		select {
		case <-d.ctx.Done():
			log.Println("[ARB] Daemon stopped by signal")
			d.saveState()
			return nil

		case <-ticker.C:
			if time.Now().Before(nextCheck) {
				continue
			}

			d.checkAndAct()
			nextCheck = time.Now().Add(checkInterval)
			log.Printf("[ARB] Next check at %s", nextCheck.Format("15:04:05"))
			d.saveState()
			d.saveStatusJSON()
		}
	}
}

// Stop gracefully stops the daemon.
func (d *BinanceArbDaemon) Stop() {
	d.cancel()
}

// checkAndAct checks funding rates and opens/closes positions as needed.
func (d *BinanceArbDaemon) checkAndAct() {
	d.stateMu.Lock()
	d.state.LastCheckTime = time.Now()
	d.stateMu.Unlock()

	for _, symbol := range d.config.Pairs {
		rate, nextFundingTime, err := d.broker.GetFundingRate(d.ctx, symbol)
		if err != nil {
			log.Printf("[ARB] %s funding rate error: %v", symbol, err)
			continue
		}

		d.stateMu.Lock()
		d.state.LastFundingRates[symbol] = rate
		d.stateMu.Unlock()

		log.Printf("[ARB] %s: funding=%.4f%%, next=%s",
			symbol, rate*100, nextFundingTime.Format("15:04 UTC"))

		// Check existing position for exit
		d.stateMu.RLock()
		pos, hasPos := d.state.ActivePositions[symbol]
		d.stateMu.RUnlock()

		if hasPos {
			// Update funding collected from API
			d.updateFundingIncome(pos)

			shouldExit, reason := strategy.ShouldExitArb(d.config, rate, pos)
			if shouldExit {
				d.closeArbPosition(pos, reason)
			} else {
				log.Printf("[ARB] %s: holding (funding=$%.4f, %d periods, capital=$%.0f)",
					symbol, pos.FundingCollected, pos.FundingPayments, pos.CapitalUsed)
			}
			continue
		}

		// Check for new entry
		d.stateMu.RLock()
		activeCount := len(d.state.ActivePositions)
		d.stateMu.RUnlock()

		availableCapital := d.getAvailableCapital()
		positionSize := math.Min(d.config.MaxCapitalUSDT, availableCapital)

		if strategy.ShouldEnterArb(d.config, rate, activeCount, positionSize) {
			breakeven := strategy.BreakevenFundingPeriods(d.config, positionSize, rate)
			log.Printf("[ARB] %s: entry signal (rate=%.4f%%, capital=$%.2f, breakeven=%d periods/%.1f days)",
				symbol, rate*100, positionSize, breakeven, float64(breakeven)*8.0/24.0)

			if breakeven <= 42 { // max ~14 days to breakeven
				d.openArbPosition(symbol, positionSize)
			} else {
				log.Printf("[ARB] %s: skipping — breakeven too long (%d periods)", symbol, breakeven)
			}
		}
	}
}

// openArbPosition opens a delta-neutral position: Spot buy + Futures short.
func (d *BinanceArbDaemon) openArbPosition(symbol string, capitalUSDT float64) {
	baseAsset := strategy.BaseAssetFromSymbol(symbol)
	halfCapital := capitalUSDT / 2.0

	// Ensure 1x leverage for this symbol
	d.broker.SetLeverage(d.ctx, symbol, 1)

	// Step 1: Transfer USDT from Futures to Spot
	log.Printf("[ARB] Transferring $%.2f USDT: Futures -> Spot", halfCapital)
	if err := d.broker.Transfer(d.ctx, "USDT", halfCapital, "UMFUTURE_MAIN"); err != nil {
		log.Printf("[ARB] %s: transfer to spot failed: %v", symbol, err)
		return
	}
	time.Sleep(1 * time.Second)

	// Step 2: Buy on Spot
	log.Printf("[ARB] Spot BUY %s: $%.2f USDT", symbol, halfCapital)
	spotQty, spotPrice, err := d.broker.SpotBuy(d.ctx, symbol, halfCapital)
	if err != nil {
		log.Printf("[ARB] %s: spot buy failed: %v — rolling back transfer", symbol, err)
		d.broker.Transfer(d.ctx, "USDT", halfCapital, "MAIN_UMFUTURE")
		return
	}

	// Step 3: Short on Futures with matching quantity
	log.Printf("[ARB] Futures SHORT %s: qty=%.6f", symbol, spotQty)
	order := broker.Order{
		Symbol:   symbol,
		Side:     broker.OrderSideSell,
		Type:     broker.OrderTypeMarket,
		Quantity: spotQty,
	}
	result, err := d.broker.PlaceOrder(d.ctx, order)
	if err != nil {
		log.Printf("[ARB] %s: futures short failed: %v — unwinding spot buy", symbol, err)
		d.broker.SpotSell(d.ctx, symbol, spotQty)
		time.Sleep(500 * time.Millisecond)
		spotFree, _ := d.broker.SpotGetBalance(d.ctx, "USDT")
		if spotFree > 1.0 {
			d.broker.Transfer(d.ctx, "USDT", spotFree, "MAIN_UMFUTURE")
		}
		return
	}

	futuresPrice := result.AvgPrice
	if futuresPrice <= 0 {
		futuresPrice = spotPrice
	}

	pos := &strategy.ArbPosition{
		Symbol:         symbol,
		BaseAsset:      baseAsset,
		SpotQty:        spotQty,
		SpotEntryPrice: spotPrice,
		FuturesQty:     spotQty,
		FuturesEntry:   futuresPrice,
		Basis:          futuresPrice - spotPrice,
		CapitalUsed:    capitalUSDT,
		OpenedAt:       time.Now(),
	}

	d.stateMu.Lock()
	d.state.ActivePositions[symbol] = pos
	d.stateMu.Unlock()

	log.Printf("[ARB] OPENED %s: spot=$%.2f, futures=$%.2f, basis=$%.4f, qty=%.6f, capital=$%.2f",
		symbol, spotPrice, futuresPrice, pos.Basis, spotQty, capitalUSDT)
}

// closeArbPosition closes both legs: Futures cover + Spot sell + transfer back.
func (d *BinanceArbDaemon) closeArbPosition(pos *strategy.ArbPosition, reason string) {
	log.Printf("[ARB] CLOSING %s: reason=%s, funding=$%.4f", pos.Symbol, reason, pos.FundingCollected)

	// Step 1: Close futures short (BUY reduceOnly)
	order := broker.Order{
		Symbol:     pos.Symbol,
		Side:       broker.OrderSideBuy,
		Type:       broker.OrderTypeMarket,
		Quantity:   pos.FuturesQty,
		ReduceOnly: true,
	}
	futuresResult, err := d.broker.PlaceOrder(d.ctx, order)
	if err != nil {
		log.Printf("[ARB] %s: futures close failed: %v — MANUAL INTERVENTION NEEDED", pos.Symbol, err)
		return
	}
	futuresExitPrice := futuresResult.AvgPrice

	// Step 2: Sell on Spot
	spotExitPrice, err := d.broker.SpotSell(d.ctx, pos.Symbol, pos.SpotQty)
	if err != nil {
		log.Printf("[ARB] %s: spot sell failed: %v — MANUAL INTERVENTION NEEDED (futures already closed)", pos.Symbol, err)
		return
	}

	// Step 3: Transfer USDT back from Spot to Futures
	time.Sleep(1 * time.Second)
	spotFreeUSDT, _ := d.broker.SpotGetBalance(d.ctx, "USDT")
	if spotFreeUSDT > 1.0 {
		if err := d.broker.Transfer(d.ctx, "USDT", spotFreeUSDT, "MAIN_UMFUTURE"); err != nil {
			log.Printf("[ARB] %s: transfer back failed: %v ($%.2f stuck in Spot)", pos.Symbol, err, spotFreeUSDT)
		}
	}

	// Calculate P&L
	spotPnL := (spotExitPrice - pos.SpotEntryPrice) * pos.SpotQty
	futuresPnL := (pos.FuturesEntry - futuresExitPrice) * pos.FuturesQty
	basisPnL := spotPnL + futuresPnL

	totalComm := strategy.EstimatedRoundTripCost(d.config, pos.CapitalUsed)
	netPnL := pos.FundingCollected + basisPnL - totalComm
	holdDuration := time.Since(pos.OpenedAt)

	log.Printf("[ARB] CLOSED %s: funding=$%.4f, basis=$%.4f, comm=$%.4f, net=$%.4f, hold=%s",
		pos.Symbol, pos.FundingCollected, basisPnL, totalComm, netPnL,
		holdDuration.Round(time.Minute))

	record := ArbTradeRecord{
		Symbol:           pos.Symbol,
		CapitalUsed:      pos.CapitalUsed,
		SpotEntryPrice:   pos.SpotEntryPrice,
		FuturesEntry:     pos.FuturesEntry,
		SpotExitPrice:    spotExitPrice,
		FuturesExitPrice: futuresExitPrice,
		FundingCollected: pos.FundingCollected,
		TotalCommission:  totalComm,
		NetPnL:           netPnL,
		HoldDuration:     holdDuration.Round(time.Minute).String(),
		FundingPeriods:   pos.FundingPayments,
		OpenedAt:         pos.OpenedAt,
		ClosedAt:         time.Now(),
		ExitReason:       reason,
	}

	d.stateMu.Lock()
	delete(d.state.ActivePositions, pos.Symbol)

	d.state.TotalStats.TotalTrades++
	d.state.TotalStats.TotalFunding += pos.FundingCollected
	d.state.TotalStats.TotalCommission += totalComm
	d.state.TotalStats.TotalNetPnL += netPnL
	d.state.TotalStats.TotalBasisPnL += basisPnL

	if netPnL > d.state.TotalStats.BestTrade {
		d.state.TotalStats.BestTrade = netPnL
	}
	if netPnL < d.state.TotalStats.WorstTrade {
		d.state.TotalStats.WorstTrade = netPnL
	}

	d.state.RecentTrades = append(d.state.RecentTrades, record)
	if len(d.state.RecentTrades) > 50 {
		d.state.RecentTrades = d.state.RecentTrades[len(d.state.RecentTrades)-50:]
	}
	d.stateMu.Unlock()

	d.saveState()
	d.saveStatusJSON()
}

// getAvailableCapital returns how much USDT the arb daemon can use.
func (d *BinanceArbDaemon) getAvailableCapital() float64 {
	bal, err := d.broker.GetBalance(d.ctx)
	if err != nil {
		log.Printf("[ARB] Balance check failed: %v", err)
		return 0
	}

	// Keep $50 buffer for scalp daemon margin needs
	safetyBuffer := 50.0
	available := bal.CashBalance - safetyBuffer
	if available > d.config.MaxCapitalUSDT {
		available = d.config.MaxCapitalUSDT
	}
	if available < 0 {
		available = 0
	}

	log.Printf("[ARB] Balance: total=$%.2f, available=$%.2f, arb_budget=$%.2f",
		bal.TotalEquity, bal.CashBalance, available)
	return available
}

// updateFundingIncome queries actual funding received from Binance.
func (d *BinanceArbDaemon) updateFundingIncome(pos *strategy.ArbPosition) {
	income, err := d.broker.GetFundingIncome(d.ctx, pos.Symbol, pos.OpenedAt)
	if err != nil {
		log.Printf("[ARB] %s: funding income query failed: %v", pos.Symbol, err)
		return
	}

	d.stateMu.Lock()
	if p, ok := d.state.ActivePositions[pos.Symbol]; ok {
		prevFunding := p.FundingCollected
		p.FundingCollected = income
		p.FundingPayments = int(time.Since(p.OpenedAt).Hours() / 8)

		if income != prevFunding {
			log.Printf("[ARB] %s: funding updated $%.4f -> $%.4f (%d periods)",
				pos.Symbol, prevFunding, income, p.FundingPayments)
		}
	}
	d.stateMu.Unlock()
}

// State persistence

func (d *BinanceArbDaemon) statePath() string {
	return filepath.Join(d.dataDir, "arb_state.json")
}

func (d *BinanceArbDaemon) statusPath() string {
	return filepath.Join(d.dataDir, "arb_status.json")
}

func (d *BinanceArbDaemon) loadState() {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()

	d.state = &ArbState{
		ActivePositions:  make(map[string]*strategy.ArbPosition),
		LastFundingRates: make(map[string]float64),
	}

	data, err := os.ReadFile(d.statePath())
	if err != nil {
		today := time.Now().UTC().Format("2006-01-02")
		d.state.TotalStats.StartDate = today
		log.Printf("[ARB] No saved state, starting fresh")
		return
	}

	if err := json.Unmarshal(data, d.state); err != nil {
		log.Printf("[ARB] Failed to parse state: %v, starting fresh", err)
		d.state.ActivePositions = make(map[string]*strategy.ArbPosition)
		d.state.LastFundingRates = make(map[string]float64)
		return
	}

	if d.state.ActivePositions == nil {
		d.state.ActivePositions = make(map[string]*strategy.ArbPosition)
	}
	if d.state.LastFundingRates == nil {
		d.state.LastFundingRates = make(map[string]float64)
	}

	log.Printf("[ARB] Restored state: %d positions, %d trades, funding=$%.4f, net=$%.4f",
		len(d.state.ActivePositions), d.state.TotalStats.TotalTrades,
		d.state.TotalStats.TotalFunding, d.state.TotalStats.TotalNetPnL)
}

func (d *BinanceArbDaemon) saveState() {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()

	data, err := json.MarshalIndent(d.state, "", "  ")
	if err != nil {
		log.Printf("[ARB] Failed to marshal state: %v", err)
		return
	}

	if err := os.WriteFile(d.statePath(), data, 0644); err != nil {
		log.Printf("[ARB] Failed to save state: %v", err)
	}
}

func (d *BinanceArbDaemon) saveStatusJSON() {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()

	status := map[string]interface{}{
		"strategy":           "funding-rate-arb",
		"exchange":           "binance-spot+futures",
		"pairs":              d.config.Pairs,
		"max_capital":        d.config.MaxCapitalUSDT,
		"min_funding_rate":   d.config.MinFundingRate,
		"active_positions":   d.state.ActivePositions,
		"last_funding_rates": d.state.LastFundingRates,
		"total": map[string]interface{}{
			"trades":        d.state.TotalStats.TotalTrades,
			"total_funding": d.state.TotalStats.TotalFunding,
			"commission":    d.state.TotalStats.TotalCommission,
			"net_pnl":       d.state.TotalStats.TotalNetPnL,
			"basis_pnl":     d.state.TotalStats.TotalBasisPnL,
			"best_trade":    d.state.TotalStats.BestTrade,
			"worst_trade":   d.state.TotalStats.WorstTrade,
			"start_date":    d.state.TotalStats.StartDate,
		},
		"last_check":    d.state.LastCheckTime,
		"updated_at":    time.Now(),
		"recent_trades": d.state.RecentTrades,
	}

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		log.Printf("[ARB] Failed to marshal status: %v", err)
		return
	}

	if err := os.WriteFile(d.statusPath(), data, 0644); err != nil {
		log.Printf("[ARB] Failed to save status: %v", err)
	}
}
