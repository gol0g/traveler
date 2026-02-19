package strategy

import (
	"context"
	"fmt"
	"log"
	"time"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// StockMetaConfig defines the regime-to-strategy mapping for stock markets.
// Each regime (bull/sideways/bear) maps to a list of strategy names.
type StockMetaConfig struct {
	Name            string            // config name (for optimization reports)
	Market          string            // "us" or "kr"
	BenchmarkSym    string            // "SPY" or "069500"
	Bull            []string          // strategy names active in bull regime
	Sideways        []string          // strategy names active in sideways regime
	Bear            []string          // strategy names active in bear regime
	MaxHoldOverride map[string]int    // strategy name → override max hold days
}

// DefaultStockMetaConfig returns the optimized config for a market.
// US: "breakout-bull" — breakout only in bull, diversified in sideways, oversold in bear.
// KR: "extended-hold" — breakout+pullback in bull, diversified sideways, oversold in bear, breakout hold extended to 20 days.
func DefaultStockMetaConfig(market string) StockMetaConfig {
	if market == "kr" {
		return StockMetaConfig{
			Name:         "extended-hold",
			Market:       "kr",
			BenchmarkSym: "069500",
			Bull:         []string{"breakout", "pullback"},
			Sideways:     []string{"mean-reversion", "oversold"},
			Bear:         []string{"oversold"},
			MaxHoldOverride: map[string]int{
				"breakout": 20,
			},
		}
	}
	return StockMetaConfig{
		Name:         "breakout-bull",
		Market:       "us",
		BenchmarkSym: "SPY",
		Bull:         []string{"breakout"},
		Sideways:     []string{"mean-reversion", "oversold"},
		Bear:         []string{"oversold"},
	}
}

// StockMetaStrategy is a regime-aware meta strategy for stock markets (US/KR).
// It detects market regime and delegates to the appropriate sub-strategies,
// following the same pattern as CryptoMetaStrategy.
type StockMetaStrategy struct {
	config   StockMetaConfig
	regime   *RegimeDetector
	provider provider.Provider
	bull     []Strategy
	sideways []Strategy
	bear     []Strategy
}

// NewStockMetaStrategy creates a new stock meta strategy from config
func NewStockMetaStrategy(cfg StockMetaConfig, p provider.Provider) *StockMetaStrategy {
	s := &StockMetaStrategy{
		config:   cfg,
		regime:   NewRegimeDetectorForSymbol(p, cfg.BenchmarkSym),
		provider: p,
	}
	s.bull = s.buildStrategies(cfg.Bull, RegimeBull)
	s.sideways = s.buildStrategies(cfg.Sideways, RegimeSideways)
	s.bear = s.buildStrategies(cfg.Bear, RegimeBear)
	return s
}

// buildStrategies creates Strategy instances from a list of strategy names.
// regime parameter allows creating regime-specific configs (e.g., relaxed conditions for sideways).
func (s *StockMetaStrategy) buildStrategies(names []string, regime Regime) []Strategy {
	var strats []Strategy
	for _, name := range names {
		strat := s.createStrategy(name, regime)
		if strat != nil {
			strats = append(strats, strat)
		}
	}
	return strats
}

// createStrategy creates a single strategy by name with market/regime-appropriate config.
// MarketRegimeSymbol is left empty so sub-strategies skip their own regime check —
// the meta strategy's regime detection is authoritative.
// For sideways regime, strategy conditions are relaxed to produce more signals.
func (s *StockMetaStrategy) createStrategy(name string, regime Regime) Strategy {
	isKR := s.config.Market == "kr"
	isSideways := regime == RegimeSideways

	switch name {
	case "pullback":
		cfg := DefaultPullbackConfig()
		if isSideways {
			cfg.RequireUptrend = false // sideways: MA50 상승추세 불요
			cfg.MaxRSI = 60            // sideways: RSI 60까지 허용 (기본 50)
		}
		if isKR {
			cfg.MinPrice = 1000
			cfg.MinDailyDollarVol = 500000000
		}
		return NewPullbackStrategy(cfg, s.provider)

	case "breakout":
		cfg := DefaultBreakoutConfig()
		if isKR {
			cfg.MinPrice = 1000
			cfg.MinDailyDollarVol = 500000000
		}
		return NewBreakoutStrategy(cfg, s.provider)

	case "mean-reversion":
		cfg := DefaultMeanReversionConfig()
		if isSideways {
			cfg.RSIOversold = 35        // sideways: RSI 35까지 완화 (기본 30)
			cfg.BBTouchTolerance = 0.02 // sideways: BB 하단 2% 허용 (기본 1%)
			cfg.RequireUptrend = false  // sideways: MA200 상승추세 불요
		}
		if isKR {
			cfg.MinPrice = 1000
			cfg.MinDailyDollarVol = 500000000
		}
		return NewMeanReversionStrategy(cfg, s.provider)

	case "oversold":
		cfg := DefaultOversoldConfig()
		if isKR {
			cfg.MinPrice = 1000
			cfg.MinDailyDollarVol = 500000000
		}
		return NewOversoldStrategy(cfg, s.provider)

	default:
		log.Printf("[STOCK-META] Unknown strategy: %s", name)
		return nil
	}
}

// Name returns the strategy name
func (s *StockMetaStrategy) Name() string {
	return "stock-meta"
}

// Description returns the strategy description
func (s *StockMetaStrategy) Description() string {
	return fmt.Sprintf("Stock Meta Strategy - regime-aware (%s)", s.config.Market)
}

// Analyze detects market regime and tries all strategies for that regime.
// Returns the best signal (highest probability x strength) or nil.
func (s *StockMetaStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	regime := s.regime.Detect(ctx)

	var strategies []Strategy
	switch regime {
	case RegimeBull:
		strategies = s.bull
	case RegimeSideways:
		strategies = s.sideways
	case RegimeBear:
		strategies = s.bear
	default:
		strategies = s.sideways
	}

	if len(strategies) == 0 {
		return nil, nil
	}

	// Try all strategies, collect best signal by score
	var bestSignal *Signal
	var bestScore float64

	for _, strat := range strategies {
		sig, err := strat.Analyze(ctx, stock)
		if err != nil || sig == nil {
			continue
		}

		// Score = probability x strength (both 0-100), matching CryptoMetaStrategy
		score := sig.Probability * sig.Strength / 100.0
		if score > bestScore {
			bestScore = score
			bestSignal = sig
		}
	}

	if bestSignal == nil {
		return nil, nil
	}

	// Enrich signal with regime info (for sizer risk reduction)
	if bestSignal.Details == nil {
		bestSignal.Details = make(map[string]float64)
	}
	switch regime {
	case RegimeBull:
		bestSignal.Details["regime"] = 1
	case RegimeSideways:
		bestSignal.Details["regime"] = 0
	case RegimeBear:
		bestSignal.Details["regime"] = -1
	}

	// MaxHoldOverride: inject into Details for backtester to pick up
	baseName := bestSignal.Strategy
	if override, ok := s.config.MaxHoldOverride[baseName]; ok {
		bestSignal.Details["max_hold_override"] = float64(override)
	}

	// Add regime prefix to reason
	bestSignal.Reason = fmt.Sprintf("[%s] %s", regimeLabel(regime), bestSignal.Reason)

	// Override strategy name to include regime info (e.g., "breakout(bull)")
	bestSignal.Strategy = fmt.Sprintf("%s(%s)", bestSignal.Strategy, regime)

	return bestSignal, nil
}

// ResetRegimeCache resets regime caches for all sub-strategies and the detector itself.
// Must be called between simulation days in backtesting.
func (s *StockMetaStrategy) ResetRegimeCache() {
	// Reset the regime detector's cache so it recalculates for the new date
	s.regime.mu.Lock()
	s.regime.updatedAt = time.Time{} // force recalculation
	s.regime.mu.Unlock()

	// Reset all sub-strategies' regime caches
	allStrats := make([]Strategy, 0, len(s.bull)+len(s.sideways)+len(s.bear))
	allStrats = append(allStrats, s.bull...)
	allStrats = append(allStrats, s.sideways...)
	allStrats = append(allStrats, s.bear...)

	seen := make(map[string]bool)
	for _, strat := range allStrats {
		key := fmt.Sprintf("%p", strat) // deduplicate by pointer
		if seen[key] {
			continue
		}
		seen[key] = true
		if rr, ok := strat.(interface{ ResetRegimeCache() }); ok {
			rr.ResetRegimeCache()
		}
	}
}

// GetCurrentRegime returns the current cached regime (for external display)
func (s *StockMetaStrategy) GetCurrentRegime(ctx context.Context) Regime {
	return s.regime.Detect(ctx)
}

// GetRegimeInfo returns detailed regime info with benchmark indicators
func (s *StockMetaStrategy) GetRegimeInfo(ctx context.Context) RegimeInfo {
	return s.regime.DetectWithInfo(ctx)
}

// GetActiveStrategyNames returns the strategy names active for the current regime
func (s *StockMetaStrategy) GetActiveStrategyNames(ctx context.Context) []string {
	regime := s.regime.Detect(ctx)
	switch regime {
	case RegimeBull:
		return s.config.Bull
	case RegimeSideways:
		return s.config.Sideways
	case RegimeBear:
		return s.config.Bear
	default:
		return s.config.Sideways
	}
}
