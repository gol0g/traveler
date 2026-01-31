package strategy

import (
	"context"
	"fmt"
	"math"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// PullbackConfig holds configuration for the pullback strategy
type PullbackConfig struct {
	MA20TouchTolerance float64 // How close to MA20 counts as "touch" (e.g., 0.02 = 2%)
	MinVolumeRatio     float64 // Maximum volume ratio for pullback (low volume = weak selling)
	RequireBullishBody bool    // Require close > open (bullish candle)
}

// DefaultPullbackConfig returns default configuration
func DefaultPullbackConfig() PullbackConfig {
	return PullbackConfig{
		MA20TouchTolerance: 0.02,  // 2% tolerance
		MinVolumeRatio:     0.8,   // Volume should be below average
		RequireBullishBody: false, // Allow long lower shadow too
	}
}

// PullbackStrategy implements the "Pullback in Uptrend" strategy
// Buy signal when:
// 1. Price is above MA50 (confirmed uptrend)
// 2. Price pulls back to touch MA20
// 3. Volume is lower than average (weak selling pressure)
// 4. Shows reversal sign (bullish candle or long lower shadow)
type PullbackStrategy struct {
	config   PullbackConfig
	provider provider.Provider
}

// NewPullbackStrategy creates a new pullback strategy
func NewPullbackStrategy(cfg PullbackConfig, p provider.Provider) *PullbackStrategy {
	return &PullbackStrategy{
		config:   cfg,
		provider: p,
	}
}

// Name returns the strategy name
func (s *PullbackStrategy) Name() string {
	return "pullback"
}

// Description returns the strategy description
func (s *PullbackStrategy) Description() string {
	return "Pullback in Uptrend - Buy when uptrending stock pulls back to MA20"
}

// Analyze analyzes a stock for pullback opportunity
func (s *PullbackStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	// Need at least 60 days of data for MA50 + buffer
	candles, err := s.provider.GetDailyCandles(ctx, stock.Symbol, 70)
	if err != nil {
		return nil, err
	}

	if len(candles) < 50 {
		return nil, fmt.Errorf("insufficient data: got %d candles, need 50", len(candles))
	}

	// Calculate indicators
	ind := CalculateIndicators(candles)
	if ind.MA50 == 0 || ind.MA20 == 0 {
		return nil, fmt.Errorf("could not calculate moving averages")
	}

	// Get latest candle
	today := candles[len(candles)-1]
	yesterday := candles[len(candles)-2]

	// Check conditions
	details := make(map[string]float64)

	// Condition 1: Price above MA50 (uptrend)
	aboveMA50 := today.Close > ind.MA50
	details["close"] = today.Close
	details["ma50"] = ind.MA50
	details["price_vs_ma50_pct"] = (today.Close - ind.MA50) / ind.MA50 * 100

	// Condition 2: Low touched MA20 (pullback)
	ma20Lower := ind.MA20 * (1 - s.config.MA20TouchTolerance)
	ma20Upper := ind.MA20 * (1 + s.config.MA20TouchTolerance)
	touchedMA20 := today.Low <= ma20Upper && today.Low >= ma20Lower*0.95
	details["ma20"] = ind.MA20
	details["low"] = today.Low
	details["price_vs_ma20_pct"] = (today.Low - ind.MA20) / ind.MA20 * 100

	// Condition 3: Volume below average (weak selling)
	todayVolume := float64(today.Volume)
	volumeRatio := todayVolume / ind.AvgVol
	lowVolume := volumeRatio < s.config.MinVolumeRatio
	details["volume_ratio"] = volumeRatio

	// Condition 4: Reversal sign
	// Option A: Bullish candle (close > open)
	bullishCandle := today.Close > today.Open

	// Option B: Long lower shadow (buyers stepped in)
	bodySize := math.Abs(today.Close - today.Open)
	lowerShadow := math.Min(today.Open, today.Close) - today.Low
	longLowerShadow := lowerShadow > bodySize*1.5

	hasReversalSign := bullishCandle || longLowerShadow
	details["bullish_candle"] = boolToFloat(bullishCandle)
	details["long_lower_shadow"] = boolToFloat(longLowerShadow)

	// Bonus: RSI in healthy range (not overbought)
	rsiOK := ind.RSI14 < 70
	details["rsi14"] = ind.RSI14

	// Bonus: Price bouncing (today's low > yesterday's low)
	bouncing := today.Low > yesterday.Low
	details["bouncing"] = boolToFloat(bouncing)

	// Calculate signal strength
	strength := calculatePullbackStrength(
		aboveMA50, touchedMA20, lowVolume, hasReversalSign,
		rsiOK, bouncing, details["price_vs_ma50_pct"],
	)

	// Determine signal type
	signalType := SignalHold
	reason := ""

	if aboveMA50 && touchedMA20 && hasReversalSign {
		if lowVolume {
			signalType = SignalBuy
			reason = fmt.Sprintf("Uptrend pullback to MA20 (%.1f%% above MA50), low volume (%.1fx), ",
				details["price_vs_ma50_pct"], volumeRatio)
			if bullishCandle {
				reason += "bullish candle"
			} else {
				reason += "long lower shadow"
			}
		} else {
			signalType = SignalBuy
			strength *= 0.7 // Reduce strength if volume is not ideal
			reason = fmt.Sprintf("Uptrend pullback to MA20, but volume is %.1fx (watch for confirmation)", volumeRatio)
		}
	} else if !aboveMA50 {
		reason = fmt.Sprintf("Not in uptrend (%.1f%% below MA50)", details["price_vs_ma50_pct"])
	} else if !touchedMA20 {
		reason = fmt.Sprintf("Price not near MA20 (%.1f%% away)", details["price_vs_ma20_pct"])
	}

	// Only return signal if it's a buy
	if signalType != SignalBuy {
		return nil, nil
	}

	return &Signal{
		Stock:       stock,
		Type:        signalType,
		Strategy:    s.Name(),
		Strength:    strength,
		Probability: calculatePullbackProbability(strength, ind.RSI14, volumeRatio, bouncing),
		Reason:      reason,
		Details:     details,
	}, nil
}

// calculatePullbackStrength calculates signal strength 0-100
func calculatePullbackStrength(
	aboveMA50, touchedMA20, lowVolume, hasReversal bool,
	rsiOK, bouncing bool, priceVsMA50 float64,
) float64 {
	var score float64

	// Core conditions (60 points max)
	if aboveMA50 {
		score += 20
		// Bonus for stronger uptrend
		if priceVsMA50 > 5 {
			score += 5
		}
	}
	if touchedMA20 {
		score += 20
	}
	if hasReversal {
		score += 20
	}

	// Supporting conditions (40 points max)
	if lowVolume {
		score += 15
	}
	if rsiOK {
		score += 10
	}
	if bouncing {
		score += 15
	}

	return math.Min(score, 100)
}

// calculatePullbackProbability estimates success probability
func calculatePullbackProbability(strength, rsi, volumeRatio float64, bouncing bool) float64 {
	prob := strength * 0.6 // Base from strength

	// RSI bonus
	if rsi < 50 {
		prob += 10 // Room to run
	} else if rsi > 65 {
		prob -= 10 // Getting overbought
	}

	// Volume bonus
	if volumeRatio < 0.7 {
		prob += 10 // Very low selling pressure
	}

	// Bouncing bonus
	if bouncing {
		prob += 10
	}

	return math.Max(0, math.Min(prob, 100))
}

func boolToFloat(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}
