package analyzer

import (
	"math"

	"traveler/pkg/model"
)

// TechnicalAnalyzer performs technical analysis on stock data
type TechnicalAnalyzer struct{}

// NewTechnicalAnalyzer creates a new technical analyzer
func NewTechnicalAnalyzer() *TechnicalAnalyzer {
	return &TechnicalAnalyzer{}
}

// Analyze performs technical analysis on the given data
func (t *TechnicalAnalyzer) Analyze(dayPatterns []model.DayPattern, dailyCandles []model.Candle) *model.TechnicalAnalysis {
	if len(dayPatterns) == 0 {
		return nil
	}

	analysis := &model.TechnicalAnalysis{}

	// Calculate RSI if we have enough daily data
	if len(dailyCandles) >= 14 {
		analysis.RSI = t.calculateRSI(dailyCandles, 14)
		analysis.RSISignal = t.getRSISignal(analysis.RSI)
	}

	// Calculate volume ratio
	if len(dailyCandles) >= 20 {
		analysis.VolumeRatio = t.calculateVolumeRatio(dailyCandles, 20)
		analysis.VolumeSignal = t.getVolumeSignal(analysis.VolumeRatio)
	}

	// Calculate price vs moving averages
	if len(dailyCandles) >= 5 {
		analysis.PriceVsMA5 = t.calculatePriceVsMA(dailyCandles, 5)
	}
	if len(dailyCandles) >= 20 {
		analysis.PriceVsMA20 = t.calculatePriceVsMA(dailyCandles, 20)
	}
	analysis.TrendSignal = t.getTrendSignal(analysis.PriceVsMA5, analysis.PriceVsMA20)

	// Calculate pattern-specific metrics
	analysis.PatternStrength = t.calculatePatternStrength(dayPatterns)
	analysis.ConsistencyScore = t.calculateConsistencyScore(dayPatterns)

	// Calculate continuation probability
	analysis.ContinuationProb = t.calculateContinuationProbability(analysis, dayPatterns)
	analysis.Recommendation = t.getRecommendation(analysis.ContinuationProb)

	return analysis
}

