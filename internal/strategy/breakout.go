package strategy

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync"

	"traveler/internal/provider"
	"traveler/internal/symbols"
	"traveler/pkg/model"
)

// BreakoutConfig holds configuration for the breakout strategy
type BreakoutConfig struct {
	HighPeriod             int     // Period for highest high (default 20 days)
	VolumeMultiple         float64 // Minimum volume vs average (default 1.5x)
	MaxRSI                 float64 // Maximum RSI (not overbought, default 80)
	ConsolidationThreshold float64 // Prior BB width must be < current * this (default 0.8)

	// KR market stricter filters
	KRVolumeMultiple float64 // KR: higher volume threshold (default 2.0x)
	KRMinBreakoutPct float64 // KR: minimum breakout % above 20d high (default 1.5%)

	// 수렴 필수 여부 (default true, bull에서 false로 완화 가능)
	RequireConsolidation bool

	// Quality filters
	MinPrice          float64
	MaxTickerLength   int
	MinDailyDollarVol float64

	// Market regime filter: broad market must be above MA20
	// US: "SPY", KR: "069500" (KODEX 200)
	MarketRegimeSymbol string
}

// DefaultBreakoutConfig returns default configuration
func DefaultBreakoutConfig() BreakoutConfig {
	return BreakoutConfig{
		HighPeriod:             20,
		VolumeMultiple:         1.5,
		MaxRSI:                 80,
		ConsolidationThreshold: 0.8,

		// KR market: stricter to avoid false breakouts
		KRVolumeMultiple: 2.0,
		KRMinBreakoutPct: 1.5,

		MinPrice:          5.0,
		MaxTickerLength:   4,
		MinDailyDollarVol: 500000,

		RequireConsolidation: true,
	}
}

