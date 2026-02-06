package strategy

import (
	"context"
	"fmt"
	"math"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// BreakoutConfig holds configuration for the breakout strategy
type BreakoutConfig struct {
	HighPeriod             int     // Period for highest high (default 20 days)
	VolumeMultiple         float64 // Minimum volume vs average (default 1.5x)
	MaxRSI                 float64 // Maximum RSI (not overbought, default 80)
	ConsolidationThreshold float64 // Prior BB width must be < current * this (default 0.8)

	// Quality filters
	MinPrice          float64
	MaxTickerLength   int
	MinDailyDollarVol float64
}

// DefaultBreakoutConfig returns default configuration
func DefaultBreakoutConfig() BreakoutConfig {
	return BreakoutConfig{
		HighPeriod:             20,
		VolumeMultiple:         1.5,
		MaxRSI:                 80,
		ConsolidationThreshold: 0.8,

		MinPrice:          5.0,
		MaxTickerLength:   4,
		MinDailyDollarVol: 500000,
	}
}

// BreakoutStrategy implements the "Breakout" strategy
// Buy signal when:
// 1. Price breaks above 20-day high
// 2. Volume 1.5x+ above average (confirmation)
// 3. Price above MA50 (trend confirmation)
// Supporting: RSI not overbought, prior consolidation, above MA20
type BreakoutStrategy struct {
	config   BreakoutConfig
	provider provider.Provider
}

// NewBreakoutStrategy creates a new breakout strategy
func NewBreakoutStrategy(cfg BreakoutConfig, p provider.Provider) *BreakoutStrategy {
	return &BreakoutStrategy{
		config:   cfg,
		provider: p,
	}
}

// Name returns the strategy name
func (s *BreakoutStrategy) Name() string {
	return "breakout"
}

// Description returns the strategy description
func (s *BreakoutStrategy) Description() string {
	return "Breakout - Buy 20-day high breakouts with volume confirmation"
}

// Analyze analyzes a stock for breakout opportunity
func (s *BreakoutStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	// Pre-filter: Ticker length
	if s.config.MaxTickerLength > 0 && len(stock.Symbol) > s.config.MaxTickerLength {
		return nil, fmt.Errorf("ticker too long: %s", stock.Symbol)
	}

	// Need at least 60 days for MA50 + buffer
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

	highestHigh := CalculateHighestHigh(candles, s.config.HighPeriod)
	if highestHigh == 0 {
		return nil, fmt.Errorf("could not calculate highest high")
	}

	priorBBWidth := CalculatePriorBBWidth(candles, 20, 2.0, 5)

	// Get latest candle
	today := candles[len(candles)-1]

	// Quality filters
	if s.config.MinPrice > 0 && today.Close < s.config.MinPrice {
		return nil, fmt.Errorf("price too low: $%.2f", today.Close)
	}

	dailyDollarVol := today.Close * float64(today.Volume)
	if s.config.MinDailyDollarVol > 0 && dailyDollarVol < s.config.MinDailyDollarVol {
		return nil, fmt.Errorf("liquidity too low: $%.0f", dailyDollarVol)
	}

	details := make(map[string]float64)
	details["daily_dollar_vol"] = dailyDollarVol
	details["close"] = today.Close
	details["high"] = today.High

	// Condition 1: Price breaks above 20-day high
	breakout := today.High > highestHigh && highestHigh > 0
	details["highest_high_20"] = highestHigh
	details["breakout_pct"] = (today.High - highestHigh) / highestHigh * 100

	// Condition 2: Volume confirmation
	volumeRatio := float64(today.Volume) / ind.AvgVol
	volumeConfirm := volumeRatio >= s.config.VolumeMultiple
	details["volume_ratio"] = volumeRatio

	// Condition 3: Above MA50 (trend confirmation)
	aboveMA50 := today.Close > ind.MA50
	details["ma50"] = ind.MA50
	details["price_vs_ma50_pct"] = (today.Close - ind.MA50) / ind.MA50 * 100

	// Supporting conditions
	rsiNotOverbought := ind.RSI14 < s.config.MaxRSI
	details["rsi14"] = ind.RSI14

	priorConsolidation := priorBBWidth > 0 && ind.BBWidth > 0 &&
		priorBBWidth < ind.BBWidth*s.config.ConsolidationThreshold
	details["bb_width"] = ind.BBWidth
	details["prior_bb_width"] = priorBBWidth

	aboveMA20 := today.Close > ind.MA20
	details["ma20"] = ind.MA20

	// Calculate strength
	strength := calculateBreakoutStrength(
		breakout, volumeConfirm, aboveMA50,
		rsiNotOverbought, priorConsolidation, aboveMA20,
		volumeRatio,
	)

	// Only return BUY signal if all 3 core conditions are met
	if !breakout || !volumeConfirm || !aboveMA50 {
		return nil, nil
	}

	probability := calculateBreakoutProbability(strength, volumeRatio, priorConsolidation, rsiNotOverbought)
	guide := s.calculateTradeGuide(today.Close, highestHigh)

	return &Signal{
		Stock:       stock,
		Type:        SignalBuy,
		Strategy:    s.Name(),
		Strength:    strength,
		Probability: probability,
		Reason: fmt.Sprintf("Breakout above 20d high ($%.2f), volume %.1fx avg, %.1f%% above MA50",
			highestHigh, volumeRatio, details["price_vs_ma50_pct"]),
		Details: details,
		Guide:   guide,
	}, nil
}

// calculateTradeGuide generates trading guidance for breakout
func (s *BreakoutStrategy) calculateTradeGuide(currentPrice, breakoutLevel float64) *TradeGuide {
	// Stop: below breakout level or 3% below entry
	stopLoss := math.Max(breakoutLevel*0.97, currentPrice*0.97)
	stopLossPct := (currentPrice - stopLoss) / currentPrice

	riskPerShare := currentPrice - stopLoss

	// Breakouts can run far - wider targets
	target1 := currentPrice + riskPerShare*1.5
	target2 := currentPrice + riskPerShare*3.0

	guide := &TradeGuide{
		EntryPrice:      currentPrice,
		EntryType:       "limit",
		StopLoss:        stopLoss,
		StopLossPct:     stopLossPct * 100,
		Target1:         target1,
		Target1Pct:      riskPerShare * 1.5 / currentPrice * 100,
		Target2:         target2,
		Target2Pct:      riskPerShare * 3.0 / currentPrice * 100,
		RiskRewardRatio: 2.25,
	}

	// Kelly fraction
	winRate := 0.45 // Breakouts have lower win rate but higher R:R
	avgWin := 2.25
	avgLoss := 1.0
	guide.KellyFraction = (winRate*avgWin - (1-winRate)*avgLoss) / avgWin
	if guide.KellyFraction < 0 {
		guide.KellyFraction = 0
	}

	return guide
}

func calculateBreakoutStrength(
	breakout, volumeConfirm, aboveMA50 bool,
	rsiNotOverbought, priorConsolidation, aboveMA20 bool,
	volumeRatio float64,
) float64 {
	var score float64

	// Core conditions (60 points)
	if breakout {
		score += 20
	}
	if volumeConfirm {
		score += 20
		// Bonus for very high volume
		if volumeRatio > 2.0 {
			score += 5
		}
	}
	if aboveMA50 {
		score += 20
	}

	// Supporting conditions (40 points)
	if rsiNotOverbought {
		score += 15
	}
	if priorConsolidation {
		score += 15
	}
	if aboveMA20 {
		score += 10
	}

	return math.Min(score, 100)
}

func calculateBreakoutProbability(strength, volumeRatio float64, priorConsolidation, rsiOK bool) float64 {
	prob := 45.0

	// Strength contribution: max +8%
	prob += strength * 0.08

	// Volume factor: high volume breakouts are more reliable
	if volumeRatio > 2.0 {
		prob += 3
	} else if volumeRatio > 1.5 {
		prob += 1
	}

	// Prior consolidation makes breakouts more reliable
	if priorConsolidation {
		prob += 3
	}

	// RSI room to run
	if rsiOK {
		prob += 1
	}

	return math.Max(45, math.Min(prob, 65))
}
