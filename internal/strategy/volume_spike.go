package strategy

import (
	"context"
	"fmt"
	"math"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// VolumeSpikeStrategy detects abnormal volume spikes combined with price dips.
// Buys when panic selling creates a volume anomaly and price shows signs of reversal.
// Best suited for bull/sideways markets.
type VolumeSpikeStrategy struct {
	provider provider.Provider
}

// NewVolumeSpikeStrategy creates a new volume spike strategy
func NewVolumeSpikeStrategy(p provider.Provider) *VolumeSpikeStrategy {
	return &VolumeSpikeStrategy{provider: p}
}

// Name returns the strategy name
func (s *VolumeSpikeStrategy) Name() string {
	return "volume-spike"
}

// Description returns the strategy description
func (s *VolumeSpikeStrategy) Description() string {
	return "Volume Spike - buy panic dips with abnormal volume and reversal confirmation"
}

// Analyze analyzes a crypto symbol for volume spike dip-buying opportunity
func (s *VolumeSpikeStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
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
	prev := candles[len(candles)-2]
	currentPrice := current.Close

	details["current_price"] = currentPrice
	details["rsi14"] = ind.RSI14
	details["ma20"] = ind.MA20

	// Condition 1: Volume spike — today's volume > 3x 20-day average
	if ind.AvgVol <= 0 {
		return nil, nil
	}
	volRatio := float64(current.Volume) / ind.AvgVol
	details["volume_ratio"] = volRatio
	details["avg_volume_20"] = ind.AvgVol

	if volRatio < 3.0 {
		return nil, nil // Not enough volume anomaly
	}

	// Condition 2: Price dip — current close below previous close (red candle or gap down)
	dayChange := (currentPrice - prev.Close) / prev.Close * 100
	details["day_change_pct"] = dayChange

	// We want a dip: price dropped or is near day's low
	high5 := CalculateHighestHigh(candles, 5)
	if high5 <= 0 {
		high5 = prev.High
	}
	dropFromHigh := (high5 - currentPrice) / high5 * 100
	details["drop_from_5d_high_pct"] = dropFromHigh

	if dropFromHigh < 3.0 {
		return nil, nil // Not enough dip
	}

	// Condition 3: RSI not overbought (< 50 — buying a dip, not chasing)
	if ind.RSI14 >= 50 {
		return nil, nil
	}

	// Condition 4: Reversal sign — close in upper half of today's range
	// (indicates buying pressure absorbed the sell-off)
	candleRange := current.High - current.Low
	if candleRange <= 0 {
		return nil, nil
	}
	closePosition := (current.Close - current.Low) / candleRange
	details["close_position"] = closePosition

	if closePosition < 0.4 {
		return nil, nil // Close near low, selling pressure still dominant
	}

	// Condition 5: Lower wick significant (buyers stepped in)
	lowerWick := math.Min(current.Open, current.Close) - current.Low
	wickRatio := lowerWick / candleRange
	details["wick_ratio"] = wickRatio

	if wickRatio < 0.25 {
		return nil, nil // No significant lower wick
	}

	// Calculate stop loss and targets
	stopLoss := current.Low * 0.97 // 3% below today's low
	target1 := prev.Close           // Recovery to previous close
	if target1 <= currentPrice {
		target1 = currentPrice * 1.03
	}
	target2 := high5 * 0.98 // Near recent 5-day high
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

	strength := s.calculateStrength(volRatio, dropFromHigh, closePosition, ind.RSI14, wickRatio)
	probability := s.calculateProbability(strength, volRatio, dropFromHigh)

	reason := fmt.Sprintf("Volume spike: vol %.1fx avg, dip -%.1f%% from 5d high, RSI=%.0f, reversal candle (close pos %.0f%%)",
		volRatio, dropFromHigh, ind.RSI14, closePosition*100)

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
		KellyFraction:   0.12, // Moderate for volume spike
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

func (s *VolumeSpikeStrategy) calculateStrength(volRatio, dropPct, closePos, rsi, wickRatio float64) float64 {
	var score float64

	// Volume spike magnitude (25 pts)
	if volRatio >= 5.0 {
		score += 25
	} else if volRatio >= 4.0 {
		score += 20
	} else if volRatio >= 3.0 {
		score += 15
	}

	// Dip depth (20 pts)
	if dropPct >= 10.0 {
		score += 20
	} else if dropPct >= 7.0 {
		score += 15
	} else if dropPct >= 3.0 {
		score += 10
	}

	// Reversal quality — close position (20 pts)
	if closePos >= 0.7 {
		score += 20
	} else if closePos >= 0.5 {
		score += 14
	} else {
		score += 8
	}

	// RSI (15 pts): lower RSI = better dip opportunity
	if rsi <= 25 {
		score += 15
	} else if rsi <= 35 {
		score += 10
	} else {
		score += 5
	}

	// Wick quality (10 pts)
	if wickRatio >= 0.5 {
		score += 10
	} else if wickRatio >= 0.35 {
		score += 7
	} else {
		score += 3
	}

	// Base
	score += 10

	return math.Min(score, 100)
}

func (s *VolumeSpikeStrategy) calculateProbability(strength, volRatio, dropPct float64) float64 {
	prob := 42.0

	prob += strength * 0.07

	if volRatio >= 5.0 {
		prob += 3
	}
	if dropPct >= 7.0 {
		prob += 2
	}

	return math.Max(42, math.Min(prob, 62))
}
