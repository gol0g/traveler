package trader

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"traveler/internal/broker"
)

// ActivePosition 활성 포지션 (진입 정보 포함)
type ActivePosition struct {
	Symbol        string
	Quantity      float64
	EntryPrice    float64
	StopLoss      float64
	Target1       float64
	Target2       float64
	EntryTime     time.Time
	Target1Hit    bool   // Target1 도달 여부
	Strategy      string // 전략 이름
	MaxHoldDays   int    // 최대 보유 거래일
	Intraday      bool   // 장중 매매 포지션 (장 마감 전 강제 청산)
	sellFailCount int    // 매도 실패 횟수 (무한 재시도 방지)
}

// SellCallback 매도 발생 시 호출되는 콜백 (invested, sold 금액)
type SellCallback func(investedAmount, sellAmount float64)

// Monitor 포지션 모니터링
type Monitor struct {
	broker       broker.Broker
	executor     *Executor
	config       Config
	planStore    *PlanStore
	history      *TradeHistory
	market       string // "us" or "kr"
	onSell       SellCallback

	mu        sync.RWMutex
	positions map[string]*ActivePosition
}

// NewMonitor 생성자
func NewMonitor(b broker.Broker, executor *Executor, cfg Config, planStore *PlanStore) *Monitor {
	return &Monitor{
		broker:    b,
		executor:  executor,
		config:    cfg,
		planStore: planStore,
		positions: make(map[string]*ActivePosition),
	}
}

// SetTradeHistory 매매 기록 저장소 설정
func (m *Monitor) SetTradeHistory(h *TradeHistory, market string) {
	m.history = h
	m.market = market
}

// SetOnSell 매도 콜백 설정 (자본 추적용)
func (m *Monitor) SetOnSell(cb SellCallback) {
	m.onSell = cb
}

// RegisterPosition 포지션 등록 (진입시 호출)
func (m *Monitor) RegisterPosition(symbol string, quantity float64, entryPrice, stopLoss, target1, target2 float64) {
	m.RegisterPositionWithPlan(symbol, quantity, entryPrice, stopLoss, target1, target2, "", 0, time.Time{})
}

