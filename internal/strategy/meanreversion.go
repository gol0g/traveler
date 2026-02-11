package strategy

import (
	"context"
	"fmt"
	"math"

	"traveler/internal/provider"
	"traveler/internal/symbols"
	"traveler/pkg/model"
)

// MeanReversionConfig holds configuration for the mean reversion strategy
type MeanReversionConfig struct {
	RSIOversold      float64 // RSI threshold for oversold (default 30)
	BBTouchTolerance float64 // How close to BB lower counts as "touch" (e.g., 0.01 = 1%)

	// Quality filters
	MinPrice          float64
	MaxTickerLength   int
	MinDailyDollarVol float64
}

// DefaultMeanReversionConfig returns default configuration
func DefaultMeanReversionConfig() MeanReversionConfig {
	return MeanReversionConfig{
		RSIOversold:      30,
		BBTouchTolerance: 0.01, // 1% tolerance

		MinPrice:          5.0,
		MaxTickerLength:   4,
		MinDailyDollarVol: 500000,
	}
}

// MeanReversionStrategy implements the "Mean Reversion" strategy
// Buy signal when:
// 1. RSI14 < 30 (oversold)
// 2. Price at or below Bollinger lower band
// 3. Reversal candle (bullish body or long lower shadow, ATR 기준 포함)
// 4. Above MA200 (or MA50 fallback) — 장기 상승 추세 내 급락만 (칼날잡기 방지)
// Supporting: volume increase, deeply oversold
type MeanReversionStrategy struct {
	config   MeanReversionConfig
	provider provider.Provider
}

// NewMeanReversionStrategy creates a new mean reversion strategy
func NewMeanReversionStrategy(cfg MeanReversionConfig, p provider.Provider) *MeanReversionStrategy {
	return &MeanReversionStrategy{
		config:   cfg,
		provider: p,
	}
}

// Name returns the strategy name
func (s *MeanReversionStrategy) Name() string {
	return "mean-reversion"
}

// Description returns the strategy description
func (s *MeanReversionStrategy) Description() string {
	return "Mean Reversion - Buy oversold stocks bouncing off Bollinger lower band"
}

// Analyze analyzes a stock for mean reversion opportunity
func (s *MeanReversionStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	// Pre-filter: Ticker length (skip for Korean 6-digit symbols)
	if s.config.MaxTickerLength > 0 && len(stock.Symbol) > s.config.MaxTickerLength && !symbols.IsKoreanSymbol(stock.Symbol) {
		return nil, fmt.Errorf("ticker too long: %s", stock.Symbol)
	}

	// Need 250 days for MA200 + buffer
	candles, err := s.provider.GetDailyCandles(ctx, stock.Symbol, 250)
	if err != nil {
		return nil, err
	}

	if len(candles) < 50 {
		return nil, fmt.Errorf("insufficient data: got %d candles, need 50", len(candles))
	}

	// Calculate indicators
	ind := CalculateIndicators(candles)
	if ind.MA20 == 0 || ind.BBLower == 0 {
		return nil, fmt.Errorf("could not calculate indicators")
	}

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

	// Condition 1: RSI oversold
	rsiOversold := ind.RSI14 < s.config.RSIOversold
	details["rsi14"] = ind.RSI14

	// Condition 2: Price at or below Bollinger lower band
	bbThreshold := ind.BBLower * (1 + s.config.BBTouchTolerance)
	atBBLower := today.Low <= bbThreshold
	details["bb_lower"] = ind.BBLower
	details["bb_upper"] = ind.BBUpper
	details["low"] = today.Low

	// Condition 3: Reversal candle
	bodySize := math.Abs(today.Close - today.Open)
	lowerShadow := math.Min(today.Open, today.Close) - today.Low
	bullishCandle := today.Close > today.Open
	// Body 대비 1.5배 또는 ATR 50% 이상이면 의미 있는 꼬리
	longLowerShadow := lowerShadow > bodySize*1.5 ||
		(ind.ATR14 > 0 && lowerShadow >= ind.ATR14*0.5)
	hasReversal := bullishCandle || longLowerShadow
	details["bullish_candle"] = boolToFloat(bullishCandle)
	details["long_lower_shadow"] = boolToFloat(longLowerShadow)

	// Condition 4 (필수): 장기 추세 내 급락에서만 평균회귀 유효 (칼날잡기 방지)
	// MA200이 있으면 MA200 위, 없으면 MA50으로 대체
	var inUptrend bool
	if ind.MA200 > 0 {
		inUptrend = today.Close > ind.MA200
	} else if ind.MA50 > 0 {
		inUptrend = today.Close > ind.MA50
	}
	details["ma200"] = ind.MA200
	details["ma50"] = ind.MA50
	details["in_uptrend"] = boolToFloat(inUptrend)

	// Supporting conditions
	volumeRatio := float64(today.Volume) / ind.AvgVol
	volumeIncrease := volumeRatio > 1.2
	details["volume_ratio"] = volumeRatio

	deeplyOversold := ind.RSI14 < 25
	details["deeply_oversold"] = boolToFloat(deeplyOversold)

	// Calculate strength
	strength := calculateMeanReversionStrength(
		rsiOversold, atBBLower, hasReversal,
		inUptrend, volumeIncrease, deeplyOversold,
	)

	// 필수 4조건: RSI 과매도 + BB 하단 + 반전 캔들 + 장기 상승 추세
	if !rsiOversold || !atBBLower || !hasReversal || !inUptrend {
		return nil, nil
	}

	probability := calculateMeanReversionProbability(strength, ind.RSI14, inUptrend, volumeIncrease)
	guide := s.calculateTradeGuide(today, ind)

	return &Signal{
		Stock:       stock,
		Type:        SignalBuy,
		Strategy:    s.Name(),
		Strength:    strength,
		Probability: probability,
		Reason: fmt.Sprintf("Oversold bounce: RSI=%.0f, at BB lower ($%.2f), %s, above %s",
			ind.RSI14, ind.BBLower, reversalDesc(bullishCandle, longLowerShadow),
			func() string { if ind.MA200 > 0 { return "MA200" }; return "MA50" }()),
		Details: details,
		Guide:   guide,
	}, nil
}

