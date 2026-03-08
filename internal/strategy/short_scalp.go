package strategy

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"
)

// ShortScalpConfig holds configuration for the Binance Futures short scalping strategy.
// Mirrors ScalpConfig but with reversed entry/exit logic for shorts.
type ShortScalpConfig struct {
	CandleInterval int // minutes (15)
	CandleCount    int // candles to fetch (100)

	RSIPeriod int     // 7
	RSIEntry  float64 // > 70 = overbought entry (SHORT)
	RSIExit   float64 // < 40 = exit (mean reverted down)

	VolumePeriod int
	VolumeMin    float64

	EMAPeriod int // 50: price must be BELOW EMA50 for shorts

	BBPeriod float64
	BBStdDev float64

	TakeProfitPct float64 // +2.0% (price drops 2%)
	StopLossPct   float64 // -2.5% (price rises 2.5% against us)
	MaxHoldBars   int

	OrderAmountUSDT float64
	MaxPositions    int
	Leverage        int
	CommissionPct   float64 // 0.04% per side (Binance Futures taker)

	Pairs []string
}

// DefaultShortScalpConfig returns the optimized short scalp config.
// Optimized via 90-day backtest (3840 combos, 9 pairs, 2026-03-08):
// Risk-adjusted best: RSI>75, RSIExit<45, Vol>1.5x, TP 3%, SL 3%
// Result: 86 trades, WR 77%, Net +30.9%, PF 2.97, MDD 4.1%
func DefaultShortScalpConfig() ShortScalpConfig {
	return ShortScalpConfig{
		CandleInterval: 15,
		CandleCount:    100,

		RSIPeriod: 7,
		RSIEntry:  75.0, // backtest optimal: RSI>75 (strongly overbought)
		RSIExit:   45.0, // backtest optimal: RSI<45 exit (was 40)

		VolumePeriod: 20,
		VolumeMin:    1.5,

		EMAPeriod: 50,

		BBPeriod: 20,
		BBStdDev: 2.0,

		TakeProfitPct: 3.0, // backtest optimal
		StopLossPct:   3.0, // backtest optimal
		MaxHoldBars:   32,  // backtest optimal: 8 hours (was 48)

		OrderAmountUSDT: 80.0,
		MaxPositions:    4,  // backtest optimal: 4 > 3 (+2%p Net, same MDD)
		Leverage:        2,
		CommissionPct:   0.04, // Binance Futures taker

		Pairs: []string{
			// 90일 백테스트 기준 (2026-03-08), RSI>70 Strategy A
			"ETHUSDT",  // 기존
			"SOLUSDT",  // 기존
			"XRPUSDT",  // 기존
			"LINKUSDT", // 11건, WR 82%, +5.1%, PF 7.17
			"DOGEUSDT", // 17건, WR 76%, +5.1%, PF 2.18
			"ADAUSDT",  // 15건, WR 80%, +4.7%, PF 2.42
			"AVAXUSDT", // 17건, WR 82%, +4.2%, PF 2.09
			"BNBUSDT",  // 12건, WR 75%, +2.3%, PF 3.29
			// MATICUSDT 제거: Symbol closed (POL 리브랜딩), POLUSDT PF 1.31 기준 미달
		},
	}
}

// ShortScalpPosition tracks an active short position.
type ShortScalpPosition struct {
	Symbol       string    `json:"symbol"`
	EntryPrice   float64   `json:"entry_price"`
	Quantity     float64   `json:"quantity"`
	AmountUSDT   float64   `json:"amount_usdt"`
	Leverage     int       `json:"leverage"`
	EntryTime    time.Time `json:"entry_time"`
	EntryBar     int       `json:"entry_bar"`
	StopLoss     float64   `json:"stop_loss"`
	TakeProfit   float64   `json:"take_profit"`
	Strategy     string    `json:"strategy"`
	RSIAtEntry   float64   `json:"rsi_at_entry"`
	BreakevenHit bool      `json:"breakeven_hit,omitempty"`
}

// ShortScalpSignal represents a short scalping trade signal.
type ShortScalpSignal struct {
	Symbol      string
	Price       float64
	RSI         float64
	VolumeRatio float64
	EMA50       float64
	BBUpper     float64
	Reason      string
	Strength    float64
	Time        time.Time
}

