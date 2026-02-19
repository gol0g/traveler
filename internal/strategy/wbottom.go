package strategy

import (
	"context"
	"fmt"
	"math"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// WBottomStrategy detects W-bottom (double bottom) patterns with confluence scoring.
// Designed for bear markets: conservative entry with ATR-based stops.
//
// Pattern: two swing lows within 5% tolerance, 3-30 days apart, ascending bottom preferred.
// Confluence (need 3+ of 6): RSI divergence, volume divergence, MACD turning,
// BB reclaim, green candle, volume spike.
type WBottomStrategy struct {
	provider provider.Provider
}

// NewWBottomStrategy creates a new W-Bottom strategy
func NewWBottomStrategy(p provider.Provider) *WBottomStrategy {
	return &WBottomStrategy{provider: p}
}

// Name returns the strategy name
func (s *WBottomStrategy) Name() string {
	return "wbottom"
}

// Description returns the strategy description
func (s *WBottomStrategy) Description() string {
	return "W-Bottom - double bottom with RSI divergence + confluence scoring (bear market)"
}

// W-bottom detection parameters (production: stricter than backtest)
const (
	wbTolerance       = 5.0  // Max % between two lows
	wbMinDays         = 3    // Min days between lows
	wbMaxDays         = 30   // Max days between lows
	wbMinConfluence   = 3    // Min confluence score (3 of 6)
	wbATRStopMul      = 2.5  // ATR(14) × this = stop distance
	wbPatternTargetMul = 0.5 // T1 = entry + pattern_height × 0.5
	wbExtendedMul     = 0.8  // T2 = entry + pattern_height × 0.8
	wbRecoveryPct     = 0.3  // Min recovery toward neckline (30%)
)

// Analyze detects W-bottom patterns and returns a buy signal if found
func (s *WBottomStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	candles, err := s.provider.GetDailyCandles(ctx, stock.Symbol, 80)
	if err != nil {
		return nil, err
	}

	n := len(candles)
	if n < 40 {
		return nil, nil
	}

	details := make(map[string]float64)
	ind := CalculateIndicators(candles)

	current := candles[n-1]
	price := current.Close

	details["current_price"] = price
	details["rsi14"] = ind.RSI14
	details["atr14"] = ind.ATR14
	details["ma20"] = ind.MA20

	// --- Trend filters ---
	// 1. MA20 slope not strongly negative
	if ind.MA20Slope < -5.0 {
		return nil, nil
	}
	// 2. RSI recovered from extreme oversold
	if ind.RSI14 < 30 {
		return nil, nil
	}
	// 3. Price higher than 5 days ago (short-term uptrend)
	if n > 5 && price <= candles[n-6].Close {
		return nil, nil
	}

	atr := ind.ATR14
	if atr <= 0 {
		return nil, nil
	}

	// --- Find swing lows ---
	type swingLow struct {
		idx   int
		price float64
		vol   int64
		rsi   float64
	}

	lookback := wbMaxDays + 10
	if lookback > n-5 {
		lookback = n - 5
	}
	startScan := n - lookback
	if startScan < 3 {
		startScan = 3
	}

	rsiSeries := CalculateRSISeries(candles, 14)

	var swingLows []swingLow
	for i := startScan; i < n-1; i++ {
		if candles[i].Low <= candles[i-1].Low && candles[i].Low <= candles[i+1].Low {
			rsi := 50.0
			rsiIdx := i - 15
			if rsiSeries != nil && rsiIdx >= 0 && rsiIdx < len(rsiSeries) {
				rsi = rsiSeries[rsiIdx]
			}
			swingLows = append(swingLows, swingLow{
				idx:   i,
				price: candles[i].Low,
				vol:   candles[i].Volume,
				rsi:   rsi,
			})
		}
	}

	if len(swingLows) < 2 {
		return nil, nil
	}

	// --- Find best W-bottom pair ---
	type wbResult struct {
		entry, stop, target1, target2 float64
		score                         int
		reasons                       string
		neckline                      float64
	}

	var best *wbResult

	for i := len(swingLows) - 1; i >= 1; i-- {
		low2 := swingLows[i]
		for j := i - 1; j >= 0; j-- {
			low1 := swingLows[j]

			daysBetween := low2.idx - low1.idx
			if daysBetween < wbMinDays || daysBetween > wbMaxDays {
				continue
			}

			// Two lows within tolerance
			pctDiff := math.Abs(low2.price-low1.price) / low1.price * 100
			if pctDiff > wbTolerance {
				continue
			}

			// Ascending bottom: second low should be same or higher
			if low2.price < low1.price*0.97 {
				continue
			}

			// Find neckline (highest high between two lows)
			neckline := 0.0
			for k := low1.idx + 1; k < low2.idx; k++ {
				if candles[k].High > neckline {
					neckline = candles[k].High
				}
			}
			if neckline <= 0 {
				continue
			}

			// Recovery check
			bottomLevel := math.Min(low1.price, low2.price)
			recovery := neckline - bottomLevel
			if recovery <= 0 {
				continue
			}
			recoveryPct := (price - bottomLevel) / recovery
			if recoveryPct < wbRecoveryPct {
				continue
			}
			if price <= low2.price*1.01 {
				continue
			}

			// --- Confluence scoring ---
			score := 0
			reasonStr := ""

			// 1. RSI divergence
			if low2.rsi > low1.rsi+1 {
				score++
				reasonStr += "RSI-div "
			}

			// 2. Volume divergence
			if low2.vol < low1.vol {
				score++
				reasonStr += "Vol-div "
			}

			// 3. MACD turning
			macdHist := CalculateMACDSeries(candles, 12, 26, 9)
			if macdHist != nil && len(macdHist) >= 3 {
				recent := macdHist[len(macdHist)-1]
				prev := macdHist[len(macdHist)-3]
				if recent > prev {
					score++
					reasonStr += "MACD-turn "
				}
			}

			// 4. BB reclaim
			if ind.BBLower > 0 && low2.price < ind.BBLower*1.02 && price > ind.BBLower {
				score++
				reasonStr += "BB-reclaim "
			}

			// 5. Green candle
			if current.Close > current.Open {
				score++
				reasonStr += "reversal "
			}

			// 6. Volume spike
			if ind.AvgVol > 0 && float64(current.Volume) > ind.AvgVol*1.3 {
				score++
				reasonStr += "vol-spike "
			}

			if score < wbMinConfluence {
				continue
			}

			// For low-confluence, require RSI has recovered above 35
			if score <= 2 && ind.RSI14 < 35 {
				continue
			}

			// Calculate levels
			entry := price
			stop := entry - atr*wbATRStopMul
			if stop > bottomLevel*0.99 {
				stop = bottomLevel * 0.99
			}

			patternHeight := neckline - bottomLevel
			target1 := entry + patternHeight*wbPatternTargetMul
			target2 := entry + patternHeight*wbExtendedMul

			stopDist := entry - stop
			if stopDist <= 0 {
				continue
			}
			rr := (target1 - entry) / stopDist
			if rr < 1.0 {
				continue
			}

			if best == nil || score > best.score {
				best = &wbResult{
					entry: entry, stop: stop,
					target1: target1, target2: target2,
					score: score, reasons: reasonStr,
					neckline: neckline,
				}
			}
			break
		}
		if best != nil {
			break
		}
	}

	if best == nil {
		return nil, nil
	}

	// Build signal
	riskPerShare := best.entry - best.stop
	details["stop_loss"] = best.stop
	details["target1"] = best.target1
	details["target2"] = best.target2
	details["neckline"] = best.neckline
	details["confluence_score"] = float64(best.score)

	strength := s.calculateStrength(best.score, ind.RSI14, price, ind.MA20)
	probability := s.calculateProbability(best.score, ind.RSI14)

	reason := fmt.Sprintf("W-Bottom: confluence=%d/6 (%s), ATR-stop=%.0f, T1=%.0f (+%.1f%%)",
		best.score, best.reasons, best.stop,
		best.target1, (best.target1-best.entry)/best.entry*100)

	guide := &TradeGuide{
		EntryPrice:      best.entry,
		EntryType:       "market",
		StopLoss:        best.stop,
		StopLossPct:     riskPerShare / best.entry * 100,
		Target1:         best.target1,
		Target1Pct:      (best.target1 - best.entry) / best.entry * 100,
		Target2:         best.target2,
		Target2Pct:      (best.target2 - best.entry) / best.entry * 100,
		RiskRewardRatio: (best.target1 - best.entry) / riskPerShare,
		KellyFraction:   0.05, // Very conservative for bear market
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

func (s *WBottomStrategy) calculateStrength(confluenceScore int, rsi, price, ma20 float64) float64 {
	var score float64

	// Confluence score (40 pts): higher = stronger
	switch {
	case confluenceScore >= 5:
		score += 40
	case confluenceScore >= 4:
		score += 32
	case confluenceScore >= 3:
		score += 24
	default:
		score += 16
	}

	// RSI position (20 pts): RSI 30-45 is ideal recovery zone
	switch {
	case rsi >= 35 && rsi <= 45:
		score += 20 // Ideal: recovered but not overbought
	case rsi >= 30 && rsi < 35:
		score += 15
	case rsi > 45 && rsi <= 55:
		score += 12
	default:
		score += 5
	}

	// Distance from MA20 (20 pts): closer to MA20 = more room for upside
	if ma20 > 0 {
		distPct := (ma20 - price) / ma20 * 100
		switch {
		case distPct >= 5:
			score += 20 // Good room above
		case distPct >= 2:
			score += 15
		default:
			score += 8
		}
	}

	// Base for passing all filters
	score += 20

	return math.Min(score, 100)
}

func (s *WBottomStrategy) calculateProbability(confluenceScore int, rsi float64) float64 {
	// Base probability from backtest: ~55-60% for high confluence
	prob := 45.0

	// Confluence bonus
	switch {
	case confluenceScore >= 5:
		prob += 15
	case confluenceScore >= 4:
		prob += 10
	case confluenceScore >= 3:
		prob += 5
	}

	// RSI in recovery sweet spot
	if rsi >= 35 && rsi <= 45 {
		prob += 5
	}

	return math.Max(40, math.Min(prob, 65))
}
