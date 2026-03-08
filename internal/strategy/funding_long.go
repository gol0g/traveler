package strategy

import (
	"context"
	"fmt"
	"math"
	"time"

	"traveler/pkg/model"
)

// FundingLongConfig holds configuration for the BTC funding rate long strategy.
// Based on 360-day backtest: funding < -0.01% + RSI > 40 → long
// Result: 14 trades, 64% WR, +8.25%, PF 3.22, Sharpe 1.86, MDD 1.32%
type FundingLongConfig struct {
	Symbol          string
	CandleInterval  int     // minutes (15)
	CandleCount     int     // candles to fetch for indicators

	// Entry
	FundingThreshold float64 // entry when funding < this (e.g. -0.0001 = -0.01%)
	RSIMin           float64 // RSI must be above this (filter out deep downtrend)
	RSIPeriod        int

	// Exit — ATR-based dynamic TP/SL
	TPAtrMultiple    float64 // TP = entry + ATR * multiple
	SLAtrMultiple    float64 // SL = entry - ATR * multiple
	ATRPeriod        int
	MaxHoldBars      int     // max bars to hold (15-min bars)

	// Position sizing
	OrderAmountUSDT  float64
	Leverage         int
	MaxPositions     int

	// Commission
	CommissionPct    float64 // per side (0.04% for Binance Futures taker)
}

// DefaultFundingLongConfig returns the optimized config from 360-day backtest.
// DefaultFundingLongConfig returns the optimized config from 180-day backtest.
// Backtest: 49 trades, 49% WR, +9.19%, PF 1.55, Sharpe 1.31, MDD 4.07%
func DefaultFundingLongConfig() FundingLongConfig {
	return FundingLongConfig{
		Symbol:         "BTCUSDT",
		CandleInterval: 15,
		CandleCount:    100,

		FundingThreshold: -0.00005, // -0.005% (relaxed from -0.01%)
		RSIMin:           35,       // lowered from 40
		RSIPeriod:        7,

		TPAtrMultiple: 2.5, // raised from 2.0
		SLAtrMultiple: 1.5,
		ATRPeriod:     14,
		MaxHoldBars:   24, // 6 hours

		OrderAmountUSDT: 80,
		Leverage:        2,
		MaxPositions:    1,

		CommissionPct: 0.04,
	}
}

// FundingLongPosition tracks an active long position.
type FundingLongPosition struct {
	Symbol       string    `json:"symbol"`
	EntryPrice   float64   `json:"entry_price"`
	Quantity     float64   `json:"quantity"`
	AmountUSDT   float64   `json:"amount_usdt"`
	Leverage     int       `json:"leverage"`
	EntryTime    time.Time `json:"entry_time"`
	EntryBar     int       `json:"entry_bar"`
	StopLoss     float64   `json:"stop_loss"`
	TakeProfit   float64   `json:"take_profit"`
	EntryATR     float64   `json:"entry_atr"`
	EntryRSI     float64   `json:"entry_rsi"`
	EntryFunding float64   `json:"entry_funding"`
}

// FundingLongSignal represents an entry signal.
type FundingLongSignal struct {
	Symbol      string
	Price       float64
	FundingRate float64
	RSI         float64
	ATR         float64
	EMA50       float64
	Reason      string
	Time        time.Time
}

// FundingProvider is the data interface for funding rate strategy.
type FundingProvider interface {
	GetRecentMinuteCandles(ctx context.Context, symbol string, interval int, count int) ([]model.Candle, error)
}

// FundingRateProvider fetches funding rate data.
type FundingRateProvider interface {
	GetFundingRate(ctx context.Context, symbol string) (float64, time.Time, error)
}

// OpenInterestProvider fetches open interest data (optional).
type OpenInterestProvider interface {
	GetOpenInterest(ctx context.Context, symbol string) (float64, error)
}

// FundingLongStrategy implements the funding rate long strategy.
type FundingLongStrategy struct {
	config       FundingLongConfig
	candleProv   FundingProvider
	fundingProv  FundingRateProvider
	oiProv       OpenInterestProvider // optional
	lastOI       float64              // previous scan's OI for divergence
	lastPrice    float64              // previous scan's price
}

// NewFundingLongStrategy creates a new funding rate long strategy.
func NewFundingLongStrategy(cfg FundingLongConfig, cp FundingProvider, fp FundingRateProvider) *FundingLongStrategy {
	return &FundingLongStrategy{
		config:      cfg,
		candleProv:  cp,
		fundingProv: fp,
	}
}

