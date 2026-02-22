package dca

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"traveler/internal/broker"
	"traveler/internal/provider"
	"traveler/internal/strategy"
)

// AssetTarget defines allocation target for one coin
type AssetTarget struct {
	Symbol    string  `json:"symbol"`     // e.g., "KRW-BTC"
	TargetPct float64 `json:"target_pct"` // target allocation 0-1 (e.g., 0.40)
}

// FGTier defines Fear & Greed multiplier tier
type FGTier struct {
	MinValue   int     `json:"min_value"`
	MaxValue   int     `json:"max_value"`
	Multiplier float64 `json:"multiplier"`
	Label      string  `json:"label"`
}

// Config holds DCA configuration
type Config struct {
	Interval       time.Duration
	DCATimeKST     string  // "09:00"
	BaseDCAAmount  float64 // KRW per interval
	MinOrderAmount float64 // Upbit minimum: 5000

	Targets []AssetTarget

	RebalanceEnabled   bool
	RebalanceThreshold float64 // deviation % to trigger (15)

	FearGreedEnabled bool
	FGTiers          []FGTier

	EMA50Enabled       bool
	EMA50BuyMultiplier float64 // when price < EMA50 (2.0)
	EMA50SellMultiplier float64 // when price > EMA50 (0.5)

	ExtGreedSellPct float64 // sell % of position in extreme greed (0.05)

	// Profit-taking tiers (sorted by PctThreshold ascending)
	TakeProfitEnabled bool
	TakeProfitTiers   []TakeProfitTier
}

// TakeProfitTier defines a profit-taking level
type TakeProfitTier struct {
	PctThreshold float64 // PnL% to trigger (e.g., 30 = +30%)
	SellPct      float64 // portion of remaining position to sell (0-1)
	Label        string
}

// DefaultConfig returns the default DCA configuration
func DefaultConfig() Config {
	return Config{
		Interval:       24 * time.Hour,
		DCATimeKST:     "09:00",
		BaseDCAAmount:  50000,
		MinOrderAmount: 5000,
		Targets: []AssetTarget{
			{Symbol: "KRW-BTC", TargetPct: 0.40},
			{Symbol: "KRW-ETH", TargetPct: 0.20},
			{Symbol: "KRW-SOL", TargetPct: 0.15},
			{Symbol: "KRW-XRP", TargetPct: 0.10},
		},
		RebalanceEnabled:    true,
		RebalanceThreshold:  15.0,
		FearGreedEnabled:    true,
		FGTiers: []FGTier{
			{MinValue: 0, MaxValue: 24, Multiplier: 1.5, Label: "Extreme Fear"},
			{MinValue: 25, MaxValue: 44, Multiplier: 1.0, Label: "Fear"},
			{MinValue: 45, MaxValue: 55, Multiplier: 0.75, Label: "Neutral"},
			{MinValue: 56, MaxValue: 74, Multiplier: 0.5, Label: "Greed"},
			{MinValue: 75, MaxValue: 100, Multiplier: 0.25, Label: "Extreme Greed"},
		},
		EMA50Enabled:        true,
		EMA50BuyMultiplier:  2.0,
		EMA50SellMultiplier: 0.5,
		ExtGreedSellPct:     0.05,
		TakeProfitEnabled:   true,
		TakeProfitTiers: []TakeProfitTier{
			// Crypto is highly volatile — tiers set accordingly.
			// +30%: lock in initial gains, recover principal
			// +60%: take substantial profit while leaving upside
			// +100%: aggressive exit, keep small moonbag
			{PctThreshold: 30, SellPct: 0.25, Label: "take-profit-1"},
			{PctThreshold: 60, SellPct: 0.35, Label: "take-profit-2"},
			{PctThreshold: 100, SellPct: 0.50, Label: "take-profit-3"},
		},
	}
}

// DCAResult describes the outcome of one DCA cycle
type DCAResult struct {
	CycleNumber int
	FearGreed   int
	FGLabel     string
	Multiplier  float64
	TotalSpent  float64
	Buys        []DCABuy
	Sells       []DCASell
	Rebalanced  bool
}

// RebalanceTrade describes a rebalancing action
type RebalanceTrade struct {
	Symbol  string  `json:"symbol"`
	Side    string  `json:"side"`   // "buy" or "sell"
	Amount  float64 `json:"amount"` // KRW amount
	Reason  string  `json:"reason"`
}