// RegisterPositionWithPlan 전략 정보 포함 포지션 등록
func (m *Monitor) RegisterPositionWithPlan(symbol string, quantity float64, entryPrice, stopLoss, target1, target2 float64, strategy string, maxHoldDays int, entryTime time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if entryTime.IsZero() {
		entryTime = time.Now()
	}
	if maxHoldDays == 0 && strategy != "" {
		maxHoldDays = GetMaxHoldDays(strategy)
	}

	m.positions[symbol] = &ActivePosition{
		Symbol:      symbol,
		Quantity:    quantity,
		EntryPrice:  entryPrice,
		StopLoss:    stopLoss,
		Target1:     target1,
		Target2:     target2,
		EntryTime:   entryTime,
		Target1Hit:  false,
		Strategy:    strategy,
		MaxHoldDays: maxHoldDays,
	}

	log.Printf("[MONITOR] Registered %s: strategy=%s, entry=$%.2f, stop=$%.2f, T1=$%.2f, T2=$%.2f, maxDays=%d",
		symbol, strategy, entryPrice, stopLoss, target1, target2, maxHoldDays)
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

// getActualQuantity 브로커에서 실제 보유 수량을 조회 (매도 수량 보정용)
// 크립토 시장가 매수 시 수수료 차감으로 계획 수량보다 적게 보유할 수 있음
func (m *Monitor) getActualQuantity(ctx context.Context, symbol string, plannedQty float64) float64 {
	positions, err := m.broker.GetPositions(ctx)
	if err != nil {
		return plannedQty // 조회 실패 시 원래 수량 사용
	}
	for _, p := range positions {
		if p.Symbol == symbol {
			if p.Quantity < plannedQty {
				log.Printf("[MONITOR] Adjusted sell qty for %s: %.8f → %.8f (broker actual, fee-adjusted)",
					symbol, plannedQty, p.Quantity)
			}
			return p.Quantity
		}
	}
	// 브로커에 포지션이 없음 (이미 매도됨 등)
	log.Printf("[MONITOR] Position %s not found in broker, removing from monitor", symbol)
	return 0
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

		// 현재가 0이면 조회 실패 - 스킵
		if currentPrice <= 0 {
			log.Printf("[MONITOR] Invalid price for %s: $%.2f, skipping", symbol, currentPrice)
			continue
		}

		// 매도 실패가 반복되면 스킵 (sellFailCount 체크)
		if active.sellFailCount >= 3 {
			// 실제 잔고로 수량 보정 시도 (3회 실패 후 1회)
			if active.sellFailCount%10 == 3 {
				actualQty := m.getActualQuantity(ctx, symbol, active.Quantity)
				if actualQty <= 0 {
					// 브로커에 포지션 없음 → 정리
					m.UnregisterPosition(symbol)
					if m.planStore != nil {
						m.planStore.Delete(symbol)
					}
					log.Printf("[MONITOR] %s: position gone from broker, cleaned up", symbol)
					continue
				}
				m.mu.Lock()
				if pos, ok := m.positions[symbol]; ok {
					pos.Quantity = actualQty
					pos.sellFailCount = 0 // 보정 후 재시도
				}
				m.mu.Unlock()
				log.Printf("[MONITOR] %s: quantity corrected to %.8f, retrying sell", symbol, actualQty)
			}
			continue
		}

		// 손절 체크
		if currentPrice <= active.StopLoss {
			log.Printf("[STOP LOSS] %s hit stop at $%.2f (current: $%.2f)",
				symbol, active.StopLoss, currentPrice)
			m.executeSell(ctx, symbol, active.Quantity, "stop_loss", currentPrice)
			continue
		}

		// Target2 완전 청산 (Target1 이후)
		if active.Target1Hit && currentPrice >= active.Target2 {
			log.Printf("[TARGET2] %s hit target2 at $%.2f - closing position",
				symbol, active.Target2)
			m.executeSell(ctx, symbol, active.Quantity, "target2", currentPrice)
			continue
		}

		// Target1 도달 - 절반 청산 (또는 1주면 stop을 본전으로 이동)
		if !active.Target1Hit && currentPrice >= active.Target1 {
			if active.Quantity > 1 {
				// 2주 이상: 절반 매도
				halfQty := math.Floor(active.Quantity / 2)
				log.Printf("[TARGET1] %s hit target1 at $%.2f - selling %.0f shares",
					symbol, active.Target1, halfQty)

				if _, err := m.executor.ExecuteSell(ctx, symbol, halfQty, "target1"); err != nil {
					log.Printf("[MONITOR] Error selling %s: %v", symbol, err)
					m.recordSellFailure(symbol)
					continue
				}

				// Target1 매도 기록
				if m.history != nil {
					pnl := halfQty * (currentPrice - active.EntryPrice)
					pnlPct := 0.0
					if active.EntryPrice > 0 {
						pnlPct = (currentPrice - active.EntryPrice) / active.EntryPrice * 100
					}
					m.history.Append(TradeRecord{
						Market:     m.market,
						Symbol:     symbol,
						Side:       "sell",
						Quantity:   halfQty,
						Price:      currentPrice,
						Strategy:   active.Strategy,
						Reason:     "target1",
						EntryPrice: active.EntryPrice,
						PnL:        pnl,
						PnLPct:     pnlPct,
					})
				}
				if m.onSell != nil {
					m.onSell(halfQty*active.EntryPrice, halfQty*currentPrice)
				}

				// 상태 업데이트
				m.mu.Lock()
				if pos, ok := m.positions[symbol]; ok {
					pos.Target1Hit = true
					pos.Quantity -= halfQty
					pos.StopLoss = pos.EntryPrice // 손절가를 본전으로 이동
					log.Printf("[MONITOR] %s: moved stop to breakeven ($%.2f), remaining %.0f shares",
						symbol, pos.StopLoss, pos.Quantity)
				}
				m.mu.Unlock()

				// PlanStore 업데이트
				if m.planStore != nil {
					remaining := active.Quantity - halfQty
					m.planStore.UpdateTarget1Hit(symbol, remaining, active.EntryPrice)
				}
			} else {
				// 1주 이하 (크립토 소수점 포함): T1에서 전량 매도
				sellQty := m.getActualQuantity(ctx, symbol, active.Quantity)
				if sellQty <= 0 {
					m.UnregisterPosition(symbol)
					if m.planStore != nil {
						m.planStore.Delete(symbol)
					}
					continue
				}

				log.Printf("[TARGET1] %s hit target1 at $%.2f - selling all (qty=%.8f)",
					symbol, active.Target1, sellQty)

				if _, err := m.executor.ExecuteSell(ctx, symbol, sellQty, "target1"); err != nil {
					log.Printf("[MONITOR] Error selling %s: %v", symbol, err)
					m.recordSellFailure(symbol)
					continue
				}

				if m.history != nil {
					pnl := sellQty * (currentPrice - active.EntryPrice)
					pnlPct := 0.0
					if active.EntryPrice > 0 {
						pnlPct = (currentPrice - active.EntryPrice) / active.EntryPrice * 100
					}
					m.history.Append(TradeRecord{
						Market:     m.market,
						Symbol:     symbol,
						Side:       "sell",
						Quantity:   sellQty,
						Price:      currentPrice,
						Strategy:   active.Strategy,
						Reason:     "target1",
						EntryPrice: active.EntryPrice,
						PnL:        pnl,
						PnLPct:     pnlPct,
					})
				}

				if m.onSell != nil {
					m.onSell(sellQty*active.EntryPrice, sellQty*currentPrice)
				}

				m.mu.Lock()
				delete(m.positions, symbol)
				m.mu.Unlock()

				if m.planStore != nil {
					m.planStore.Delete(symbol)
				}
			}
			continue
		}

		// Time stop: 최대 보유일 초과
		if active.MaxHoldDays > 0 && !active.EntryTime.IsZero() {
			// 크립토는 주말 포함 달력일 기준, 주식은 거래일 기준
			var heldDays int
			if m.market == "crypto" {
				heldDays = CalendarDaysSince(active.EntryTime)
			} else {
				heldDays = TradingDaysSince(active.EntryTime)
			}
			if heldDays >= active.MaxHoldDays {
				pnlPct := (currentPrice - active.EntryPrice) / active.EntryPrice * 100
				reason := fmt.Sprintf("time_stop_%dd (P&L: %.1f%%)", heldDays, pnlPct)
				log.Printf("[TIME STOP] %s held %d days (max %d), current=$%.2f, P&L=%.1f%% - closing",
					symbol, heldDays, active.MaxHoldDays, currentPrice, pnlPct)
				m.executeSell(ctx, symbol, active.Quantity, reason, currentPrice)
				continue
			}
		}
	}
}