// SetOIProvider sets the optional open interest provider for OI divergence filtering.
func (s *FundingLongStrategy) SetOIProvider(p OpenInterestProvider) {
	s.oiProv = p
}

// ScanResult holds the result of a signal scan with all indicator values for logging.
type FundingScanResult struct {
	Time        time.Time `json:"time"`
	Symbol      string    `json:"symbol"`
	Price       float64   `json:"price"`
	FundingRate float64   `json:"funding_rate"`
	RSI7        float64   `json:"rsi7"`
	ATR14       float64   `json:"atr14"`
	EMA50       float64   `json:"ema50"`
	Volume      float64   `json:"volume"`
	AvgVolume   float64   `json:"avg_volume"`
	OI          float64   `json:"oi"`           // current open interest
	OIChange    float64   `json:"oi_change"`    // % change from last scan
	OIDivergence string   `json:"oi_divergence"` // "bullish", "bearish", "neutral", "n/a"
	Signal      string    `json:"signal"`   // "long", "none", "filtered_rsi", etc.
	Reason      string    `json:"reason"`
}

// Scan checks for entry signal and returns both the signal (if any) and full scan data for logging.
func (s *FundingLongStrategy) Scan(ctx context.Context) (*FundingLongSignal, *FundingScanResult, error) {
	result := &FundingScanResult{
		Time:   time.Now().UTC(),
		Symbol: s.config.Symbol,
		Signal: "none",
	}

	// 1. Get funding rate
	fundingRate, _, err := s.fundingProv.GetFundingRate(ctx, s.config.Symbol)
	if err != nil {
		return nil, result, fmt.Errorf("funding rate: %w", err)
	}
	result.FundingRate = fundingRate

	// 2. Get candles
	candles, err := s.candleProv.GetRecentMinuteCandles(ctx, s.config.Symbol, s.config.CandleInterval, s.config.CandleCount)
	if err != nil {
		return nil, result, fmt.Errorf("candles: %w", err)
	}

	if len(candles) < s.config.CandleCount/2 {
		result.Reason = "insufficient_data"
		return nil, result, nil
	}

	// Use completed candles only (drop in-progress candle from Binance API)
	completedCandles := candles[:len(candles)-1]
	if len(completedCandles) == 0 {
		result.Reason = "insufficient_data"
		return nil, result, nil
	}
	latest := completedCandles[len(completedCandles)-1]
	result.Price = latest.Close

	// 3. Calculate indicators
	rsi := CalculateRSI(completedCandles, s.config.RSIPeriod)
	atr := CalculateATR(completedCandles, s.config.ATRPeriod)
	ema50 := CalculateEMA(completedCandles, 50)
	avgVol := CalculateAvgVolume(completedCandles[:len(completedCandles)-1], 20)

	result.RSI7 = rsi
	result.ATR14 = atr
	result.EMA50 = ema50
	result.Volume = float64(latest.Volume)
	result.AvgVolume = avgVol

	// 3b. OI divergence analysis
	result.OIDivergence = "n/a"
	if s.oiProv != nil {
		if oi, err := s.oiProv.GetOpenInterest(ctx, s.config.Symbol); err == nil && oi > 0 {
			result.OI = oi
			if s.lastOI > 0 && s.lastPrice > 0 {
				oiChg := (oi - s.lastOI) / s.lastOI * 100
				priceChg := (latest.Close - s.lastPrice) / s.lastPrice * 100
				result.OIChange = oiChg

				// OI Divergence classification
				// Price↑ + OI↑ = strong trend (bullish)
				// Price↓ + OI↓ = long liquidation, potential bottom (bullish)
				// Price↑ + OI↓ = short cover, weak (bearish)
				// Price↓ + OI↑ = short accumulation (bearish)
				if (priceChg > 0 && oiChg > 0) || (priceChg < 0 && oiChg < 0) {
					result.OIDivergence = "bullish"
				} else if (priceChg > 0 && oiChg < -0.5) || (priceChg < -0.1 && oiChg > 0.5) {
					result.OIDivergence = "bearish"
				} else {
					result.OIDivergence = "neutral"
				}
			}
			s.lastOI = oi
			s.lastPrice = latest.Close
		}
	}

	// 4. Check entry conditions
	if fundingRate >= s.config.FundingThreshold {
		result.Signal = "no_funding"
		result.Reason = fmt.Sprintf("funding=%.4f%% >= %.4f%%", fundingRate*100, s.config.FundingThreshold*100)
		return nil, result, nil
	}

	if rsi > 0 && rsi < s.config.RSIMin {
		result.Signal = "filtered_rsi"
		result.Reason = fmt.Sprintf("rsi=%.1f < %.0f (deep downtrend)", rsi, s.config.RSIMin)
		return nil, result, nil
	}

	if atr <= 0 {
		result.Signal = "no_atr"
		result.Reason = "ATR is zero"
		return nil, result, nil
	}

	// 4d. OI divergence filter: skip if bearish divergence (short accumulation / weak rally)
	if result.OIDivergence == "bearish" {
		result.Signal = "filtered_oi"
		result.Reason = fmt.Sprintf("OI divergence bearish (OI chg=%.2f%%, price trend opposite)", result.OIChange)
		return nil, result, nil
	}

	// Signal triggered
	result.Signal = "long"
	result.Reason = fmt.Sprintf("funding=%.4f%%, RSI=%.1f, ATR=%.1f, OI=%s",
		fundingRate*100, rsi, atr, result.OIDivergence)

	sig := &FundingLongSignal{
		Symbol:      s.config.Symbol,
		Price:       latest.Close,
		FundingRate: fundingRate,
		RSI:         rsi,
		ATR:         atr,
		EMA50:       ema50,
		Time:        latest.Time,
		Reason:      result.Reason,
	}

	return sig, result, nil
}

