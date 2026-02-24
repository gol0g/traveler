package dca

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
	"traveler/internal/provider"
	"traveler/internal/strategy"
)

// KR DCA: RSI-based fear gauge + EMA50 overlay for KODEX 200 (069500)
// Share-based buying (integer quantities), weekly schedule

// KRDCAConfig holds configuration for KR stock DCA
type KRDCAConfig struct {
	Symbol         string        // "069500" (KODEX 200)
	SymbolName     string        // "KODEX 200"
	BaseShares     int           // base shares per cycle (1)
	Interval       time.Duration // weekly
	DCATimeKST     string        // "09:30"
	DCAWeekday     time.Weekday  // Monday

	// RSI fear gauge (replaces F&G for stocks)
	RSIEnabled bool
	RSIPeriod  int // 14
	RSITiers   []RSITier

	// EMA50 overlay
	EMA50Enabled    bool
	EMA50BonusShare int // bonus shares when price < EMA50

	// Take-profit
	TakeProfitEnabled bool
	TakeProfitTiers   []TakeProfitTier

	// RSI high sell
	RSIHighSellThreshold float64 // RSI > this → sell signal
	RSIHighSellPct       float64 // sell this % of position
}

// RSITier maps RSI ranges to share multipliers
type RSITier struct {
	MinRSI     float64 `json:"min_rsi"`
	MaxRSI     float64 `json:"max_rsi"`
	Shares     int     `json:"shares"`
	Label      string  `json:"label"`
	Action     string  `json:"action"` // "buy", "skip", "sell"
}

// DefaultKRDCAConfig returns default config for ~₩100만 budget
func DefaultKRDCAConfig() KRDCAConfig {
	return KRDCAConfig{
		Symbol:     "069500",
		SymbolName: "KODEX 200",
		BaseShares: 1,
		Interval:   7 * 24 * time.Hour,
		DCATimeKST: "09:30",
		DCAWeekday: time.Monday,

		RSIEnabled: true,
		RSIPeriod:  14,
		RSITiers: []RSITier{
			{MinRSI: 0, MaxRSI: 25, Shares: 3, Label: "Extreme Oversold", Action: "buy"},
			{MinRSI: 25, MaxRSI: 35, Shares: 2, Label: "Oversold", Action: "buy"},
			{MinRSI: 35, MaxRSI: 50, Shares: 1, Label: "Neutral-Low", Action: "buy"},
			{MinRSI: 50, MaxRSI: 65, Shares: 0, Label: "Neutral-High", Action: "skip"},
			{MinRSI: 65, MaxRSI: 100, Shares: 0, Label: "Overbought", Action: "sell"},
		},

		EMA50Enabled:    true,
		EMA50BonusShare: 1,

		TakeProfitEnabled: true,
		TakeProfitTiers: []TakeProfitTier{
			{PctThreshold: 15, SellPct: 0.20, Label: "take-profit-1"},
			{PctThreshold: 30, SellPct: 0.30, Label: "take-profit-2"},
			{PctThreshold: 50, SellPct: 0.40, Label: "take-profit-3"},
		},

		RSIHighSellThreshold: 65,
		RSIHighSellPct:       0.10,
	}
}

// KRDCAResult describes the outcome of one KR DCA cycle
type KRDCAResult struct {
	CycleNumber int
	RSI         float64
	RSILabel    string
	Shares      int
	Action      string // "buy", "skip", "sell"
	Price       float64
	Amount      float64 // total KRW spent/received
	EMA50Bonus  bool
}

// KRDCAStatus is the web-facing status snapshot
type KRDCAStatus struct {
	Symbol         string             `json:"symbol"`
	SymbolName     string             `json:"symbol_name"`
	BaseShares     int                `json:"base_shares"`
	Interval       string             `json:"interval"`
	RSI            float64            `json:"rsi"`
	RSILabel       string             `json:"rsi_label"`
	CurrentAction  string             `json:"current_action"`
	CurrentShares  int                `json:"current_shares"`
	EMA50          float64            `json:"ema50"`
	PriceVsEMA50   string             `json:"price_vs_ema50"` // "above" or "below"
	NextDCATime    time.Time          `json:"next_dca_time"`
	LastDCATime    time.Time          `json:"last_dca_time"`
	TotalInvested  float64            `json:"total_invested"`
	CurrentValue   float64            `json:"current_value"`
	UnrealizedPnL  float64            `json:"unrealized_pnl"`
	UnrealizedPct  float64            `json:"unrealized_pct"`
	TotalShares    float64            `json:"total_shares"`
	AvgCost        float64            `json:"avg_cost"`
	CurrentPrice   float64            `json:"current_price"`
	TotalDCACycles int                `json:"total_dca_cycles"`
	History        []KRDCAHistoryEntry `json:"history"`
}

