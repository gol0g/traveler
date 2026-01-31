package position

import (
	"math"
)

// TradeGuide provides actionable trading guidance
type TradeGuide struct {
	// Entry
	EntryPrice    float64 `json:"entry_price"`
	EntryType     string  `json:"entry_type"` // "market", "limit"

	// Exit points
	StopLoss      float64 `json:"stop_loss"`
	StopLossPct   float64 `json:"stop_loss_pct"`
	Target1       float64 `json:"target_1"`       // 1st target (1:1 R/R)
	Target1Pct    float64 `json:"target_1_pct"`
	Target2       float64 `json:"target_2"`       // 2nd target (1:2 R/R)
	Target2Pct    float64 `json:"target_2_pct"`
	Target3       float64 `json:"target_3"`       // 3rd target (1:3 R/R)
	Target3Pct    float64 `json:"target_3_pct"`

	// Position sizing
	RiskRewardRatio float64 `json:"risk_reward_ratio"`
	PositionSize    int     `json:"position_size"`    // Number of shares
	InvestAmount    float64 `json:"invest_amount"`    // Total investment
	RiskAmount      float64 `json:"risk_amount"`      // Max loss if stop hit

	// Kelly criterion
	KellyFraction   float64 `json:"kelly_fraction"`   // Optimal bet size (0-1)
	KellyAdjusted   float64 `json:"kelly_adjusted"`   // Half-Kelly for safety

	// Risk metrics
	MaxLossPct      float64 `json:"max_loss_pct"`     // % of portfolio at risk
	BreakevenPct    float64 `json:"breakeven_pct"`    // Required win rate to breakeven
}

// PositionSizer calculates optimal position sizes
type PositionSizer struct {
	AccountBalance  float64 // Total account value
	RiskPerTrade    float64 // Max risk per trade (e.g., 0.01 = 1%)
	Commission      float64 // Commission rate (e.g., 0.00015 = 0.015%)
	Slippage        float64 // Expected slippage (e.g., 0.001 = 0.1%)
	TaxRate         float64 // Sell tax rate (e.g., 0.0018 = 0.18%)
}

// NewPositionSizer creates a new position sizer with defaults
func NewPositionSizer(accountBalance float64) *PositionSizer {
	return &PositionSizer{
		AccountBalance: accountBalance,
		RiskPerTrade:   0.01,    // 1% risk per trade (conservative)
		Commission:     0.00015, // 0.015% (typical Korean broker)
		Slippage:       0.001,   // 0.1% estimated
		TaxRate:        0.0018,  // 0.18% sell tax
	}
}

// CalculateGuide generates a complete trade guide
func (p *PositionSizer) CalculateGuide(
	entryPrice float64,
	stopLossPrice float64,
	winRate float64, // Historical win rate (0-1)
) *TradeGuide {
	guide := &TradeGuide{
		EntryPrice: entryPrice,
		EntryType:  "limit",
		StopLoss:   stopLossPrice,
	}

	// Calculate risk per share
	riskPerShare := entryPrice - stopLossPrice
	if riskPerShare <= 0 {
		return nil // Invalid: stop loss above entry
	}

	guide.StopLossPct = (riskPerShare / entryPrice) * 100

	// Calculate targets based on R multiples
	guide.Target1 = entryPrice + riskPerShare*1.0 // 1R
	guide.Target2 = entryPrice + riskPerShare*2.0 // 2R
	guide.Target3 = entryPrice + riskPerShare*3.0 // 3R

	guide.Target1Pct = (guide.Target1 - entryPrice) / entryPrice * 100
	guide.Target2Pct = (guide.Target2 - entryPrice) / entryPrice * 100
	guide.Target3Pct = (guide.Target3 - entryPrice) / entryPrice * 100

	// Risk/Reward ratio (to Target2)
	guide.RiskRewardRatio = 2.0 // Using 2R as primary target

	// Position sizing based on fixed risk
	maxRiskAmount := p.AccountBalance * p.RiskPerTrade
	guide.PositionSize = int(maxRiskAmount / riskPerShare)
	guide.InvestAmount = float64(guide.PositionSize) * entryPrice
	guide.RiskAmount = float64(guide.PositionSize) * riskPerShare

	// Adjust for costs
	totalCosts := p.calculateTotalCosts(guide.InvestAmount)
	guide.RiskAmount += totalCosts

	// Kelly Criterion
	if winRate > 0 && winRate < 1 {
		// Kelly = W - (1-W)/R where W=win rate, R=win/loss ratio
		// For 2R target: win gives 2R, loss gives 1R
		avgWin := 2.0 // 2R
		avgLoss := 1.0 // 1R

		guide.KellyFraction = (winRate*avgWin - (1-winRate)*avgLoss) / avgWin
		guide.KellyFraction = math.Max(0, guide.KellyFraction) // Can't be negative
		guide.KellyAdjusted = guide.KellyFraction * 0.5 // Half-Kelly for safety
	}

	// Risk metrics
	guide.MaxLossPct = (guide.RiskAmount / p.AccountBalance) * 100

	// Breakeven win rate for this R/R ratio
	// At 2R: need to win 1/(1+2) = 33.3% to breakeven
	guide.BreakevenPct = 1 / (1 + guide.RiskRewardRatio) * 100

	return guide
}

