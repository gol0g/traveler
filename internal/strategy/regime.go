package strategy

import (
	"context"
	"log"
	"sync"
	"time"

	"traveler/internal/provider"
)

// Regime represents the current market regime
type Regime string

const (
	RegimeBull     Regime = "bull"
	RegimeSideways Regime = "sideways"
	RegimeBear     Regime = "bear"
)

// RegimeDetector detects the current crypto market regime using BTC indicators.
// Results are cached for 30 minutes to avoid excessive API calls.
type RegimeDetector struct {
	provider provider.Provider

	mu        sync.RWMutex
	regime    Regime
	updatedAt time.Time
}

// NewRegimeDetector creates a new regime detector
func NewRegimeDetector(p provider.Provider) *RegimeDetector {
	return &RegimeDetector{
		provider: p,
		regime:   RegimeSideways, // default
	}
}

const regimeCacheDuration = 30 * time.Minute

// Detect returns the current market regime. Results are cached for 30 minutes.
func (rd *RegimeDetector) Detect(ctx context.Context) Regime {
	rd.mu.RLock()
	if time.Since(rd.updatedAt) < regimeCacheDuration {
		r := rd.regime
		rd.mu.RUnlock()
		return r
	}
	rd.mu.RUnlock()

	// Cache expired, recalculate
	regime := rd.calculate(ctx)

	rd.mu.Lock()
	rd.regime = regime
	rd.updatedAt = time.Now()
	rd.mu.Unlock()

	log.Printf("[REGIME] Detected regime: %s", regime)
	return regime
}

// calculate performs the actual regime detection using BTC daily candles
func (rd *RegimeDetector) calculate(ctx context.Context) Regime {
	// Fetch BTC daily candles (need 50+ for MA50)
	candles, err := rd.provider.GetDailyCandles(ctx, "KRW-BTC", 55)
	if err != nil {
		log.Printf("[REGIME] Failed to get BTC candles: %v, defaulting to sideways", err)
		return RegimeSideways
	}

	if len(candles) < 50 {
		log.Printf("[REGIME] Insufficient BTC data (%d candles), defaulting to sideways", len(candles))
		return RegimeSideways
	}

	ind := CalculateIndicators(candles)
	currentPrice := candles[len(candles)-1].Close

	// Bull: BTC > MA20 AND BTC > MA50 AND RSI > 45 AND MA20 rising
	if currentPrice > ind.MA20 && currentPrice > ind.MA50 &&
		ind.RSI14 > 45 && ind.MA20Slope > 0 {
		return RegimeBull
	}

	// Bear: BTC < MA20 AND BTC < MA50 AND RSI < 40
	if currentPrice < ind.MA20 && currentPrice < ind.MA50 && ind.RSI14 < 40 {
		return RegimeBear
	}

	// Default: sideways
	return RegimeSideways
}
