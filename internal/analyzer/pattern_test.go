package analyzer

import (
	"testing"
	"time"

	"traveler/pkg/model"
)

func TestIntradayAnalyzer_Analyze(t *testing.T) {
	analyzer := NewIntradayAnalyzer(60, 60) // 60 min morning/closing windows

	// Create test data: a day with morning dip and closing rise
	loc, _ := time.LoadLocation("America/New_York")
	date := time.Date(2024, 1, 15, 0, 0, 0, 0, loc)

	// Generate 5-minute candles for the trading day (9:30 - 16:00)
	candles := generateTestCandles(date, loc)

	data := &model.IntradayData{
		Symbol:  "TEST",
		Date:    date,
		Candles: candles,
	}

	stats := analyzer.Analyze(data)
	if stats == nil {
		t.Fatal("Expected stats, got nil")
	}

	if !stats.HasFullData {
		t.Error("Expected HasFullData to be true")
	}

	// Verify basic stats
	if stats.OpenPrice != 100.0 {
		t.Errorf("Expected OpenPrice 100.0, got %f", stats.OpenPrice)
	}

	// Morning low should be lower than open (dip pattern)
	if stats.MorningLow >= stats.OpenPrice {
		t.Errorf("Expected MorningLow < OpenPrice, got MorningLow=%f, OpenPrice=%f",
			stats.MorningLow, stats.OpenPrice)
	}

	// Close should be higher than open (rise pattern)
	if stats.ClosePrice <= stats.OpenPrice {
		t.Errorf("Expected ClosePrice > OpenPrice, got ClosePrice=%f, OpenPrice=%f",
			stats.ClosePrice, stats.OpenPrice)
	}
}

func TestCalculateMorningDipPercent(t *testing.T) {
	stats := &IntradayStats{
		OpenPrice:  100.0,
		MorningLow: 98.0, // 2% dip
	}

	dip := CalculateMorningDipPercent(stats)
	expected := -2.0

	if dip != expected {
		t.Errorf("Expected dip %f%%, got %f%%", expected, dip)
	}
}

func TestCalculateCloseRisePercent(t *testing.T) {
	stats := &IntradayStats{
		OpenPrice:  100.0,
		ClosePrice: 101.5, // 1.5% rise
	}

	rise := CalculateCloseRisePercent(stats)
	expected := 1.5

	if rise != expected {
		t.Errorf("Expected rise %f%%, got %f%%", expected, rise)
	}
}

func TestCalculateReboundPercent(t *testing.T) {
	stats := &IntradayStats{
		MorningLow: 98.0,
		ClosePrice: 101.0, // ~3.06% rebound
	}

	rebound := CalculateReboundPercent(stats)
	expected := ((101.0 - 98.0) / 98.0) * 100

	if rebound != expected {
		t.Errorf("Expected rebound %f%%, got %f%%", expected, rebound)
	}
}

func TestPatternMatching(t *testing.T) {
	tests := []struct {
		name           string
		morningDip     float64
		closeRise      float64
		rebound        float64
		dropThreshold  float64
		riseThreshold  float64
		reboundThresh  float64
		shouldMatch    bool
	}{
		{
			name:          "Clear pattern match",
			morningDip:    -2.0,
			closeRise:     1.0,
			rebound:       3.0,
			dropThreshold: -1.0,
			riseThreshold: 0.5,
			reboundThresh: 2.0,
			shouldMatch:   true,
		},
		{
			name:          "No morning dip",
			morningDip:    -0.5, // Not enough dip
			closeRise:     1.0,
			rebound:       3.0,
			dropThreshold: -1.0,
			riseThreshold: 0.5,
			reboundThresh: 2.0,
			shouldMatch:   false,
		},
		{
			name:          "No close rise but good rebound",
			morningDip:    -2.0,
			closeRise:     0.2, // Not enough rise
			rebound:       2.5, // But good rebound
			dropThreshold: -1.0,
			riseThreshold: 0.5,
			reboundThresh: 2.0,
			shouldMatch:   true, // Should match because rebound is good
		},
		{
			name:          "Borderline pattern",
			morningDip:    -1.0, // Exactly at threshold
			closeRise:     0.5,  // Exactly at threshold
			rebound:       2.0,
			dropThreshold: -1.0,
			riseThreshold: 0.5,
			reboundThresh: 2.0,
			shouldMatch:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := tt.morningDip <= tt.dropThreshold &&
				(tt.closeRise >= tt.riseThreshold || tt.rebound >= tt.reboundThresh)

			if matches != tt.shouldMatch {
				t.Errorf("Expected match=%v, got %v", tt.shouldMatch, matches)
			}
		})
	}
}

// generateTestCandles creates realistic test candles for a trading day
func generateTestCandles(date time.Time, loc *time.Location) []model.Candle {
	candles := make([]model.Candle, 0)

	// Trading hours: 9:30 - 16:00 (6.5 hours = 78 5-minute candles)
	startTime := time.Date(date.Year(), date.Month(), date.Day(), 9, 30, 0, 0, loc)

	// Simulate: Open at 100, dip to 98 in morning, close at 101
	openPrice := 100.0
	morningLow := 98.0
	closePrice := 101.0

	for i := 0; i < 78; i++ {
		candleTime := startTime.Add(time.Duration(i*5) * time.Minute)
		progress := float64(i) / 77.0 // 0 to 1

		var price float64
		if progress < 0.15 { // First hour: dip phase
			// Go from 100 to 98
			price = openPrice - (openPrice-morningLow)*(progress/0.15)
		} else if progress < 0.3 { // Recovery phase
			// Go from 98 back to 100
			recoveryProgress := (progress - 0.15) / 0.15
			price = morningLow + (openPrice-morningLow)*recoveryProgress
		} else { // Rest of day: gradual rise to close
			riseProgress := (progress - 0.3) / 0.7
			price = openPrice + (closePrice-openPrice)*riseProgress
		}

		candles = append(candles, model.Candle{
			Time:   candleTime,
			Open:   price - 0.1,
			High:   price + 0.2,
			Low:    price - 0.2,
			Close:  price,
			Volume: 1000000,
		})
	}

	// Fix first candle open price
	if len(candles) > 0 {
		candles[0].Open = openPrice
	}

	return candles
}
