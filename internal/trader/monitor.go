package trader

import (
	"context"
	"log"
	"sync"
	"time"

	"traveler/internal/broker"
)

// ActivePosition 활성 포지션 (진입 정보 포함)
type ActivePosition struct {
	Symbol     string
	Quantity   int
	EntryPrice float64
	StopLoss   float64
	Target1    float64
	Target2    float64
	EntryTime  time.Time
	Target1Hit bool // Target1 도달 여부
}

// Monitor 포지션 모니터링
type Monitor struct {
	broker   broker.Broker
	executor *Executor
	config   Config

	mu        sync.RWMutex
	positions map[string]*ActivePosition
}

// NewMonitor 생성자
func NewMonitor(b broker.Broker, executor *Executor, cfg Config) *Monitor {
	return &Monitor{
		broker:    b,
		executor:  executor,
		config:    cfg,
		positions: make(map[string]*ActivePosition),
	}
}

// RegisterPosition 포지션 등록 (진입시 호출)
func (m *Monitor) RegisterPosition(symbol string, quantity int, entryPrice, stopLoss, target1, target2 float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.positions[symbol] = &ActivePosition{
		Symbol:     symbol,
		Quantity:   quantity,
		EntryPrice: entryPrice,
		StopLoss:   stopLoss,
		Target1:    target1,
		Target2:    target2,
		EntryTime:  time.Now(),
		Target1Hit: false,
	}

	log.Printf("[MONITOR] Registered %s: entry=$%.2f, stop=$%.2f, T1=$%.2f, T2=$%.2f",
		symbol, entryPrice, stopLoss, target1, target2)
}

// UnregisterPosition 포지션 등록 해제
func (m *Monitor) UnregisterPosition(symbol string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.positions, symbol)
}

// GetActivePositions 활성 포지션 목록 반환
func (m *Monitor) GetActivePositions() []*ActivePosition {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*ActivePosition, 0, len(m.positions))
	for _, p := range m.positions {
		result = append(result, p)
	}
	return result
}

// CheckPositions 모든 포지션 체크 및 청산 조건 확인
func (m *Monitor) CheckPositions(ctx context.Context) {
	m.mu.Lock()
	positionsCopy := make(map[string]*ActivePosition)
	for k, v := range m.positions {
		positionsCopy[k] = v
	}
	m.mu.Unlock()

	for symbol, active := range positionsCopy {
		// 현재가 조회
		currentPrice, err := m.broker.GetQuote(ctx, symbol)
		if err != nil {
			log.Printf("[MONITOR] Error getting quote for %s: %v", symbol, err)
			continue
		}

		// 손절 체크
		if currentPrice <= active.StopLoss {
			log.Printf("[STOP LOSS] %s hit stop at $%.2f (current: $%.2f)",
				symbol, active.StopLoss, currentPrice)
			m.executeSell(ctx, symbol, active.Quantity, "stop_loss")
			continue
		}

		// Target2 완전 청산 (Target1 이후)
		if active.Target1Hit && currentPrice >= active.Target2 {
			log.Printf("[TARGET2] %s hit target2 at $%.2f - closing position",
				symbol, active.Target2)
			m.executeSell(ctx, symbol, active.Quantity, "target2")
			continue
		}

		// Target1 도달 - 절반 청산
		if !active.Target1Hit && currentPrice >= active.Target1 && active.Quantity > 1 {
			halfQty := active.Quantity / 2
			log.Printf("[TARGET1] %s hit target1 at $%.2f - selling %d shares",
				symbol, active.Target1, halfQty)

			if _, err := m.executor.ExecuteSell(ctx, symbol, halfQty, "target1"); err != nil {
				log.Printf("[MONITOR] Error selling %s: %v", symbol, err)
				continue
			}

			// 상태 업데이트
			m.mu.Lock()
			if pos, ok := m.positions[symbol]; ok {
				pos.Target1Hit = true
				pos.Quantity -= halfQty
				pos.StopLoss = pos.EntryPrice // 손절가를 본전으로 이동
				log.Printf("[MONITOR] %s: moved stop to breakeven ($%.2f), remaining %d shares",
					symbol, pos.StopLoss, pos.Quantity)
			}
			m.mu.Unlock()
		}
	}
}

// executeSell 전량 매도
func (m *Monitor) executeSell(ctx context.Context, symbol string, quantity int, reason string) {
	_, err := m.executor.ExecuteSell(ctx, symbol, quantity, reason)
	if err != nil {
		log.Printf("[MONITOR] Error selling %s: %v", symbol, err)
		return
	}

	m.UnregisterPosition(symbol)
	log.Printf("[MONITOR] Closed position %s (%s)", symbol, reason)
}

// SyncWithBroker 브로커 잔고와 동기화
func (m *Monitor) SyncWithBroker(ctx context.Context) error {
	positions, err := m.broker.GetPositions(ctx)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 브로커에 없는 포지션 제거
	brokerSymbols := make(map[string]bool)
	for _, p := range positions {
		brokerSymbols[p.Symbol] = true
	}

	for symbol := range m.positions {
		if !brokerSymbols[symbol] {
			log.Printf("[MONITOR] Position %s no longer exists in broker, removing", symbol)
			delete(m.positions, symbol)
		}
	}

	return nil
}