// DCAStatus is the web-facing status snapshot
type DCAStatus struct {
	BaseDCAAmount     float64        `json:"base_dca_amount"`
	Interval          string         `json:"interval"`
	FearGreed         int            `json:"fear_greed"`
	FGLabel           string         `json:"fg_label"`
	CurrentMultiplier float64        `json:"current_multiplier"`
	NextDCATime       time.Time      `json:"next_dca_time"`
	LastDCATime       time.Time      `json:"last_dca_time"`
	TotalInvested     float64        `json:"total_invested"`
	CurrentValue      float64        `json:"current_value"`
	UnrealizedPnL     float64        `json:"unrealized_pnl"`
	UnrealizedPct     float64        `json:"unrealized_pct"`
	NeedsRebalance    bool           `json:"needs_rebalance"`
	Assets            []AssetStatus  `json:"assets"`
	TotalDCACycles    int            `json:"total_dca_cycles"`
	History           []DCAHistoryEntry `json:"history"`
}

// AssetStatus shows current state of a DCA asset
type AssetStatus struct {
	Symbol        string  `json:"symbol"`
	Name          string  `json:"name"`
	TargetPct     float64 `json:"target_pct"`
	CurrentPct    float64 `json:"current_pct"`
	Deviation     float64 `json:"deviation"`
	TotalInvested float64 `json:"total_invested"`
	CurrentValue  float64 `json:"current_value"`
	AvgCost       float64 `json:"avg_cost"`
	CurrentPrice  float64 `json:"current_price"`
	Quantity      float64 `json:"quantity"`
	PnL           float64 `json:"pnl"`
	PnLPct        float64 `json:"pnl_pct"`
}

// Engine executes DCA logic
type Engine struct {
	config   Config
	broker   broker.Broker
	provider provider.Provider
	fgClient *provider.FearGreedClient
	state    *StateManager
	dataDir  string
}

// NewEngine creates a new DCA engine
func NewEngine(cfg Config, b broker.Broker, p provider.Provider, dataDir string) *Engine {
	return &Engine{
		config:   cfg,
		broker:   b,
		provider: p,
		fgClient: provider.NewFearGreedClient(),
		state:    NewStateManager(dataDir),
		dataDir:  dataDir,
	}
}