// KRDCAHistoryEntry records each KR DCA execution
type KRDCAHistoryEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	RSI        float64   `json:"rsi"`
	RSILabel   string    `json:"rsi_label"`
	Action     string    `json:"action"`
	Shares     int       `json:"shares"`
	Price      float64   `json:"price"`
	Amount     float64   `json:"amount"`
	EMA50Bonus bool      `json:"ema50_bonus,omitempty"`
}

// KRDCAState persists KR DCA progress
type KRDCAState struct {
	NextDCATime    time.Time              `json:"next_dca_time"`
	LastDCATime    time.Time              `json:"last_dca_time"`
	TotalInvested  float64                `json:"total_invested"`
	TotalShares    float64                `json:"total_shares"`
	AvgCost        float64                `json:"avg_cost"`
	TotalDCACycles int                    `json:"total_dca_cycles"`
	TotalSold      float64                `json:"total_sold"`
	RealizedPnL    float64                `json:"realized_pnl"`
	TriggeredTiers []float64              `json:"triggered_tiers,omitempty"`
	History        []KRDCAHistoryEntry    `json:"history"`
}

// KRDCAStateManager manages persistent KR DCA state
type KRDCAStateManager struct {
	mu       sync.RWMutex
	state    KRDCAState
	filepath string
}

// NewKRDCAStateManager loads or initializes KR DCA state
func NewKRDCAStateManager(dataDir string) *KRDCAStateManager {
	fp := filepath.Join(dataDir, "kr_dca_state.json")
	sm := &KRDCAStateManager{filepath: fp}

	if data, err := os.ReadFile(fp); err == nil {
		if json.Unmarshal(data, &sm.state) == nil {
			log.Printf("[KR-DCA] Loaded state: %d cycles, ₩%.0f invested, %.0f shares",
				sm.state.TotalDCACycles, sm.state.TotalInvested, sm.state.TotalShares)
			return sm
		}
	}

	sm.state = KRDCAState{}
	return sm
}

func (sm *KRDCAStateManager) GetState() KRDCAState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state
}

func (sm *KRDCAStateManager) RecordBuy(shares int, price, amount float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.state.TotalInvested += amount
	sm.state.TotalShares += float64(shares)
	if sm.state.TotalShares > 0 {
		sm.state.AvgCost = sm.state.TotalInvested / sm.state.TotalShares
	}
	sm.save()
}

func (sm *KRDCAStateManager) RecordSell(shares int, price, amount float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	soldShares := float64(shares)
	costBasis := soldShares * sm.state.AvgCost
	sm.state.RealizedPnL += amount - costBasis
	sm.state.TotalSold += amount

	sm.state.TotalShares -= soldShares
	if sm.state.TotalShares < 0 {
		sm.state.TotalShares = 0
	}
	// Reduce invested proportionally
	sm.state.TotalInvested -= costBasis
	if sm.state.TotalInvested < 0 {
		sm.state.TotalInvested = 0
	}
	sm.save()
}

func (sm *KRDCAStateManager) CompleteCycle(entry KRDCAHistoryEntry, nextDCA time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.state.TotalDCACycles++
	sm.state.LastDCATime = entry.Timestamp
	sm.state.NextDCATime = nextDCA

	sm.state.History = append(sm.state.History, entry)
	if len(sm.state.History) > 52 { // keep ~1 year of weekly entries
		sm.state.History = sm.state.History[len(sm.state.History)-52:]
	}
	sm.save()
}

