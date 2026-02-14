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

// OversoldConfig holds configuration for the oversold bounce strategy
type OversoldConfig struct {
	// Entry conditions
	MinDropPct    float64 // Minimum daily drop % to qualify (default 5.0)
	RSI2Threshold float64 // RSI(2) must be below this (default 10)
	RequireAboveMA int    // Price must be above this MA period (default 50)
	MinVolRatio   float64 // Volume must be >= this × average (default 1.5)

	// Quality filters
	MinPrice         float64 // Minimum stock price (default $3)
	MaxTickerLength  int     // Max ticker length (4 = exclude OTC)
	MinDailyDollarVol float64 // Minimum daily dollar volume

	// Exit
	StopLossPct  float64 // Hard stop loss % (default 5.0)
	MaxHoldDays  int     // Time stop in days (default 5)

	// Market regime: SPY must not be in extreme oversold
	MarketRegimeSymbol string // "SPY" for US, "069500" for KR
	MarketRSI2MinOK    float64 // Skip if SPY RSI(2) < this (default 5)
}

// DefaultOversoldConfig returns default configuration
func DefaultOversoldConfig() OversoldConfig {
	return OversoldConfig{
		MinDropPct:     5.0,
		RSI2Threshold:  10,
		RequireAboveMA: 50,
		MinVolRatio:    1.5,

		MinPrice:         3.0,
		MaxTickerLength:  4,
		MinDailyDollarVol: 200000,

		StopLossPct: 5.0,
		MaxHoldDays: 5,

		MarketRSI2MinOK: 5,
	}
}

// OversoldStrategy implements the extreme oversold bounce strategy.
// Targets stocks that dropped sharply but remain in a structural uptrend,
// expecting a short-term mean reversion bounce.
type OversoldStrategy struct {
	config   OversoldConfig
	provider provider.Provider

	// Market regime cache
	regimeMu      sync.Mutex
	regimeChecked bool
	regimeOK      bool
}

// NewOversoldStrategy creates a new oversold bounce strategy
func NewOversoldStrategy(cfg OversoldConfig, p provider.Provider) *OversoldStrategy {
	return &OversoldStrategy{
		config:   cfg,
		provider: p,
	}
}

// Name returns the strategy name
func (s *OversoldStrategy) Name() string {
	return "oversold"
}

// Description returns the strategy description
func (s *OversoldStrategy) Description() string {
	return "Oversold Bounce - Buy extreme oversold stocks for short-term mean reversion"
}

// ResetRegimeCache resets the cached regime check
func (s *OversoldStrategy) ResetRegimeCache() {
	s.regimeMu.Lock()
	s.regimeChecked = false
	s.regimeOK = false
	s.regimeMu.Unlock()
}

// checkMarketRegime checks if SPY RSI(2) is not in extreme oversold territory.
// When the entire market is crashing (SPY RSI(2) < 5), individual bounces fail.
func (s *OversoldStrategy) checkMarketRegime(ctx context.Context) bool {
	s.regimeMu.Lock()
	defer s.regimeMu.Unlock()

	if s.regimeChecked {
		return s.regimeOK
	}

	s.regimeChecked = true
	s.regimeOK = true

	sym := s.config.MarketRegimeSymbol
	if sym == "" {
		return s.regimeOK
	}

	candles, err := s.provider.GetDailyCandles(ctx, sym, 30)
	if err != nil {
		log.Printf("[OVERSOLD] regime check: failed to fetch %s: %v (allowing entries)", sym, err)
		return s.regimeOK
	}

	if len(candles) < 5 {
		return s.regimeOK
	}

	rsi2 := CalculateRSI(candles, 2)
	s.regimeOK = rsi2 >= s.config.MarketRSI2MinOK

	if !s.regimeOK {
		log.Printf("[OVERSOLD] regime PANIC: %s RSI(2)=%.1f < %.0f — market-wide crash, skipping bounces",
			sym, rsi2, s.config.MarketRSI2MinOK)
	} else {
		log.Printf("[OVERSOLD] regime OK: %s RSI(2)=%.1f", sym, rsi2)
	}

	return s.regimeOK
}

