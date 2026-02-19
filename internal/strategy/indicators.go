package strategy

import (
	"math"

	"traveler/pkg/model"
)

// Indicators contains calculated technical indicators
type Indicators struct {
	MA5     float64
	MA10    float64
	MA20    float64
	MA50    float64
	MA100   float64 // ~20-week MA (weekly timeframe filter)
	MA200   float64
	RSI14   float64
	ATR14   float64 // 14-period Average True Range
	AvgVol  float64 // Average volume (20-day)
	BBUpper float64 // Bollinger Band Upper
	BBLower float64 // Bollinger Band Lower
	BBWidth float64 // Bollinger Bandwidth

	MA50Slope float64 // MA50 기울기 (5일전 대비 변화율%, 양수=상승)
	MA20Slope float64 // MA20 기울기 (3일전 대비 변화율%, 양수=상승)

	MACDLine   float64 // MACD line (EMA12 - EMA26)
	SignalLine float64 // Signal line (EMA9 of MACD)
	MACDHist   float64 // MACD histogram (MACD - Signal)
}

// CalculateMA calculates Simple Moving Average for the given period
func CalculateMA(candles []model.Candle, period int) float64 {
	if len(candles) < period {
		return 0
	}

	var sum float64
	for i := len(candles) - period; i < len(candles); i++ {
		sum += candles[i].Close
	}
	return sum / float64(period)
}

