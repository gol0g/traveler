package model

import "time"

// Candle represents a single candlestick (OHLCV data)
type Candle struct {
	Time   time.Time `json:"time"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume int64     `json:"volume"`
}

// Stock represents basic stock information
type Stock struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Exchange string `json:"exchange"` // NYSE, NASDAQ
}

// DayPattern represents the pattern analysis for a single day
type DayPattern struct {
	Date           time.Time `json:"date"`
	OpenPrice      float64   `json:"open_price"`
	ClosePrice     float64   `json:"close_price"`
	MorningLow     float64   `json:"morning_low"`      // Lowest price in morning window
	MorningDipPct  float64   `json:"morning_dip_pct"`  // Percentage drop from open
	CloseRisePct   float64   `json:"close_rise_pct"`   // Percentage rise at close vs open
	ReboundPct     float64   `json:"rebound_pct"`      // Percentage rise from morning low
	MatchesPattern bool      `json:"matches_pattern"`
}

// TechnicalAnalysis contains technical indicators and prediction
type TechnicalAnalysis struct {
	RSI              float64 `json:"rsi"`
	RSISignal        string  `json:"rsi_signal"`
	VolumeRatio      float64 `json:"volume_ratio"`
	VolumeSignal     string  `json:"volume_signal"`
	PriceVsMA5       float64 `json:"price_vs_ma5"`
	PriceVsMA20      float64 `json:"price_vs_ma20"`
	TrendSignal      string  `json:"trend_signal"`
	PatternStrength  float64 `json:"pattern_strength"`
	ConsistencyScore float64 `json:"consistency_score"`
	ContinuationProb float64 `json:"continuation_prob"`
	Recommendation   string  `json:"recommendation"`
}

// PatternResult represents the complete pattern analysis for a stock
type PatternResult struct {
	Stock            Stock              `json:"stock"`
	ConsecutiveDays  int                `json:"consecutive_days"`
	DayPatterns      []DayPattern       `json:"day_patterns"`
	AvgMorningDipPct float64            `json:"avg_morning_dip_pct"`
	AvgCloseRisePct  float64            `json:"avg_close_rise_pct"`
	Technical        *TechnicalAnalysis `json:"technical,omitempty"`
}

// IntradayData represents a full day's intraday candles
type IntradayData struct {
	Symbol  string    `json:"symbol"`
	Date    time.Time `json:"date"`
	Candles []Candle  `json:"candles"`
}

// ScanResult represents the final scan output
type ScanResult struct {
	TotalScanned  int             `json:"total_scanned"`
	MatchingCount int             `json:"matching_count"`
	Results       []PatternResult `json:"results"`
	ScanTime      time.Duration   `json:"scan_time"`
}
