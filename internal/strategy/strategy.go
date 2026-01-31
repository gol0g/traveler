package strategy

import (
	"context"

	"traveler/pkg/model"
)

// SignalType represents the type of trading signal
type SignalType string

const (
	SignalBuy  SignalType = "BUY"
	SignalSell SignalType = "SELL"
	SignalHold SignalType = "HOLD"
)

// TradeGuide provides actionable trading guidance
type TradeGuide struct {
	// Entry
	EntryPrice    float64 `json:"entry_price"`
	EntryType     string  `json:"entry_type"` // "market", "limit"

	// Exit points
	StopLoss      float64 `json:"stop_loss"`
	StopLossPct   float64 `json:"stop_loss_pct"`
	Target1       float64 `json:"target_1"`
	Target1Pct    float64 `json:"target_1_pct"`
	Target2       float64 `json:"target_2"`
	Target2Pct    float64 `json:"target_2_pct"`

	// Position sizing
	RiskRewardRatio float64 `json:"risk_reward_ratio"`
	PositionSize    int     `json:"position_size"`
	InvestAmount    float64 `json:"invest_amount"`
	RiskAmount      float64 `json:"risk_amount"`
	RiskPct         float64 `json:"risk_pct"` // % of portfolio

	// Kelly
	KellyFraction   float64 `json:"kelly_fraction"`
}

// Signal represents a trading signal from a strategy
type Signal struct {
	Stock       model.Stock              `json:"stock"`
	Type        SignalType               `json:"type"`
	Strategy    string                   `json:"strategy"`
	Strength    float64                  `json:"strength"`     // 0-100
	Probability float64                  `json:"probability"`  // Success probability 0-100
	Reason      string                   `json:"reason"`       // Human readable reason
	Details     map[string]float64       `json:"details"`      // Strategy-specific metrics
	Technical   *model.TechnicalAnalysis `json:"technical,omitempty"`
	Guide       *TradeGuide              `json:"guide,omitempty"` // Trading guide
}

// Strategy defines the interface for trading strategies
type Strategy interface {
	// Name returns the strategy name
	Name() string

	// Description returns a brief description
	Description() string

	// Analyze analyzes a stock and returns a signal if conditions are met
	Analyze(ctx context.Context, stock model.Stock) (*Signal, error)
}

// ScanResult represents results from scanning with a strategy
type ScanResult struct {
	Strategy      string        `json:"strategy"`
	TotalScanned  int           `json:"total_scanned"`
	SignalsFound  int           `json:"signals_found"`
	Signals       []Signal      `json:"signals"`
	ScanTime      string        `json:"scan_time"`
}