// CalculateRSI calculates RSI for the given period
func CalculateRSI(candles []model.Candle, period int) float64 {
	if len(candles) < period+1 {
		return 50
	}

	var gains, losses float64
	for i := len(candles) - period; i < len(candles); i++ {
		change := candles[i].Close - candles[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses -= change
		}
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	if avgLoss == 0 {
		return 100
	}

	rs := avgGain / avgLoss
	return 100 - (100 / (1 + rs))
}

// CalculateAvgVolume calculates average volume
func CalculateAvgVolume(candles []model.Candle, period int) float64 {
	if len(candles) < period {
		return 0
	}

	var sum int64
	for i := len(candles) - period; i < len(candles); i++ {
		sum += candles[i].Volume
	}
	return float64(sum) / float64(period)
}

// CalculateBollingerBands calculates Bollinger Bands
func CalculateBollingerBands(candles []model.Candle, period int, stdDev float64) (upper, lower, bandwidth float64) {
	if len(candles) < period {
		return 0, 0, 0
	}

	ma := CalculateMA(candles, period)

	// Calculate standard deviation
	var sumSquares float64
	for i := len(candles) - period; i < len(candles); i++ {
		diff := candles[i].Close - ma
		sumSquares += diff * diff
	}
	std := sqrt(sumSquares / float64(period))

	upper = ma + (std * stdDev)
	lower = ma - (std * stdDev)

	if ma > 0 {
		bandwidth = (upper - lower) / ma * 100
	}

	return upper, lower, bandwidth
}

// CalculateATR calculates Average True Range
// TR = max(High-Low, |High-PrevClose|, |Low-PrevClose|)
// ATR = SMA of TR over period
func CalculateATR(candles []model.Candle, period int) float64 {
	if len(candles) < period+1 {
		return 0
	}

	var sum float64
	for i := len(candles) - period; i < len(candles); i++ {
		highLow := candles[i].High - candles[i].Low
		highPrevClose := math.Abs(candles[i].High - candles[i-1].Close)
		lowPrevClose := math.Abs(candles[i].Low - candles[i-1].Close)
		tr := math.Max(highLow, math.Max(highPrevClose, lowPrevClose))
		sum += tr
	}
	return sum / float64(period)
}

// CalculateIndicators calculates all indicators for the given candles
func CalculateIndicators(candles []model.Candle) *Indicators {
	ind := &Indicators{}

	if len(candles) >= 5 {
		ind.MA5 = CalculateMA(candles, 5)
	}
	if len(candles) >= 10 {
		ind.MA10 = CalculateMA(candles, 10)
	}
	if len(candles) >= 20 {
		ind.MA20 = CalculateMA(candles, 20)
		ind.AvgVol = CalculateAvgVolume(candles, 20)
		ind.BBUpper, ind.BBLower, ind.BBWidth = CalculateBollingerBands(candles, 20, 2.0)
	}
	if len(candles) >= 50 {
		ind.MA50 = CalculateMA(candles, 50)
	}
	if len(candles) >= 100 {
		ind.MA100 = CalculateMA(candles, 100)
	}
	if len(candles) >= 200 {
		ind.MA200 = CalculateMA(candles, 200)
	}
	if len(candles) >= 15 {
		ind.RSI14 = CalculateRSI(candles, 14)
		ind.ATR14 = CalculateATR(candles, 14)
	}

	// MA50 기울기: 현재 MA50 vs 5일전 MA50
	if len(candles) >= 55 && ind.MA50 > 0 {
		prevMA50 := CalculateMA(candles[:len(candles)-5], 50)
		if prevMA50 > 0 {
			ind.MA50Slope = (ind.MA50 - prevMA50) / prevMA50 * 100
		}
	}

	// MA20 기울기: 현재 MA20 vs 3일전 MA20
	if len(candles) >= 23 && ind.MA20 > 0 {
		prevMA20 := CalculateMA(candles[:len(candles)-3], 20)
		if prevMA20 > 0 {
			ind.MA20Slope = (ind.MA20 - prevMA20) / prevMA20 * 100
		}
	}

	// MACD(12, 26, 9)
	if len(candles) >= 35 {
		macd := CalculateMACD(candles, 12, 26, 9)
		ind.MACDLine = macd.Line
		ind.SignalLine = macd.Signal
		ind.MACDHist = macd.Hist
	}

	return ind
}

// CalculateHighestHigh returns the highest High in the previous N candles (excluding the latest)
func CalculateHighestHigh(candles []model.Candle, period int) float64 {
	if len(candles) < period+1 {
		return 0
	}
	high := 0.0
	for i := len(candles) - period - 1; i < len(candles)-1; i++ {
		if candles[i].High > high {
			high = candles[i].High
		}
	}
	return high
}

// CalculateLowestLow returns the lowest Low in the previous N candles (excluding the latest)
func CalculateLowestLow(candles []model.Candle, period int) float64 {
	if len(candles) < period+1 {
		return 0
	}
	low := candles[len(candles)-period-1].Low
	for i := len(candles) - period - 1; i < len(candles)-1; i++ {
		if candles[i].Low < low {
			low = candles[i].Low
		}
	}
	return low
}

// CalculatePriorBBWidth calculates Bollinger bandwidth ending 'offset' bars before the latest candle
func CalculatePriorBBWidth(candles []model.Candle, period int, stdDev float64, offset int) float64 {
	if len(candles) < period+offset {
		return 0
	}
	subset := candles[:len(candles)-offset]
	_, _, bandwidth := CalculateBollingerBands(subset, period, stdDev)
	return bandwidth
}

// CalculateEMA calculates Exponential Moving Average for the given period
func CalculateEMA(candles []model.Candle, period int) float64 {
	if len(candles) < period {
		return 0
	}
	// Seed with SMA of first `period` values
	sma := 0.0
	for i := 0; i < period; i++ {
		sma += candles[i].Close
	}
	sma /= float64(period)

	k := 2.0 / float64(period+1)
	ema := sma
	for i := period; i < len(candles); i++ {
		ema = candles[i].Close*k + ema*(1-k)
	}
	return ema
}

// CalculateEMASeries returns full EMA series (length = len(candles) - period + 1)
func CalculateEMASeries(candles []model.Candle, period int) []float64 {
	if len(candles) < period {
		return nil
	}
	sma := 0.0
	for i := 0; i < period; i++ {
		sma += candles[i].Close
	}
	sma /= float64(period)

	k := 2.0 / float64(period+1)
	result := make([]float64, 0, len(candles)-period+1)
	result = append(result, sma)
	ema := sma
	for i := period; i < len(candles); i++ {
		ema = candles[i].Close*k + ema*(1-k)
		result = append(result, ema)
	}
	return result
}

// MACD holds MACD indicator values
type MACD struct {
	Line   float64 // EMA(fast) - EMA(slow)
	Signal float64 // EMA of MACD line
	Hist   float64 // Line - Signal
}

// CalculateMACD calculates MACD(fast, slow, signal) — typically (12, 26, 9)
func CalculateMACD(candles []model.Candle, fast, slow, signal int) MACD {
	if len(candles) < slow+signal {
		return MACD{}
	}

	// Calculate EMA series for fast and slow
	fastEMA := CalculateEMASeries(candles, fast)
	slowEMA := CalculateEMASeries(candles, slow)

	if len(fastEMA) == 0 || len(slowEMA) == 0 {
		return MACD{}
	}

	// Align: slowEMA starts at index (slow-1), fastEMA at (fast-1)
	// MACD line = fastEMA - slowEMA, aligned to slowEMA start
	offset := slow - fast // fastEMA has more elements
	if offset < 0 || offset >= len(fastEMA) {
		return MACD{}
	}

	macdLine := make([]float64, len(slowEMA))
	for i := range slowEMA {
		macdLine[i] = fastEMA[i+offset] - slowEMA[i]
	}

	if len(macdLine) < signal {
		return MACD{Line: macdLine[len(macdLine)-1]}
	}

	// Signal line = EMA of MACD line
	k := 2.0 / float64(signal+1)
	sma := 0.0
	for i := 0; i < signal; i++ {
		sma += macdLine[i]
	}
	sma /= float64(signal)

	sigEMA := sma
	for i := signal; i < len(macdLine); i++ {
		sigEMA = macdLine[i]*k + sigEMA*(1-k)
	}

	lastMACD := macdLine[len(macdLine)-1]
	return MACD{
		Line:   lastMACD,
		Signal: sigEMA,
		Hist:   lastMACD - sigEMA,
	}
}

// CalculateMACDSeries returns MACD histogram series for divergence detection
func CalculateMACDSeries(candles []model.Candle, fast, slow, signal int) []float64 {
	if len(candles) < slow+signal {
		return nil
	}

	fastEMA := CalculateEMASeries(candles, fast)
	slowEMA := CalculateEMASeries(candles, slow)
	if len(fastEMA) == 0 || len(slowEMA) == 0 {
		return nil
	}

	offset := slow - fast
	if offset < 0 || offset >= len(fastEMA) {
		return nil
	}

	macdLine := make([]float64, len(slowEMA))
	for i := range slowEMA {
		macdLine[i] = fastEMA[i+offset] - slowEMA[i]
	}

	if len(macdLine) < signal {
		return nil
	}

	k := 2.0 / float64(signal+1)
	sma := 0.0
	for i := 0; i < signal; i++ {
		sma += macdLine[i]
	}
	sma /= float64(signal)

	sigEMA := sma
	hist := make([]float64, 0, len(macdLine)-signal+1)
	hist = append(hist, macdLine[signal-1]-sigEMA)
	for i := signal; i < len(macdLine); i++ {
		sigEMA = macdLine[i]*k + sigEMA*(1-k)
		hist = append(hist, macdLine[i]-sigEMA)
	}
	return hist
}

// CalculateRSISeries returns RSI series for divergence detection
func CalculateRSISeries(candles []model.Candle, period int) []float64 {
	if len(candles) < period+1 {
		return nil
	}

	result := make([]float64, 0, len(candles)-period)
	for i := period + 1; i <= len(candles); i++ {
		result = append(result, CalculateRSI(candles[:i], period))
	}
	return result
}

// FindSwingHigh finds the highest high within the last `period` candles (excluding today)
// and returns both the price and the index. Returns (0, -1) if insufficient data.
func FindSwingHigh(candles []model.Candle, period int) (float64, int) {
	if len(candles) < period+1 {
		return 0, -1
	}
	high := 0.0
	idx := -1
	for i := len(candles) - period - 1; i < len(candles)-1; i++ {
		if candles[i].High > high {
			high = candles[i].High
			idx = i
		}
	}
	return high, idx
}

// FindSwingLow finds the lowest low within the last `period` candles (excluding today)
// and returns both the price and the index. Returns (0, -1) if insufficient data.
func FindSwingLow(candles []model.Candle, period int) (float64, int) {
	if len(candles) < period+1 {
		return 0, -1
	}
	low := candles[len(candles)-period-1].Low
	idx := len(candles) - period - 1
	for i := len(candles) - period - 1; i < len(candles)-1; i++ {
		if candles[i].Low < low {
			low = candles[i].Low
			idx = i
		}
	}
	return low, idx
}

// FibonacciExtension calculates Fibonacci extension from swing low (A) to swing high (B).
// level = B + (B - A) * ratio
// Common: ratio=0.272 → 1.272x extension, ratio=0.618 → 1.618x extension
func FibonacciExtension(swingLow, swingHigh, ratio float64) float64 {
	if swingHigh <= swingLow {
		return 0
	}
	return swingHigh + (swingHigh-swingLow)*ratio
}

// SwingPoint represents a local price extremum (resistance or support)
type SwingPoint struct {
	Price float64
	Index int
}

// FindSwingHighs finds local maxima (resistance levels) where High >= `order` neighbors on each side.
// lookback: search range from end of candles, order: # of candles each side (2 recommended).
// Returns results sorted most recent first. Excludes the latest candle (still forming).
func FindSwingHighs(candles []model.Candle, lookback, order int) []SwingPoint {
	n := len(candles)
	if n < order*2+1 {
		return nil
	}
	start := n - lookback
	if start < order {
		start = order
	}
	end := n - 1 // exclude latest

	var result []SwingPoint
	for i := end - 1; i >= start; i-- {
		isHigh := true
		for j := 1; j <= order; j++ {
			if i-j < 0 || i+j >= n {
				isHigh = false
				break
			}
			if candles[i].High < candles[i-j].High || candles[i].High < candles[i+j].High {
				isHigh = false
				break
			}
		}
		if isHigh {
			result = append(result, SwingPoint{Price: candles[i].High, Index: i})
		}
	}
	return result
}

// FindSwingLows finds local minima (support levels) where Low <= `order` neighbors on each side.
func FindSwingLows(candles []model.Candle, lookback, order int) []SwingPoint {
	n := len(candles)
	if n < order*2+1 {
		return nil
	}
	start := n - lookback
	if start < order {
		start = order
	}
	end := n - 1

	var result []SwingPoint
	for i := end - 1; i >= start; i-- {
		isLow := true
		for j := 1; j <= order; j++ {
			if i-j < 0 || i+j >= n {
				isLow = false
				break
			}
			if candles[i].Low > candles[i-j].Low || candles[i].Low > candles[i+j].Low {
				isLow = false
				break
			}
		}
		if isLow {
			result = append(result, SwingPoint{Price: candles[i].Low, Index: i})
		}
	}
	return result
}

// FindNearestResistance returns the lowest swing high above the given price.
// Returns 0 if no resistance found.
func FindNearestResistance(candles []model.Candle, price float64, lookback, order int) float64 {
	highs := FindSwingHighs(candles, lookback, order)
	nearest := 0.0
	for _, h := range highs {
		if h.Price > price {
			if nearest == 0 || h.Price < nearest {
				nearest = h.Price
			}
		}
	}
	return nearest
}

// FindNearestSupport returns the highest swing low below the given price.
// Returns 0 if no support found.
func FindNearestSupport(candles []model.Candle, price float64, lookback, order int) float64 {
	lows := FindSwingLows(candles, lookback, order)
	nearest := 0.0
	for _, l := range lows {
		if l.Price < price {
			if l.Price > nearest {
				nearest = l.Price
			}
		}
	}
	return nearest
}

// Helper function for square root
func sqrt(x float64) float64 {
	return math.Sqrt(x)
}