// Run executes one DCA cycle
func (e *Engine) Run(ctx context.Context) (*DCAResult, error) {
	result := &DCAResult{}

	// 1. Fetch Fear & Greed index
	fgData, err := e.fgClient.GetIndex(ctx)
	if err != nil {
		log.Printf("[DCA] F&G fetch failed (using neutral): %v", err)
	}
	result.FearGreed = fgData.Value
	result.FGLabel = fgData.Classification

	// 2. Calculate F&G multiplier
	fgMult := 1.0
	if e.config.FearGreedEnabled {
		fgMult = e.getFGMultiplier(fgData.Value)
	}

	log.Printf("[DCA] Fear & Greed: %d (%s), multiplier: %.2fx", fgData.Value, fgData.Classification, fgMult)

	// 3. Process each target asset
	var totalSpent float64
	var buys []DCABuy
	var sells []DCASell

	for _, target := range e.config.Targets {
		// Calculate EMA50 multiplier
		ema50Mult := 1.0
		if e.config.EMA50Enabled {
			ema50Mult = e.getEMA50Multiplier(ctx, target.Symbol)
		}

		combinedMult := fgMult * ema50Mult
		amount := e.config.BaseDCAAmount * target.TargetPct * combinedMult

		// Round to integer (KRW has no decimals)
		amount = math.Floor(amount)

		if amount < e.config.MinOrderAmount {
			log.Printf("[DCA] %s: ₩%.0f < minimum ₩%.0f, skipping", target.Symbol, amount, e.config.MinOrderAmount)
			continue
		}

		// Place market buy
		buy, err := e.placeBuy(ctx, target.Symbol, amount)
		if err != nil {
			log.Printf("[DCA] %s buy failed: %v", target.Symbol, err)
			continue
		}
		buys = append(buys, *buy)
		totalSpent += buy.Amount
		log.Printf("[DCA] BUY %s: ₩%.0f → %.8f @ ₩%.0f (F&G=%.2fx, EMA50=%.2fx)",
			target.Symbol, buy.Amount, buy.Quantity, buy.Price, fgMult, ema50Mult)
	}

	// 4. Extreme Greed partial sell
	if fgData.Value >= 75 && e.config.ExtGreedSellPct > 0 {
		log.Printf("[DCA] Extreme Greed (%d) — selling %.0f%% of positions", fgData.Value, e.config.ExtGreedSellPct*100)
		for _, target := range e.config.Targets {
			sell, err := e.partialSell(ctx, target.Symbol, e.config.ExtGreedSellPct)
			if err != nil {
				log.Printf("[DCA] %s sell failed: %v", target.Symbol, err)
				continue
			}
			if sell != nil {
				sells = append(sells, *sell)
				log.Printf("[DCA] SELL %s: %.8f @ ₩%.0f = ₩%.0f", sell.Symbol, sell.Quantity, sell.Price, sell.Amount)
			}
		}
	}

	// 4b. Take-profit check — sell at tiered profit thresholds
	if e.config.TakeProfitEnabled {
		tpSells := e.checkTakeProfit(ctx)
		sells = append(sells, tpSells...)
	}

	result.Multiplier = fgMult
	result.TotalSpent = totalSpent
	result.Buys = buys
	result.Sells = sells

	// 5. Check rebalancing (weekly)
	if e.config.RebalanceEnabled {
		state := e.state.GetState()
		if time.Since(state.LastRebalance) > 7*24*time.Hour {
			trades, err := e.CheckRebalance(ctx)
			if err != nil {
				log.Printf("[DCA] Rebalance check failed: %v", err)
			} else if len(trades) > 0 {
				if err := e.ExecuteRebalance(ctx, trades); err != nil {
					log.Printf("[DCA] Rebalance execution failed: %v", err)
				} else {
					result.Rebalanced = true
					e.state.RecordRebalance()
				}
			}
		}
	}

	// 6. Complete cycle and save state
	state := e.state.GetState()
	result.CycleNumber = state.TotalDCACycles + 1

	nextDCA := e.calculateNextDCATime()
	e.state.CompleteCycle(DCAHistoryEntry{
		Timestamp:   time.Now(),
		FearGreed:   result.FearGreed,
		FGLabel:     result.FGLabel,
		TotalAmount: totalSpent,
		Multiplier:  result.Multiplier,
		Buys:        buys,
		Sells:       sells,
		Rebalanced:  result.Rebalanced,
	}, nextDCA)

	return result, nil
}

// CheckRebalance checks if any asset deviates from target by more than threshold
func (e *Engine) CheckRebalance(ctx context.Context) ([]RebalanceTrade, error) {
	bal, err := e.broker.GetBalance(ctx)
	if err != nil {
		return nil, fmt.Errorf("get balance: %w", err)
	}

	// Build current portfolio values from DCA-tracked assets only
	state := e.state.GetState()
	var totalValue float64
	assetValues := make(map[string]float64)

	for _, target := range e.config.Targets {
		a, ok := state.Assets[target.Symbol]
		if !ok || a.TotalQuantity <= 0 {
			continue
		}

		// Find current price from balance positions
		var currentPrice float64
		for _, pos := range bal.Positions {
			if pos.Symbol == target.Symbol {
				if pos.Quantity > 0 {
					currentPrice = (pos.MarketValue) / pos.Quantity
				}
				break
			}
		}

		if currentPrice <= 0 {
			// Fetch price from provider
			candles, cerr := e.provider.GetDailyCandles(ctx, target.Symbol, 1)
			if cerr == nil && len(candles) > 0 {
				currentPrice = candles[len(candles)-1].Close
			}
		}

		if currentPrice > 0 {
			value := a.TotalQuantity * currentPrice
			assetValues[target.Symbol] = value
			totalValue += value
		}
	}

	if totalValue <= 0 {
		return nil, nil
	}

	// Check deviations
	var trades []RebalanceTrade
	for _, target := range e.config.Targets {
		currentValue := assetValues[target.Symbol]
		currentPct := currentValue / totalValue
		deviation := (currentPct - target.TargetPct) / target.TargetPct * 100

		if math.Abs(deviation) > e.config.RebalanceThreshold {
			diff := currentValue - (target.TargetPct * totalValue)
			if diff > e.config.MinOrderAmount {
				// Over-allocated — sell excess
				trades = append(trades, RebalanceTrade{
					Symbol: target.Symbol,
					Side:   "sell",
					Amount: math.Floor(diff),
					Reason: fmt.Sprintf("over-allocated +%.0f%% (target %.0f%%)", deviation, target.TargetPct*100),
				})
			} else if diff < -e.config.MinOrderAmount {
				// Under-allocated — buy deficit
				trades = append(trades, RebalanceTrade{
					Symbol: target.Symbol,
					Side:   "buy",
					Amount: math.Floor(-diff),
					Reason: fmt.Sprintf("under-allocated %.0f%% (target %.0f%%)", deviation, target.TargetPct*100),
				})
			}
		}
	}

	if len(trades) > 0 {
		log.Printf("[DCA] Rebalance needed: %d trades", len(trades))
		for _, t := range trades {
			log.Printf("[DCA]   %s %s ₩%.0f (%s)", t.Side, t.Symbol, t.Amount, t.Reason)
		}
	}

	return trades, nil
}

