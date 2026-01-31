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

// Signal represents a trading signal from a strategy
type Signal struct {
	Stock       model.Stock        `json:"stock"`
	Type        SignalType         `json:"type"`
	Strategy    string             `json:"strategy"`
	Strength    float64            `json:"strength"`     // 0-100
	Probability float64            `json:"probability"`  // Success probability 0-100
	Reason      string             `json:"reason"`       // Human readable reason
	Details     map[string]float64 `json:"details"`      // Strategy-specific metrics
	Technical   *model.TechnicalAnalysis `json:"technical,omitempty"`
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
