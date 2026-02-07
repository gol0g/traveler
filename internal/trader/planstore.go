package trader

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PositionPlan stores the original trading plan for a position
type PositionPlan struct {
	Symbol      string    `json:"symbol"`
	Strategy    string    `json:"strategy"`
	EntryPrice  float64   `json:"entry_price"`
	Quantity    int       `json:"quantity"`
	StopLoss    float64   `json:"stop_loss"`
	Target1     float64   `json:"target1"`
	Target2     float64   `json:"target2"`
	Target1Hit  bool      `json:"target1_hit"`
	EntryTime   time.Time `json:"entry_time"`
	MaxHoldDays int       `json:"max_hold_days"` // trading days

	// Strategy invalidation fields
	BreakoutLevel        float64 `json:"breakout_level,omitempty"`         // breakout: 20D high at entry
	ConsecutiveDaysBelow int     `json:"consecutive_days_below,omitempty"` // pullback: days close < MA20
}

// MaxHoldDays per strategy
var strategyMaxHoldDays = map[string]int{
	"pullback":        7,
	"mean-reversion":  5,
	"breakout":        15,
}

// GetMaxHoldDays returns the max hold days for a strategy
func GetMaxHoldDays(strategy string) int {
	if days, ok := strategyMaxHoldDays[strategy]; ok {
		return days
	}
	return 7 // default
}

// TradingDaysSince counts weekday days between entry and now
func TradingDaysSince(entry time.Time) int {
	now := time.Now()
	if now.Before(entry) {
		return 0
	}

	days := 0
	current := entry
	for current.Before(now) {
		current = current.AddDate(0, 0, 1)
		wd := current.Weekday()
		if wd != time.Saturday && wd != time.Sunday {
			days++
		}
	}
	return days
}

// PlanStore persists position plans to a JSON file
type PlanStore struct {
	mu       sync.RWMutex
	filepath string
	plans    map[string]*PositionPlan
}

// NewPlanStore creates a new plan store
func NewPlanStore(dir string) (*PlanStore, error) {
	// Ensure directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	ps := &PlanStore{
		filepath: filepath.Join(dir, "plans.json"),
		plans:    make(map[string]*PositionPlan),
	}

	// Load existing plans
	if err := ps.load(); err != nil && !os.IsNotExist(err) {
		log.Printf("[PLANSTORE] Warning: could not load plans: %v", err)
		// Start fresh if corrupted
		ps.plans = make(map[string]*PositionPlan)
	}

	log.Printf("[PLANSTORE] Loaded %d plans from %s", len(ps.plans), ps.filepath)
	return ps, nil
}

// Save stores a position plan
func (ps *PlanStore) Save(plan *PositionPlan) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	ps.plans[plan.Symbol] = plan
	log.Printf("[PLANSTORE] Saved plan for %s (strategy=%s, stop=$%.2f, T1=$%.2f, T2=$%.2f, maxDays=%d)",
		plan.Symbol, plan.Strategy, plan.StopLoss, plan.Target1, plan.Target2, plan.MaxHoldDays)
	return ps.persist()
}

// Get retrieves a plan by symbol
func (ps *PlanStore) Get(symbol string) *PositionPlan {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.plans[symbol]
}

// Delete removes a plan
func (ps *PlanStore) Delete(symbol string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if _, ok := ps.plans[symbol]; ok {
		delete(ps.plans, symbol)
		log.Printf("[PLANSTORE] Deleted plan for %s", symbol)
		return ps.persist()
	}
	return nil
}

// UpdateTarget1Hit marks target1 as hit and updates quantity
func (ps *PlanStore) UpdateTarget1Hit(symbol string, remainingQty int, newStopLoss float64) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if plan, ok := ps.plans[symbol]; ok {
		plan.Target1Hit = true
		plan.Quantity = remainingQty
		plan.StopLoss = newStopLoss
		log.Printf("[PLANSTORE] Updated %s: target1 hit, qty=%d, new stop=$%.2f",
			symbol, remainingQty, newStopLoss)
		return ps.persist()
	}
	return nil
}

// UpdateConsecutiveDaysBelow updates the consecutive days below counter
func (ps *PlanStore) UpdateConsecutiveDaysBelow(symbol string, days int) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if plan, ok := ps.plans[symbol]; ok {
		plan.ConsecutiveDaysBelow = days
		return ps.persist()
	}
	return nil
}

// Reload re-reads plans from disk (for cross-process freshness)
func (ps *PlanStore) Reload() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	ps.plans = make(map[string]*PositionPlan)
	if err := ps.load(); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// All returns all plans
func (ps *PlanStore) All() map[string]*PositionPlan {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	result := make(map[string]*PositionPlan, len(ps.plans))
	for k, v := range ps.plans {
		copy := *v
		result[k] = &copy
	}
	return result
}

func (ps *PlanStore) load() error {
	data, err := os.ReadFile(ps.filepath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &ps.plans)
}

func (ps *PlanStore) persist() error {
	data, err := json.MarshalIndent(ps.plans, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ps.filepath, data, 0644)
}
