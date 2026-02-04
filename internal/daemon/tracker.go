package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DailyConfig 일일 거래 설정
type DailyConfig struct {
	TargetPct     float64 // 일일 목표 수익률 (예: 1.0 = 1%)
	LossLimitPct  float64 // 일일 최대 손실 (예: -2.0 = -2%)
	MaxTrades     int     // 일일 최대 거래 횟수
}

// DefaultDailyConfig 기본 설정
func DefaultDailyConfig() DailyConfig {
	return DailyConfig{
		TargetPct:    1.0,  // 1% 목표
		LossLimitPct: -2.0, // -2% 손절
		MaxTrades:    10,   // 최대 10회
	}
}

// TradeLog 거래 로그
type TradeLog struct {
	Timestamp   time.Time `json:"timestamp"`
	Symbol      string    `json:"symbol"`
	Side        string    `json:"side"` // "BUY" or "SELL"
	Quantity    int       `json:"quantity"`
	Price       float64   `json:"price"`
	Amount      float64   `json:"amount"`
	Commission  float64   `json:"commission"` // 수수료
	OrderID     string    `json:"order_id,omitempty"`
	Reason      string    `json:"reason,omitempty"` // "signal", "stop_loss", "take_profit", "manual"
}

// CommissionRate 수수료율 (KIS 해외주식 기본 0.25%)
const CommissionRate = 0.0025

// DailyState 일일 상태
type DailyState struct {
	Date            string      `json:"date"`
	StartingBalance float64     `json:"starting_balance"`
	CurrentBalance  float64     `json:"current_balance"`
	RealizedPnL     float64     `json:"realized_pnl"`
	UnrealizedPnL   float64     `json:"unrealized_pnl"`
	TotalCommission float64     `json:"total_commission"` // 총 수수료
	TotalPnL        float64     `json:"total_pnl"`        // 수수료 차감 후
	TotalPnLPct     float64     `json:"total_pnl_pct"`
	TradeCount      int         `json:"trade_count"`
	WinCount        int         `json:"win_count"`
	LossCount       int         `json:"loss_count"`
	Trades          []TradeLog  `json:"trades"`
	Status          string      `json:"status"` // "running", "target_reached", "loss_limit", "market_closed", "error"
	StartTime       time.Time   `json:"start_time"`
	EndTime         time.Time   `json:"end_time,omitempty"`
}

// DailyTracker 일일 P&L 추적기
type DailyTracker struct {
	config   DailyConfig
	state    DailyState
	dataDir  string
	mu       sync.RWMutex
}

// NewDailyTracker 생성자
func NewDailyTracker(cfg DailyConfig, dataDir string) *DailyTracker {
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".traveler")
	}
	os.MkdirAll(dataDir, 0755)

	return &DailyTracker{
		config:  cfg,
		dataDir: dataDir,
	}
}

// Start 새로운 거래일 시작
func (t *DailyTracker) Start(startingBalance float64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	today := time.Now().Format("2006-01-02")

	// 이미 오늘 데이터가 있는지 확인
	existing, err := t.loadState(today)
	if err == nil && existing != nil {
		// 기존 상태 복원
		t.state = *existing
		t.state.CurrentBalance = startingBalance // 현재 잔고 업데이트
		return nil
	}

	// 새로운 상태 시작
	t.state = DailyState{
		Date:            today,
		StartingBalance: startingBalance,
		CurrentBalance:  startingBalance,
		Trades:          make([]TradeLog, 0),
		Status:          "running",
		StartTime:       time.Now(),
	}

	return t.saveState()
}

// EnsureDate 날짜가 없으면 오늘 날짜로 설정
func (t *DailyTracker) EnsureDate() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.state.Date == "" {
		t.state.Date = time.Now().Format("2006-01-02")
		t.state.StartTime = time.Now()
	}
}

// RecordTrade 거래 기록
func (t *DailyTracker) RecordTrade(log TradeLog) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	log.Timestamp = time.Now()

	// 수수료 계산 (설정 안 됐으면 자동 계산)
	if log.Commission == 0 {
		log.Commission = log.Amount * CommissionRate
	}

	t.state.Trades = append(t.state.Trades, log)
	t.state.TradeCount++
	t.state.TotalCommission += log.Commission

	return t.saveState()
}

// UpdatePnL P&L 업데이트
func (t *DailyTracker) UpdatePnL(realizedPnL, unrealizedPnL, currentBalance float64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.state.RealizedPnL = realizedPnL
	t.state.UnrealizedPnL = unrealizedPnL
	t.state.CurrentBalance = currentBalance

	// 수수료 차감한 순 P&L
	t.state.TotalPnL = realizedPnL + unrealizedPnL - t.state.TotalCommission

	if t.state.StartingBalance > 0 {
		t.state.TotalPnLPct = (t.state.TotalPnL / t.state.StartingBalance) * 100
	}

	t.saveState()
}

// CheckTargets 목표/한도 체크
type TargetCheckResult struct {
	TargetReached   bool
	LossLimitHit    bool
	MaxTradesHit    bool
	CurrentPnLPct   float64
	ShouldStop      bool
	Reason          string
}