// ExecuteRebalance performs rebalancing trades
func (e *Engine) ExecuteRebalance(ctx context.Context, trades []RebalanceTrade) error {
	// Execute sells first, then buys
	for _, t := range trades {
		if t.Side != "sell" {
			continue
		}
		if err := e.rebalanceSell(ctx, t); err != nil {
			log.Printf("[DCA] Rebalance sell %s failed: %v", t.Symbol, err)
		}
	}
	for _, t := range trades {
		if t.Side != "buy" {
			continue
		}
		if err := e.rebalanceBuy(ctx, t); err != nil {
			log.Printf("[DCA] Rebalance buy %s failed: %v", t.Symbol, err)
		}
	}
	return nil
}

// GetStatus returns current DCA status for web UI
func (e *Engine) GetStatus(ctx context.Context) *DCAStatus {
	state := e.state.GetState()

	fgData, _ := e.fgClient.GetIndex(ctx)
	fgMult := 1.0
	if e.config.FearGreedEnabled && fgData != nil {
		fgMult = e.getFGMultiplier(fgData.Value)
	}

	status := &DCAStatus{
		BaseDCAAmount:     e.config.BaseDCAAmount,
		Interval:          formatDuration(e.config.Interval),
		FearGreed:         fgData.Value,
		FGLabel:           fgData.Classification,
		CurrentMultiplier: fgMult,
		NextDCATime:       state.NextDCATime,
		LastDCATime:       state.LastDCATime,
		TotalInvested:     state.TotalInvested,
		TotalDCACycles:    state.TotalDCACycles,
		History:           state.History,
	}

	// Build asset statuses
	var totalCurrentValue float64
	for _, target := range e.config.Targets {
		a := state.Assets[target.Symbol]
		as := AssetStatus{
			Symbol:    target.Symbol,
			TargetPct: target.TargetPct * 100,
		}

		if a != nil {
			as.TotalInvested = a.TotalInvested
			as.AvgCost = a.AvgCost
			as.Quantity = a.TotalQuantity

			// Fetch current price
			candles, err := e.provider.GetDailyCandles(ctx, target.Symbol, 1)
			if err == nil && len(candles) > 0 {
				as.CurrentPrice = candles[len(candles)-1].Close
				as.CurrentValue = as.Quantity * as.CurrentPrice
				as.PnL = as.CurrentValue - as.TotalInvested
				if as.TotalInvested > 0 {
					as.PnLPct = as.PnL / as.TotalInvested * 100
				}
				totalCurrentValue += as.CurrentValue
			}
		}

		status.Assets = append(status.Assets, as)
	}

	// Calculate allocation percentages
	if totalCurrentValue > 0 {
		for i := range status.Assets {
			status.Assets[i].CurrentPct = status.Assets[i].CurrentValue / totalCurrentValue * 100
			status.Assets[i].Deviation = status.Assets[i].CurrentPct - status.Assets[i].TargetPct
		}
	}

	status.CurrentValue = totalCurrentValue
	status.UnrealizedPnL = totalCurrentValue - state.TotalInvested
	if state.TotalInvested > 0 {
		status.UnrealizedPct = status.UnrealizedPnL / state.TotalInvested * 100
	}

	return status
}

// GetNextDCATime returns the next scheduled DCA time
func (e *Engine) GetNextDCATime() time.Time {
	state := e.state.GetState()
	// First cycle ever → run immediately
	if state.TotalDCACycles == 0 && state.NextDCATime.IsZero() {
		return time.Now()
	}
	if state.NextDCATime.IsZero() {
		return e.calculateNextDCATime()
	}
	return state.NextDCATime
}

