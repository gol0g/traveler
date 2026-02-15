package strategy

import (
	"context"
	"fmt"
	"math"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// RangeTradingStrategy implements a sideways market range trading strategy for crypto.
// It buys near support (20-day low) and sells near resistance (MA20 / 20-day high).
type RangeTradingStrategy struct {
	provider provider.Provider
}

// NewRangeTradingStrategy creates a new range trading strategy
func NewRangeTradingStrategy(p provider.Provider) *RangeTradingStrategy {
	return &RangeTradingStrategy{provider: p}
}

// Name returns the strategy name
func (s *RangeTradingStrategy) Name() string {
	return "range-trading"
}

// Description returns the strategy description
func (s *RangeTradingStrategy) Description() string {
	return "Range Trading - buy near support, sell near resistance (sideways crypto market)"
}

// Analyze analyzes a crypto symbol for range trading opportunity
func (s *RangeTradingStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	candles, err := s.provider.GetDailyCandles(ctx, stock.Symbol, 30)
	if err != nil {
		return nil, err
	}

	if len(candles) < 22 {
		return nil, fmt.Errorf("insufficient data: got %d candles, need 22", len(candles))
	}

	details := make(map[string]float64)
	ind := CalculateIndicators(candles)

	current := candles[len(candles)-1]
	currentPrice := current.Close

	details["current_price"] = currentPrice
	details["rsi14"] = ind.RSI14
	details["ma20"] = ind.MA20
	details["bb_lower"] = ind.BBLower
	details["bb_upper"] = ind.BBUpper

	// Calculate 20-day low (support) and 20-day high (resistance)
	low20 := CalculateLowestLow(candles, 20)
	high20 := CalculateHighestHigh(candles, 20)
	details["support_20d"] = low20
	details["resistance_20d"] = high20

	if low20 <= 0 || high20 <= 0 {
		return nil, nil
	}

	// Range width check: need meaningful range (at least 3%)
	rangeWidth := (high20 - low20) / low20 * 100
	details["range_width_pct"] = rangeWidth
	if rangeWidth < 3.0 {
		return nil, nil // Range too narrow
	}

	// Condition 1: Price within 2% of 20-day low (near support)
	supportProximity := (currentPrice - low20) / low20 * 100
	details["support_proximity_pct"] = supportProximity
	if supportProximity > 2.0 {
		return nil, nil // Not near support
	}

	// Condition 2: RSI < 35 (oversold)
	if ind.RSI14 >= 35 {
		return nil, nil
	}

	// Condition 3: Price ≤ BB lower (Bollinger Band touch)
	if ind.BBLower <= 0 || currentPrice > ind.BBLower {
		return nil, nil
	}

	// Condition 4: Reversal candle (bullish candle with lower wick > body)
	body := current.Close - current.Open
	lowerWick := math.Min(current.Open, current.Close) - current.Low
	if body <= 0 || lowerWick <= body {
		return nil, nil // Not a reversal candle
	}
	details["reversal_candle"] = 1

	// Condition 5: Volume not abnormally high (≤ 2x average)
	if ind.AvgVol > 0 {
		volRatio := float64(current.Volume) / ind.AvgVol
		details["volume_ratio"] = volRatio
		if volRatio > 2.0 {
			return nil, nil // Abnormal volume spike, avoid
		}
	}

	// Calculate stop loss and targets
	stopLoss := low20 * 0.97
	target1 := ind.MA20                // First target: MA20
	target2 := high20 * 0.98           // Second target: near resistance

	if target1 <= currentPrice {
		target1 = currentPrice * 1.02
	}
	if target2 <= target1 {
		target2 = target1 * 1.02
	}

	riskPerShare := currentPrice - stopLoss
	if riskPerShare <= 0 {
		return nil, nil
	}

	details["stop_loss"] = stopLoss
	details["target1"] = target1
	details["target2"] = target2

	// Calculate strength
	strength := s.calculateStrength(supportProximity, ind.RSI14, rangeWidth, lowerWick, body)
	details["strength"] = strength

	probability := s.calculateProbability(strength, supportProximity, ind.RSI14)

	reason := fmt.Sprintf("Range trade: price %.0f near support %.0f (%.1f%%), RSI=%.0f, BB lower=%.0f",
		currentPrice, low20, supportProximity, ind.RSI14, ind.BBLower)

	guide := &TradeGuide{
		EntryPrice:      currentPrice,
		EntryType:       "limit",
		StopLoss:        stopLoss,
		StopLossPct:     riskPerShare / currentPrice * 100,
		Target1:         target1,
		Target1Pct:      (target1 - currentPrice) / currentPrice * 100,
		Target2:         target2,
		Target2Pct:      (target2 - currentPrice) / currentPrice * 100,
		RiskRewardRatio: (target1 - currentPrice) / riskPerShare,
		KellyFraction:   0.15, // Conservative for range trading
	}

	return &Signal{
		Stock:       stock,
		Type:        SignalBuy,
		Strategy:    s.Name(),
		Strength:    strength,
		Probability: probability,
		Reason:      reason,
		Details:     details,
		Guide:       guide,
		Candles:     candles,
	}, nil
}

func (s *RangeTradingStrategy) calculateStrength(proximity, rsi, rangeWidth, lowerWick, body float64) float64 {
	var score float64

	// Support proximity (25 pts): closer to support = stronger
	if proximity <= 0.5 {
		score += 25
	} else if proximity <= 1.0 {
		score += 20
	} else {
		score += 12
	}

	// RSI oversold depth (25 pts)
	if rsi <= 25 {
		score += 25
	} else if rsi <= 30 {
		score += 20
	} else {
		score += 12
	}

	// Range width (15 pts): wider range = more profit potential
	if rangeWidth >= 8.0 {
		score += 15
	} else if rangeWidth >= 5.0 {
		score += 10
	} else {
		score += 5
	}

	// Reversal candle quality (15 pts)
	wickRatio := lowerWick / body
	if wickRatio >= 2.0 {
		score += 15
	} else if wickRatio >= 1.5 {
		score += 10
	} else {
		score += 5
	}

	// Base score for passing all conditions
	score += 20

	return math.Min(score, 100)
}

func (s *RangeTradingStrategy) calculateProbability(strength, proximity, rsi float64) float64 {
	prob := 45.0

	prob += strength * 0.06

	if proximity <= 0.5 {
		prob += 3
	}
	if rsi <= 25 {
		prob += 2
	}

	return math.Max(45, math.Min(prob, 60))
}
