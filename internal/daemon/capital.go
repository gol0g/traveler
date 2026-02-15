package daemon

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// CapitalState 자동매매 전용 자본 상태 (persistent)
type CapitalState struct {
	InitialCapital float64 `json:"initial_capital"` // 최초 설정 금액
	CurrentCapital float64 `json:"current_capital"` // 현재 잔여 자본
	TotalInvested  float64 `json:"total_invested"`  // 현재 투자 중인 금액
	RealizedPnL    float64 `json:"realized_pnl"`    // 누적 실현 손익
	TradeCount     int     `json:"trade_count"`     // 총 매매 횟수
}

// CapitalTracker 자동매매 자본 추적기
type CapitalTracker struct {
	mu       sync.RWMutex
	state    CapitalState
	filepath string
}

// NewCapitalTracker 생성자. 저장 파일이 있으면 로드, 없으면 initialCapital로 초기화.
func NewCapitalTracker(dataDir string, initialCapital float64) *CapitalTracker {
	fp := filepath.Join(dataDir, "crypto_capital.json")
	ct := &CapitalTracker{filepath: fp}

	// 기존 상태 로드 시도
	if data, err := os.ReadFile(fp); err == nil {
		var saved CapitalState
		if json.Unmarshal(data, &saved) == nil && saved.InitialCapital > 0 {
			ct.state = saved
			log.Printf("[CAPITAL] Loaded saved state: initial=₩%.0f, current=₩%.0f, pnl=₩%.0f, trades=%d",
				saved.InitialCapital, saved.CurrentCapital, saved.RealizedPnL, saved.TradeCount)
			return ct
		}
	}

	// 새로 초기화
	ct.state = CapitalState{
		InitialCapital: initialCapital,
		CurrentCapital: initialCapital,
	}
	ct.save()
	log.Printf("[CAPITAL] Initialized new capital tracker: ₩%.0f", initialCapital)
	return ct
}

// GetCurrentCapital 현재 가용 자본 반환
func (ct *CapitalTracker) GetCurrentCapital() float64 {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.state.CurrentCapital
}

// GetState 전체 상태 반환
func (ct *CapitalTracker) GetState() CapitalState {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.state
}

// RecordBuy 매수 기록 — 투자 금액만큼 가용 자본 차감
func (ct *CapitalTracker) RecordBuy(amount float64) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.state.CurrentCapital -= amount
	ct.state.TotalInvested += amount
	ct.state.TradeCount++
	ct.save()
	log.Printf("[CAPITAL] BUY ₩%.0f → available=₩%.0f, invested=₩%.0f",
		amount, ct.state.CurrentCapital, ct.state.TotalInvested)
}

// RecordSell 매도 기록 — 매도 대금 회수 + 손익 반영
func (ct *CapitalTracker) RecordSell(investedAmount, sellAmount float64) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	pnl := sellAmount - investedAmount
	ct.state.CurrentCapital += sellAmount
	ct.state.TotalInvested -= investedAmount
	if ct.state.TotalInvested < 0 {
		ct.state.TotalInvested = 0
	}
	ct.state.RealizedPnL += pnl
	ct.state.TradeCount++
	ct.save()
	log.Printf("[CAPITAL] SELL ₩%.0f (pnl=₩%.0f) → available=₩%.0f, total_pnl=₩%.0f",
		sellAmount, pnl, ct.state.CurrentCapital, ct.state.RealizedPnL)
}

// Reset 자본 초기화 (새 금액으로 리셋)
func (ct *CapitalTracker) Reset(newCapital float64) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.state = CapitalState{
		InitialCapital: newCapital,
		CurrentCapital: newCapital,
	}
	ct.save()
	log.Printf("[CAPITAL] Reset to ₩%.0f", newCapital)
}

func (ct *CapitalTracker) save() {
	data, err := json.MarshalIndent(ct.state, "", "  ")
	if err != nil {
		log.Printf("[CAPITAL] Failed to marshal: %v", err)
		return
	}
	if err := os.WriteFile(ct.filepath, data, 0644); err != nil {
		log.Printf("[CAPITAL] Failed to save: %v", err)
	}
}
