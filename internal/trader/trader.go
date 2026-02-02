package trader

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"traveler/internal/broker"
	"traveler/internal/strategy"
)

// Config 자동 매매 설정
type Config struct {
	DryRun          bool          // 모의 실행 (실제 주문 안함)
	MaxPositions    int           // 최대 동시 포지션 수
	MaxPositionPct  float64       // 종목당 최대 투자 비율 (예: 0.2 = 20%)
	TotalCapital    float64       // 총 투자 자본
	RiskPerTrade    float64       // 거래당 리스크 비율 (예: 0.01 = 1%)
	MonitorInterval time.Duration // 포지션 모니터링 주기
}

// DefaultConfig 기본 설정
func DefaultConfig() Config {
	return Config{
		DryRun:          true, // 안전을 위해 기본 dry-run
		MaxPositions:    5,
		MaxPositionPct:  0.20,
		TotalCapital:    10000,
		RiskPerTrade:    0.01,
		MonitorInterval: 30 * time.Second,
	}
}

// AutoTrader 자동 매매 오케스트레이터
type AutoTrader struct {
	config   Config
	broker   broker.Broker
	executor *Executor
	monitor  *Monitor
	risk     *RiskManager

	mu         sync.RWMutex
	isRunning  bool
	stopChan   chan struct{}
}

// NewAutoTrader 생성자
func NewAutoTrader(cfg Config, b broker.Broker, marketOrder bool) *AutoTrader {
	executor := NewExecutor(b, cfg, marketOrder)

	return &AutoTrader{
		config:   cfg,
		broker:   b,
		executor: executor,
		monitor:  NewMonitor(b, executor, cfg),
		risk:     NewRiskManager(cfg),
		stopChan: make(chan struct{}),
	}
}

// ExecuteSignals Signal 목록을 받아 주문 실행
func (t *AutoTrader) ExecuteSignals(ctx context.Context, signals []strategy.Signal) ([]ExecutionResult, error) {
	// 1. 현재 포지션 확인
	positions, err := t.broker.GetPositions(ctx)
	if err != nil {
		// dry-run 모드에서는 빈 포지션으로 진행
		if t.config.DryRun {
			positions = []broker.Position{}
		} else {
			return nil, fmt.Errorf("get positions: %w", err)
		}
	}

	// 2. 리스크 검증
	approved, rejected := t.risk.ValidateSignals(signals, positions)

	if len(rejected) > 0 {
		log.Printf("[RISK] %d signals rejected:", len(rejected))
		for _, r := range rejected {
			log.Printf("  - %s: %s", r.Signal.Stock.Symbol, r.Reason)
		}
	}

	if len(approved) == 0 {
		log.Println("[TRADER] No signals approved for execution")
		return nil, nil
	}

	// 3. 투자 요약
	totalInvest := t.risk.CalculateTotalInvestment(approved)
	totalRisk := t.risk.CalculateTotalRisk(approved)
	log.Printf("[TRADER] Executing %d orders: invest=$%.2f, risk=$%.2f (%.2f%%)",
		len(approved), totalInvest, totalRisk, totalRisk/t.config.TotalCapital*100)

	// 4. 주문 실행
	results := make([]ExecutionResult, 0, len(approved))
	for _, sig := range approved {
		result := t.executor.Execute(ctx, sig)
		results = append(results, result)

		if result.Success {
			log.Printf("[EXECUTED] %s: BUY %d shares @ $%.2f",
				sig.Stock.Symbol, result.Order.Quantity, result.Order.LimitPrice)

			// 모니터링 등록
			if sig.Guide != nil {
				t.monitor.RegisterPosition(
					sig.Stock.Symbol,
					sig.Guide.PositionSize,
					sig.Guide.EntryPrice,
					sig.Guide.StopLoss,
					sig.Guide.Target1,
					sig.Guide.Target2,
				)
			}
		} else {
			log.Printf("[FAILED] %s: %s", sig.Stock.Symbol, result.Error)
		}
	}

	return results, nil
}

// StartMonitoring 포지션 모니터링 시작 (백그라운드)
func (t *AutoTrader) StartMonitoring(ctx context.Context) {
	t.mu.Lock()
	if t.isRunning {
		t.mu.Unlock()
		return
	}
	t.isRunning = true
	t.stopChan = make(chan struct{})
	t.mu.Unlock()

	log.Printf("[MONITOR] Starting position monitor (interval: %s)", t.config.MonitorInterval)

	ticker := time.NewTicker(t.config.MonitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[MONITOR] Context cancelled, stopping")
			t.setRunning(false)
			return
		case <-t.stopChan:
			log.Println("[MONITOR] Stop signal received")
			t.setRunning(false)
			return
		case <-ticker.C:
			t.monitor.CheckPositions(ctx)
		}
	}
}

// StopMonitoring 모니터링 중지
func (t *AutoTrader) StopMonitoring() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.isRunning {
		close(t.stopChan)
		t.isRunning = false
	}
}

// IsRunning 모니터링 실행 중 여부
func (t *AutoTrader) IsRunning() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.isRunning
}

func (t *AutoTrader) setRunning(running bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.isRunning = running
}

// GetMonitor Monitor 인스턴스 반환
func (t *AutoTrader) GetMonitor() *Monitor {
	return t.monitor
}

// GetConfig 설정 반환
func (t *AutoTrader) GetConfig() Config {
	return t.config
}
