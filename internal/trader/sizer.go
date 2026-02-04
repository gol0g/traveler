package trader

import (
	"math"

	"traveler/internal/strategy"
)

// SizerConfig 포지션 사이징 설정
type SizerConfig struct {
	TotalCapital      float64 // 총 자본
	RiskPerTrade      float64 // 거래당 리스크 비율 (예: 0.01 = 1%)
	MaxPositionPct    float64 // 종목당 최대 비율 (예: 0.2 = 20%)
	MaxPositions      int     // 최대 동시 포지션
	MinRiskReward     float64 // 최소 R/R (이하면 스킵)
	MinExpectedReturn float64 // 최소 기대수익률 (수수료 커버용, 예: 0.01 = 1%)
	CommissionRate    float64 // 수수료율 (왕복, 예: 0.005 = 0.5%)
}

// DefaultSizerConfig 기본 설정
func DefaultSizerConfig(capital float64) SizerConfig {
	return SizerConfig{
		TotalCapital:      capital,
		RiskPerTrade:      0.01,   // 1%
		MaxPositionPct:    0.20,   // 20%
		MaxPositions:      5,
		MinRiskReward:     1.5,
		MinExpectedReturn: 0.01,   // 1% (수수료 0.5% + 마진 0.5%)
		CommissionRate:    0.005,  // 0.5% (매수 0.25% + 매도 0.25%)
	}
}

// PositionSizer stop-distance 기반 포지션 사이징
type PositionSizer struct {
	config SizerConfig
}

// NewPositionSizer 생성자
func NewPositionSizer(cfg SizerConfig) *PositionSizer {
	return &PositionSizer{config: cfg}
}

// SizingResult 사이징 결과
type SizingResult struct {
	Symbol        string
	Quantity      int
	EntryPrice    float64
	StopLoss      float64
	Target        float64
	StopDistance  float64 // 진입가 - 손절가
	RiskAmount    float64 // 실제 리스크 금액
	InvestAmount  float64 // 투자 금액
	RiskPct       float64 // 자본 대비 리스크 %
	AllocationPct float64 // 자본 대비 투자 %
	RiskReward    float64 // R/R 비율
	Skipped       bool
	SkipReason    string
}

// CalculateSize 단일 시그널 사이징
// 핵심 공식: qty = floor(riskBudget / stopDistance)
func (p *PositionSizer) CalculateSize(sig *strategy.Signal) SizingResult {
	result := SizingResult{
		Symbol: sig.Stock.Symbol,
	}

	if sig.Guide == nil {
		result.Skipped = true
		result.SkipReason = "no trade guide"
		return result
	}

	g := sig.Guide
	result.EntryPrice = g.EntryPrice
	result.StopLoss = g.StopLoss
	result.Target = g.Target1
	result.RiskReward = g.RiskRewardRatio

	// 1. Stop distance 계산
	stopDistance := g.EntryPrice - g.StopLoss
	if stopDistance <= 0 {
		result.Skipped = true
		result.SkipReason = "invalid stop distance"
		return result
	}
	result.StopDistance = stopDistance

	// 2. R/R 체크
	if result.RiskReward < p.config.MinRiskReward {
		result.Skipped = true
		result.SkipReason = "R/R too low"
		return result
	}

	// 3. 기대수익률 체크 (수수료 커버 확인)
	expectedReturn := (g.Target1 - g.EntryPrice) / g.EntryPrice
	if expectedReturn < p.config.MinExpectedReturn {
		result.Skipped = true
		result.SkipReason = "expected return too low (< commission)"
		return result
	}

	// 4. 가격이 최대 포지션 금액 초과 체크
	maxPositionValue := p.config.TotalCapital * p.config.MaxPositionPct
	if g.EntryPrice > maxPositionValue {
		result.Skipped = true
		result.SkipReason = "price exceeds max position value"
		return result
	}

	// 5. 리스크 예산 계산
	riskBudget := p.config.TotalCapital * p.config.RiskPerTrade

	// 6. Stop-distance 기반 수량 계산 (핵심!)
	// qty = floor(riskBudget / stopDistance)
	qtyByRisk := int(math.Floor(riskBudget / stopDistance))

	// 7. 최대 포지션 금액 기반 수량 제한
	qtyByAllocation := int(math.Floor(maxPositionValue / g.EntryPrice))

	// 8. 둘 중 작은 값 선택
	qty := qtyByRisk
	if qtyByAllocation < qty {
		qty = qtyByAllocation
	}

	// 9. 최소 1주
	if qty < 1 {
		qty = 1
	}

	result.Quantity = qty
	result.InvestAmount = float64(qty) * g.EntryPrice
	result.RiskAmount = float64(qty) * stopDistance
	result.RiskPct = result.RiskAmount / p.config.TotalCapital * 100
	result.AllocationPct = result.InvestAmount / p.config.TotalCapital * 100

	return result
}