// --- internal helpers ---

func (e *Engine) getFGMultiplier(value int) float64 {
	for _, tier := range e.config.FGTiers {
		if value >= tier.MinValue && value <= tier.MaxValue {
			return tier.Multiplier
		}
	}
	return 1.0
}

func (e *Engine) getEMA50Multiplier(ctx context.Context, symbol string) float64 {
	candles, err := e.provider.GetDailyCandles(ctx, symbol, 55)
	if err != nil || len(candles) < 50 {
		return 1.0
	}

	ema50 := strategy.CalculateEMA(candles, 50)
	currentPrice := candles[len(candles)-1].Close

	if currentPrice < ema50 {
		return e.config.EMA50BuyMultiplier
	}
	return e.config.EMA50SellMultiplier
}

func (e *Engine) placeBuy(ctx context.Context, symbol string, amount float64) (*DCABuy, error) {
	order := broker.Order{
		Symbol: symbol,
		Side:   broker.OrderSideBuy,
		Type:   broker.OrderTypeMarket,
		Amount: amount,
	}

	result, err := e.broker.PlaceOrder(ctx, order)
	if err != nil {
		return nil, err
	}

	// Wait for fill
	time.Sleep(2 * time.Second)

	// Get executed price/quantity
	var price, qty float64
	if result.AvgPrice > 0 && result.FilledQty > 0 {
		price = result.AvgPrice
		qty = result.FilledQty
	} else {
		// Fetch from order status
		if result.OrderID != "" {
			orderInfo, err := e.broker.GetOrder(ctx, result.OrderID)
			if err == nil && orderInfo.AvgPrice > 0 {
				price = orderInfo.AvgPrice
				qty = orderInfo.FilledQty
			}
		}
	}

	if price <= 0 || qty <= 0 {
		// Estimate from amount
		candles, cerr := e.provider.GetDailyCandles(ctx, symbol, 1)
		if cerr == nil && len(candles) > 0 {
			price = candles[len(candles)-1].Close
			qty = amount / price
		}
	}

	buy := &DCABuy{
		Symbol:   symbol,
		Amount:   amount,
		Quantity: qty,
		Price:    price,
	}

	e.state.RecordBuy(symbol, amount, qty, price)
	return buy, nil
}

func (e *Engine) partialSell(ctx context.Context, symbol string, pct float64) (*DCASell, error) {
	state := e.state.GetState()
	a, ok := state.Assets[symbol]
	if !ok || a.TotalQuantity <= 0 {
		return nil, nil
	}

	sellQty := a.TotalQuantity * pct
	if sellQty <= 0 {
		return nil, nil
	}

	order := broker.Order{
		Symbol:   symbol,
		Side:     broker.OrderSideSell,
		Type:     broker.OrderTypeMarket,
		Quantity: sellQty,
	}

	result, err := e.broker.PlaceOrder(ctx, order)
	if err != nil {
		return nil, err
	}

	time.Sleep(2 * time.Second)

	var price, amount float64
	if result.AvgPrice > 0 {
		price = result.AvgPrice
		amount = sellQty * price
	} else if result.OrderID != "" {
		orderInfo, oerr := e.broker.GetOrder(ctx, result.OrderID)
		if oerr == nil && orderInfo.AvgPrice > 0 {
			price = orderInfo.AvgPrice
			amount = sellQty * price
		}
	}

	sell := &DCASell{
		Symbol:   symbol,
		Quantity: sellQty,
		Price:    price,
		Amount:   amount,
	}

	e.state.RecordSell(symbol, sellQty, price, amount)
	return sell, nil
}

func (e *Engine) rebalanceSell(ctx context.Context, t RebalanceTrade) error {
	state := e.state.GetState()
	a, ok := state.Assets[t.Symbol]
	if !ok || a.TotalQuantity <= 0 {
		return nil
	}

	// Estimate quantity from amount / current price
	candles, err := e.provider.GetDailyCandles(ctx, t.Symbol, 1)
	if err != nil || len(candles) == 0 {
		return fmt.Errorf("no price data for %s", t.Symbol)
	}
	price := candles[len(candles)-1].Close
	qty := t.Amount / price

	if qty > a.TotalQuantity {
		qty = a.TotalQuantity
	}

	order := broker.Order{
		Symbol:   t.Symbol,
		Side:     broker.OrderSideSell,
		Type:     broker.OrderTypeMarket,
		Quantity: qty,
	}

	_, err = e.broker.PlaceOrder(ctx, order)
	if err != nil {
		return err
	}

	time.Sleep(2 * time.Second)
	e.state.RecordSell(t.Symbol, qty, price, t.Amount)
	log.Printf("[DCA] Rebalance SELL %s: %.8f @ ₩%.0f = ₩%.0f", t.Symbol, qty, price, t.Amount)
	return nil
}