// ShortScalpResult summarizes one scan cycle.
type ShortScalpResult struct {
	ScannedPairs int
	Signals      []ShortScalpSignal
	ScanTime     time.Duration
}

// ShortScalper implements the RSI overbought mean-reversion SHORT strategy.
type ShortScalper struct {
	config   ShortScalpConfig
	provider ScalpProvider // Same interface: GetRecentMinuteCandles
}

// NewShortScalper creates a new short scalping strategy.
func NewShortScalper(cfg ShortScalpConfig, p ScalpProvider) *ShortScalper {
	return &ShortScalper{config: cfg, provider: p}
}

// Scan checks all pairs for short entry signals.
func (s *ShortScalper) Scan(ctx context.Context, activePositions map[string]*ShortScalpPosition) (*ShortScalpResult, error) {
	start := time.Now()
	result := &ShortScalpResult{ScannedPairs: len(s.config.Pairs)}

	for _, symbol := range s.config.Pairs {
		if _, held := activePositions[symbol]; held {
			continue
		}

		sig, err := s.analyze(ctx, symbol)
		if err != nil {
			log.Printf("[SHORT] %s analyze error: %v", symbol, err)
			continue
		}
		if sig != nil {
			result.Signals = append(result.Signals, *sig)
		}
	}

	result.ScanTime = time.Since(start)
	return result, nil
}

// CheckExit determines if a short position should be closed.
func (s *ShortScalper) CheckExit(ctx context.Context, pos *ShortScalpPosition, currentBar int) (shouldExit bool, reason string, currentPrice float64) {
	candles, err := s.provider.GetRecentMinuteCandles(ctx, pos.Symbol, s.config.CandleInterval, 10)
	if err != nil || len(candles) == 0 {
		return false, "", 0
	}

	latest := candles[len(candles)-1]
	currentPrice = latest.Close

	// SHORT PnL: profit when price drops
	pnlPct := (pos.EntryPrice - currentPrice) / pos.EntryPrice * 100

	// 1. Stop loss (price went UP against us)
	if pnlPct <= -s.config.StopLossPct {
		return true, fmt.Sprintf("stop_loss (%.2f%%)", pnlPct), currentPrice
	}

	// 2. Take profit (price went DOWN in our favor)
	if pnlPct >= s.config.TakeProfitPct {
		return true, fmt.Sprintf("take_profit (+%.2f%%)", pnlPct), currentPrice
	}

	// 2.5. Breakeven stop for shorts: 수익이 SL의 50% 도달 후 본전으로 되돌아오면 청산
	breakevenThreshold := s.config.StopLossPct * 0.5
	commPct := 0.08 // Futures 왕복 수수료 0.08%
	breakevenPrice := pos.EntryPrice * (1 - commPct/100) // short: 본전은 약간 아래
	if !pos.BreakevenHit && pnlPct >= breakevenThreshold {
		pos.BreakevenHit = true
	}
	if pos.BreakevenHit && currentPrice >= breakevenPrice {
		return true, fmt.Sprintf("breakeven_stop (%.2f%%)", pnlPct), currentPrice
	}

	// 3. RSI exit — mean reverted DOWN (RSI dropped back to normal)
	if len(candles) >= s.config.RSIPeriod+1 {
		rsi := CalculateRSI(candles, s.config.RSIPeriod)
		if rsi <= s.config.RSIExit {
			return true, fmt.Sprintf("rsi_exit (RSI=%.1f, pnl=%.2f%%)", rsi, pnlPct), currentPrice
		}
	}

	// 4. Max hold
	barsHeld := currentBar - pos.EntryBar
	if barsHeld >= s.config.MaxHoldBars {
		return true, fmt.Sprintf("max_hold (%d bars, pnl=%.2f%%)", barsHeld, pnlPct), currentPrice
	}

	return false, "", currentPrice
}

// CalculateStopLoss for shorts: price goes UP = our loss
func (s *ShortScalper) CalculateStopLoss(entryPrice float64) float64 {
	return entryPrice * (1.0 + s.config.StopLossPct/100.0)
}

// CalculateTakeProfit for shorts: price goes DOWN = our profit
func (s *ShortScalper) CalculateTakeProfit(entryPrice float64) float64 {
	return entryPrice * (1.0 - s.config.TakeProfitPct/100.0)
}