func (sm *KRDCAStateManager) HasTriggeredTier(tierPct float64) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for _, t := range sm.state.TriggeredTiers {
		if t == tierPct {
			return true
		}
	}
	return false
}

func (sm *KRDCAStateManager) RecordTakeProfit(tierPct float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, t := range sm.state.TriggeredTiers {
		if t == tierPct {
			return
		}
	}
	sm.state.TriggeredTiers = append(sm.state.TriggeredTiers, tierPct)
	sm.save()
}

func (sm *KRDCAStateManager) ResetTriggeredTiers() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if len(sm.state.TriggeredTiers) > 0 {
		sm.state.TriggeredTiers = nil
		sm.save()
	}
}

func (sm *KRDCAStateManager) SetNextDCATime(t time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.state.NextDCATime = t
	sm.save()
}

func (sm *KRDCAStateManager) save() {
	data, err := json.MarshalIndent(sm.state, "", "  ")
	if err != nil {
		log.Printf("[KR-DCA] Failed to marshal state: %v", err)
		return
	}
	if err := os.WriteFile(sm.filepath, data, 0644); err != nil {
		log.Printf("[KR-DCA] Failed to save state: %v", err)
	}
}

// KRDCAEngine executes KR stock DCA logic
type KRDCAEngine struct {
	config   KRDCAConfig
	broker   broker.Broker
	provider provider.Provider
	state    *KRDCAStateManager
	dataDir  string
}

// NewKRDCAEngine creates a new KR DCA engine
func NewKRDCAEngine(cfg KRDCAConfig, b broker.Broker, p provider.Provider, dataDir string) *KRDCAEngine {
	return &KRDCAEngine{
		config:   cfg,
		broker:   b,
		provider: p,
		state:    NewKRDCAStateManager(dataDir),
		dataDir:  dataDir,
	}
}

// Run executes one KR DCA cycle
func (e *KRDCAEngine) Run(ctx context.Context) (*KRDCAResult, error) {
	result := &KRDCAResult{}

	// 1. Fetch candles for RSI and EMA50 calculation
	candles, err := e.provider.GetDailyCandles(ctx, e.config.Symbol, 60)
	if err != nil {
		return nil, fmt.Errorf("fetch %s candles: %w", e.config.Symbol, err)
	}
	if len(candles) < 20 {
		return nil, fmt.Errorf("insufficient candle data: %d < 20", len(candles))
	}

	currentPrice := candles[len(candles)-1].Close
	result.Price = currentPrice

	// 2. Calculate RSI
	rsi := strategy.CalculateRSI(candles, e.config.RSIPeriod)
	result.RSI = rsi
	log.Printf("[KR-DCA] %s: price=₩%.0f, RSI(14)=%.1f", e.config.SymbolName, currentPrice, rsi)

	// 3. Determine action from RSI tier
	tier := e.getRSITier(rsi)
	result.RSILabel = tier.Label
	result.Action = tier.Action
	result.Shares = tier.Shares

	// 4. EMA50 bonus
	ema50Bonus := false
	if e.config.EMA50Enabled && tier.Action == "buy" {
		ema50 := strategy.CalculateEMA(candles, 50)
		if currentPrice < ema50 {
			result.Shares += e.config.EMA50BonusShare
			ema50Bonus = true
			log.Printf("[KR-DCA] EMA50 bonus: price ₩%.0f < EMA50 ₩%.0f, +%d share", currentPrice, ema50, e.config.EMA50BonusShare)
		}
	}
	result.EMA50Bonus = ema50Bonus

	// 5. Execute action
	switch tier.Action {
	case "buy":
		if result.Shares > 0 {
			amount, err := e.placeBuy(ctx, result.Shares, currentPrice)
			if err != nil {
				log.Printf("[KR-DCA] Buy failed: %v", err)
				return result, err
			}
			result.Amount = amount
			log.Printf("[KR-DCA] BUY %d shares of %s @ ₩%.0f = ₩%.0f (RSI=%.1f %s%s)",
				result.Shares, e.config.SymbolName, currentPrice, amount,
				rsi, tier.Label, boolStr(ema50Bonus, " +EMA50"))
		}

	case "sell":
		if e.config.RSIHighSellPct > 0 {
			state := e.state.GetState()
			if state.TotalShares > 0 {
				sellShares := int(math.Floor(state.TotalShares * e.config.RSIHighSellPct))
				if sellShares < 1 {
					sellShares = 1
				}
				if float64(sellShares) > state.TotalShares {
					sellShares = int(state.TotalShares)
				}
				amount, err := e.placeSell(ctx, sellShares, currentPrice)
				if err != nil {
					log.Printf("[KR-DCA] RSI-high sell failed: %v", err)
				} else {
					result.Amount = amount
					result.Shares = sellShares
					log.Printf("[KR-DCA] SELL %d shares of %s @ ₩%.0f = ₩%.0f (RSI=%.1f Overbought)",
						sellShares, e.config.SymbolName, currentPrice, amount, rsi)
				}
			}
		}

	case "skip":
		log.Printf("[KR-DCA] SKIP: RSI=%.1f (%s), no action", rsi, tier.Label)
	}

	// 6. Take-profit check
	if e.config.TakeProfitEnabled && tier.Action != "sell" {
		e.checkTakeProfit(ctx, currentPrice)
	}

	// 7. Complete cycle
	state := e.state.GetState()
	result.CycleNumber = state.TotalDCACycles + 1

	nextDCA := e.calculateNextDCATime()
	e.state.CompleteCycle(KRDCAHistoryEntry{
		Timestamp:  time.Now(),
		RSI:        rsi,
		RSILabel:   tier.Label,
		Action:     tier.Action,
		Shares:     result.Shares,
		Price:      currentPrice,
		Amount:     result.Amount,
		EMA50Bonus: ema50Bonus,
	}, nextDCA)

	return result, nil
}