// calculateRSI calculates the Relative Strength Index
func (t *TechnicalAnalyzer) calculateRSI(candles []model.Candle, period int) float64 {
	if len(candles) < period+1 {
		return 50 // neutral
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
	rsi := 100 - (100 / (1 + rs))

	return math.Round(rsi*100) / 100
}

// getRSISignal interprets RSI value
func (t *TechnicalAnalyzer) getRSISignal(rsi float64) string {
	if rsi < 30 {
		return "oversold"
	} else if rsi > 70 {
		return "overbought"
	}
	return "neutral"
}

// calculateVolumeRatio calculates today's volume vs average
func (t *TechnicalAnalyzer) calculateVolumeRatio(candles []model.Candle, period int) float64 {
	if len(candles) < period {
		return 1.0
	}

	var sum int64
	for i := len(candles) - period; i < len(candles)-1; i++ {
		sum += candles[i].Volume
	}
	avgVolume := float64(sum) / float64(period-1)

	if avgVolume == 0 {
		return 1.0
	}

	todayVolume := float64(candles[len(candles)-1].Volume)
	return math.Round(todayVolume/avgVolume*100) / 100
}

// getVolumeSignal interprets volume ratio
func (t *TechnicalAnalyzer) getVolumeSignal(ratio float64) string {
	if ratio < 0.7 {
		return "low"
	} else if ratio > 1.5 {
		return "high"
	}
	return "normal"
}

// calculatePriceVsMA calculates price position vs moving average
func (t *TechnicalAnalyzer) calculatePriceVsMA(candles []model.Candle, period int) float64 {
	if len(candles) < period {
		return 0
	}

	var sum float64
	for i := len(candles) - period; i < len(candles); i++ {
		sum += candles[i].Close
	}
	ma := sum / float64(period)

	if ma == 0 {
		return 0
	}

	currentPrice := candles[len(candles)-1].Close
	return math.Round((currentPrice-ma)/ma*10000) / 100 // percentage with 2 decimals
}

// getTrendSignal determines trend based on MA positions
func (t *TechnicalAnalyzer) getTrendSignal(priceVsMA5, priceVsMA20 float64) string {
	if priceVsMA5 > 1 && priceVsMA20 > 1 {
		return "uptrend"
	} else if priceVsMA5 < -1 && priceVsMA20 < -1 {
		return "downtrend"
	}
	return "neutral"
}

// calculatePatternStrength calculates how strong the pattern is
func (t *TechnicalAnalyzer) calculatePatternStrength(patterns []model.DayPattern) float64 {
	if len(patterns) == 0 {
		return 0
	}

	var totalStrength float64
	for _, p := range patterns {
		// Stronger dip = higher strength
		dipStrength := math.Min(math.Abs(p.MorningDipPct)/5*100, 100)

		// Stronger recovery = higher strength
		recoveryStrength := math.Min(p.ReboundPct/5*100, 100)

		// Combined strength
		totalStrength += (dipStrength + recoveryStrength) / 2
	}

	return math.Round(totalStrength/float64(len(patterns))*100) / 100
}

// calculateConsistencyScore measures pattern consistency
func (t *TechnicalAnalyzer) calculateConsistencyScore(patterns []model.DayPattern) float64 {
	if len(patterns) < 2 {
		return 50 // neutral for single day
	}

	// Calculate standard deviation of dip percentages
	var sumDip, sumRebound float64
	for _, p := range patterns {
		sumDip += p.MorningDipPct
		sumRebound += p.ReboundPct
	}
	avgDip := sumDip / float64(len(patterns))
	avgRebound := sumRebound / float64(len(patterns))

	var varianceDip, varianceRebound float64
	for _, p := range patterns {
		varianceDip += math.Pow(p.MorningDipPct-avgDip, 2)
		varianceRebound += math.Pow(p.ReboundPct-avgRebound, 2)
	}

	stdDip := math.Sqrt(varianceDip / float64(len(patterns)))
	stdRebound := math.Sqrt(varianceRebound / float64(len(patterns)))

	// Lower variance = higher consistency
	// Normalize: if std is 0, consistency is 100; if std > 2, consistency approaches 0
	dipConsistency := math.Max(0, 100-stdDip*25)
	reboundConsistency := math.Max(0, 100-stdRebound*25)

	return math.Round((dipConsistency+reboundConsistency)/2*100) / 100
}

// calculateContinuationProbability estimates probability of pattern continuing
func (t *TechnicalAnalyzer) calculateContinuationProbability(analysis *model.TechnicalAnalysis, patterns []model.DayPattern) float64 {
	var score float64

	// Factor 1: Pattern strength (0-25 points)
	score += analysis.PatternStrength * 0.25

	// Factor 2: Consistency score (0-25 points)
	score += analysis.ConsistencyScore * 0.25

	// Factor 3: RSI - oversold is favorable (0-20 points)
	if analysis.RSI > 0 {
		if analysis.RSI < 30 {
			score += 20 // Oversold - good for bounce
		} else if analysis.RSI < 50 {
			score += 15
		} else if analysis.RSI < 70 {
			score += 10
		} else {
			score += 5 // Overbought - less favorable
		}
	}

	// Factor 4: Volume - high volume confirms pattern (0-15 points)
	if analysis.VolumeRatio > 0 {
		if analysis.VolumeRatio > 1.5 {
			score += 15
		} else if analysis.VolumeRatio > 1.0 {
			score += 10
		} else {
			score += 5
		}
	}

	// Factor 5: Consecutive days (0-15 points)
	consecutiveDays := len(patterns)
	if consecutiveDays >= 5 {
		score += 15
	} else if consecutiveDays >= 3 {
		score += 12
	} else if consecutiveDays >= 2 {
		score += 8
	} else {
		score += 5
	}

	// Normalize to 0-100
	maxPossible := float64(25 + 25 + 20 + 15 + 15) // 100
	probability := (score / maxPossible) * 100

	return math.Round(probability*100) / 100
}

// getRecommendation provides a recommendation based on probability
func (t *TechnicalAnalyzer) getRecommendation(probability float64) string {
	if probability >= 70 {
		return "strong"
	} else if probability >= 50 {
		return "moderate"
	} else if probability >= 30 {
		return "weak"
	}
	return "avoid"
}

// AnalyzeFromIntraday performs analysis using intraday data when daily data is limited
func (t *TechnicalAnalyzer) AnalyzeFromIntraday(dayPatterns []model.DayPattern, intradayData []model.IntradayData) *model.TechnicalAnalysis {
	if len(dayPatterns) == 0 {
		return nil
	}

	// Convert intraday data to daily candles for analysis
	dailyCandles := make([]model.Candle, 0, len(intradayData))
	for _, day := range intradayData {
		if len(day.Candles) == 0 {
			continue
		}

		// Create daily candle from intraday
		var high, low float64 = day.Candles[0].High, day.Candles[0].Low
		var volume int64
		for _, c := range day.Candles {
			if c.High > high {
				high = c.High
			}
			if c.Low < low {
				low = c.Low
			}
			volume += c.Volume
		}

		dailyCandles = append(dailyCandles, model.Candle{
			Time:   day.Date,
			Open:   day.Candles[0].Open,
			High:   high,
			Low:    low,
			Close:  day.Candles[len(day.Candles)-1].Close,
			Volume: volume,
		})
	}

	return t.Analyze(dayPatterns, dailyCandles)
}
