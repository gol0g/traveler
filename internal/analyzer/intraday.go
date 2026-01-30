package analyzer

import (
	"sort"
	"time"

	"traveler/pkg/model"
)

// IntradayAnalyzer analyzes intraday candle data
type IntradayAnalyzer struct {
	morningWindowMinutes int
	closingWindowMinutes int
}

// NewIntradayAnalyzer creates a new intraday analyzer
func NewIntradayAnalyzer(morningWindow, closingWindow int) *IntradayAnalyzer {
	return &IntradayAnalyzer{
		morningWindowMinutes: morningWindow,
		closingWindowMinutes: closingWindow,
	}
}

// MarketHours represents the trading session hours
type MarketHours struct {
	Open  time.Time
	Close time.Time
}

// GetUSMarketHours returns US market hours for a given date
func GetUSMarketHours(date time.Time) MarketHours {
	loc, _ := time.LoadLocation("America/New_York")
	return MarketHours{
		Open:  time.Date(date.Year(), date.Month(), date.Day(), 9, 30, 0, 0, loc),
		Close: time.Date(date.Year(), date.Month(), date.Day(), 16, 0, 0, 0, loc),
	}
}

// IntradayStats represents computed statistics for a trading day
type IntradayStats struct {
	Date           time.Time
	OpenPrice      float64
	ClosePrice     float64
	HighPrice      float64
	LowPrice       float64
	MorningLow     float64 // Lowest price in morning window
	MorningHigh    float64 // Highest price in morning window
	ClosingLow     float64 // Lowest price in closing window
	ClosingHigh    float64 // Highest price in closing window
	HasFullData    bool    // Whether we have data for the full trading day
}

// Analyze computes statistics for a single day's intraday data
func (a *IntradayAnalyzer) Analyze(data *model.IntradayData) *IntradayStats {
	if len(data.Candles) == 0 {
		return nil
	}

	// Sort candles by time
	candles := make([]model.Candle, len(data.Candles))
	copy(candles, data.Candles)
	sort.Slice(candles, func(i, j int) bool {
		return candles[i].Time.Before(candles[j].Time)
	})

	marketHours := GetUSMarketHours(data.Date)
	morningEnd := marketHours.Open.Add(time.Duration(a.morningWindowMinutes) * time.Minute)
	closingStart := marketHours.Close.Add(-time.Duration(a.closingWindowMinutes) * time.Minute)

	stats := &IntradayStats{
		Date:        data.Date,
		OpenPrice:   candles[0].Open,
		ClosePrice:  candles[len(candles)-1].Close,
		HighPrice:   candles[0].High,
		LowPrice:    candles[0].Low,
		MorningLow:  candles[0].Low,
		MorningHigh: candles[0].High,
		ClosingLow:  candles[len(candles)-1].Low,
		ClosingHigh: candles[len(candles)-1].High,
		HasFullData: true,
	}

	morningCandleCount := 0
	closingCandleCount := 0

	for _, c := range candles {
		// Overall high/low
		if c.High > stats.HighPrice {
			stats.HighPrice = c.High
		}
		if c.Low < stats.LowPrice {
			stats.LowPrice = c.Low
		}

		// Morning window (first hour)
		if !c.Time.After(morningEnd) {
			morningCandleCount++
			if c.Low < stats.MorningLow {
				stats.MorningLow = c.Low
			}
			if c.High > stats.MorningHigh {
				stats.MorningHigh = c.High
			}
		}

		// Closing window (last hour)
		if !c.Time.Before(closingStart) {
			closingCandleCount++
			if c.Low < stats.ClosingLow {
				stats.ClosingLow = c.Low
			}
			if c.High > stats.ClosingHigh {
				stats.ClosingHigh = c.High
			}
		}
	}

	// Check if we have enough data
	// For 1-min candles, we should have ~60 in morning and ~30 in closing (half hour at least)
	if morningCandleCount < 10 || closingCandleCount < 5 {
		stats.HasFullData = false
	}

	return stats
}

// AnalyzeMultipleDays analyzes multiple days of intraday data
func (a *IntradayAnalyzer) AnalyzeMultipleDays(data []model.IntradayData) []*IntradayStats {
	stats := make([]*IntradayStats, 0, len(data))
	for i := range data {
		s := a.Analyze(&data[i])
		if s != nil && s.HasFullData {
			stats = append(stats, s)
		}
	}

	// Sort by date descending (most recent first)
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Date.After(stats[j].Date)
	})

	return stats
}

// CalculateMorningDipPercent calculates the percentage drop from open to morning low
func CalculateMorningDipPercent(stats *IntradayStats) float64 {
	if stats.OpenPrice == 0 {
		return 0
	}
	return ((stats.MorningLow - stats.OpenPrice) / stats.OpenPrice) * 100
}

// CalculateCloseRisePercent calculates the percentage rise at close vs open
func CalculateCloseRisePercent(stats *IntradayStats) float64 {
	if stats.OpenPrice == 0 {
		return 0
	}
	return ((stats.ClosePrice - stats.OpenPrice) / stats.OpenPrice) * 100
}

// CalculateReboundPercent calculates the percentage rise from morning low to close
func CalculateReboundPercent(stats *IntradayStats) float64 {
	if stats.MorningLow == 0 {
		return 0
	}
	return ((stats.ClosePrice - stats.MorningLow) / stats.MorningLow) * 100
}