// BreakoutStrategy implements the "Breakout" strategy
// Buy signal when:
// 1. Close breaks above 20-day high (종가 기준, 장중 터치 제외)
// 2. Volume 1.5x+ above 20-day average (confirmation)
// 3. Price above MA50 (trend confirmation)
// Supporting: RSI not overbought, prior consolidation (없으면 strength 0.7x), above MA20
type BreakoutStrategy struct {
	config   BreakoutConfig
	provider provider.Provider

	// Market regime cache (per scan session)
	regimeMu      sync.Mutex
	regimeChecked bool
	regimeOK      bool
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

// ResetRegimeCache resets the cached regime check (call at start of each scan cycle)
func (s *BreakoutStrategy) ResetRegimeCache() {
	s.regimeMu.Lock()
	s.regimeChecked = false
	s.regimeOK = false
	s.regimeMu.Unlock()
}

// checkMarketRegime checks if the broad market is above MA20.
// Result is cached for the entire scan session.
func (s *BreakoutStrategy) checkMarketRegime(ctx context.Context) bool {
	s.regimeMu.Lock()
	defer s.regimeMu.Unlock()

	if s.regimeChecked {
		return s.regimeOK
	}

	s.regimeChecked = true
	s.regimeOK = true // default to true if no symbol configured or on error

	sym := s.config.MarketRegimeSymbol
	if sym == "" {
		return s.regimeOK
	}

	candles, err := s.provider.GetDailyCandles(ctx, sym, 30)
	if err != nil {
		log.Printf("[BREAKOUT] regime check: failed to fetch %s: %v (allowing entries)", sym, err)
		return s.regimeOK
	}

	if len(candles) < 20 {
		log.Printf("[BREAKOUT] regime check: insufficient %s data (%d candles)", sym, len(candles))
		return s.regimeOK
	}

	ma20 := CalculateMA(candles, 20)
	lastClose := candles[len(candles)-1].Close
	s.regimeOK = lastClose > ma20

	if !s.regimeOK {
		log.Printf("[BREAKOUT] regime BEARISH: %s %.2f < MA20 %.2f — skipping breakout entries", sym, lastClose, ma20)
	} else {
		log.Printf("[BREAKOUT] regime OK: %s %.2f > MA20 %.2f", sym, lastClose, ma20)
	}

	return s.regimeOK
}

// Analyze analyzes a stock for breakout opportunity
func (s *BreakoutStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	// Market regime filter: skip all entries if broad market is below MA20
	if !s.checkMarketRegime(ctx) {
		return nil, nil
	}

	// Pre-filter: Ticker length (skip for Korean 6-digit symbols)
	if s.config.MaxTickerLength > 0 && len(stock.Symbol) > s.config.MaxTickerLength && !symbols.IsKoreanSymbol(stock.Symbol) {
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

	// Condition 1: 종가가 20일 최고가 위에서 마감 (장중 터치만으로는 페이크)
	breakout := today.Close > highestHigh && highestHigh > 0
	details["highest_high_20"] = highestHigh
	details["breakout_pct"] = (today.Close - highestHigh) / highestHigh * 100

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

	// KR market: stricter filters for false breakout prevention
	isKR := symbols.IsKoreanSymbol(stock.Symbol)
	details["is_kr"] = boolToFloat(isKR)

	if isKR {
		// KR: 거래량 기준 상향 (2.0x)
		volumeConfirm = volumeRatio >= s.config.KRVolumeMultiple
		details["kr_volume_threshold"] = s.config.KRVolumeMultiple

		// KR: 최소 돌파 폭 (20일 고점 대비 1.5% 이상)
		breakoutPct := details["breakout_pct"]
		if breakoutPct < s.config.KRMinBreakoutPct {
			breakout = false
			details["kr_min_breakout_pct"] = s.config.KRMinBreakoutPct
		}
	}

	// Strong close filter: 종가가 당일 레인지 상위 영역 마감 (약한 마감 → 허위 돌파)
	dayRange := today.High - today.Low
	if dayRange > 0 {
		closePosition := (today.Close - today.Low) / dayRange
		details["close_position"] = closePosition
		minClosePos := 0.6 // US: 상위 40%
		if isKR {
			minClosePos = 0.5 // KR: 상위 50% (변동성 큰 시장)
		}
		if closePosition < minClosePos {
			return nil, nil
		}
	}

	// Only return BUY signal if all 3 core conditions are met
	if !breakout || !volumeConfirm || !aboveMA50 {
		return nil, nil
	}

	// 수렴 체크: RequireConsolidation=true이면 필수, false이면 probability 감소
	if !priorConsolidation {
		if s.config.RequireConsolidation {
			return nil, nil
		}
		// 수렴 없으면 허위 돌파 위험 → probability 30% 감소
	}
	details["prior_consolidation"] = boolToFloat(priorConsolidation)

	reason := fmt.Sprintf("Breakout above 20d high ($%.2f), volume %.1fx avg, %.1f%% above MA50",
		highestHigh, volumeRatio, details["price_vs_ma50_pct"])

	probability := calculateBreakoutProbability(strength, volumeRatio, priorConsolidation, rsiNotOverbought)
	if !priorConsolidation {
		probability *= 0.7
	}
	guide := s.calculateTradeGuide(today.Close, highestHigh, ind.ATR14, candles)

	return &Signal{
		Stock:       stock,
		Type:        SignalBuy,
		Strategy:    s.Name(),
		Strength:    strength,
		Probability: probability,
		Reason:  reason,
		Details: details,
		Guide:   guide,
	}, nil
}

// calculateTradeGuide generates trading guidance for breakout
func (s *BreakoutStrategy) calculateTradeGuide(currentPrice, breakoutLevel, atr float64, candles []model.Candle) *TradeGuide {
	// === 손절: 하이브리드 (ATR + breakout레벨 + 구조적 스윙로우) ===
	atrStop := currentPrice - atr*2.5
	breakoutStop := breakoutLevel * 0.97
	stopLoss := math.Max(atrStop, breakoutStop)

	// 구조적 손절: breakout 레벨 아래 가장 가까운 스윙로우
	structuralStop := FindNearestSupport(candles, breakoutLevel, 30, 2)
	if structuralStop > 0 {
		structuralStop -= atr * 0.25
		if structuralStop > stopLoss {
			stopLoss = structuralStop
		}
	}

	minStop := currentPrice * 0.95
	if stopLoss < minStop {
		stopLoss = minStop
	}

	stopLossPct := (currentPrice - stopLoss) / currentPrice
	riskPerShare := currentPrice - stopLoss

	// === 익절: 측정이동(Measured Move) + 구조적 저항 ===
	consolidationLow := CalculateLowestLow(candles, 20)
	measuredMove := 0.0
	if consolidationLow > 0 && breakoutLevel > consolidationLow {
		measuredMove = breakoutLevel - consolidationLow
	}

	// T1: 측정이동 50% 또는 가장 가까운 저항
	target1 := 0.0
	if measuredMove > 0 {
		target1 = breakoutLevel + measuredMove*0.5
	}
	// 구조적 저항이 더 가까우면 사용 (최소 1.5R)
	nearestRes := FindNearestResistance(candles, currentPrice*1.005, 60, 2)
	if nearestRes > 0 && (nearestRes-currentPrice) >= riskPerShare*1.5 {
		if target1 <= 0 || nearestRes < target1 {
			target1 = nearestRes
		}
	}
	if target1 <= 0 || (target1-currentPrice) < riskPerShare*1.5 {
		target1 = currentPrice + riskPerShare*1.5
	}

	// T2: 측정이동 100% 또는 피보나치 1.618 확장
	target2 := 0.0
	if measuredMove > 0 {
		target2 = breakoutLevel + measuredMove
	}
	if target2 <= target1 && consolidationLow > 0 && breakoutLevel > consolidationLow {
		target2 = FibonacciExtension(consolidationLow, breakoutLevel, 0.618)
	}
	if target2 <= target1 {
		target2 = currentPrice + atr*3.0
	}
	if target2 <= target1 {
		target2 = currentPrice + riskPerShare*3.0
	}

	guide := &TradeGuide{
		EntryPrice:  currentPrice,
		EntryType:   "limit",
		StopLoss:    stopLoss,
		StopLossPct: stopLossPct * 100,
		Target1:     target1,
		Target1Pct:  (target1 - currentPrice) / currentPrice * 100,
		Target2:     target2,
		Target2Pct:  (target2 - currentPrice) / currentPrice * 100,
	}
	if riskPerShare > 0 {
		guide.RiskRewardRatio = (target1 - currentPrice) / riskPerShare
	}

	// Kelly fraction
	winRate := 0.45 // Breakouts have lower win rate but higher R:R
	avgWin := 2.25
	avgLoss := 1.0
	guide.KellyFraction = (winRate*avgWin - (1-winRate)*avgLoss) / avgWin
	if guide.KellyFraction < 0 {
		guide.KellyFraction = 0
	}

	// Trailing stop: infrastructure ready but disabled for short-term swing trades.
	// Enable when hold period is extended or for trend-following mode.
	guide.UseTrailingStop = false
	guide.TrailingMultiplier = 2.5
	guide.EntryATR = atr

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