// Analyze analyzes a stock for oversold bounce opportunity
func (s *OversoldStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	// Market regime: skip if entire market is crashing
	if !s.checkMarketRegime(ctx) {
		return nil, nil
	}

	// Pre-filter: Ticker length
	if s.config.MaxTickerLength > 0 && len(stock.Symbol) > s.config.MaxTickerLength && !symbols.IsKoreanSymbol(stock.Symbol) {
		return nil, fmt.Errorf("ticker too long: %s", stock.Symbol)
	}

	// Need enough data for MA50 + RSI(2)
	candles, err := s.provider.GetDailyCandles(ctx, stock.Symbol, 70)
	if err != nil {
		return nil, err
	}

	if len(candles) < 52 {
		return nil, fmt.Errorf("insufficient data: %d candles", len(candles))
	}

	today := candles[len(candles)-1]
	yesterday := candles[len(candles)-2]

	// Quality filter: minimum price
	if s.config.MinPrice > 0 && today.Close < s.config.MinPrice {
		return nil, fmt.Errorf("price too low: $%.2f", today.Close)
	}

	// Quality filter: minimum dollar volume
	dailyDollarVol := today.Close * float64(today.Volume)
	if s.config.MinDailyDollarVol > 0 && dailyDollarVol < s.config.MinDailyDollarVol {
		return nil, fmt.Errorf("liquidity too low: $%.0f", dailyDollarVol)
	}

	// Calculate indicators
	ind := CalculateIndicators(candles)
	rsi2 := CalculateRSI(candles, 2)
	ma5 := CalculateMA(candles, 5)

	details := make(map[string]float64)
	details["close"] = today.Close
	details["rsi2"] = rsi2
	details["rsi14"] = ind.RSI14
	details["ma5"] = ma5
	details["ma50"] = ind.MA50
	details["atr14"] = ind.ATR14

	// Condition 1: Significant daily drop
	dropPct := 0.0
	if yesterday.Close > 0 {
		dropPct = (today.Close - yesterday.Close) / yesterday.Close * 100
	}
	details["drop_pct"] = dropPct
	bigDrop := dropPct <= -s.config.MinDropPct

	// Condition 2: RSI(2) extreme oversold
	rsiOversold := rsi2 < s.config.RSI2Threshold
	details["rsi2_threshold"] = s.config.RSI2Threshold

	// Condition 3: Above structural MA (uptrend intact)
	var aboveMA bool
	switch s.config.RequireAboveMA {
	case 200:
		aboveMA = ind.MA200 > 0 && today.Close > ind.MA200
		details["required_ma"] = ind.MA200
	default: // 50
		aboveMA = ind.MA50 > 0 && today.Close > ind.MA50
		details["required_ma"] = ind.MA50
	}
	details["above_ma"] = boolToFloat(aboveMA)

	// Condition 4: Volume spike (panic selling confirmed)
	volRatio := float64(today.Volume) / ind.AvgVol
	details["vol_ratio"] = volRatio
	volSpike := volRatio >= s.config.MinVolRatio

	// Additional safety: not already bouncing (avoid chasing)
	// If today's close is near the high, the bounce already happened
	dayRange := today.High - today.Low
	closePosition := 0.0
	if dayRange > 0 {
		closePosition = (today.Close - today.Low) / dayRange
	}
	details["close_position"] = closePosition
	// Allow: close in lower 60% of day's range (hasn't fully bounced yet)
	notChasing := closePosition < 0.6

	// Signal decision
	if bigDrop && rsiOversold && aboveMA && volSpike && notChasing {
		// Calculate trade guide
		entryPrice := today.Close
		stopLoss := entryPrice * (1 - s.config.StopLossPct/100)

		// Target: MA5 (short-term mean)
		target1 := ma5
		if target1 <= entryPrice {
			// MA5 is below current price (very beaten down) — use 3% as minimum target
			target1 = entryPrice * 1.03
		}

		// Target2: slightly above MA5 or 5%
		target2 := entryPrice * 1.05
		if target2 < target1*1.01 {
			target2 = target1 * 1.01
		}

		riskPerShare := entryPrice - stopLoss
		rewardPerShare := target1 - entryPrice
		rr := 0.0
		if riskPerShare > 0 {
			rr = rewardPerShare / riskPerShare
		}

		// Probability: base 60% for qualified oversold bounces
		prob := calculateOversoldProbability(rsi2, dropPct, volRatio, aboveMA, ind.RSI14)

		strength := calculateOversoldStrength(rsi2, dropPct, volRatio, closePosition)

		reason := fmt.Sprintf("Oversold bounce: %.1f%% drop, RSI(2)=%.0f, vol %.1fx, above MA%d, target MA5=$%.2f",
			dropPct, rsi2, volRatio, s.config.RequireAboveMA, ma5)

		guide := &TradeGuide{
			EntryPrice:      entryPrice,
			EntryType:       "limit",
			StopLoss:        stopLoss,
			StopLossPct:     s.config.StopLossPct,
			Target1:         target1,
			Target1Pct:      (target1 - entryPrice) / entryPrice * 100,
			Target2:         target2,
			Target2Pct:      (target2 - entryPrice) / entryPrice * 100,
			RiskRewardRatio: rr,
		}

		// Kelly fraction
		if prob > 0 {
			w := prob / 100
			avgWin := (target1 - entryPrice) / entryPrice
			avgLoss := s.config.StopLossPct / 100
			guide.KellyFraction = (w*avgWin - (1-w)*avgLoss) / avgWin
			if guide.KellyFraction < 0 {
				guide.KellyFraction = 0
			}
		}

		return &Signal{
			Stock:       stock,
			Type:        SignalBuy,
			Strategy:    s.Name(),
			Strength:    strength,
			Probability: prob,
			Reason:      reason,
			Details:     details,
			Guide:       guide,
		}, nil
	}

	return nil, nil
}