func (e *KRDCAEngine) placeBuy(ctx context.Context, shares int, estimatedPrice float64) (float64, error) {
	order := broker.Order{
		Symbol:   e.config.Symbol,
		Side:     broker.OrderSideBuy,
		Type:     broker.OrderTypeMarket,
		Quantity: float64(shares),
	}

	result, err := e.broker.PlaceOrder(ctx, order)
	if err != nil {
		return 0, err
	}

	// Wait for fill
	time.Sleep(3 * time.Second)

	var price float64
	if result.AvgPrice > 0 {
		price = result.AvgPrice
	} else {
		// Fallback: check positions for avg cost
		positions, perr := e.broker.GetPositions(ctx)
		if perr == nil {
			for _, pos := range positions {
				if pos.Symbol == e.config.Symbol {
					price = pos.AvgCost
					break
				}
			}
		}
	}
	if price <= 0 {
		price = estimatedPrice
	}

	amount := price * float64(shares)
	e.state.RecordBuy(shares, price, amount)
	return amount, nil
}

func (e *KRDCAEngine) placeSell(ctx context.Context, shares int, estimatedPrice float64) (float64, error) {
	order := broker.Order{
		Symbol:   e.config.Symbol,
		Side:     broker.OrderSideSell,
		Type:     broker.OrderTypeMarket,
		Quantity: float64(shares),
	}

	result, err := e.broker.PlaceOrder(ctx, order)
	if err != nil {
		return 0, err
	}

	time.Sleep(3 * time.Second)

	var price float64
	if result.AvgPrice > 0 {
		price = result.AvgPrice
	}
	if price <= 0 {
		price = estimatedPrice
	}

	amount := price * float64(shares)
	e.state.RecordSell(shares, price, amount)
	return amount, nil
}

