package analyzer

import (
	"context"
	"sort"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// PatternConfig holds pattern detection configuration
type PatternConfig struct {
	ConsecutiveDays      int
	MorningDropThreshold float64 // Negative value (e.g., -1.0 for -1%)
	CloseRiseThreshold   float64 // Positive value (e.g., 0.5 for +0.5%)
	ReboundThreshold     float64 // Positive value (e.g., 2.0 for +2%)
	MorningWindow        int     // Minutes
	ClosingWindow        int     // Minutes
}

// PatternAnalyzer detects morning dip → closing rise patterns
type PatternAnalyzer struct {
	config            PatternConfig
	intradayAnalyzer  *IntradayAnalyzer
	technicalAnalyzer *TechnicalAnalyzer
	provider          provider.Provider
}

// NewPatternAnalyzer creates a new pattern analyzer
func NewPatternAnalyzer(cfg PatternConfig, p provider.Provider) *PatternAnalyzer {
	return &PatternAnalyzer{
		config:            cfg,
		intradayAnalyzer:  NewIntradayAnalyzer(cfg.MorningWindow, cfg.ClosingWindow),
		technicalAnalyzer: NewTechnicalAnalyzer(),
		provider:          p,
	}
}

// AnalyzeStock analyzes a single stock for the pattern
func (a *PatternAnalyzer) AnalyzeStock(ctx context.Context, stock model.Stock) (*model.PatternResult, error) {
	// Fetch intraday data for the required number of days (plus buffer)
	daysToFetch := a.config.ConsecutiveDays + 5 // Extra days for weekends/holidays
	intradayData, err := a.provider.GetMultiDayIntraday(ctx, stock.Symbol, daysToFetch, 5) // 5-min candles
	if err != nil {
		return nil, err
	}

	if len(intradayData) == 0 {
		return nil, nil
	}

	// Analyze each day
	stats := a.intradayAnalyzer.AnalyzeMultipleDays(intradayData)
	if len(stats) < a.config.ConsecutiveDays {
		return nil, nil // Not enough data
	}

	// Check for pattern in each day
	dayPatterns := make([]model.DayPattern, len(stats))
	for i, s := range stats {
		morningDip := CalculateMorningDipPercent(s)
		closeRise := CalculateCloseRisePercent(s)
		rebound := CalculateReboundPercent(s)

		// Pattern matches if:
		// 1. Morning dip is at least the threshold (e.g., -1% or lower)
		// 2. Either close rise is above threshold OR rebound is above threshold
		matchesPattern := morningDip <= a.config.MorningDropThreshold &&
			(closeRise >= a.config.CloseRiseThreshold || rebound >= a.config.ReboundThreshold)

		dayPatterns[i] = model.DayPattern{
			Date:           s.Date,
			OpenPrice:      s.OpenPrice,
			ClosePrice:     s.ClosePrice,
			MorningLow:     s.MorningLow,
			MorningDipPct:  morningDip,
			CloseRisePct:   closeRise,
			ReboundPct:     rebound,
			MatchesPattern: matchesPattern,
		}
	}

	// Sort by date (most recent first)
	sort.Slice(dayPatterns, func(i, j int) bool {
		return dayPatterns[i].Date.After(dayPatterns[j].Date)
	})

	// Count consecutive days from most recent
	consecutiveDays := 0
	for _, dp := range dayPatterns {
		if dp.MatchesPattern {
			consecutiveDays++
		} else {
			break // Streak broken
		}
	}

	// Return nil if not enough consecutive days
	if consecutiveDays < a.config.ConsecutiveDays {
		return nil, nil
	}

	// Calculate averages for the consecutive days
	var totalDip, totalRise float64
	matchingPatterns := dayPatterns[:consecutiveDays]
	for _, dp := range matchingPatterns {
		totalDip += dp.MorningDipPct
		totalRise += dp.CloseRisePct
	}

	// Perform technical analysis
	technical := a.technicalAnalyzer.AnalyzeFromIntraday(matchingPatterns, intradayData)

	return &model.PatternResult{
		Stock:            stock,
		ConsecutiveDays:  consecutiveDays,
		DayPatterns:      matchingPatterns,
		AvgMorningDipPct: totalDip / float64(consecutiveDays),
		AvgCloseRisePct:  totalRise / float64(consecutiveDays),
		Technical:        technical,
	}, nil
}

// CheckSingleDay checks if a single day matches the pattern
func (a *PatternAnalyzer) CheckSingleDay(data *model.IntradayData) *model.DayPattern {
	stats := a.intradayAnalyzer.Analyze(data)
	if stats == nil || !stats.HasFullData {
		return nil
	}

	morningDip := CalculateMorningDipPercent(stats)
	closeRise := CalculateCloseRisePercent(stats)
	rebound := CalculateReboundPercent(stats)

	matchesPattern := morningDip <= a.config.MorningDropThreshold &&
		(closeRise >= a.config.CloseRiseThreshold || rebound >= a.config.ReboundThreshold)

	return &model.DayPattern{
		Date:           stats.Date,
		OpenPrice:      stats.OpenPrice,
		ClosePrice:     stats.ClosePrice,
		MorningLow:     stats.MorningLow,
		MorningDipPct:  morningDip,
		CloseRisePct:   closeRise,
		ReboundPct:     rebound,
		MatchesPattern: matchesPattern,
	}
}

// BatchAnalyze analyzes multiple stocks (used by scanner)
func (a *PatternAnalyzer) BatchAnalyze(ctx context.Context, stocks []model.Stock, resultChan chan<- *model.PatternResult) {
	for _, stock := range stocks {
		select {
		case <-ctx.Done():
			return
		default:
			result, err := a.AnalyzeStock(ctx, stock)
			if err == nil && result != nil {
				resultChan <- result
			}
		}
	}
}