// calculateTradeGuide generates trading guidance for mean reversion
func (s *MeanReversionStrategy) calculateTradeGuide(today model.Candle, ind *Indicators) *TradeGuide {
	// ATR 기반 손절: 변동성에 맞춰 조절
	atrStop := today.Close - ind.ATR14*1.5   // 1.5 ATR
	lowStop := today.Low * 0.99              // 당일 저점 -1%
	stopLoss := math.Max(atrStop, lowStop)   // 둘 중 보수적(높은) 쪽

	// 최소 보장: -5% floor
	minStop := today.Close * 0.95
	if stopLoss < minStop {
		stopLoss = minStop
	}

	stopLossPct := (today.Close - stopLoss) / today.Close

	// Target 1: MA20 (mean reversion)
	target1 := ind.MA20
	// Target 2: Bollinger upper band
	target2 := ind.BBUpper

	riskPerShare := today.Close - stopLoss
	var rr float64
	if riskPerShare > 0 {
		rr = (target1 - today.Close) / riskPerShare
	}

	guide := &TradeGuide{
		EntryPrice:      today.Close,
		EntryType:       "limit",
		StopLoss:        stopLoss,
		StopLossPct:     stopLossPct * 100,
		Target1:         target1,
		Target1Pct:      (target1 - today.Close) / today.Close * 100,
		Target2:         target2,
		Target2Pct:      (target2 - today.Close) / today.Close * 100,
		RiskRewardRatio: rr,
	}

	// Kelly fraction
	winRate := 0.55
	avgWin := rr
	avgLoss := 1.0
	guide.KellyFraction = (winRate*avgWin - (1-winRate)*avgLoss) / avgWin
	if guide.KellyFraction < 0 {
		guide.KellyFraction = 0
	}

	return guide
}

func calculateMeanReversionStrength(
	rsiOversold, atBBLower, hasReversal bool,
	aboveMA200, volumeIncrease, deeplyOversold bool,
) float64 {
	var score float64

	// Core conditions (60 points)
	if rsiOversold {
		score += 20
	}
	if atBBLower {
		score += 20
	}
	if hasReversal {
		score += 20
	}

	// Supporting conditions (40 points)
	if aboveMA200 {
		score += 15
	}
	if volumeIncrease {
		score += 15
	}
	if deeplyOversold {
		score += 10
	}

	return math.Min(score, 100)
}

func calculateMeanReversionProbability(strength, rsi float64, aboveMA200, volumeIncrease bool) float64 {
	prob := 45.0

	// Strength contribution: max +8%
	prob += strength * 0.08

	// RSI factor
	if rsi < 25 {
		prob += 3
	} else if rsi < 30 {
		prob += 1
	}

	// MA200 factor
	if aboveMA200 {
		prob += 3
	}

	// Volume factor
	if volumeIncrease {
		prob += 2
	}

	return math.Max(45, math.Min(prob, 65))
}

func reversalDesc(bullish, longShadow bool) string {
	if bullish && longShadow {
		return "bullish candle + long lower shadow"
	}
	if bullish {
		return "bullish candle"
	}
	return "long lower shadow"
}
