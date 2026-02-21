package trader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultCommissionRate 기본 수수료율 (0.25% — US stocks)
const DefaultCommissionRate = 0.0025

// CommissionRateByMarket 마켓별 수수료율 (편도)
func CommissionRateByMarket(market string) float64 {
	switch market {
	case "crypto":
		return 0.0005 // 0.05% (Upbit)
	case "kr":
		return 0.0025 // 0.25% (KIS domestic)
	default:
		return 0.0025 // 0.25% (KIS overseas)
	}
}

// TradeRecord 개별 매매 기록
type TradeRecord struct {
	Timestamp  time.Time `json:"timestamp"`
	Market     string    `json:"market"`               // "us" or "kr"
	Symbol     string    `json:"symbol"`
	Name       string    `json:"name,omitempty"`
	Side       string    `json:"side"`                 // "buy" or "sell"
	Quantity   float64   `json:"quantity"`
	Price      float64   `json:"price"`                // 체결가
	Amount     float64   `json:"amount"`               // 총액
	Commission float64   `json:"commission"`
	Strategy   string    `json:"strategy,omitempty"`
	Reason     string    `json:"reason"`               // signal, stop_loss, target1, target2, time_stop, invalidation
	EntryPrice float64   `json:"entry_price,omitempty"` // 매도 시 진입가
	PnL        float64   `json:"pnl,omitempty"`         // 매도 시 실현손익 (수수료 포함 순손익)
	PnLPct     float64   `json:"pnl_pct,omitempty"`     // 매도 시 수익률%
}

// StrategySummary 전략별 요약
type StrategySummary struct {
	Trades    int     `json:"trades"`
	Wins      int     `json:"wins"`
	Losses    int     `json:"losses"`
	WinRate   float64 `json:"win_rate"`
	PnL       float64 `json:"pnl"`
	NetPnL    float64 `json:"net_pnl"`
	Commission float64 `json:"commission"`
}

// MarketSummary 마켓별 요약
type MarketSummary struct {
	BuyCount    int     `json:"buy_count"`
	SellCount   int     `json:"sell_count"`
	Wins        int     `json:"wins"`
	Losses      int     `json:"losses"`
	WinRate     float64 `json:"win_rate"`
	PnL         float64 `json:"pnl"`
	NetPnL      float64 `json:"net_pnl"`
	Commission  float64 `json:"commission"`
}

// TradeSummary 전체 요약
type TradeSummary struct {
	TotalTrades      int                       `json:"total_trades"`
	BuyCount         int                       `json:"buy_count"`
	SellCount        int                       `json:"sell_count"`
	WinCount         int                       `json:"win_count"`
	LossCount        int                       `json:"loss_count"`
	WinRate          float64                   `json:"win_rate"`
	TotalRealizedPnL float64                   `json:"total_realized_pnl"`
	TotalCommission  float64                   `json:"total_commission"`
	NetPnL           float64                   `json:"net_pnl"`
	ByStrategy       map[string]StrategySummary `json:"by_strategy"`
	ByMarket         map[string]MarketSummary   `json:"by_market"`
}

// TradeHistory 영구 매매 기록 저장소
type TradeHistory struct {
	mu      sync.RWMutex
	records []TradeRecord
	path    string
}

// NewTradeHistory 생성자
func NewTradeHistory(dataDir string) (*TradeHistory, error) {
	h := &TradeHistory{
		path: filepath.Join(dataDir, "trade_history.json"),
	}
	if err := h.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return h, nil
}

// Reload 디스크에서 최신 데이터 리로드
func (h *TradeHistory) Reload() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
}

func (h *TradeHistory) load() error {
	data, err := os.ReadFile(h.path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &h.records)
}