func (e *KRDCAEngine) checkTakeProfit(ctx context.Context, currentPrice float64) {
	state := e.state.GetState()
	if state.TotalShares <= 0 || state.AvgCost <= 0 {
		return
	}

	pnlPct := (currentPrice - state.AvgCost) / state.AvgCost * 100

	// Reset tiers if price dropped below lowest threshold
	if pnlPct < e.config.TakeProfitTiers[0].PctThreshold {
		e.state.ResetTriggeredTiers()
		return
	}

	for _, tier := range e.config.TakeProfitTiers {
		if pnlPct < tier.PctThreshold {
			break
		}
		if e.state.HasTriggeredTier(tier.PctThreshold) {
			continue
		}

		sellShares := int(math.Floor(state.TotalShares * tier.SellPct))
		if sellShares < 1 {
			continue
		}
		if float64(sellShares) > state.TotalShares {
			sellShares = int(state.TotalShares)
		}

		log.Printf("[KR-DCA] TAKE-PROFIT: PnL %.1f%% >= +%.0f%% → selling %d shares",
			pnlPct, tier.PctThreshold, sellShares)

		amount, err := e.placeSell(ctx, sellShares, currentPrice)
		if err != nil {
			log.Printf("[KR-DCA] Take-profit sell failed: %v", err)
			continue
		}
		e.state.RecordTakeProfit(tier.PctThreshold)
		log.Printf("[KR-DCA] TAKE-PROFIT SELL %d shares @ ₩%.0f = ₩%.0f (%s)",
			sellShares, currentPrice, amount, tier.Label)
	}
}

// GetStatus returns current KR DCA status for web UI
func (e *KRDCAEngine) GetStatus(ctx context.Context) *KRDCAStatus {
	state := e.state.GetState()

	status := &KRDCAStatus{
		Symbol:         e.config.Symbol,
		SymbolName:     e.config.SymbolName,
		BaseShares:     e.config.BaseShares,
		Interval:       "weekly",
		NextDCATime:    state.NextDCATime,
		LastDCATime:    state.LastDCATime,
		TotalInvested:  state.TotalInvested,
		TotalShares:    state.TotalShares,
		AvgCost:        state.AvgCost,
		TotalDCACycles: state.TotalDCACycles,
		History:        state.History,
	}

	// Fetch current candles for RSI/EMA50/price
	candles, err := e.provider.GetDailyCandles(ctx, e.config.Symbol, 60)
	if err == nil && len(candles) >= 20 {
		price := candles[len(candles)-1].Close
		status.CurrentPrice = price
		status.RSI = strategy.CalculateRSI(candles, e.config.RSIPeriod)

		tier := e.getRSITier(status.RSI)
		status.RSILabel = tier.Label
		status.CurrentAction = tier.Action
		status.CurrentShares = tier.Shares

		if e.config.EMA50Enabled && len(candles) >= 50 {
			ema50 := strategy.CalculateEMA(candles, 50)
			status.EMA50 = ema50
			if price < ema50 {
				status.PriceVsEMA50 = "below"
				if tier.Action == "buy" {
					status.CurrentShares += e.config.EMA50BonusShare
				}
			} else {
				status.PriceVsEMA50 = "above"
			}
		}

		if state.TotalShares > 0 {
			status.CurrentValue = state.TotalShares * price
			status.UnrealizedPnL = status.CurrentValue - state.TotalInvested
			if state.TotalInvested > 0 {
				status.UnrealizedPct = status.UnrealizedPnL / state.TotalInvested * 100
			}
		}
	}

	return status
}

// GetNextDCATime returns the next scheduled DCA time
func (e *KRDCAEngine) GetNextDCATime() time.Time {
	state := e.state.GetState()
	if state.TotalDCACycles == 0 && state.NextDCATime.IsZero() {
		return time.Now()
	}
	if state.NextDCATime.IsZero() {
		return e.calculateNextDCATime()
	}
	return state.NextDCATime
}

func (e *KRDCAEngine) getRSITier(rsi float64) RSITier {
	for _, tier := range e.config.RSITiers {
		if rsi >= tier.MinRSI && rsi < tier.MaxRSI {
			return tier
		}
	}
	// Default: skip
	return RSITier{Shares: 0, Label: "unknown", Action: "skip"}
}

func (e *KRDCAEngine) calculateNextDCATime() time.Time {
	loc, _ := time.LoadLocation("Asia/Seoul")
	if loc == nil {
		loc = time.FixedZone("KST", 9*60*60)
	}

	now := time.Now().In(loc)

	hour, min := 9, 30
	fmt.Sscanf(e.config.DCATimeKST, "%d:%d", &hour, &min)

	// Find next target weekday
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, loc)

	// Move to next occurrence of target weekday
	for next.Weekday() != e.config.DCAWeekday || !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}

	return next
}

func boolStr(b bool, s string) string {
	if b {
		return s
	}
	return ""
}
