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

// PullbackConfig holds configuration for the pullback strategy
type PullbackConfig struct {
	MA20TouchTolerance float64 // How close to MA20 counts as "touch" (e.g., 0.02 = 2%)
	MinVolumeRatio     float64 // Maximum volume ratio for pullback (low volume = weak selling)
	RequireBullishBody bool    // Require close > open (bullish candle)

	// Quality filters (to avoid penny stocks, OTC, illiquid stocks)
	MinPrice         float64 // Minimum stock price (default $5)
	MaxTickerLength  int     // Maximum ticker length (4 = exclude OTC 5-letter tickers)
	MinDailyDollarVol float64 // Minimum daily dollar volume (price * volume)

	// Market regime filter: broad market must be above MA20
	// US: "SPY", KR: "069500" (KODEX 200)
	MarketRegimeSymbol string

	// Sideways mode relaxations (set by StockMetaStrategy for sideways regime)
	RequireUptrend bool    // Require price > MA50 + trend confirmation (default true)
	MaxRSI         float64 // Maximum RSI for entry (default 50)

	// KR bull relaxations — some conditions become optional (contribute to strength only)
	RequireVolumePattern bool // Require pullback low vol + reversal vol (default true)
	RequireBouncing      bool // Require today's low > yesterday's low (default true)
}

// DefaultPullbackConfig returns default configuration
func DefaultPullbackConfig() PullbackConfig {
	return PullbackConfig{
		MA20TouchTolerance: 0.02,  // 2% tolerance
		MinVolumeRatio:     0.8,   // Volume should be below average
		RequireBullishBody: false, // Allow long lower shadow too

		// Quality filters
		MinPrice:         5.0,     // $5 minimum (no penny stocks)
		MaxTickerLength:  4,       // Exclude 5+ letter tickers (OTC, warrants)
		MinDailyDollarVol: 500000, // $500K daily volume minimum

		// Uptrend requirement (relaxed in sideways regime)
		RequireUptrend: true,
		MaxRSI:         50,

		// Volume/bouncing required by default
		RequireVolumePattern: true,
		RequireBouncing:      true,
	}
}

// PullbackStrategy implements the "Pullback in Uptrend" strategy
// Buy signal when:
// 1. Broad market (SPY/KOSPI) above MA20 (regime filter)
// 2. Price is above MA50 (confirmed uptrend)
// 3. Price pulls back to touch MA20
// 4. Volume pattern: pullback low volume + reversal volume recovery
// 5. Bouncing: today's low > yesterday's low
// 6. RSI < 50 (room to run)
// 7. Shows reversal sign (bullish candle or long lower shadow)
type PullbackStrategy struct {
	config   PullbackConfig
	provider provider.Provider

	// Market regime cache (per scan session)
	regimeMu      sync.Mutex
	regimeChecked bool
	regimeOK      bool
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

// ResetRegimeCache resets the cached regime check (call at start of each scan cycle)
func (s *PullbackStrategy) ResetRegimeCache() {
	s.regimeMu.Lock()
	s.regimeChecked = false
	s.regimeOK = false
	s.regimeMu.Unlock()
}

// checkMarketRegime checks if the broad market is above MA20.
// Result is cached for the entire scan session.
func (s *PullbackStrategy) checkMarketRegime(ctx context.Context) bool {
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
		log.Printf("[PULLBACK] regime check: failed to fetch %s: %v (allowing entries)", sym, err)
		return s.regimeOK
	}

	if len(candles) < 20 {
		log.Printf("[PULLBACK] regime check: insufficient %s data (%d candles)", sym, len(candles))
		return s.regimeOK
	}

	ma20 := CalculateMA(candles, 20)
	lastClose := candles[len(candles)-1].Close
	s.regimeOK = lastClose > ma20

	if !s.regimeOK {
		log.Printf("[PULLBACK] regime BEARISH: %s %.2f < MA20 %.2f — skipping pullback entries", sym, lastClose, ma20)
	} else {
		log.Printf("[PULLBACK] regime OK: %s %.2f > MA20 %.2f", sym, lastClose, ma20)
	}

	return s.regimeOK
}