// CalculatePortfolio 여러 시그널 포트폴리오 사이징
// 시그널들을 받아서 자본에 맞게 분배
func (p *PositionSizer) CalculatePortfolio(signals []strategy.Signal) ([]SizingResult, PortfolioSummary) {
	results := make([]SizingResult, 0, len(signals))
	summary := PortfolioSummary{}

	// 최대 포지션 수 제한
	maxSignals := p.config.MaxPositions
	if len(signals) < maxSignals {
		maxSignals = len(signals)
	}

	for i := 0; i < maxSignals; i++ {
		result := p.CalculateSize(&signals[i])
		results = append(results, result)

		if !result.Skipped {
			summary.TotalInvest += result.InvestAmount
			summary.TotalRisk += result.RiskAmount
			summary.PositionCount++
		}
	}

	summary.TotalInvestPct = summary.TotalInvest / p.config.TotalCapital * 100
	summary.TotalRiskPct = summary.TotalRisk / p.config.TotalCapital * 100
	summary.AvgRiskPerPosition = 0
	if summary.PositionCount > 0 {
		summary.AvgRiskPerPosition = summary.TotalRiskPct / float64(summary.PositionCount)
	}

	return results, summary
}

// PortfolioSummary 포트폴리오 요약
type PortfolioSummary struct {
	PositionCount      int
	TotalInvest        float64
	TotalRisk          float64
	TotalInvestPct     float64
	TotalRiskPct       float64
	AvgRiskPerPosition float64
}

// ApplyToSignals 시그널들에 사이징 결과 적용
func (p *PositionSizer) ApplyToSignals(signals []strategy.Signal) []strategy.Signal {
	results, _ := p.CalculatePortfolio(signals)

	sized := make([]strategy.Signal, 0)
	for i, result := range results {
		if result.Skipped {
			continue
		}

		sig := signals[i]
		if sig.Guide != nil {
			sig.Guide.PositionSize = result.Quantity
			sig.Guide.InvestAmount = result.InvestAmount
			sig.Guide.RiskAmount = result.RiskAmount
			sig.Guide.RiskPct = result.RiskPct
			sig.Guide.AllocationPct = result.AllocationPct
		}
		sized = append(sized, sig)
	}

	return sized
}

// AdjustForBalance 잔고에 맞게 설정 조정
// 잔고가 적으면 더 보수적으로, 많으면 표준 설정
func AdjustConfigForBalance(balance float64) SizerConfig {
	cfg := DefaultSizerConfig(balance)

	switch {
	case balance < 500:
		// 소액: 보수적
		cfg.RiskPerTrade = 0.02      // 2% (적은 금액이라 비율 높여도 절대금액 작음)
		cfg.MaxPositions = 3
		cfg.MinRiskReward = 1.5
		cfg.MinExpectedReturn = 0.015 // 1.5% (소액은 수수료 부담 큼)
	case balance < 5000:
		// 중간: 표준
		cfg.RiskPerTrade = 0.01      // 1%
		cfg.MaxPositions = 5
		cfg.MinRiskReward = 1.5
		cfg.MinExpectedReturn = 0.01  // 1%
	default:
		// 고액: 약간 보수적
		cfg.RiskPerTrade = 0.01      // 1%
		cfg.MaxPositions = 5
		cfg.MinRiskReward = 2.0      // R/R 기준 높임
		cfg.MinExpectedReturn = 0.01  // 1%
	}

	return cfg
}