// calculateOversoldStrength calculates signal strength 0-100
func calculateOversoldStrength(rsi2, dropPct, volRatio, closePosition float64) float64 {
	score := 0.0

	// RSI(2) depth: lower = stronger signal
	if rsi2 < 2 {
		score += 30
	} else if rsi2 < 5 {
		score += 25
	} else if rsi2 < 10 {
		score += 20
	}

	// Drop magnitude
	absDrop := math.Abs(dropPct)
	if absDrop >= 10 {
		score += 25
	} else if absDrop >= 7 {
		score += 20
	} else if absDrop >= 5 {
		score += 15
	}

	// Volume spike
	if volRatio >= 3.0 {
		score += 20
	} else if volRatio >= 2.0 {
		score += 15
	} else if volRatio >= 1.5 {
		score += 10
	}

	// Close position in day range: lower = more room to bounce
	if closePosition < 0.3 {
		score += 15
	} else if closePosition < 0.5 {
		score += 10
	} else {
		score += 5
	}

	return math.Min(score, 100)
}

// calculateOversoldProbability estimates bounce probability
func calculateOversoldProbability(rsi2, dropPct, volRatio float64, aboveMA bool, rsi14 float64) float64 {
	// Base: 58% (oversold bounces are statistically favorable)
	prob := 58.0

	// RSI(2) extreme: +3% for very extreme
	if rsi2 < 3 {
		prob += 3
	} else if rsi2 < 5 {
		prob += 2
	}

	// Bigger drops tend to bounce more reliably
	absDrop := math.Abs(dropPct)
	if absDrop >= 8 {
		prob += 3
	} else if absDrop >= 6 {
		prob += 1
	}

	// Volume confirmation
	if volRatio >= 2.5 {
		prob += 2 // strong capitulation
	}

	// Above MA = structural support
	if aboveMA {
		prob += 2
	}

	// RSI(14) context: if also oversold on longer timeframe, bounce is more likely
	if rsi14 < 30 {
		prob += 2
	} else if rsi14 > 60 {
		prob -= 2 // overextended on higher timeframe, drop might continue
	}

	return math.Max(50, math.Min(prob, 72))
}