// Analyze analyzes a stock for pullback opportunity
func (s *PullbackStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	// Market regime filter: skip all entries if broad market is below MA20
	if !s.checkMarketRegime(ctx) {
		return nil, nil
	}

	// Pre-filter: Ticker length (exclude OTC 5-letter tickers, warrants, etc.)
	// Korean symbols are 6-digit numeric codes — skip this filter for them
	if s.config.MaxTickerLength > 0 && len(stock.Symbol) > s.config.MaxTickerLength && !symbols.IsKoreanSymbol(stock.Symbol) {
		return nil, fmt.Errorf("ticker too long: %s (likely OTC/warrant)", stock.Symbol)
	}

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

	// Quality filter: Minimum price (no penny stocks)
	if s.config.MinPrice > 0 && today.Close < s.config.MinPrice {
		return nil, fmt.Errorf("price too low: $%.2f < $%.2f", today.Close, s.config.MinPrice)
	}

	// Quality filter: Minimum daily dollar volume (liquidity)
	dailyDollarVol := today.Close * float64(today.Volume)
	if s.config.MinDailyDollarVol > 0 && dailyDollarVol < s.config.MinDailyDollarVol {
		return nil, fmt.Errorf("liquidity too low: $%.0f < $%.0f", dailyDollarVol, s.config.MinDailyDollarVol)
	}

	// Check conditions
	details := make(map[string]float64)

	// Record quality metrics
	details["daily_dollar_vol"] = dailyDollarVol
	details["avg_volume"] = ind.AvgVol

	// Condition 1: Price above MA50 (uptrend) + 추세 확인
	aboveMA50 := today.Close > ind.MA50
	details["close"] = today.Close
	details["ma50"] = ind.MA50
	details["price_vs_ma50_pct"] = (today.Close - ind.MA50) / ind.MA50 * 100
	details["ma50_slope"] = ind.MA50Slope
	details["atr14"] = ind.ATR14

	// 추세 확인: MA50 기울기 상승 또는 MA20 > MA50 (정배열)
	trendConfirmed := ind.MA50Slope > 0 || (ind.MA20 > 0 && ind.MA20 > ind.MA50)
	details["trend_confirmed"] = boolToFloat(trendConfirmed)

	// Condition 2: Low touched MA20 (pullback)
	ma20Lower := ind.MA20 * (1 - s.config.MA20TouchTolerance)
	ma20Upper := ind.MA20 * (1 + s.config.MA20TouchTolerance)
	touchedMA20 := today.Low <= ma20Upper && today.Low >= ma20Lower*0.95
	details["ma20"] = ind.MA20
	details["low"] = today.Low
	details["price_vs_ma20_pct"] = (today.Low - ind.MA20) / ind.MA20 * 100

	// Condition 3: Volume pattern (조정구간 거래량 감소 + 반전일 거래량 회복)
	todayVolume := float64(today.Volume)
	volumeRatio := todayVolume / ind.AvgVol
	details["volume_ratio"] = volumeRatio

	// 조정 구간 평균 거래량 (반전 당일 제외, 직전 3일)
	var pullbackAvgVol float64
	if len(candles) >= 5 {
		var pbVolSum float64
		for i := len(candles) - 4; i < len(candles)-1; i++ {
			pbVolSum += float64(candles[i].Volume)
		}
		pullbackAvgVol = pbVolSum / 3.0
	}
	details["pullback_avg_vol_ratio"] = pullbackAvgVol / ind.AvgVol

	// 정석 패턴: 조정일 거래량 감소 + 반전일 거래량 회복
	pullbackVolLow := pullbackAvgVol < ind.AvgVol          // 조정구간 매도 약함
	reversalVolUp := todayVolume >= ind.AvgVol*0.8          // 반전일 수급 유입
	volumePattern := pullbackVolLow && reversalVolUp
	details["volume_pattern"] = boolToFloat(volumePattern)

	// Condition 4: Reversal sign
	// Option A: Bullish candle (close > open)
	bullishCandle := today.Close > today.Open

	// Option B: Long lower shadow (buyers stepped in)
	bodySize := math.Abs(today.Close - today.Open)
	lowerShadow := math.Min(today.Open, today.Close) - today.Low
	// Body 대비 1.5배 또는 ATR 50% 이상이면 의미 있는 꼬리
	longLowerShadow := lowerShadow > bodySize*1.5 ||
		(ind.ATR14 > 0 && lowerShadow >= ind.ATR14*0.5)

	hasReversalSign := bullishCandle || longLowerShadow
	details["bullish_candle"] = boolToFloat(bullishCandle)
	details["long_lower_shadow"] = boolToFloat(longLowerShadow)

	// Required: RSI below threshold (default 50, relaxed to 60 in sideways)
	maxRSI := s.config.MaxRSI
	if maxRSI == 0 {
		maxRSI = 50
	}
	rsiOK := ind.RSI14 < maxRSI
	details["rsi14"] = ind.RSI14

	// Required: Price bouncing (today's low > yesterday's low)
	bouncing := today.Low > yesterday.Low
	details["bouncing"] = boolToFloat(bouncing)

	// Calculate signal strength
	strength := calculatePullbackStrength(
		aboveMA50, trendConfirmed, touchedMA20, volumePattern, hasReversalSign,
		rsiOK, bouncing, details["price_vs_ma50_pct"],
	)

	// Uptrend check: required by default, skipped in sideways regime
	uptrendOK := !s.config.RequireUptrend || (aboveMA50 && trendConfirmed)

	// Determine signal type — ALL required conditions must be met
	signalType := SignalHold
	reason := ""

	// Volume/bouncing: required by default, optional when configured (KR bull)
	volumeOK := volumePattern || !s.config.RequireVolumePattern
	bouncingOK := bouncing || !s.config.RequireBouncing

	if uptrendOK && touchedMA20 && hasReversalSign && volumeOK && bouncingOK && rsiOK {
		signalType = SignalBuy
		if s.config.RequireUptrend {
			reason = fmt.Sprintf("Pullback to MA20 (%.1f%% >MA50, slope %.2f%%, RSI %.0f), vol OK (pb:%.1fx, rev:%.1fx), ",
				details["price_vs_ma50_pct"], ind.MA50Slope, ind.RSI14, pullbackAvgVol/ind.AvgVol, volumeRatio)
		} else {
			reason = fmt.Sprintf("Pullback to MA20 (RSI %.0f), vol OK (pb:%.1fx, rev:%.1fx), ",
				ind.RSI14, pullbackAvgVol/ind.AvgVol, volumeRatio)
		}
		if bullishCandle {
			reason += "bullish candle, bouncing"
		} else {
			reason += "long lower shadow, bouncing"
		}
	} else if s.config.RequireUptrend && !aboveMA50 {
		reason = fmt.Sprintf("Not in uptrend (%.1f%% below MA50)", details["price_vs_ma50_pct"])
	} else if s.config.RequireUptrend && !trendConfirmed {
		reason = fmt.Sprintf("Trend not confirmed (MA50 slope: %.2f%%, MA20 vs MA50: %.1f%%)",
			ind.MA50Slope, (ind.MA20-ind.MA50)/ind.MA50*100)
	} else if !touchedMA20 {
		reason = fmt.Sprintf("Price not near MA20 (%.1f%% away)", details["price_vs_ma20_pct"])
	} else if !hasReversalSign {
		reason = "No reversal sign (need bullish candle or long lower shadow)"
	} else if !volumeOK {
		reason = fmt.Sprintf("Volume pattern weak (pb:%.1fx, rev:%.1fx)", pullbackAvgVol/ind.AvgVol, volumeRatio)
	} else if !bouncingOK {
		reason = "Not bouncing (today's low <= yesterday's low)"
	} else if !rsiOK {
		reason = fmt.Sprintf("RSI too high (%.0f >= %.0f)", ind.RSI14, maxRSI)
	}

	// Only return signal if it's a buy
	if signalType != SignalBuy {
		return nil, nil
	}

	// Calculate trading guide
	probability := calculatePullbackProbability(strength, ind.RSI14, volumeRatio, bouncing)
	guide := s.calculateTradeGuide(today.Close, ind.MA20, ind.ATR14, probability, candles)

	return &Signal{
		Stock:       stock,
		Type:        signalType,
		Strategy:    s.Name(),
		Strength:    strength,
		Probability: probability,
		Reason:      reason,
		Details:     details,
		Guide:       guide,
	}, nil
}