// analyze checks a single pair for SHORT entry signals.
func (s *ShortScalper) analyze(ctx context.Context, symbol string) (*ShortScalpSignal, error) {
	candles, err := s.provider.GetRecentMinuteCandles(ctx, symbol, s.config.CandleInterval, s.config.CandleCount)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", symbol, err)
	}

	if len(candles) < s.config.EMAPeriod+2 {
		return nil, nil
	}

	// Use second-to-last candle (last COMPLETED candle) for signal analysis.
	// Binance klines API returns the current in-progress candle as last element,
	// which has partial volume (e.g. 30 sec of 15 min = ~3% volume).
	// Indicators computed on completed candles only.
	completedCandles := candles[:len(candles)-1]
	latest := completedCandles[len(completedCandles)-1]
	price := latest.Close

	// 1. RSI(7) must be OVERBOUGHT
	rsi := CalculateRSI(completedCandles, s.config.RSIPeriod)
	if rsi <= s.config.RSIEntry {
		log.Printf("[BSCALP] %s skip: RSI=%.1f <= %.0f", symbol, rsi, s.config.RSIEntry)
		return nil, nil
	}

	// 2. Volume filter
	avgVol := CalculateAvgVolume(completedCandles[:len(completedCandles)-1], s.config.VolumePeriod)
	currentVol := float64(latest.Volume)
	volRatio := 0.0
	if avgVol > 0 {
		volRatio = currentVol / avgVol
	}
	if volRatio < s.config.VolumeMin {
		log.Printf("[BSCALP] %s skip: vol=%.2fx < %.1fx (RSI=%.1f)", symbol, volRatio, s.config.VolumeMin, rsi)
		return nil, nil
	}

	// 3. EMA50 filter — price must be BELOW EMA50 (bearish trend confirmation)
	ema := CalculateEMA(completedCandles, s.config.EMAPeriod)
	if ema <= 0 || price >= ema {
		log.Printf("[BSCALP] %s skip: price=%.4f >= EMA50=%.4f (RSI=%.1f)", symbol, price, ema, rsi)
		return nil, nil
	}

	// 4. Bollinger Band UPPER touch — additional confirmation for overbought
	bbUpper, _, _ := CalculateBollingerBands(completedCandles, int(s.config.BBPeriod), s.config.BBStdDev)
	nearBBUpper := price >= bbUpper*0.995 // within 0.5% of upper band

	// Signal strength (0-100)
	strength := 0.0

	// RSI: higher = stronger short signal
	rsiScore := (rsi - s.config.RSIEntry) / (100 - s.config.RSIEntry) * 40
	strength += rsiScore

	// Volume (floor at 0 — low volume shouldn't penalize)
	volScore := math.Max(0, math.Min((volRatio-1.0)*20, 30))
	strength += volScore

	// BB upper touch bonus
	if nearBBUpper {
		strength += 15
	}

	// EMA distance below (further below EMA = stronger bearish trend)
	emaDistPct := (ema - price) / ema * 100
	if emaDistPct < 1.0 {
		strength += 15
	} else if emaDistPct < 2.0 {
		strength += 10
	}

	if strength < 15 {
		log.Printf("[BSCALP] %s skip: strength=%.1f < 15 (RSI=%.1f, vol=%.2fx, EMA dist=%.2f%%)", symbol, strength, rsi, volRatio, emaDistPct)
		return nil, nil
	}
	log.Printf("[BSCALP] %s SIGNAL: strength=%.1f, RSI=%.1f, vol=%.2fx, EMA dist=%.2f%%", symbol, strength, rsi, volRatio, emaDistPct)

	reason := fmt.Sprintf("RSI(7)=%.1f (>%.0f), Vol=%.1fx (>%.1fx), EMA%d=%.2f (price below)",
		rsi, s.config.RSIEntry, volRatio, s.config.VolumeMin, s.config.EMAPeriod, ema)
	if nearBBUpper {
		reason += fmt.Sprintf(", BB_upper=%.2f (touch)", bbUpper)
	}

	return &ShortScalpSignal{
		Symbol:      symbol,
		Price:       price,
		RSI:         rsi,
		VolumeRatio: volRatio,
		EMA50:       ema,
		BBUpper:     bbUpper,
		Reason:      reason,
		Strength:    strength,
		Time:        latest.Time,
	}, nil
}