// CheckExit checks if an active position should be closed.
func (s *FundingLongStrategy) CheckExit(ctx context.Context, pos *FundingLongPosition, currentBar int) (shouldExit bool, reason string, currentPrice float64, scanData *FundingScanResult) {
	scanData = &FundingScanResult{
		Time:   time.Now().UTC(),
		Symbol: pos.Symbol,
		Signal: "hold",
	}

	candles, err := s.candleProv.GetRecentMinuteCandles(ctx, pos.Symbol, s.config.CandleInterval, 20)
	if err != nil || len(candles) == 0 {
		return false, "", 0, scanData
	}

	latest := candles[len(candles)-1]
	currentPrice = latest.Close
	pnlPct := (currentPrice - pos.EntryPrice) / pos.EntryPrice * 100

	scanData.Price = currentPrice

	// Dynamic TP/SL based on entry ATR
	tpPrice := pos.TakeProfit
	slPrice := pos.StopLoss
	tpPct := (tpPrice - pos.EntryPrice) / pos.EntryPrice * 100
	slPct := (pos.EntryPrice - slPrice) / pos.EntryPrice * 100

	// 1. Stop loss
	if currentPrice <= slPrice {
		scanData.Signal = "exit_sl"
		return true, fmt.Sprintf("stop_loss (%.2f%%, SL=$%.0f)", pnlPct, slPrice), currentPrice, scanData
	}

	// 2. Take profit
	if currentPrice >= tpPrice {
		scanData.Signal = "exit_tp"
		return true, fmt.Sprintf("take_profit (+%.2f%%, TP=$%.0f)", pnlPct, tpPrice), currentPrice, scanData
	}

	// 3. Max hold time
	barsHeld := currentBar - pos.EntryBar
	if barsHeld >= s.config.MaxHoldBars {
		scanData.Signal = "exit_time"
		return true, fmt.Sprintf("max_hold (%d bars, %.2f%%)", barsHeld, pnlPct), currentPrice, scanData
	}

	scanData.Reason = fmt.Sprintf("pnl=%.2f%%, tp=%.2f%%, sl=%.2f%%, bars=%d/%d",
		pnlPct, tpPct, slPct, barsHeld, s.config.MaxHoldBars)

	_ = tpPct
	_ = slPct
	return false, "", currentPrice, scanData
}

// CalculateTP returns take profit price based on ATR.
func (s *FundingLongStrategy) CalculateTP(entryPrice, atr float64) float64 {
	return entryPrice + atr*s.config.TPAtrMultiple
}

// CalculateSL returns stop loss price based on ATR.
func (s *FundingLongStrategy) CalculateSL(entryPrice, atr float64) float64 {
	return entryPrice - atr*s.config.SLAtrMultiple
}

// NetProfitPct returns net profit after round-trip commission.
func (s *FundingLongStrategy) NetProfitPct(grossPnlPct float64) float64 {
	return grossPnlPct - 2*s.config.CommissionPct
}

// RiskRewardRatio returns the risk/reward ratio for a trade.
func (s *FundingLongStrategy) RiskRewardRatio(atr float64) float64 {
	if s.config.SLAtrMultiple == 0 {
		return 0
	}
	return s.config.TPAtrMultiple / s.config.SLAtrMultiple
}

// Unused but needed for interface compliance
var _ = math.Abs
