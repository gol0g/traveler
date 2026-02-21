package strategy

import (
	"context"
	"fmt"
	"log"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// CryptoTrendStrategy implements a simple EMA crossover trend following strategy for BTC.
// Designed for small crypto accounts (< ₩200K) where diversification is not viable.
//
// Rules:
//   - BUY: EMA12 > EMA50 AND RSI > 40 (trend confirmation)
//   - EXIT: EMA12 < EMA50 (trend reversal) — handled by monitor/daemon
//   - SL: 2.5x ATR trailing
//   - Hold: weeks to months (trend duration)
type CryptoTrendStrategy struct {
	provider provider.Provider
	emaFast  int
	emaSlow  int
	minRSI   float64
}

// NewCryptoTrendStrategy creates a BTC trend following strategy
func NewCryptoTrendStrategy(p provider.Provider) *CryptoTrendStrategy {
	return &CryptoTrendStrategy{
		provider: p,
		emaFast:  12,
		emaSlow:  50,
		minRSI:   40,
	}
}

func (s *CryptoTrendStrategy) Name() string {
	return "crypto-trend"
}

func (s *CryptoTrendStrategy) Description() string {
	return "BTC trend following via EMA12/EMA50 crossover"
}

// Analyze checks if the given crypto should be bought based on EMA trend.
// Only generates signals for BTC (KRW-BTC). Other coins return nil.
func (s *CryptoTrendStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	sym := stock.Symbol
	if sym != "KRW-BTC" {
		return nil, nil
	}

	// 60일 캔들 (EMA50 + buffer)
	candles, err := s.provider.GetDailyCandles(ctx, sym, 65)
	if err != nil {
		return nil, fmt.Errorf("BTC candle error: %w", err)
	}
	if len(candles) < s.emaSlow+5 {
		return nil, fmt.Errorf("BTC insufficient data: %d", len(candles))
	}

	emaFast := CalculateEMA(candles, s.emaFast)
	emaSlow := CalculateEMA(candles, s.emaSlow)
	rsi := CalculateRSI(candles, 14)
	atr := CalculateATR(candles, 14)
	price := candles[len(candles)-1].Close

	// 조건: EMA12 > EMA50 (상승 추세) AND RSI > 40 (모멘텀 확인)
	if emaFast <= emaSlow {
		log.Printf("[CRYPTO-TREND] BTC EMA12 %.0f <= EMA50 %.0f, no trend", emaFast, emaSlow)
		return nil, nil
	}
	if rsi < s.minRSI {
		log.Printf("[CRYPTO-TREND] BTC RSI %.1f < %.0f, weak momentum", rsi, s.minRSI)
		return nil, nil
	}

	// EMA 간격 비율 (추세 강도)
	emaGap := (emaFast - emaSlow) / emaSlow * 100
	stopLoss := price - atr*2.5
	stopPct := (price - stopLoss) / price * 100
	target1 := price + atr*3.0
	target1Pct := (target1 - price) / price * 100
	target2 := price + atr*5.0
	target2Pct := (target2 - price) / price * 100

	reason := fmt.Sprintf("[CRYPTO-TREND] BTC uptrend: EMA12 ₩%.0f > EMA50 ₩%.0f (+%.1f%%), RSI=%.1f",
		emaFast, emaSlow, emaGap, rsi)

	// 추세 강도에 따른 probability
	probability := 55.0
	if emaGap > 3.0 {
		probability = 65.0
	} else if emaGap > 1.0 {
		probability = 60.0
	}

	return &Signal{
		Stock:       stock,
		Type:        SignalBuy,
		Strategy:    s.Name(),
		Strength:    70,
		Probability: probability,
		Reason:      reason,
		Details: map[string]float64{
			"ema_fast":  emaFast,
			"ema_slow":  emaSlow,
			"ema_gap":   emaGap,
			"rsi14":     rsi,
			"atr14":     atr,
			"regime":    0,
		},
		Guide: &TradeGuide{
			EntryPrice:      price,
			EntryType:       "market",
			StopLoss:        stopLoss,
			StopLossPct:     stopPct,
			Target1:         target1,
			Target1Pct:      target1Pct,
			Target2:         target2,
			Target2Pct:      target2Pct,
			RiskRewardRatio: target1Pct / stopPct,
			EntryATR:        atr,
		},
		Candles: trimCandles(candles, 60),
	}, nil
}
