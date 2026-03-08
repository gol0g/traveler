package strategy

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"traveler/pkg/model"
)

// ScalpProvider is the data interface needed by the scalping strategy.
// Implemented by UpbitProvider.
type ScalpProvider interface {
	GetRecentMinuteCandles(ctx context.Context, symbol string, interval int, count int) ([]model.Candle, error)
}

// ScalpConfig holds configuration for the crypto scalping strategy.
type ScalpConfig struct {
	// Candle settings
	CandleInterval int // minutes (15)
	CandleCount    int // how many candles to fetch for indicators (100)

	// RSI entry
	RSIPeriod    int     // 7 (fast RSI)
	RSIEntry     float64 // < 25 = oversold entry
	RSIExit      float64 // > 50 = exit (mean reverted)

	// Volume filter
	VolumePeriod int     // 20 candles average
	VolumeMin    float64 // 1.5x minimum multiplier

	// Trend filter (EMA on minute candles)
	EMAPeriod int // 50 candles

	// Bollinger Band confirmation
	BBPeriod float64 // 20
	BBStdDev float64 // 2.0

	// Risk management
	TakeProfitPct float64 // +0.8%
	StopLossPct   float64 // -1.5%
	MaxHoldBars   int     // max hold in candle bars (e.g. 16 bars = 4 hours at 15min)

	// Position sizing
	OrderAmountKRW float64 // KRW per trade (₩50,000)
	MaxPositions   int     // concurrent positions

	// Commission
	CommissionPct float64 // 0.05% per side

	// Pairs to scan
	Pairs []string
}

// DefaultScalpConfig returns the optimized scalping config.
// Optimized via 90-day backtest (2160 param combos):
// Net +31.5%, WR 68%, PF 1.94, MDD 6.4%, EV +0.18%/trade
func DefaultScalpConfig() ScalpConfig {
	return ScalpConfig{
		CandleInterval: 15,
		CandleCount:    100, // ~25 hours of 15-min data

		RSIPeriod: 7,
		RSIEntry:  30.0, // optimized from 25 → 30 (more opportunities)
		RSIExit:   60.0, // backtest optimal: exit>60 dominates all top results (9-pair 90d grid)

		VolumePeriod: 20,
		VolumeMin:    1.5,

		EMAPeriod: 50, // EMA50: 12.5h lookback on 15min — best backtest result

		BBPeriod: 20,
		BBStdDev: 2.0,

		TakeProfitPct: 2.5, // backtest optimal: TP 2.5 > 2.0 (+1.8%p Net, same MDD)
		StopLossPct:   2.5, // optimized from 1.5 → 2.5 (reduce noise stops)
		MaxHoldBars:   32,  // optimized from 16 → 32 (8 hours, allow time for reversion)

		OrderAmountKRW: 50000,
		MaxPositions:   3,

		CommissionPct: 0.05,

		Pairs: []string{
			// DCA 보유 코인도 포함: preBuyQty delta로 자기 수량만 매도 (Bug #016)
			// 90일 Upbit 15분봉 백테스트 기준 (2026-03-08)
			"KRW-ETH",  // 7건, WR 86%, +4.9%, PF 5.26
			"KRW-LINK", // 4건, WR 75%, +2.7%, PF 4.19
			"KRW-SOL",  // 2건, WR 100%, +2.5%
			"KRW-AVAX", // 2건, WR 100%, +1.5%
			"KRW-SUI",  // 6건, WR 67%, +1.4%, PF 1.50 (신규 추가)
			"KRW-XRP",  // 4건, WR 50%, +0.9%, PF 3.85
			"KRW-ADA",  // 0건 (약세장), 상승장 대비 유지
			"KRW-DOGE", // 0건 (약세장), 상승장 대비 유지
			"KRW-TRX",  // 0건 (약세장), 상승장 대비 유지
		},
	}
}

// ScalpSignal represents a scalping trade signal.
type ScalpSignal struct {
	Symbol     string
	Side       string  // "buy" or "sell"
	Price      float64 // current price
	RSI        float64
	VolumeRatio float64
	EMA50      float64
	BBLower    float64
	Reason     string
	Strength   float64 // 0-100
	Time       time.Time
}

