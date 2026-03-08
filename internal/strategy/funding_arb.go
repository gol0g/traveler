package strategy

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// FundingArbConfig holds configuration for the funding rate arbitrage strategy.
type FundingArbConfig struct {
	// Minimum funding rate to enter (0.0001 = 0.01%)
	MinFundingRate float64

	// Close if funding rate goes below this
	ExitBelowRate float64

	// Position sizing
	MaxCapitalUSDT float64
	MaxPositions   int

	// Trading pairs (should NOT overlap with short scalp pairs to avoid leverage conflict)
	Pairs []string

	// Commission rates
	SpotCommissionPct    float64 // 0.10% Spot taker
	FuturesCommissionPct float64 // 0.04% Futures taker

	// Check interval in minutes
	CheckIntervalMin int
}

// DefaultFundingArbConfig returns conservative defaults.
func DefaultFundingArbConfig() FundingArbConfig {
	return FundingArbConfig{
		MinFundingRate:       0.0001, // 0.01%
		ExitBelowRate:        0.0,    // exit if rate goes negative
		MaxCapitalUSDT:       150.0,
		MaxPositions:         1,
		Pairs:                []string{"BTCUSDT"},
		SpotCommissionPct:    0.10,
		FuturesCommissionPct: 0.04,
		CheckIntervalMin:     30,
	}
}

// ArbPosition tracks one active arb position (spot long + futures short).
type ArbPosition struct {
	Symbol           string    `json:"symbol"`
	BaseAsset        string    `json:"base_asset"`
	SpotQty          float64   `json:"spot_qty"`
	SpotEntryPrice   float64   `json:"spot_entry_price"`
	FuturesQty       float64   `json:"futures_qty"`
	FuturesEntry     float64   `json:"futures_entry"`
	Basis            float64   `json:"basis"`
	CapitalUsed      float64   `json:"capital_used"`
	OpenedAt         time.Time `json:"opened_at"`
	FundingCollected float64   `json:"funding_collected"`
	FundingPayments  int       `json:"funding_payments"`
	LowRateCount     int       `json:"low_rate_count"`
}

// ShouldEnterArb determines if we should open a new arb position.
func ShouldEnterArb(cfg FundingArbConfig, fundingRate float64, activePositions int, availableCapital float64) bool {
	if activePositions >= cfg.MaxPositions {
		return false
	}
	if availableCapital < 20.0 {
		return false
	}
	if fundingRate < cfg.MinFundingRate {
		return false
	}
	return true
}

// ShouldExitArb determines if we should close an arb position.
func ShouldExitArb(cfg FundingArbConfig, fundingRate float64, pos *ArbPosition) (bool, string) {
	if fundingRate < cfg.ExitBelowRate {
		pos.LowRateCount++
		return true, fmt.Sprintf("negative_funding (rate=%.4f%%)", fundingRate*100)
	}
	pos.LowRateCount = 0
	return false, ""
}

// EstimatedRoundTripCost calculates total commission for opening + closing.
func EstimatedRoundTripCost(cfg FundingArbConfig, capitalUSDT float64) float64 {
	halfCapital := capitalUSDT / 2.0
	spotCost := halfCapital * cfg.SpotCommissionPct / 100.0 * 2
	futuresCost := halfCapital * cfg.FuturesCommissionPct / 100.0 * 2
	return spotCost + futuresCost
}

// BreakevenFundingPeriods calculates how many 8h periods needed to cover commissions.
func BreakevenFundingPeriods(cfg FundingArbConfig, capitalUSDT, fundingRate float64) int {
	totalCost := EstimatedRoundTripCost(cfg, capitalUSDT)
	halfCapital := capitalUSDT / 2.0
	perPeriodIncome := halfCapital * fundingRate
	if perPeriodIncome <= 0 {
		return 999
	}
	return int(math.Ceil(totalCost / perPeriodIncome))
}

// BaseAssetFromSymbol extracts "ETH" from "ETHUSDT".
func BaseAssetFromSymbol(symbol string) string {
	return strings.TrimSuffix(symbol, "USDT")
}
