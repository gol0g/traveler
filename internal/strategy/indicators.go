package strategy

import (
	"traveler/pkg/model"
)

// Indicators contains calculated technical indicators
type Indicators struct {
	MA5     float64
	MA10    float64
	MA20    float64
	MA50    float64
	MA200   float64
	RSI14   float64
	AvgVol  float64 // Average volume (20-day)
	BBUpper float64 // Bollinger Band Upper
	BBLower float64 // Bollinger Band Lower
	BBWidth float64 // Bollinger Bandwidth
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
	if len(candles) >= 200 {
		ind.MA200 = CalculateMA(candles, 200)
	}
	if len(candles) >= 15 {
		ind.RSI14 = CalculateRSI(candles, 14)
	}

	return ind
}

// Helper function for square root
func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x / 2
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}
