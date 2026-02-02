package broker

import (
	"context"
	"time"
)

// OrderType 주문 유형
type OrderType string

const (
	OrderTypeMarket OrderType = "market"
	OrderTypeLimit  OrderType = "limit"
)

// OrderSide 매수/매도
type OrderSide string

const (
	OrderSideBuy  OrderSide = "buy"
	OrderSideSell OrderSide = "sell"
)

// Order 주문 요청
type Order struct {
	Symbol     string
	Side       OrderSide
	Type       OrderType
	Quantity   int
	LimitPrice float64 // limit 주문시 가격
	StopPrice  float64 // stop loss 가격 (참고용)
}

// OrderResult 주문 결과
type OrderResult struct {
	OrderID     string
	Symbol      string
	Side        OrderSide
	Type        OrderType
	Quantity    int
	FilledQty   int
	AvgPrice    float64
	Status      string // submitted, filled, partial, rejected, cancelled
	Message     string
	SubmittedAt time.Time
	FilledAt    time.Time
}

// Position 보유 포지션
type Position struct {
	Symbol        string
	Quantity      int
	AvgCost       float64
	CurrentPrice  float64
	MarketValue   float64
	UnrealizedPnL float64
	UnrealizedPct float64
}

// AccountBalance 계좌 잔고
type AccountBalance struct {
	Currency    string
	CashBalance float64
	BuyingPower float64
	TotalEquity float64
	Positions   []Position
}

// PendingOrder 미체결 주문
type PendingOrder struct {
	OrderID   string
	Symbol    string
	Side      OrderSide
	Type      OrderType
	Quantity  int
	FilledQty int
	Price     float64
	Status    string
	CreatedAt time.Time
}

// Broker 브로커 인터페이스
type Broker interface {
	// Name 브로커 이름
	Name() string

	// IsReady 연결 및 인증 상태 확인
	IsReady() bool

	// 주문 관련
	PlaceOrder(ctx context.Context, order Order) (*OrderResult, error)
	CancelOrder(ctx context.Context, orderID string) error
	GetOrder(ctx context.Context, orderID string) (*OrderResult, error)

	// 조회 관련
	GetBalance(ctx context.Context) (*AccountBalance, error)
	GetPositions(ctx context.Context) ([]Position, error)
	GetPendingOrders(ctx context.Context) ([]PendingOrder, error)

	// 시세
	GetQuote(ctx context.Context, symbol string) (float64, error)
}