func (h *TradeHistory) save() error {
	data, err := json.MarshalIndent(h.records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(h.path, data, 0644)
}

// Append 매매 기록 추가
func (h *TradeHistory) Append(rec TradeRecord) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	if rec.Amount == 0 {
		rec.Amount = rec.Quantity * rec.Price
	}
	if rec.Commission == 0 {
		rec.Commission = rec.Amount * CommissionRateByMarket(rec.Market)
	}

	h.records = append(h.records, rec)
	return h.save()
}

// GetAll 전체 기록 반환 (마켓 필터 옵션)
func (h *TradeHistory) GetAll(market string) []TradeRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if market == "" {
		result := make([]TradeRecord, len(h.records))
		copy(result, h.records)
		return result
	}

	var filtered []TradeRecord
	for _, r := range h.records {
		rm := r.Market
		if rm == "" {
			rm = "us"
		}
		if rm == market {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// Summary 요약 통계 생성
func (h *TradeHistory) Summary(market string) TradeSummary {
	records := h.GetAll(market)

	s := TradeSummary{
		ByStrategy: make(map[string]StrategySummary),
		ByMarket:   make(map[string]MarketSummary),
	}

	// 실현된 거래(매도)의 수수료만 gross PnL 계산에 사용
	realizedCommission := 0.0
	realizedCommByMarket := make(map[string]float64)

	for _, r := range records {
		s.TotalTrades++
		s.TotalCommission += r.Commission

		mkt := r.Market
		if mkt == "" {
			mkt = "us"
		}

		if r.Side == "buy" {
			s.BuyCount++
		} else {
			s.SellCount++
			s.TotalRealizedPnL += r.PnL

			if r.PnL > 0 {
				s.WinCount++
			} else if r.PnL < 0 {
				s.LossCount++
			}

			// 실현 수수료: 매도 수수료 + 매수 수수료 (진입가 기반 추정)
			sellComm := r.Commission
			buyComm := 0.0
			if r.EntryPrice > 0 {
				buyComm = r.EntryPrice * r.Quantity * CommissionRateByMarket(mkt)
			}
			realizedCommission += sellComm + buyComm
			realizedCommByMarket[mkt] += sellComm + buyComm

			// 전략별
			strat := r.Strategy
			if strat == "" {
				strat = "unknown"
			}
			ss := s.ByStrategy[strat]
			ss.Trades++
			ss.PnL += r.PnL
			ss.Commission += sellComm + buyComm
			if r.PnL > 0 {
				ss.Wins++
			} else if r.PnL < 0 {
				ss.Losses++
			}
			if ss.Trades > 0 {
				ss.WinRate = float64(ss.Wins) / float64(ss.Trades) * 100
			}
			s.ByStrategy[strat] = ss
		}

		// 마켓별
		ms := s.ByMarket[mkt]
		if r.Side == "buy" {
			ms.BuyCount++
		} else {
			ms.SellCount++
			ms.PnL += r.PnL
			if r.PnL > 0 {
				ms.Wins++
			} else if r.PnL < 0 {
				ms.Losses++
			}
			if ms.SellCount > 0 {
				ms.WinRate = float64(ms.Wins) / float64(ms.SellCount) * 100
			}
		}
		ms.Commission += r.Commission
		s.ByMarket[mkt] = ms
	}

	if s.SellCount > 0 {
		s.WinRate = float64(s.WinCount) / float64(s.SellCount) * 100
	}

	// PnL 필드는 이미 수수료 차감된 순손익(net)
	// gross = net + 실현 거래 수수료만 (매수만 있는 미실현 수수료 제외)
	s.NetPnL = s.TotalRealizedPnL
	s.TotalRealizedPnL += realizedCommission

	for k, ms := range s.ByMarket {
		ms.NetPnL = ms.PnL
		ms.PnL += realizedCommByMarket[k] // gross = net + 실현 수수료만
		s.ByMarket[k] = ms
	}
	for k, ss := range s.ByStrategy {
		ss.NetPnL = ss.PnL
		s.ByStrategy[k] = ss
	}

	return s
}