func (e *Engine) rebalanceBuy(ctx context.Context, t RebalanceTrade) error {
	if t.Amount < e.config.MinOrderAmount {
		return nil
	}

	order := broker.Order{
		Symbol: t.Symbol,
		Side:   broker.OrderSideBuy,
		Type:   broker.OrderTypeMarket,
		Amount: math.Floor(t.Amount),
	}

	result, err := e.broker.PlaceOrder(ctx, order)
	if err != nil {
		return err
	}

	time.Sleep(2 * time.Second)

	var price, qty float64
	if result.AvgPrice > 0 && result.FilledQty > 0 {
		price = result.AvgPrice
		qty = result.FilledQty
	}

	e.state.RecordBuy(t.Symbol, t.Amount, qty, price)
	log.Printf("[DCA] Rebalance BUY %s: ₩%.0f → %.8f @ ₩%.0f", t.Symbol, t.Amount, qty, price)
	return nil
}

// checkTakeProfit checks each DCA asset against tiered profit thresholds
// and executes partial sells for newly triggered tiers.
func (e *Engine) checkTakeProfit(ctx context.Context) []DCASell {
	var sells []DCASell
	state := e.state.GetState()

	for _, target := range e.config.Targets {
		a, ok := state.Assets[target.Symbol]
		if !ok || a.TotalQuantity <= 0 || a.AvgCost <= 0 {
			continue
		}

		// Get current price
		candles, err := e.provider.GetDailyCandles(ctx, target.Symbol, 1)
		if err != nil || len(candles) == 0 {
			continue
		}
		currentPrice := candles[len(candles)-1].Close
		if currentPrice <= 0 {
			continue
		}

		pnlPct := (currentPrice - a.AvgCost) / a.AvgCost * 100

		// If price dropped below the lowest tier, reset all triggered tiers
		// so they can fire again on the next rally
		if pnlPct < e.config.TakeProfitTiers[0].PctThreshold {
			e.state.ResetTriggeredTiers(target.Symbol)
			continue
		}

		// Check each tier (ascending)
		for _, tier := range e.config.TakeProfitTiers {
			if pnlPct < tier.PctThreshold {
				break // tiers are sorted ascending, no point checking higher ones
			}
			if e.state.HasTriggeredTier(target.Symbol, tier.PctThreshold) {
				continue // already sold at this tier
			}

			// Sell portion of remaining DCA position
			log.Printf("[DCA] TAKE-PROFIT %s: PnL %.1f%% >= +%.0f%% tier → selling %.0f%%",
				target.Symbol, pnlPct, tier.PctThreshold, tier.SellPct*100)

			sell, err := e.partialSell(ctx, target.Symbol, tier.SellPct)
			if err != nil {
				log.Printf("[DCA] %s take-profit sell failed: %v", target.Symbol, err)
				continue
			}
			if sell != nil {
				sells = append(sells, *sell)
				e.state.RecordTakeProfit(target.Symbol, tier.PctThreshold)
				log.Printf("[DCA] TAKE-PROFIT SELL %s: %.8f @ ₩%.0f = ₩%.0f (%s)",
					sell.Symbol, sell.Quantity, sell.Price, sell.Amount, tier.Label)
			}
		}
	}

	return sells
}

func (e *Engine) calculateNextDCATime() time.Time {
	loc, _ := time.LoadLocation("Asia/Seoul")
	if loc == nil {
		loc = time.FixedZone("KST", 9*60*60)
	}

	now := time.Now().In(loc)

	// Parse target time
	hour, min := 9, 0
	fmt.Sscanf(e.config.DCATimeKST, "%d:%d", &hour, &min)

	next := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, loc)
	if next.Before(now) {
		next = next.Add(e.config.Interval)
	}

	return next
}

func formatDuration(d time.Duration) string {
	switch {
	case d >= 7*24*time.Hour:
		return "weekly"
	case d >= 24*time.Hour:
		return "daily"
	default:
		return d.String()
	}
}

