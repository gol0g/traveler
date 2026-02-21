package dca

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// State persists DCA progress to disk
type State struct {
	NextDCATime    time.Time                `json:"next_dca_time"`
	LastDCATime    time.Time                `json:"last_dca_time"`
	TotalInvested  float64                  `json:"total_invested"`
	TotalDCACycles int                      `json:"total_dca_cycles"`
	Assets         map[string]*AssetState   `json:"assets"`
	LastRebalance  time.Time                `json:"last_rebalance_time"`
	RebalanceCount int                      `json:"rebalance_count"`
	History        []DCAHistoryEntry        `json:"history"`
}

// AssetState tracks per-coin DCA state
type AssetState struct {
	Symbol        string  `json:"symbol"`
	TotalInvested float64 `json:"total_invested"`
	TotalQuantity float64 `json:"total_quantity"`
	AvgCost       float64 `json:"avg_cost"`
	DCACount      int     `json:"dca_count"`
	LastBuyTime   time.Time `json:"last_buy_time"`
	LastBuyPrice  float64 `json:"last_buy_price"`
	LastBuyAmount float64 `json:"last_buy_amount"`
	// Track which take-profit tiers have been triggered (by PctThreshold)
	TriggeredTiers []float64 `json:"triggered_tiers,omitempty"`
}

// DCAHistoryEntry records each DCA execution
type DCAHistoryEntry struct {
	Timestamp   time.Time `json:"timestamp"`
	FearGreed   int       `json:"fear_greed"`
	FGLabel     string    `json:"fg_label"`
	TotalAmount float64   `json:"total_amount"`
	Multiplier  float64   `json:"multiplier"`
	Buys        []DCABuy  `json:"buys"`
	Sells       []DCASell `json:"sells,omitempty"`
	Rebalanced  bool      `json:"rebalanced,omitempty"`
}

// DCABuy records a single buy within a DCA cycle
type DCABuy struct {
	Symbol   string  `json:"symbol"`
	Amount   float64 `json:"amount"`   // KRW spent
	Quantity float64 `json:"quantity"` // coins received
	Price    float64 `json:"price"`    // execution price
}

// DCASell records a partial sell
type DCASell struct {
	Symbol   string  `json:"symbol"`
	Quantity float64 `json:"quantity"`
	Price    float64 `json:"price"`
	Amount   float64 `json:"amount"` // KRW received
}

// StateManager manages persistent DCA state
type StateManager struct {
	mu       sync.RWMutex
	state    State
	filepath string
}

// NewStateManager loads or initializes DCA state
func NewStateManager(dataDir string) *StateManager {
	fp := filepath.Join(dataDir, "dca_state.json")
	sm := &StateManager{filepath: fp}

	if data, err := os.ReadFile(fp); err == nil {
		if json.Unmarshal(data, &sm.state) == nil {
			log.Printf("[DCA] Loaded state: %d cycles, ₩%.0f invested, %d assets",
				sm.state.TotalDCACycles, sm.state.TotalInvested, len(sm.state.Assets))
			return sm
		}
	}

	sm.state = State{
		Assets: make(map[string]*AssetState),
	}
	return sm
}

// GetState returns a copy of the current state
func (sm *StateManager) GetState() State {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	// shallow copy is fine for reading
	s := sm.state
	return s
}

// RecordBuy records a DCA buy for a specific asset
func (sm *StateManager) RecordBuy(symbol string, amount, quantity, price float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state.Assets == nil {
		sm.state.Assets = make(map[string]*AssetState)
	}

	a, ok := sm.state.Assets[symbol]
	if !ok {
		a = &AssetState{Symbol: symbol}
		sm.state.Assets[symbol] = a
	}

	a.TotalInvested += amount
	a.TotalQuantity += quantity
	if a.TotalQuantity > 0 {
		a.AvgCost = a.TotalInvested / a.TotalQuantity
	}
	a.DCACount++
	a.LastBuyTime = time.Now()
	a.LastBuyPrice = price
	a.LastBuyAmount = amount

	sm.state.TotalInvested += amount
}

// RecordSell records a partial sell for a specific asset
func (sm *StateManager) RecordSell(symbol string, quantity, price, amount float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	a, ok := sm.state.Assets[symbol]
	if !ok {
		return
	}

	// Reduce quantity, keep avg cost unchanged
	a.TotalQuantity -= quantity
	if a.TotalQuantity < 0 {
		a.TotalQuantity = 0
	}
	// Reduce invested proportionally
	if quantity > 0 && a.AvgCost > 0 {
		a.TotalInvested -= quantity * a.AvgCost
		if a.TotalInvested < 0 {
			a.TotalInvested = 0
		}
	}
}

// CompleteCycle marks a DCA cycle as completed
func (sm *StateManager) CompleteCycle(entry DCAHistoryEntry, nextDCA time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.state.TotalDCACycles++
	sm.state.LastDCATime = entry.Timestamp
	sm.state.NextDCATime = nextDCA

	sm.state.History = append(sm.state.History, entry)
	// Keep only last 90 entries
	if len(sm.state.History) > 90 {
		sm.state.History = sm.state.History[len(sm.state.History)-90:]
	}

	sm.save()
}

// RecordRebalance marks a rebalancing event
func (sm *StateManager) RecordRebalance() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.state.LastRebalance = time.Now()
	sm.state.RebalanceCount++
	sm.save()
}

// RecordTakeProfit records that a take-profit tier was triggered for an asset
func (sm *StateManager) RecordTakeProfit(symbol string, tierPct float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	a, ok := sm.state.Assets[symbol]
	if !ok {
		return
	}

	// Avoid duplicates
	for _, t := range a.TriggeredTiers {
		if t == tierPct {
			return
		}
	}
	a.TriggeredTiers = append(a.TriggeredTiers, tierPct)
	sm.save()
}

// HasTriggeredTier checks if a take-profit tier was already triggered
func (sm *StateManager) HasTriggeredTier(symbol string, tierPct float64) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	a, ok := sm.state.Assets[symbol]
	if !ok {
		return false
	}
	for _, t := range a.TriggeredTiers {
		if t == tierPct {
			return true
		}
	}
	return false
}

// ResetTriggeredTiers clears take-profit tier history (e.g., after price drops back below thresholds)
func (sm *StateManager) ResetTriggeredTiers(symbol string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	a, ok := sm.state.Assets[symbol]
	if !ok {
		return
	}
	if len(a.TriggeredTiers) > 0 {
		a.TriggeredTiers = nil
		sm.save()
	}
}

// SetNextDCATime updates the next DCA time
func (sm *StateManager) SetNextDCATime(t time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.state.NextDCATime = t
	sm.save()
}

func (sm *StateManager) save() {
	data, err := json.MarshalIndent(sm.state, "", "  ")
	if err != nil {
		log.Printf("[DCA] Failed to marshal state: %v", err)
		return
	}
	if err := os.WriteFile(sm.filepath, data, 0644); err != nil {
		log.Printf("[DCA] Failed to save state: %v", err)
	}
}