// calculateTotalCosts estimates all trading costs
func (p *PositionSizer) calculateTotalCosts(investAmount float64) float64 {
	buyCommission := investAmount * p.Commission
	sellCommission := investAmount * p.Commission
	slippage := investAmount * p.Slippage * 2 // Both ways
	tax := investAmount * p.TaxRate

	return buyCommission + sellCommission + slippage + tax
}

// CalculateKelly calculates the Kelly fraction for given parameters
func CalculateKelly(winRate, avgWin, avgLoss float64) float64 {
	if avgLoss == 0 {
		return 0
	}

	// Kelly = (W * B - L) / B
	// where W = win probability, L = loss probability, B = win/loss ratio
	b := avgWin / avgLoss
	kelly := (winRate*b - (1 - winRate)) / b

	return math.Max(0, math.Min(kelly, 1)) // Clamp between 0 and 1
}

// RiskAssessment provides risk level assessment
type RiskAssessment struct {
	Level       string  // "LOW", "MEDIUM", "HIGH", "EXTREME"
	Score       int     // 0-100
	Description string
}

// AssessRisk evaluates the risk level of a trade
func AssessRisk(guide *TradeGuide, winRate float64) *RiskAssessment {
	score := 0

	// Factor 1: Stop loss distance (closer = higher risk of stop hunt)
	if guide.StopLossPct < 1.0 {
		score += 30 // Very tight stop
	} else if guide.StopLossPct < 2.0 {
		score += 20
	} else if guide.StopLossPct < 3.0 {
		score += 10
	}

	// Factor 2: Win rate vs breakeven
	if winRate < guide.BreakevenPct/100 {
		score += 40 // Below breakeven - losing strategy
	} else if winRate < (guide.BreakevenPct/100 + 0.1) {
		score += 20 // Barely profitable
	}

	// Factor 3: Portfolio risk
	if guide.MaxLossPct > 2.0 {
		score += 30 // Too much at risk
	} else if guide.MaxLossPct > 1.0 {
		score += 15
	}

	assessment := &RiskAssessment{Score: score}

	switch {
	case score >= 70:
		assessment.Level = "EXTREME"
		assessment.Description = "Very high risk - consider skipping this trade"
	case score >= 50:
		assessment.Level = "HIGH"
		assessment.Description = "Elevated risk - reduce position size"
	case score >= 30:
		assessment.Level = "MEDIUM"
		assessment.Description = "Moderate risk - proceed with caution"
	default:
		assessment.Level = "LOW"
		assessment.Description = "Acceptable risk - trade within plan"
	}

	return assessment
}