// ScalpPosition tracks an active scalping position.
type ScalpPosition struct {
	Symbol       string    `json:"symbol"`
	EntryPrice   float64   `json:"entry_price"`
	Quantity     float64   `json:"quantity"`
	AmountKRW    float64   `json:"amount_krw"`
	EntryTime    time.Time `json:"entry_time"`
	EntryBar     int       `json:"entry_bar"` // candle index at entry
	StopLoss     float64   `json:"stop_loss"`
	TakeProfit   float64   `json:"take_profit"`
	Strategy     string    `json:"strategy"` // "rsi-mean-revert"
	RSIAtEntry   float64   `json:"rsi_at_entry"`
	BreakevenHit bool      `json:"breakeven_hit,omitempty"` // breakeven stop 활성화 여부
}

// ScalpResult summarizes one scan cycle.
type ScalpResult struct {
	ScannedPairs int
	Signals      []ScalpSignal
	ScanTime     time.Duration
}

// CryptoScalper implements the RSI mean-reversion scalping strategy.
type CryptoScalper struct {
	config   ScalpConfig
	provider ScalpProvider
}

// NewCryptoScalper creates a new scalping strategy instance.
func NewCryptoScalper(cfg ScalpConfig, p ScalpProvider) *CryptoScalper {
	return &CryptoScalper{
		config:   cfg,
		provider: p,
	}
}

// Scan checks all configured pairs for scalping entry signals.
func (s *CryptoScalper) Scan(ctx context.Context, activePositions map[string]*ScalpPosition) (*ScalpResult, error) {
	start := time.Now()
	result := &ScalpResult{ScannedPairs: len(s.config.Pairs)}

	for _, symbol := range s.config.Pairs {
		// Skip if already holding this symbol
		if _, held := activePositions[symbol]; held {
			continue
		}

		sig, err := s.analyze(ctx, symbol)
		if err != nil {
			log.Printf("[SCALP] %s analyze error: %v", symbol, err)
			continue
		}
		if sig != nil {
			result.Signals = append(result.Signals, *sig)
		}
	}

	result.ScanTime = time.Since(start)
	return result, nil
}

// CheckExit determines if an active position should be closed.
func (s *CryptoScalper) CheckExit(ctx context.Context, pos *ScalpPosition, currentBar int) (shouldExit bool, reason string, currentPrice float64) {
	// Get latest price via 1 candle
	candles, err := s.provider.GetRecentMinuteCandles(ctx, pos.Symbol, s.config.CandleInterval, 10)
	if err != nil || len(candles) == 0 {
		return false, "", 0
	}

	latest := candles[len(candles)-1]
	currentPrice = latest.Close
	pnlPct := (currentPrice - pos.EntryPrice) / pos.EntryPrice * 100

	// 1. Stop loss
	if pnlPct <= -s.config.StopLossPct {
		return true, fmt.Sprintf("stop_loss (%.2f%%)", pnlPct), currentPrice
	}

	// 2. Take profit
	if pnlPct >= s.config.TakeProfitPct {
		return true, fmt.Sprintf("take_profit (+%.2f%%)", pnlPct), currentPrice
	}

	// 2.5. Breakeven stop: 수익이 SL의 50% 도달 시 활성화, 이후 본전 아래로 내려오면 청산
	breakevenThreshold := s.config.StopLossPct * 0.5 // SL 2.5% → 1.25% 수익 시 활성
	commPct := 0.1                                    // 왕복 수수료 0.1%
	breakevenPrice := pos.EntryPrice * (1 + commPct/100)
	if !pos.BreakevenHit && pnlPct >= breakevenThreshold {
		pos.BreakevenHit = true
	}
	if pos.BreakevenHit && currentPrice <= breakevenPrice {
		return true, fmt.Sprintf("breakeven_stop (%.2f%%)", pnlPct), currentPrice
	}

	// 3. RSI exit (mean reverted)
	//    Exit when RSI normalizes regardless of P&L.
	//    Small RSI-exit losses (-0.5%) are better than holding to SL (-2.5%).
	if len(candles) >= s.config.RSIPeriod+1 {
		rsi := CalculateRSI(candles, s.config.RSIPeriod)
		if rsi >= s.config.RSIExit {
			return true, fmt.Sprintf("rsi_exit (RSI=%.1f, pnl=%.2f%%)", rsi, pnlPct), currentPrice
		}
	}

	// 4. Time-based exit (max hold)
	barsHeld := currentBar - pos.EntryBar
	if barsHeld >= s.config.MaxHoldBars {
		return true, fmt.Sprintf("max_hold (%d bars, pnl=%.2f%%)", barsHeld, pnlPct), currentPrice
	}

	return false, "", currentPrice
}

