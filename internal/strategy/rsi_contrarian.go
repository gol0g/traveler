package strategy

import (
	"context"
	"fmt"
	"math"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// RSIContrarianStrategy buys on extreme RSI oversold conditions (mean reversion).
// Uses regime-aware RSI thresholds:
//   - Bull:     RSI < 30
//   - Sideways: RSI < 25
//   - Bear:     RSI < 20 (very conservative, extreme oversold only)
type RSIContrarianStrategy struct {
	provider     provider.Provider
	rsiThreshold float64 // RSI buy threshold (set by meta strategy per regime)
}

// NewRSIContrarianStrategy creates a new RSI contrarian strategy
func NewRSIContrarianStrategy(p provider.Provider, rsiThreshold float64) *RSIContrarianStrategy {
	return &RSIContrarianStrategy{
		provider:     p,
		rsiThreshold: rsiThreshold,
	}
}

// Name returns the strategy name
func (s *RSIContrarianStrategy) Name() string {
	return "rsi-contrarian"
}

// Description returns the strategy description
func (s *RSIContrarianStrategy) Description() string {
	return "RSI Contrarian - mean reversion on extreme oversold conditions"
}

// Analyze analyzes a crypto symbol for RSI contrarian opportunity
func (s *RSIContrarianStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	candles, err := s.provider.GetDailyCandles(ctx, stock.Symbol, 55)
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
	details["rsi_threshold"] = s.rsiThreshold
	details["ma20"] = ind.MA20
	details["bb_lower"] = ind.BBLower

	// Condition 1: RSI below threshold (extreme oversold)
	if ind.RSI14 >= s.rsiThreshold {
		return nil, nil
	}

	// Condition 2: Price below BB lower band
	if ind.BBLower <= 0 || currentPrice > ind.BBLower {
		return nil, nil
	}

	// Condition 3: Volume present (not dead market) — at least 50% of avg
	if ind.AvgVol > 0 {
		volRatio := float64(current.Volume) / ind.AvgVol
		details["volume_ratio"] = volRatio
		if volRatio < 0.5 {
			return nil, nil // Dead volume, skip
		}
	}

	// Condition 4: Not in free-fall — current candle should show some buying pressure
	// (close in upper 40% of candle range, OR green candle)
	candleRange := current.High - current.Low
	if candleRange > 0 {
		closePosition := (current.Close - current.Low) / candleRange
		details["close_position"] = closePosition
		isGreen := current.Close > current.Open
		if closePosition < 0.3 && !isGreen {
			return nil, nil // Still falling hard
		}
	}

	// Condition 5: Drop from MA20 should be significant (at least 5%)
	if ind.MA20 > 0 {
		dropFromMA20 := (ind.MA20 - currentPrice) / ind.MA20 * 100
		details["drop_from_ma20_pct"] = dropFromMA20
		if dropFromMA20 < 5.0 {
			return nil, nil // Not enough drop to justify contrarian entry
		}
	}

	// Calculate stop loss and targets
	low20 := CalculateLowestLow(candles, 20)
	if low20 <= 0 {
		low20 = current.Low
	}

	stopLoss := low20 * 0.95 // 5% below 20-day low (wider stop for contrarian)
	target1 := ind.MA20       // Mean reversion to MA20
	if target1 <= currentPrice {
		target1 = currentPrice * 1.03
	}

	// Target2: halfway between MA20 and BB upper
	target2 := ind.MA20
	if ind.BBUpper > 0 && ind.MA20 > 0 {
		target2 = (ind.MA20 + ind.BBUpper) / 2
	}
	if target2 <= target1 {
		target2 = target1 * 1.03
	}

	riskPerShare := currentPrice - stopLoss
	if riskPerShare <= 0 {
		return nil, nil
	}

	details["stop_loss"] = stopLoss
	details["target1"] = target1
	details["target2"] = target2
	details["low_20d"] = low20

	strength := s.calculateStrength(ind.RSI14, currentPrice, ind.MA20, ind.BBLower)
	probability := s.calculateProbability(strength, ind.RSI14)

	reason := fmt.Sprintf("RSI contrarian: RSI=%.0f (<%0.f), price %.0f below BB lower %.0f, target MA20 %.0f",
		ind.RSI14, s.rsiThreshold, currentPrice, ind.BBLower, ind.MA20)

	guide := &TradeGuide{
		EntryPrice:      currentPrice,
		EntryType:       "market",
		StopLoss:        stopLoss,
		StopLossPct:     riskPerShare / currentPrice * 100,
		Target1:         target1,
		Target1Pct:      (target1 - currentPrice) / currentPrice * 100,
		Target2:         target2,
		Target2Pct:      (target2 - currentPrice) / currentPrice * 100,
		RiskRewardRatio: (target1 - currentPrice) / riskPerShare,
		KellyFraction:   0.10, // Conservative for contrarian
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

func (s *RSIContrarianStrategy) calculateStrength(rsi, price, ma20, bbLower float64) float64 {
	var score float64

	// RSI depth (30 pts): deeper oversold = stronger signal
	if rsi <= 15 {
		score += 30
	} else if rsi <= 20 {
		score += 25
	} else if rsi <= 25 {
		score += 18
	} else {
		score += 10
	}

	// Distance below BB lower (25 pts)
	if bbLower > 0 {
		bbDist := (bbLower - price) / bbLower * 100
		if bbDist >= 3.0 {
			score += 25
		} else if bbDist >= 1.0 {
			score += 18
		} else {
			score += 10
		}
	}

	// Drop from MA20 (25 pts)
	if ma20 > 0 {
		drop := (ma20 - price) / ma20 * 100
		if drop >= 15.0 {
			score += 25
		} else if drop >= 10.0 {
			score += 20
		} else if drop >= 5.0 {
			score += 12
		}
	}

	// Base score for passing all conditions
	score += 20

	return math.Min(score, 100)
}

func (s *RSIContrarianStrategy) calculateProbability(strength, rsi float64) float64 {
	prob := 40.0

	prob += strength * 0.08

	// Extremely oversold RSI boosts probability
	if rsi <= 15 {
		prob += 5
	} else if rsi <= 20 {
		prob += 3
	}

	return math.Max(40, math.Min(prob, 60))
}
