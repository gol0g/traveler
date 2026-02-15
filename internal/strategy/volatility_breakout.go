package strategy

import (
	"context"
	"fmt"
	"math"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// VolatilityBreakoutConfig holds configuration for the volatility breakout strategy
type VolatilityBreakoutConfig struct {
	K                  float64 // Breakout multiplier (0.5 for major, 0.6 for alt)
	MinRange           float64 // Minimum range as % of price (filter flat days)
	MaxRange           float64 // Maximum range as % (filter extreme days)
	VolumeMultiple     float64 // Today's volume vs 20-day avg (1.2x+)
	StopLossPct        float64 // Maximum stop loss (3%)
	MarketRegimeSymbol string  // BTC symbol for regime filter
	MinMarketRegimeMA  int     // MA period for BTC regime
}

// DefaultVolatilityBreakoutConfig returns default configuration
func DefaultVolatilityBreakoutConfig() VolatilityBreakoutConfig {
	return VolatilityBreakoutConfig{
		K:                  0.5,
		MinRange:           0.5,   // 0.5%
		MaxRange:           15.0,  // 15%
		VolumeMultiple:     1.2,
		StopLossPct:        0.03,  // 3%
		MarketRegimeSymbol: "KRW-BTC",
		MinMarketRegimeMA:  20,
	}
}

// VolatilityBreakoutStrategy implements the Larry Williams volatility breakout for crypto.
//
// Logic:
//
//	Range = PrevHigh - PrevLow  (yesterday's range)
//	BreakoutLevel = TodayOpen + Range * K
//	Entry: CurrentPrice > BreakoutLevel AND volume confirmation
//	Stop: Max(PrevLow, Entry - 3%)
type VolatilityBreakoutStrategy struct {
	config   VolatilityBreakoutConfig
	provider provider.Provider
}

// NewVolatilityBreakoutStrategy creates a new volatility breakout strategy
func NewVolatilityBreakoutStrategy(cfg VolatilityBreakoutConfig, p provider.Provider) *VolatilityBreakoutStrategy {
	return &VolatilityBreakoutStrategy{
		config:   cfg,
		provider: p,
	}
}

// Name returns the strategy name
func (s *VolatilityBreakoutStrategy) Name() string {
	return "volatility-breakout"
}

// Description returns the strategy description
func (s *VolatilityBreakoutStrategy) Description() string {
	return "Volatility Breakout - Larry Williams style breakout for crypto (Upbit KRW market)"
}

// Analyze analyzes a crypto symbol for volatility breakout opportunity
func (s *VolatilityBreakoutStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	// Fetch 30 daily candles for indicators
	candles, err := s.provider.GetDailyCandles(ctx, stock.Symbol, 30)
	if err != nil {
		return nil, err
	}

	if len(candles) < 22 {
		return nil, fmt.Errorf("insufficient data: got %d candles, need 22", len(candles))
	}

	details := make(map[string]float64)

	// BTC regime check: skip if BTC is below its MA
	if stock.Symbol != s.config.MarketRegimeSymbol {
		btcOK, err := s.checkBTCRegime(ctx)
		if err != nil {
			// If we can't check BTC regime, proceed anyway
			details["btc_regime_error"] = 1
		} else if !btcOK {
			return nil, nil // Skip in BTC downtrend
		}
		details["btc_regime_ok"] = 1
	}

	// Yesterday's candle (second to last)
	prev := candles[len(candles)-2]
	// Today's candle (latest)
	today := candles[len(candles)-1]

	// Calculate yesterday's range
	prevRange := prev.High - prev.Low
	if prevRange <= 0 || prev.Close <= 0 {
		return nil, fmt.Errorf("invalid previous candle data")
	}

	rangePct := prevRange / prev.Close * 100
	details["prev_range"] = prevRange
	details["prev_range_pct"] = rangePct
	details["prev_high"] = prev.High
	details["prev_low"] = prev.Low

	// Check range validity
	if rangePct < s.config.MinRange {
		return nil, nil // Too flat, no volatility
	}
	if rangePct > s.config.MaxRange {
		return nil, nil // Too extreme, dangerous
	}

	// Determine K value: 0.5 for BTC/ETH (major), 0.6 for altcoins
	k := s.config.K
	if stock.Symbol != "KRW-BTC" && stock.Symbol != "KRW-ETH" {
		k = 0.6
	}
	details["k_factor"] = k

	// Calculate breakout level
	breakoutLevel := today.Open + prevRange*k
	details["today_open"] = today.Open
	details["breakout_level"] = breakoutLevel

	// Check if current price > breakout level
	currentPrice := today.Close
	details["current_price"] = currentPrice

	breakout := currentPrice > breakoutLevel
	details["breakout_pct"] = (currentPrice - breakoutLevel) / breakoutLevel * 100

	if !breakout {
		return nil, nil
	}

	// Volume check: today's volume > 20-day average * VolumeMultiple
	avgVol := CalculateAvgVolume(candles[:len(candles)-1], 20) // Exclude today
	todayVol := float64(today.Volume)
	if avgVol <= 0 {
		return nil, fmt.Errorf("could not calculate average volume")
	}

	volumeRatio := todayVol / avgVol
	details["volume_ratio"] = volumeRatio
	details["avg_volume_20"] = avgVol

	volumeConfirm := volumeRatio >= s.config.VolumeMultiple
	if !volumeConfirm {
		return nil, nil
	}

	// Calculate indicators
	ind := CalculateIndicators(candles)
	details["rsi14"] = ind.RSI14
	details["ma20"] = ind.MA20

	// Calculate stop loss: max(prevLow, entry - 3%)
	stopByPct := currentPrice * (1 - s.config.StopLossPct)
	stopLoss := math.Max(prev.Low, stopByPct)
	details["stop_loss"] = stopLoss

	riskPerShare := currentPrice - stopLoss
	if riskPerShare <= 0 {
		return nil, fmt.Errorf("invalid stop loss calculation")
	}

	// Targets: 2R and 3R
	target1 := currentPrice + riskPerShare*2.0
	target2 := currentPrice + riskPerShare*3.0

	// Calculate strength
	strength := s.calculateStrength(rangePct, volumeRatio, ind.RSI14, currentPrice, ind.MA20)
	details["strength"] = strength

	// Calculate probability
	probability := s.calculateProbability(strength, volumeRatio, rangePct)

	reason := fmt.Sprintf("Volatility breakout: price %.0f > level %.0f (K=%.1f), range %.1f%%, vol %.1fx avg",
		currentPrice, breakoutLevel, k, rangePct, volumeRatio)

	guide := &TradeGuide{
		EntryPrice:      currentPrice,
		EntryType:       "market",
		StopLoss:        stopLoss,
		StopLossPct:     riskPerShare / currentPrice * 100,
		Target1:         target1,
		Target1Pct:      riskPerShare * 2.0 / currentPrice * 100,
		Target2:         target2,
		Target2Pct:      riskPerShare * 3.0 / currentPrice * 100,
		RiskRewardRatio: 2.0,
		KellyFraction:   s.calculateKelly(),
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

// checkBTCRegime checks if BTC is above its MA (market regime filter)
func (s *VolatilityBreakoutStrategy) checkBTCRegime(ctx context.Context) (bool, error) {
	btcCandles, err := s.provider.GetDailyCandles(ctx, s.config.MarketRegimeSymbol, s.config.MinMarketRegimeMA+5)
	if err != nil {
		return false, err
	}

	if len(btcCandles) < s.config.MinMarketRegimeMA {
		return true, nil // Not enough data, assume OK
	}

	btcMA := CalculateMA(btcCandles, s.config.MinMarketRegimeMA)
	btcPrice := btcCandles[len(btcCandles)-1].Close

	return btcPrice > btcMA, nil
}

func (s *VolatilityBreakoutStrategy) calculateStrength(rangePct, volumeRatio, rsi, price, ma20 float64) float64 {
	var score float64

	// Range quality (20 pts): moderate range is best
	if rangePct >= 1.0 && rangePct <= 5.0 {
		score += 20
	} else if rangePct >= 0.5 && rangePct <= 10.0 {
		score += 12
	} else {
		score += 5
	}

	// Volume surge (25 pts)
	if volumeRatio >= 2.0 {
		score += 25
	} else if volumeRatio >= 1.5 {
		score += 18
	} else if volumeRatio >= 1.2 {
		score += 12
	}

	// RSI position (20 pts): mid-range RSI is ideal
	if rsi >= 40 && rsi <= 65 {
		score += 20
	} else if rsi >= 30 && rsi <= 75 {
		score += 12
	} else {
		score += 5
	}

	// Price vs MA20 (15 pts): above MA20 is better
	if ma20 > 0 && price > ma20 {
		score += 15
	} else if ma20 > 0 {
		score += 5
	}

	// Base points for passing all conditions (20 pts)
	score += 20

	return math.Min(score, 100)
}

func (s *VolatilityBreakoutStrategy) calculateProbability(strength, volumeRatio, rangePct float64) float64 {
	prob := 45.0

	// Strength contribution
	prob += strength * 0.08

	// Volume factor
	if volumeRatio > 2.0 {
		prob += 3
	} else if volumeRatio > 1.5 {
		prob += 1.5
	}

	// Range quality
	if rangePct >= 1.0 && rangePct <= 5.0 {
		prob += 2
	}

	return math.Max(45, math.Min(prob, 65))
}

func (s *VolatilityBreakoutStrategy) calculateKelly() float64 {
	winRate := 0.50 // Volatility breakout typical win rate
	avgWin := 2.0   // 2R target
	avgLoss := 1.0
	kelly := (winRate*avgWin - (1-winRate)*avgLoss) / avgWin
	if kelly < 0 {
		kelly = 0
	}
	return kelly
}