// analyze checks a single pair for entry signals.
func (s *CryptoScalper) analyze(ctx context.Context, symbol string) (*ScalpSignal, error) {
	candles, err := s.provider.GetRecentMinuteCandles(ctx, symbol, s.config.CandleInterval, s.config.CandleCount)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", symbol, err)
	}

	if len(candles) < s.config.EMAPeriod+2 {
		return nil, nil // insufficient data
	}

	// Use second-to-last candle (last COMPLETED candle) for signal analysis.
	// Upbit API returns current in-progress candle as last element,
	// which has partial volume and incomplete OHLC.
	completedCandles := candles[:len(candles)-1]
	latest := completedCandles[len(completedCandles)-1]
	price := latest.Close

	// 0. Minimum price filter — 저가 코인은 틱사이즈(₩1) 영향이 너무 커서 스캘핑 부적합
	// ₩1,000 미만: 틱 1원 = 0.1%+, RSI가 가격 변동 없이 극단값 왕복 → 수수료만 손실
	if price < 1000 {
		return nil, nil
	}

	// 1. RSI(7) check — must be oversold
	rsi := CalculateRSI(completedCandles, s.config.RSIPeriod)
	if rsi >= s.config.RSIEntry {
		return nil, nil
	}

	// 2. Volume filter — current candle volume must exceed average
	avgVol := CalculateAvgVolume(completedCandles[:len(completedCandles)-1], s.config.VolumePeriod)
	currentVol := float64(latest.Volume)
	volRatio := 0.0
	if avgVol > 0 {
		volRatio = currentVol / avgVol
	}
	if volRatio < s.config.VolumeMin {
		return nil, nil
	}

	// 3. EMA trend filter — price must be above EMA
	//    EMA50 on 15min = 12.5h lookback — best backtest result (Net +29.8%, PF 1.86)
	//    Tested EMA20 (-4.2%) and no-EMA (-31%): EMA50 is optimal.
	ema := CalculateEMA(completedCandles, s.config.EMAPeriod)
	if ema <= 0 || price <= ema {
		return nil, nil
	}

	// 4. Bollinger Band lower touch — additional confirmation
	bbUpper, bbLower, _ := CalculateBollingerBands(completedCandles, int(s.config.BBPeriod), s.config.BBStdDev)
	nearBBLower := price <= bbLower*1.005 // within 0.5% of lower band

	// Calculate signal strength (0-100)
	strength := 0.0

	// RSI contribution (lower = stronger signal)
	rsiScore := (s.config.RSIEntry - rsi) / s.config.RSIEntry * 40
	strength += rsiScore

	// Volume contribution (higher = stronger)
	volScore := math.Min((volRatio-1.0)*20, 30)
	strength += volScore

	// BB touch bonus
	if nearBBLower {
		strength += 15
	}

	// EMA distance bonus (closer to EMA = safer entry)
	emaDistPct := (price - ema) / ema * 100
	if emaDistPct < 1.0 {
		strength += 15
	} else if emaDistPct < 2.0 {
		strength += 10
	}

	// Minimum strength threshold
	if strength < 30 {
		return nil, nil
	}

	reason := fmt.Sprintf("RSI(7)=%.1f (<%.0f), Vol=%.1fx (>%.1fx), EMA%d=%.0f",
		rsi, s.config.RSIEntry, volRatio, s.config.VolumeMin, s.config.EMAPeriod, ema)
	if nearBBLower {
		reason += fmt.Sprintf(", BB_lower=%.0f (touch)", bbLower)
	}

	_ = bbUpper // not used for entry

	return &ScalpSignal{
		Symbol:      symbol,
		Side:        "buy",
		Price:       price,
		RSI:         rsi,
		VolumeRatio: volRatio,
		EMA50:       ema,
		BBLower:     bbLower,
		Reason:      reason,
		Strength:    strength,
		Time:        latest.Time,
	}, nil
}

// CalculateStopLoss returns the stop loss price for a long entry.
func (s *CryptoScalper) CalculateStopLoss(entryPrice float64) float64 {
	return entryPrice * (1.0 - s.config.StopLossPct/100.0)
}

// CalculateTakeProfit returns the take profit price for a long entry.
func (s *CryptoScalper) CalculateTakeProfit(entryPrice float64) float64 {
	return entryPrice * (1.0 + s.config.TakeProfitPct/100.0)
}

// NetProfitPct returns the net profit after commission for a given gross PnL%.
func (s *CryptoScalper) NetProfitPct(grossPnlPct float64) float64 {
	return grossPnlPct - 2*s.config.CommissionPct // buy + sell commission
}