// calculateTradeGuide generates actionable trade guidance
func (s *PullbackStrategy) calculateTradeGuide(currentPrice, ma20, atr, winRate float64, candles []model.Candle) *TradeGuide {
	// === 손절: 하이브리드 (ATR + MA20 + 구조적 스윙로우) ===
	atrStop := currentPrice - atr*1.5
	ma20Stop := ma20 * 0.98
	stopLoss := math.Max(atrStop, ma20Stop)

	// 구조적 손절: 가장 가까운 스윙로우 아래 (ATR×0.25 버퍼)
	structuralStop := FindNearestSupport(candles, currentPrice, 30, 2)
	if structuralStop > 0 {
		structuralStop -= atr * 0.25
		if structuralStop > stopLoss {
			stopLoss = structuralStop
		}
	}

	// Floor: -5%
	minStop := currentPrice * 0.95
	if stopLoss < minStop {
		stopLoss = minStop
	}

	stopLossPct := (currentPrice - stopLoss) / currentPrice
	riskPerShare := currentPrice - stopLoss

	// === 익절: 로컬 극값 저항선 기반, R-배수 폴백 ===
	// 가장 가까운 저항 (entry 위 0.5% 이상)
	nearestRes := FindNearestResistance(candles, currentPrice*1.005, 40, 2)

	// T1: 가장 가까운 저항선 (최소 1R 보상)
	target1 := 0.0
	if nearestRes > 0 && (nearestRes-currentPrice) >= riskPerShare {
		target1 = nearestRes
	}
	if target1 <= 0 {
		// 폴백: 20일 최고점
		high20, _ := FindSwingHigh(candles, 20)
		if high20 > currentPrice && (high20-currentPrice) >= riskPerShare {
			target1 = high20
		}
	}
	if target1 <= 0 {
		target1 = currentPrice + riskPerShare*1.5 // R-배수 최종 폴백
	}

	// T2: 다음 저항선 또는 피보나치 확장
	swingLow, _ := FindSwingLow(candles, 20)
	target2 := 0.0
	// 피보나치 1.272 확장 (스윙로우 → T1 레벨)
	if swingLow > 0 && target1 > swingLow {
		target2 = FibonacciExtension(swingLow, target1, 0.272)
	}
	if target2 <= target1 {
		target2 = currentPrice + atr*3.0 // ATR 기반 폴백
	}
	if target2 <= target1 {
		target2 = currentPrice + riskPerShare*2.5
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

	// Position sizing (will be calculated with account balance in CLI)
	// Kelly fraction based on probability
	if winRate > 0 {
		avgWin := 2.0 // 2R target
		avgLoss := 1.0
		w := winRate / 100
		guide.KellyFraction = (w*avgWin - (1-w)*avgLoss) / avgWin
		if guide.KellyFraction < 0 {
			guide.KellyFraction = 0
		}
	}

	// Trailing stop: infrastructure ready but disabled for short-term swing trades.
	// Enable when hold period is extended or for trend-following mode.
	guide.UseTrailingStop = false
	guide.TrailingMultiplier = 2.0
	guide.EntryATR = atr

	return guide
}

// calculatePullbackStrength calculates signal strength 0-100
func calculatePullbackStrength(
	aboveMA50, trendConfirmed, touchedMA20, volumePattern, hasReversal bool,
	rsiOK, bouncing bool, priceVsMA50 float64,
) float64 {
	var score float64

	// Core conditions (70 points max)
	if aboveMA50 {
		score += 15
		if priceVsMA50 > 5 {
			score += 5
		}
	}
	if trendConfirmed {
		score += 15 // MA50 기울기 상승 또는 정배열
	}
	if touchedMA20 {
		score += 20
	}
	if hasReversal {
		score += 15
	}

	// Supporting conditions (30 points max)
	if volumePattern {
		score += 15 // 조정구간 거래량 감소 + 반전일 증가
	}
	if rsiOK {
		score += 5
	}
	if bouncing {
		score += 10
	}

	return math.Min(score, 100)
}

// calculatePullbackProbability estimates success probability
// Realistic range: 45-65% (no strategy consistently exceeds 60-65%)
func calculatePullbackProbability(strength, rsi, volumeRatio float64, bouncing bool) float64 {
	// Base probability: 45% (slightly better than coin flip)
	prob := 45.0

	// Strength contribution: max +10% (strength 0-100 maps to 0-10)
	prob += strength * 0.10

	// RSI factor: +3% if oversold, -5% if overbought
	if rsi < 40 {
		prob += 3 // Oversold, room to bounce
	} else if rsi < 50 {
		prob += 1 // Slightly oversold
	} else if rsi > 70 {
		prob -= 5 // Overbought, risky
	} else if rsi > 60 {
		prob -= 2 // Getting warm
	}

	// Volume factor: +2% for very low volume pullback
	if volumeRatio < 0.5 {
		prob += 2 // Very weak selling pressure
	} else if volumeRatio > 1.2 {
		prob -= 3 // High volume pullback = more selling
	}

	// Bouncing factor: +2% if showing recovery
	if bouncing {
		prob += 2
	}

	// Cap at realistic range: 45-65%
	return math.Max(45, math.Min(prob, 65))
}

func boolToFloat(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}
