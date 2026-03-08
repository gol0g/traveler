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

// RegimeDetector detects the current market regime using a benchmark symbol's indicators.
// Results are cached for 30 minutes to avoid excessive API calls.
// Supports: "KRW-BTC" (crypto), "SPY" (US), "069500" (KR/KODEX200)
type RegimeDetector struct {
	provider provider.Provider
	symbol   string // benchmark symbol for regime detection

	mu        sync.RWMutex
	regime    Regime
	updatedAt time.Time
}

// NewRegimeDetector creates a new regime detector for crypto (BTC default)
func NewRegimeDetector(p provider.Provider) *RegimeDetector {
	return NewRegimeDetectorForSymbol(p, "KRW-BTC")
}

// NewRegimeDetectorForSymbol creates a regime detector for any benchmark symbol
func NewRegimeDetectorForSymbol(p provider.Provider, symbol string) *RegimeDetector {
	return &RegimeDetector{
		provider: p,
		symbol:   symbol,
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

	log.Printf("[REGIME] Detected regime: %s (benchmark: %s)", regime, rd.symbol)
	return regime
}

// RegimeInfo contains detailed regime detection results for display
type RegimeInfo struct {
	Regime       Regime  `json:"regime"`
	Symbol       string  `json:"symbol"`
	Price        float64 `json:"price"`
	PrevClose    float64 `json:"prev_close"`
	DayChangePct float64 `json:"day_change_pct"` // % change from previous close
	MA20         float64 `json:"ma20"`
	MA50         float64 `json:"ma50"`
	RSI14        float64 `json:"rsi14"`
	MA20Slope    float64 `json:"ma20_slope"`
}

// DetectWithInfo returns regime along with benchmark indicator details
func (rd *RegimeDetector) DetectWithInfo(ctx context.Context) RegimeInfo {
	candles, err := rd.provider.GetDailyCandles(ctx, rd.symbol, 55)
	if err != nil || len(candles) < 50 {
		return RegimeInfo{Regime: rd.Detect(ctx), Symbol: rd.symbol}
	}
	ind := CalculateIndicators(candles)
	price := candles[len(candles)-1].Close
	prevClose := 0.0
	dayChangePct := 0.0
	if len(candles) >= 2 {
		prevClose = candles[len(candles)-2].Close
		if prevClose > 0 {
			dayChangePct = (price - prevClose) / prevClose * 100
		}
	}
	return RegimeInfo{
		Regime:       rd.Detect(ctx),
		Symbol:       rd.symbol,
		Price:        price,
		PrevClose:    prevClose,
		DayChangePct: dayChangePct,
		MA20:         ind.MA20,
		MA50:         ind.MA50,
		RSI14:        ind.RSI14,
		MA20Slope:    ind.MA20Slope,
	}
}

// calculate performs the actual regime detection using benchmark daily candles
func (rd *RegimeDetector) calculate(ctx context.Context) Regime {
	candles, err := rd.provider.GetDailyCandles(ctx, rd.symbol, 55)
	if err != nil {
		// KIS 토큰 rate limit (1분당 1회) 등 일시적 오류 시 65초 후 1회 재시도
		log.Printf("[REGIME] First attempt failed for %s: %v, retrying in 65s...", rd.symbol, err)
		time.Sleep(65 * time.Second)
		candles, err = rd.provider.GetDailyCandles(ctx, rd.symbol, 55)
		if err != nil {
			log.Printf("[REGIME] Retry failed for %s: %v, defaulting to sideways", rd.symbol, err)
			return RegimeSideways
		}
	}

	if len(candles) < 50 {
		log.Printf("[REGIME] Insufficient %s data (%d candles), defaulting to sideways", rd.symbol, len(candles))
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
