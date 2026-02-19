package trader

import (
	"context"
	"fmt"
	"log"
	"time"

	"traveler/internal/broker"
	"traveler/internal/strategy"
)

// ExecutionResult 실행 결과
type ExecutionResult struct {
	Signal  strategy.Signal
	Order   *broker.Order
	Result  *broker.OrderResult
	Success bool
	Error   string
}

// Executor Signal을 Order로 변환하고 실행
type Executor struct {
	broker      broker.Broker
	config      Config
	marketOrder bool
}

// NewExecutor 생성자
func NewExecutor(b broker.Broker, cfg Config, marketOrder bool) *Executor {
	return &Executor{
		broker:      b,
		config:      cfg,
		marketOrder: marketOrder,
	}
}

// Execute Signal을 주문으로 변환하여 실행
func (e *Executor) Execute(ctx context.Context, signal strategy.Signal) ExecutionResult {
	result := ExecutionResult{Signal: signal}

	// Signal → Order 변환
	order, err := e.signalToOrder(signal)
	if err != nil {
		result.Error = fmt.Sprintf("convert signal: %v", err)
		return result
	}
	result.Order = order

	// Dry-run 모드
	if e.config.DryRun {
		result.Success = true
		result.Result = &broker.OrderResult{
			OrderID:  "DRY-RUN",
			Symbol:   order.Symbol,
			Side:     order.Side,
			Type:     order.Type,
			Quantity: order.Quantity,
			Status:   "simulated",
			Message:  "Dry-run mode - no actual order placed",
		}
		if e.marketOrder {
			log.Printf("[DRY-RUN] %s %s MARKET ₩%.0f",
				order.Side, order.Symbol, order.Amount)
		} else {
			log.Printf("[DRY-RUN] %s %s %.0f shares @ $%.2f",
				order.Side, order.Symbol, order.Quantity, order.LimitPrice)
		}
		return result
	}

	// 실제 주문 실행
	orderResult, err := e.broker.PlaceOrder(ctx, *order)
	if err != nil {
		result.Error = fmt.Sprintf("place order: %v", err)
		return result
	}

	result.Result = orderResult
	result.Success = orderResult.Status != "rejected"

	// 매수 성공 시: 실제 체결가 조회
	// KIS는 PlaceOrder에서 체결가를 안 줌 (AvgPrice=0) → GetPositions로 조회
	// Upbit는 PlaceOrder 응답에 AvgPrice가 있으므로 스킵
	if result.Success && order.Side == broker.OrderSideBuy && orderResult.AvgPrice == 0 {
		time.Sleep(3 * time.Second) // 체결 대기
		positions, posErr := e.broker.GetPositions(ctx)
		if posErr == nil {
			for _, p := range positions {
				if p.Symbol == order.Symbol && p.AvgCost > 0 {
					orderResult.AvgPrice = p.AvgCost
					orderResult.FilledQty = p.Quantity
					orderResult.Status = "filled"
					log.Printf("[EXECUTOR] %s actual fill: $%.2f (order: $%.2f)",
						order.Symbol, p.AvgCost, order.LimitPrice)
					break
				}
			}
		}
	}

	return result
}

// ExecuteSell 매도 주문 실행
func (e *Executor) ExecuteSell(ctx context.Context, symbol string, quantity float64, reason string) (*broker.OrderResult, error) {
	order := broker.Order{
		Symbol:   symbol,
		Side:     broker.OrderSideSell,
		Type:     broker.OrderTypeMarket, // 매도는 항상 시장가
		Quantity: quantity,
	}

	if e.config.DryRun {
		log.Printf("[DRY-RUN] SELL %s %.0f shares (%s)", symbol, quantity, reason)
		return &broker.OrderResult{
			OrderID:  "DRY-RUN",
			Symbol:   symbol,
			Side:     broker.OrderSideSell,
			Type:     broker.OrderTypeMarket,
			Quantity: quantity,
			Status:   "simulated",
			Message:  fmt.Sprintf("Dry-run sell (%s)", reason),
		}, nil
	}

	return e.broker.PlaceOrder(ctx, order)
}

// signalToOrder Signal을 Order로 변환
func (e *Executor) signalToOrder(signal strategy.Signal) (*broker.Order, error) {
	if signal.Guide == nil {
		return nil, fmt.Errorf("signal has no trade guide")
	}

	guide := signal.Guide

	// 주문 유형 결정
	orderType := broker.OrderTypeLimit
	if e.marketOrder {
		orderType = broker.OrderTypeMarket
	}

	order := &broker.Order{
		Symbol:     signal.Stock.Symbol,
		Side:       broker.OrderSideBuy,
		Type:       orderType,
		Quantity:   guide.PositionSize,
		LimitPrice: guide.EntryPrice,
		StopPrice:  guide.StopLoss,
	}

	// 시장가 매수: KRW 투자금액 설정 (Upbit는 Amount 기반)
	if e.marketOrder {
		order.Amount = guide.PositionSize * guide.EntryPrice
	}

	return order, nil
}
