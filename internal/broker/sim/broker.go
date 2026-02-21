package sim

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
	"traveler/internal/provider"
)

// SimBroker implements broker.Broker with virtual capital for paper trading.
// All trades are executed instantly at the order's limit price.
type SimBroker struct {
	mu        sync.RWMutex
	market    string             // "us" or "kr"
	capital   float64            // initial virtual capital
	balance   float64            // current cash
	positions map[string]*simPos // symbol -> position
	orderSeq  int                // order ID sequence
	provider  provider.Provider  // for price data
	dataDir   string             // directory for sim_state.json
	readOnly  bool               // true = reload from disk on every read (web viewer mode)
}

type simPos struct {
	Symbol   string  `json:"symbol"`
	Quantity float64 `json:"quantity"`
	AvgCost  float64 `json:"avg_cost"`
}

type simState struct {
	Market    string             `json:"market"`
	Capital   float64            `json:"capital"`
	Balance   float64            `json:"balance"`
	Positions map[string]*simPos `json:"positions"`
	OrderSeq  int                `json:"order_seq"`
	SavedAt   time.Time          `json:"saved_at"`
}

const stateFile = "sim_state.json"

// commissionRate returns the per-trade commission rate for the market.
func commissionRate(market string) float64 {
	switch market {
	case "kr":
		return 0.0025 // 0.25%
	default:
		return 0.0025 // 0.25%
	}
}

// NewSimBroker creates a paper trading broker.
// If a saved state exists in dataDir it is restored; otherwise starts fresh.
func NewSimBroker(market string, capital float64, prov provider.Provider, dataDir string) *SimBroker {
	sb := &SimBroker{
		market:    market,
		capital:   capital,
		balance:   capital,
		positions: make(map[string]*simPos),
		provider:  prov,
		dataDir:   dataDir,
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Printf("[SIM] Warning: could not create data dir: %v", err)
	}

	if err := sb.loadState(); err != nil {
		log.Printf("[SIM] No saved state, starting fresh (market=%s, capital=%.0f): %v", market, capital, err)
	} else {
		log.Printf("[SIM] Restored state: market=%s, balance=%.0f, %d positions", market, sb.balance, len(sb.positions))
	}

	return sb
}

// SetReadOnly marks this broker as read-only (web viewer mode).
// In this mode, state is reloaded from disk on every GetBalance/GetPositions call.
func (sb *SimBroker) SetReadOnly(ro bool) {
	sb.readOnly = ro
}

// reloadIfReadOnly reloads state from disk if in read-only mode.
func (sb *SimBroker) reloadIfReadOnly() {
	if !sb.readOnly {
		return
	}
	if err := sb.loadState(); err != nil {
		// state file may not exist yet; ignore
	}
}

// --- broker.Broker interface ---

func (sb *SimBroker) Name() string {
	return "sim-" + sb.market
}

func (sb *SimBroker) IsReady() bool {
	return true
}