func (t *DailyTracker) CheckTargets() TargetCheckResult {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := TargetCheckResult{
		CurrentPnLPct: t.state.TotalPnLPct,
	}

	// 목표 수익 달성
	if t.state.TotalPnLPct >= t.config.TargetPct {
		result.TargetReached = true
		result.ShouldStop = true
		result.Reason = fmt.Sprintf("target reached: %.2f%% >= %.2f%%", t.state.TotalPnLPct, t.config.TargetPct)
	}

	// 최대 손실 도달
	if t.state.TotalPnLPct <= t.config.LossLimitPct {
		result.LossLimitHit = true
		result.ShouldStop = true
		result.Reason = fmt.Sprintf("loss limit hit: %.2f%% <= %.2f%%", t.state.TotalPnLPct, t.config.LossLimitPct)
	}

	// 최대 거래 횟수
	if t.state.TradeCount >= t.config.MaxTrades {
		result.MaxTradesHit = true
		result.ShouldStop = true
		result.Reason = fmt.Sprintf("max trades reached: %d >= %d", t.state.TradeCount, t.config.MaxTrades)
	}

	return result
}

// SetStatus 상태 설정
func (t *DailyTracker) SetStatus(status string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.state.Status = status
	if status != "running" {
		t.state.EndTime = time.Now()
	}
	t.saveState()
}

// GetState 현재 상태 조회
func (t *DailyTracker) GetState() DailyState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state
}

// GetConfig 설정 조회
func (t *DailyTracker) GetConfig() DailyConfig {
	return t.config
}

// 상태 파일 경로
func (t *DailyTracker) stateFilePath(date string) string {
	return filepath.Join(t.dataDir, fmt.Sprintf("daily_%s.json", date))
}

// 상태 저장
func (t *DailyTracker) saveState() error {
	data, err := json.MarshalIndent(t.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.stateFilePath(t.state.Date), data, 0644)
}

// 상태 로드
func (t *DailyTracker) loadState(date string) (*DailyState, error) {
	data, err := os.ReadFile(t.stateFilePath(date))
	if err != nil {
		return nil, err
	}

	var state DailyState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}

	return &state, nil
}

// GenerateReport 일일 리포트 생성
func (t *DailyTracker) GenerateReport() string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	s := t.state

	report := fmt.Sprintf(`
================================================================================
                         DAILY TRADING REPORT
                         %s
================================================================================

SUMMARY
-------
  Status:           %s
  Starting Balance: $%.2f
  Current Balance:  $%.2f
  Realized P&L:     $%.2f
  Unrealized P&L:   $%.2f
  Commission:       $%.2f (%.2f%%)
  Net P&L:          $%.2f (%.2f%%)

STATISTICS
----------
  Total Trades:     %d
  Winning Trades:   %d
  Losing Trades:    %d
  Win Rate:         %.1f%%

TIME
----
  Start:            %s
  End:              %s
  Duration:         %s

`, s.Date, s.Status,
		s.StartingBalance, s.CurrentBalance,
		s.RealizedPnL, s.UnrealizedPnL,
		s.TotalCommission, commissionPct(s.TotalCommission, s.StartingBalance),
		s.TotalPnL, s.TotalPnLPct,
		s.TradeCount, s.WinCount, s.LossCount,
		winRate(s.WinCount, s.LossCount),
		s.StartTime.Format("15:04:05"),
		formatEndTime(s.EndTime),
		formatDuration(s.StartTime, s.EndTime))

	if len(s.Trades) > 0 {
		report += "TRADES\n------\n"
		for i, trade := range s.Trades {
			report += fmt.Sprintf("  %d. [%s] %s %s x%d @ $%.2f = $%.2f (%s)\n",
				i+1,
				trade.Timestamp.Format("15:04:05"),
				trade.Side,
				trade.Symbol,
				trade.Quantity,
				trade.Price,
				trade.Amount,
				trade.Reason)
		}
	}

	report += "\n================================================================================"

	return report
}

// SaveReport 리포트를 파일로 저장
func (t *DailyTracker) SaveReport() (string, error) {
	report := t.GenerateReport()

	filename := fmt.Sprintf("report_%s.txt", t.state.Date)
	filepath := filepath.Join(t.dataDir, filename)

	if err := os.WriteFile(filepath, []byte(report), 0644); err != nil {
		return "", err
	}

	return filepath, nil
}

func winRate(wins, losses int) float64 {
	total := wins + losses
	if total == 0 {
		return 0
	}
	return float64(wins) / float64(total) * 100
}

func commissionPct(commission, balance float64) float64 {
	if balance == 0 {
		return 0
	}
	return (commission / balance) * 100
}

func formatEndTime(t time.Time) string {
	if t.IsZero() {
		return "(running)"
	}
	return t.Format("15:04:05")
}

func formatDuration(start, end time.Time) string {
	if end.IsZero() {
		end = time.Now()
	}
	d := end.Sub(start)
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", hours, mins)
}
