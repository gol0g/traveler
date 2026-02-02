package trader

import (
	"traveler/internal/broker"
	"traveler/internal/strategy"
)

// RejectedSignal 거절된 시그널
type RejectedSignal struct {
	Signal strategy.Signal
	Reason string
}

// RiskManager 리스크 관리
type RiskManager struct {
	config Config
}

// NewRiskManager 생성자
func NewRiskManager(cfg Config) *RiskManager {
	return &RiskManager{config: cfg}
}

// ValidateSignals 시그널 유효성 검증
func (r *RiskManager) ValidateSignals(signals []strategy.Signal, positions []broker.Position) ([]strategy.Signal, []RejectedSignal) {
	approved := make([]strategy.Signal, 0)
	rejected := make([]RejectedSignal, 0)

	currentPositionCount := len(positions)
	currentSymbols := make(map[string]bool)
	for _, p := range positions {
		currentSymbols[p.Symbol] = true
	}

	for _, sig := range signals {
		// BUY 시그널만 처리
		if sig.Type != strategy.SignalBuy {
			continue
		}

		// 이미 보유중인 종목
		if currentSymbols[sig.Stock.Symbol] {
			rejected = append(rejected, RejectedSignal{
				Signal: sig,
				Reason: "already holding position",
			})
			continue
		}

		// 최대 포지션 수 초과
		if currentPositionCount >= r.config.MaxPositions {
			rejected = append(rejected, RejectedSignal{
				Signal: sig,
				Reason: "max positions reached",
			})
			continue
		}

		// TradeGuide 없음
		if sig.Guide == nil {
			rejected = append(rejected, RejectedSignal{
				Signal: sig,
				Reason: "no trade guide",
			})
			continue
		}

		// 투자금액이 최대 허용 비율 초과
		maxAmount := r.config.TotalCapital * r.config.MaxPositionPct
		if sig.Guide.InvestAmount > maxAmount {
			rejected = append(rejected, RejectedSignal{
				Signal: sig,
				Reason: "exceeds max position size",
			})
			continue
		}

		// 최소 수량 체크
		if sig.Guide.PositionSize < 1 {
			rejected = append(rejected, RejectedSignal{
				Signal: sig,
				Reason: "position size too small",
			})
			continue
		}

		approved = append(approved, sig)
		currentPositionCount++
		currentSymbols[sig.Stock.Symbol] = true
	}

	return approved, rejected
}

// CalculateTotalRisk 총 리스크 계산
func (r *RiskManager) CalculateTotalRisk(signals []strategy.Signal) float64 {
	var totalRisk float64
	for _, sig := range signals {
		if sig.Guide != nil {
			totalRisk += sig.Guide.RiskAmount
		}
	}
	return totalRisk
}

// CalculateTotalInvestment 총 투자금액 계산
func (r *RiskManager) CalculateTotalInvestment(signals []strategy.Signal) float64 {
	var totalInvest float64
	for _, sig := range signals {
		if sig.Guide != nil {
			totalInvest += sig.Guide.InvestAmount
		}
	}
	return totalInvest
}
