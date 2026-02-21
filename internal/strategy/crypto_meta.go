package strategy

import (
	"context"
	"fmt"
	"log"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// CryptoMetaStrategy is a regime-aware meta strategy for crypto.
// It detects market regime (bull/sideways/bear) and delegates to the appropriate sub-strategies.
// Multiple strategies are tried per regime; the best signal (highest probability × strength) wins.
//
// Regime mapping:
//   - Bull:     VolatilityBreakout + VolumeSpike
//   - Sideways: RangeTrading + RSI Contrarian (RSI<25) + VolumeSpike
//   - Bear:     RSI Contrarian extreme only (RSI<20)
type CryptoMetaStrategy struct {
	regime      *RegimeDetector
	bull        []Strategy // Bull regime strategies
	sideways    []Strategy // Sideways regime strategies
	bear        []Strategy // Bear regime strategies (conservative only)
}

// NewCryptoMetaStrategy creates a new crypto meta strategy.
// Capital tier:
//   - < ₩200K (btc-only): BTC trend following only (EMA12/50)
//   - ₩200K-₩1M (extended): BTC trend + existing strategies for top alts
//   - ≥ ₩1M (full): Full multi-strategy
func NewCryptoMetaStrategy(p provider.Provider, capital ...float64) *CryptoMetaStrategy {
	cap := 0.0
	if len(capital) > 0 {
		cap = capital[0]
	}
	tier := GetCapitalTier("crypto", cap)

	if tier == "btc-only" {
		// 소액: BTC 트렌드 팔로잉만 — 모든 레짐에서 동일
		trend := NewCryptoTrendStrategy(p)
		return &CryptoMetaStrategy{
			regime:   NewRegimeDetector(p),
			bull:     []Strategy{trend},
			sideways: []Strategy{trend},
			bear:     []Strategy{trend},
		}
	}

	// extended/full: 기존 로직
	vbCfg := DefaultVolatilityBreakoutConfig()
	return &CryptoMetaStrategy{
		regime: NewRegimeDetector(p),
		bull: []Strategy{
			NewVolatilityBreakoutStrategy(vbCfg, p),
			NewVolumeSpikeStrategy(p),
		},
		sideways: []Strategy{
			NewRangeTradingStrategy(p),
			NewRSIContrarianStrategy(p, 25),
			NewVolumeSpikeStrategy(p),
		},
		bear: []Strategy{
			NewWBottomStrategy(p),           // W-Bottom with confluence scoring
			NewRSIContrarianStrategy(p, 20), // Extreme oversold only (fallback)
		},
	}
}

// Name returns the strategy name
func (s *CryptoMetaStrategy) Name() string {
	return "crypto-meta"
}

// Description returns the strategy description
func (s *CryptoMetaStrategy) Description() string {
	return "Crypto Meta Strategy - regime-aware multi-strategy (bull/sideways/bear)"
}

// Analyze detects market regime and tries all strategies for that regime.
// Returns the best signal (highest probability × strength) or nil if no signal.
func (s *CryptoMetaStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
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
		log.Printf("[META] No strategies for regime %s — skipping %s", regime, stock.Symbol)
		return nil, nil
	}

	// Try all strategies, collect signals
	var bestSignal *Signal
	var bestScore float64

	for _, strat := range strategies {
		sig, err := strat.Analyze(ctx, stock)
		if err != nil || sig == nil {
			continue
		}

		// Score = probability × strength (both 0-100)
		score := sig.Probability * sig.Strength / 100.0
		if score > bestScore {
			bestScore = score
			bestSignal = sig
		}
	}

	if bestSignal == nil {
		return nil, nil
	}

	// Enrich signal with regime info
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

	// Add regime prefix to reason
	bestSignal.Reason = fmt.Sprintf("[%s] %s", regimeLabel(regime), bestSignal.Reason)

	// Override strategy name to include regime info
	bestSignal.Strategy = fmt.Sprintf("%s(%s)", bestSignal.Strategy, regime)

	return bestSignal, nil
}

// GetCurrentRegime returns the current cached regime (for external display)
func (s *CryptoMetaStrategy) GetCurrentRegime(ctx context.Context) Regime {
	return s.regime.Detect(ctx)
}

// GetRegimeInfo returns detailed regime info with benchmark indicators
func (s *CryptoMetaStrategy) GetRegimeInfo(ctx context.Context) RegimeInfo {
	return s.regime.DetectWithInfo(ctx)
}

func regimeLabel(r Regime) string {
	switch r {
	case RegimeBull:
		return "BULL"
	case RegimeSideways:
		return "SIDEWAYS"
	case RegimeBear:
		return "BEAR"
	default:
		return "UNKNOWN"
	}
}