// recordSellFailure 매도 실패 카운트 증가
func (m *Monitor) recordSellFailure(symbol string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pos, ok := m.positions[symbol]; ok {
		pos.sellFailCount++
	}
}

// executeSell 전량 매도 (exitPrice: 매도 트리거 시점의 현재가)
func (m *Monitor) executeSell(ctx context.Context, symbol string, quantity float64, reason string, exitPrice float64) {
	// 매도 전에 포지션 정보 캡처 (history 기록용)
	m.mu.RLock()
	active, hasActive := m.positions[symbol]
	m.mu.RUnlock()

	// 실제 보유 수량 확인 (크립토 수수료 차감 등으로 계획 수량과 다를 수 있음)
	sellQty := m.getActualQuantity(ctx, symbol, quantity)
	if sellQty <= 0 {
		log.Printf("[MONITOR] %s: no position in broker, cleaning up", symbol)
		m.UnregisterPosition(symbol)
		if m.planStore != nil {
			m.planStore.Delete(symbol)
		}
		return
	}

	_, err := m.executor.ExecuteSell(ctx, symbol, sellQty, reason)
	if err != nil {
		log.Printf("[MONITOR] Error selling %s: %v", symbol, err)
		m.recordSellFailure(symbol)
		return
	}

	// 매매 기록 저장
	if m.history != nil && hasActive {
		pnl := sellQty * (exitPrice - active.EntryPrice)
		pnlPct := 0.0
		if active.EntryPrice > 0 {
			pnlPct = (exitPrice - active.EntryPrice) / active.EntryPrice * 100
		}
		m.history.Append(TradeRecord{
			Market:     m.market,
			Symbol:     symbol,
			Side:       "sell",
			Quantity:   sellQty,
			Price:      exitPrice,
			Strategy:   active.Strategy,
			Reason:     reason,
			EntryPrice: active.EntryPrice,
			PnL:        pnl,
			PnLPct:     pnlPct,
		})
	}

	// 자본 추적 콜백
	if m.onSell != nil && hasActive {
		investedAmount := sellQty * active.EntryPrice
		sellAmount := sellQty * exitPrice
		m.onSell(investedAmount, sellAmount)
	}

	m.UnregisterPosition(symbol)

	// PlanStore에서 삭제
	if m.planStore != nil {
		m.planStore.Delete(symbol)
	}

	log.Printf("[MONITOR] Closed position %s (%s)", symbol, reason)
}

// ClosePosition 외부에서 호출 가능한 포지션 청산 (전략 무효화 등)
func (m *Monitor) ClosePosition(ctx context.Context, symbol string, reason string) error {
	m.mu.RLock()
	active, ok := m.positions[symbol]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("position %s not found in monitor", symbol)
	}

	// 현재가 조회 (매도 기록용)
	exitPrice, err := m.broker.GetQuote(ctx, symbol)
	if err != nil || exitPrice <= 0 {
		exitPrice = active.EntryPrice // fallback
	}

	// executeSell이 내부에서 실제 수량 조회함
	m.executeSell(ctx, symbol, active.Quantity, reason, exitPrice)
	return nil
}

// ForceCloseIntraday 장중 포지션 전량 강제 청산
func (m *Monitor) ForceCloseIntraday(ctx context.Context) {
	m.mu.RLock()
	var intradaySymbols []string
	for symbol, pos := range m.positions {
		if pos.Intraday {
			intradaySymbols = append(intradaySymbols, symbol)
		}
	}
	m.mu.RUnlock()

	for _, symbol := range intradaySymbols {
		log.Printf("[MONITOR] Force closing intraday position: %s", symbol)
		m.ClosePosition(ctx, symbol, "intraday_force_close")
	}
}

// GetIntradayPositions 장중 포지션만 반환
func (m *Monitor) GetIntradayPositions() []*ActivePosition {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*ActivePosition
	for _, p := range m.positions {
		if p.Intraday {
			result = append(result, p)
		}
	}
	return result
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
			// PlanStore에서도 삭제
			if m.planStore != nil {
				m.planStore.Delete(symbol)
			}
		}
	}

	return nil
}
