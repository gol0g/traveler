package strategy

import (
	"context"
	"fmt"

	"traveler/internal/analyzer"
	"traveler/internal/provider"
	"traveler/pkg/model"
)

// MorningDipConfig holds configuration for morning dip strategy
type MorningDipConfig struct {
	ConsecutiveDays      int
	MorningDropThreshold float64
	CloseRiseThreshold   float64
	ReboundThreshold     float64
	MorningWindow        int
	ClosingWindow        int
}

// DefaultMorningDipConfig returns default configuration
func DefaultMorningDipConfig() MorningDipConfig {
	return MorningDipConfig{
		ConsecutiveDays:      1,
		MorningDropThreshold: -1.0,
		CloseRiseThreshold:   0.5,
		ReboundThreshold:     2.0,
		MorningWindow:        60,
		ClosingWindow:        60,
	}
}

// MorningDipStrategy wraps the existing pattern analyzer as a Strategy
type MorningDipStrategy struct {
	config   MorningDipConfig
	analyzer *analyzer.PatternAnalyzer
	provider provider.Provider
}

// NewMorningDipStrategy creates a new morning dip strategy
func NewMorningDipStrategy(cfg MorningDipConfig, p provider.Provider) *MorningDipStrategy {
	patternCfg := analyzer.PatternConfig{
		ConsecutiveDays:      cfg.ConsecutiveDays,
		MorningDropThreshold: cfg.MorningDropThreshold,
		CloseRiseThreshold:   cfg.CloseRiseThreshold,
		ReboundThreshold:     cfg.ReboundThreshold,
		MorningWindow:        cfg.MorningWindow,
		ClosingWindow:        cfg.ClosingWindow,
	}

	return &MorningDipStrategy{
		config:   cfg,
		analyzer: analyzer.NewPatternAnalyzer(patternCfg, p),
		provider: p,
	}
}

// Name returns the strategy name
func (s *MorningDipStrategy) Name() string {
	return "morning-dip"
}

// Description returns the strategy description
func (s *MorningDipStrategy) Description() string {
	return "Morning Dip Pattern - Buy stocks that dip in morning and recover by close"
}

// Analyze analyzes a stock for morning dip pattern
func (s *MorningDipStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	result, err := s.analyzer.AnalyzeStock(ctx, stock)
	if err != nil {
		return nil, err
	}

	if result == nil {
		return nil, nil // No pattern found
	}

	// Convert PatternResult to Signal
	details := make(map[string]float64)
	details["consecutive_days"] = float64(result.ConsecutiveDays)
	details["avg_morning_dip"] = result.AvgMorningDipPct
	details["avg_close_rise"] = result.AvgCloseRisePct

	if len(result.DayPatterns) > 0 {
		latest := result.DayPatterns[0]
		details["today_dip"] = latest.MorningDipPct
		details["today_rise"] = latest.CloseRisePct
		details["today_rebound"] = latest.ReboundPct
	}

	strength := 50.0
	probability := 30.0

	if result.Technical != nil {
		strength = result.Technical.PatternStrength
		probability = result.Technical.ContinuationProb
		details["rsi"] = result.Technical.RSI
		details["volume_ratio"] = result.Technical.VolumeRatio
	}

	reason := fmt.Sprintf("%d consecutive days: avg dip %.1f%%, avg rise %.1f%%",
		result.ConsecutiveDays, result.AvgMorningDipPct, result.AvgCloseRisePct)

	return &Signal{
		Stock:       result.Stock,
		Type:        SignalBuy,
		Strategy:    s.Name(),
		Strength:    strength,
		Probability: probability,
		Reason:      reason,
		Details:     details,
		Technical:   result.Technical,
	}, nil
}