func (sb *SimBroker) PlaceOrder(ctx context.Context, order broker.Order) (*broker.OrderResult, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	sb.orderSeq++
	orderID := fmt.Sprintf("SIM-%s-%d", sb.market, sb.orderSeq)
	now := time.Now()

	price := order.LimitPrice
	if price <= 0 {
		// Market order: try to get current price
		if q, err := sb.getQuoteFromProvider(ctx, order.Symbol); err == nil && q > 0 {
			price = q
		}
		if price <= 0 {
			return &broker.OrderResult{
				OrderID: orderID,
				Symbol:  order.Symbol,
				Side:    order.Side,
				Status:  "rejected",
				Message: "no price available for market order",
			}, fmt.Errorf("sim: no price for %s", order.Symbol)
		}
	}

	qty := order.Quantity
	commRate := commissionRate(sb.market)

	switch order.Side {
	case broker.OrderSideBuy:
		cost := qty * price
		commission := cost * commRate
		totalCost := cost + commission

		if totalCost > sb.balance {
			return &broker.OrderResult{
				OrderID: orderID,
				Symbol:  order.Symbol,
				Side:    order.Side,
				Status:  "rejected",
				Message: fmt.Sprintf("insufficient balance: need %.0f, have %.0f", totalCost, sb.balance),
			}, fmt.Errorf("sim: insufficient balance for %s (need %.0f, have %.0f)", order.Symbol, totalCost, sb.balance)
		}

		sb.balance -= totalCost

		// Update or create position with weighted average cost.
		pos, exists := sb.positions[order.Symbol]
		if exists {
			oldCost := pos.AvgCost * pos.Quantity
			newCost := price * qty
			pos.Quantity += qty
			if pos.Quantity > 0 {
				pos.AvgCost = (oldCost + newCost) / pos.Quantity
			}
		} else {
			sb.positions[order.Symbol] = &simPos{
				Symbol:   order.Symbol,
				Quantity: qty,
				AvgCost:  price,
			}
		}

		sb.saveStateLocked()

		log.Printf("[SIM] BUY %s x%.0f @ %.2f (commission: %.2f, balance: %.0f)",
			order.Symbol, qty, price, cost*commRate, sb.balance)

		return &broker.OrderResult{
			OrderID:     orderID,
			Symbol:      order.Symbol,
			Side:        broker.OrderSideBuy,
			Type:        order.Type,
			Quantity:    qty,
			FilledQty:   qty,
			AvgPrice:    price,
			Status:      "filled",
			SubmittedAt: now,
			FilledAt:    now,
		}, nil

	case broker.OrderSideSell:
		pos, exists := sb.positions[order.Symbol]
		if !exists || pos.Quantity < qty {
			held := 0.0
			if exists {
				held = pos.Quantity
			}
			return &broker.OrderResult{
				OrderID: orderID,
				Symbol:  order.Symbol,
				Side:    order.Side,
				Status:  "rejected",
				Message: fmt.Sprintf("insufficient position: need %.0f, have %.0f", qty, held),
			}, fmt.Errorf("sim: insufficient position for %s (need %.0f, have %.0f)", order.Symbol, qty, held)
		}

		proceeds := qty * price
		commission := proceeds * commRate
		netProceeds := proceeds - commission

		sb.balance += netProceeds

		pos.Quantity -= qty
		if pos.Quantity <= 0 {
			delete(sb.positions, order.Symbol)
		}

		sb.saveStateLocked()

		log.Printf("[SIM] SELL %s x%.0f @ %.2f (commission: %.2f, balance: %.0f)",
			order.Symbol, qty, price, commission, sb.balance)

		return &broker.OrderResult{
			OrderID:     orderID,
			Symbol:      order.Symbol,
			Side:        broker.OrderSideSell,
			Type:        order.Type,
			Quantity:    qty,
			FilledQty:   qty,
			AvgPrice:    price,
			Status:      "filled",
			SubmittedAt: now,
			FilledAt:    now,
		}, nil

	default:
		return nil, fmt.Errorf("sim: unknown order side %q", order.Side)
	}
}

func (sb *SimBroker) CancelOrder(ctx context.Context, orderID string) error {
	// All orders are instantly filled, nothing to cancel.
	return nil
}

func (sb *SimBroker) GetOrder(ctx context.Context, orderID string) (*broker.OrderResult, error) {
	// Not tracked after fill.
	return nil, nil
}

func (sb *SimBroker) GetBalance(ctx context.Context) (*broker.AccountBalance, error) {
	sb.mu.Lock()
	sb.reloadIfReadOnly()
	sb.mu.Unlock()

	sb.mu.RLock()
	defer sb.mu.RUnlock()

	positions := sb.buildPositions(ctx)

	totalEquity := sb.balance
	for _, p := range positions {
		totalEquity += p.MarketValue
	}

	currency := "USD"
	if sb.market == "kr" {
		currency = "KRW"
	}

	return &broker.AccountBalance{
		Currency:    currency,
		CashBalance: sb.balance,
		BuyingPower: sb.balance,
		TotalEquity: totalEquity,
		Positions:   positions,
	}, nil
}

func (sb *SimBroker) GetPositions(ctx context.Context) ([]broker.Position, error) {
	sb.mu.Lock()
	sb.reloadIfReadOnly()
	sb.mu.Unlock()

	sb.mu.RLock()
	defer sb.mu.RUnlock()

	return sb.buildPositions(ctx), nil
}

func (sb *SimBroker) GetPendingOrders(ctx context.Context) ([]broker.PendingOrder, error) {
	return nil, nil
}

func (sb *SimBroker) GetQuote(ctx context.Context, symbol string) (float64, error) {
	return sb.getQuoteFromProvider(ctx, symbol)
}

// --- helpers ---

// getQuoteFromProvider fetches latest close price via provider's GetDailyCandles.
func (sb *SimBroker) getQuoteFromProvider(ctx context.Context, symbol string) (float64, error) {
	if sb.provider == nil {
		return 0, fmt.Errorf("no provider")
	}
	candles, err := sb.provider.GetDailyCandles(ctx, symbol, 2)
	if err != nil || len(candles) == 0 {
		return 0, fmt.Errorf("no candle data for %s: %v", symbol, err)
	}
	return candles[len(candles)-1].Close, nil
}

func (sb *SimBroker) buildPositions(ctx context.Context) []broker.Position {
	out := make([]broker.Position, 0, len(sb.positions))
	for _, p := range sb.positions {
		currentPrice := p.AvgCost // fallback
		if q, err := sb.getQuoteFromProvider(ctx, p.Symbol); err == nil && q > 0 {
			currentPrice = q
		}
		marketValue := currentPrice * p.Quantity
		unrealizedPnL := (currentPrice - p.AvgCost) * p.Quantity
		unrealizedPct := 0.0
		if p.AvgCost > 0 {
			unrealizedPct = (currentPrice - p.AvgCost) / p.AvgCost * 100
		}

		out = append(out, broker.Position{
			Symbol:        p.Symbol,
			Quantity:      p.Quantity,
			AvgCost:       p.AvgCost,
			CurrentPrice:  currentPrice,
			MarketValue:   marketValue,
			UnrealizedPnL: unrealizedPnL,
			UnrealizedPct: unrealizedPct,
		})
	}
	return out
}

// --- state persistence ---

func (sb *SimBroker) statePath() string {
	return filepath.Join(sb.dataDir, stateFile)
}

func (sb *SimBroker) saveStateLocked() {
	state := simState{
		Market:    sb.market,
		Capital:   sb.capital,
		Balance:   sb.balance,
		Positions: sb.positions,
		OrderSeq:  sb.orderSeq,
		SavedAt:   time.Now(),
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("[SIM] Failed to marshal state: %v", err)
		return
	}

	tmpPath := sb.statePath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		log.Printf("[SIM] Failed to write state: %v", err)
		return
	}
	if err := os.Rename(tmpPath, sb.statePath()); err != nil {
		log.Printf("[SIM] Failed to rename state file: %v", err)
	}
}

func (sb *SimBroker) loadState() error {
	data, err := os.ReadFile(sb.statePath())
	if err != nil {
		return err
	}

	var state simState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("unmarshal sim state: %w", err)
	}

	if state.Balance < 0 {
		return fmt.Errorf("corrupted state: negative balance %.2f", state.Balance)
	}

	sb.balance = state.Balance
	sb.orderSeq = state.OrderSeq
	if state.Positions != nil {
		sb.positions = state.Positions
	}
	// Restore capital from state if available, otherwise keep constructor value.
	if state.Capital > 0 {
		sb.capital = state.Capital
	}

	return nil
}
