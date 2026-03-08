package strategy

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// ETFMode defines the ETF rotation sub-strategy
type ETFMode string

const (
	ETFModeGEM      ETFMode = "gem"       // Antonacci's Global Equities Momentum (monthly)
	ETFModeTQQQSMA  ETFMode = "tqqq_sma"  // TQQQ with QQQ 200-day SMA filter (daily)
	ETFModeKRTiming ETFMode = "kr_timing" // KODEX leverage/inverse timing (monthly)
)

// ETFMomentumConfig defines configuration for ETF momentum strategy
type ETFMomentumConfig struct {
	Mode   ETFMode
	Market string // "us" or "kr"
}

// ETFMomentumStrategy implements regime-independent ETF rotation.
// For small capital accounts where individual stock trading is not viable due to commission drag.
//
// Sub-modes:
//   - GEM: Monthly rotation among SPY/VXUS/SHY based on 12-month momentum
//   - TQQQ/SMA: Daily check — hold TQQQ when QQQ > 200-day SMA
//   - KR Timing: Monthly — KODEX leverage when KOSPI200 > 200-day SMA
type ETFMomentumStrategy struct {
	config   ETFMomentumConfig
	provider provider.Provider
}

// NewETFMomentumStrategy creates a new ETF momentum strategy
func NewETFMomentumStrategy(cfg ETFMomentumConfig, p provider.Provider) *ETFMomentumStrategy {
	return &ETFMomentumStrategy{config: cfg, provider: p}
}

func (s *ETFMomentumStrategy) Name() string {
	return fmt.Sprintf("etf-momentum(%s)", s.config.Mode)
}

func (s *ETFMomentumStrategy) Description() string {
	switch s.config.Mode {
	case ETFModeGEM:
		return "Global Equities Momentum: SPY vs VXUS vs SHY monthly rotation"
	case ETFModeTQQQSMA:
		return "TQQQ with QQQ 200-day SMA filter"
	case ETFModeKRTiming:
		return "KODEX leverage/inverse timing via 200-day SMA"
	default:
		return "ETF momentum rotation"
	}
}

// Analyze checks if the given stock should be bought under current ETF momentum rules.
// Returns BUY signal only for the target ETF, nil for non-targets.
func (s *ETFMomentumStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	switch s.config.Mode {
	case ETFModeGEM:
		return s.analyzeGEM(ctx, stock)
	case ETFModeTQQQSMA:
		return s.analyzeTQQQSMA(ctx, stock)
	case ETFModeKRTiming:
		return s.analyzeKRTiming(ctx, stock)
	}
	return nil, nil
}

// analyzeGEM implements Antonacci's Global Equities Momentum.
// Monthly check: SPY 12mo return > SHY? If no → SHY. If yes → SPY vs VXUS winner.
func (s *ETFMomentumStrategy) analyzeGEM(ctx context.Context, stock model.Stock) (*Signal, error) {
	sym := stock.Symbol
	if sym != "SPY" && sym != "VXUS" && sym != "SHY" {
		return nil, nil
	}

	// GEM은 월말에만 새 포지션 진입 (기존 보유 중이면 데몬이 스킵)
	// 최초 진입 시에는 월말 아니어도 허용

	// 12개월 수익률 비교 (252 거래일)
	spyReturn, spyPrice, spyCandles, err := s.calcReturn(ctx, "SPY", 252)
	if err != nil {
		return nil, fmt.Errorf("SPY data: %w", err)
	}
	vxusReturn, _, _, err := s.calcReturn(ctx, "VXUS", 252)
	if err != nil {
		return nil, fmt.Errorf("VXUS data: %w", err)
	}
	shyReturn, _, _, err := s.calcReturn(ctx, "SHY", 252)
	if err != nil {
		return nil, fmt.Errorf("SHY data: %w", err)
	}

	// GEM 규칙
	var target string
	var reason string
	if spyReturn <= shyReturn {
		target = "SHY"
		reason = fmt.Sprintf("[GEM] Risk-off: SPY 12mo %.1f%% <= SHY %.1f%%, holding T-Bills",
			spyReturn*100, shyReturn*100)
	} else if spyReturn > vxusReturn {
		target = "SPY"
		reason = fmt.Sprintf("[GEM] US equity: SPY 12mo %.1f%% > VXUS %.1f%% > SHY %.1f%%",
			spyReturn*100, vxusReturn*100, shyReturn*100)
	} else {
		target = "VXUS"
		reason = fmt.Sprintf("[GEM] Intl equity: VXUS 12mo %.1f%% > SPY %.1f%% > SHY %.1f%%",
			vxusReturn*100, spyReturn*100, shyReturn*100)
	}

	if sym != target {
		return nil, nil
	}

	// 타겟 ETF의 캔들 및 가격
	candles := spyCandles
	price := spyPrice
	if sym != "SPY" {
		_, price, candles, _ = s.calcReturn(ctx, sym, 252)
	}

	atr := CalculateATR(candles, 14)
	// GEM SL: 시그널 역전(SMA200 이탈)이 주 청산 기준
	// 가격 SL은 극단적 폭락 방어용 safety net (7%)
	stopLoss := price * 0.93
	stopLossPct := 7.0
	tp1Amount := atr * 3.0
	tp1Pct := tp1Amount / price * 100
	// GEM ETF TP 상한: TP1 5%, TP2 8%
	if tp1Pct > 5.0 {
		tp1Amount = price * 0.05
		tp1Pct = 5.0
	}
	target1 := price + tp1Amount
	target1Pct := tp1Pct
	tp2Amount := atr * 5.0
	tp2Pct := tp2Amount / price * 100
	if tp2Pct > 8.0 {
		tp2Amount = price * 0.08
		tp2Pct = 8.0
	}
	target2 := price + tp2Amount
	tp2Pct = tp2Amount / price * 100
	rrRatio := tp1Amount / (price - stopLoss)

	return &Signal{
		Stock:       stock,
		Type:        SignalBuy,
		Strategy:    s.Name(),
		Strength:    70,
		Probability: 65,
		Reason:      reason,
		Details: map[string]float64{
			"spy_return_12m":  spyReturn,
			"vxus_return_12m": vxusReturn,
			"shy_return_12m":  shyReturn,
			"regime":          0, // ETF는 레짐 무관
		},
		Guide: &TradeGuide{
			EntryPrice:      price,
			EntryType:       "market",
			StopLoss:        stopLoss,
			StopLossPct:     stopLossPct,
			Target1:         target1,
			Target1Pct:      target1Pct,
			Target2:         target2,
			Target2Pct:      tp2Pct,
			RiskRewardRatio: rrRatio,
			EntryATR:        atr,
		},
		Candles: trimCandles(candles, 90),
	}, nil
}

// analyzeTQQQSMA — hold TQQQ when QQQ > 200-day SMA, else stay out.
func (s *ETFMomentumStrategy) analyzeTQQQSMA(ctx context.Context, stock model.Stock) (*Signal, error) {
	if stock.Symbol != "TQQQ" {
		return nil, nil
	}

	// QQQ 200일 SMA 체크 (TQQQ가 아닌 QQQ 기준)
	qqqCandles, err := s.provider.GetDailyCandles(ctx, "QQQ", 210)
	if err != nil {
		return nil, fmt.Errorf("QQQ data: %w", err)
	}
	if len(qqqCandles) < 200 {
		return nil, fmt.Errorf("QQQ insufficient data: %d < 200", len(qqqCandles))
	}

	qqqPrice := qqqCandles[len(qqqCandles)-1].Close
	qqqSMA200 := CalculateMA(qqqCandles, 200)

	if qqqPrice <= qqqSMA200 {
		log.Printf("[ETF-TQQQ] QQQ $%.2f <= SMA200 $%.2f, risk-off", qqqPrice, qqqSMA200)
		return nil, nil
	}

	// QQQ > SMA200 → TQQQ 매수
	tqqqCandles, err := s.provider.GetDailyCandles(ctx, "TQQQ", 60)
	if err != nil {
		return nil, fmt.Errorf("TQQQ data: %w", err)
	}
	if len(tqqqCandles) == 0 {
		return nil, fmt.Errorf("TQQQ no data")
	}

	price := tqqqCandles[len(tqqqCandles)-1].Close
	atr := CalculateATR(tqqqCandles, 14)
	// TQQQ SL: QQQ SMA200 이탈이 주 청산 기준
	// 가격 SL은 극단적 폭락 방어용 safety net (12% - 레버리지)
	stopLoss := price * 0.88
	stopLossPct := 12.0
	tp1Amount := atr * 3.0
	tp1Pct := tp1Amount / price * 100
	// TQQQ 레버리지 TP 상한: TP1 10%, TP2 15%
	if tp1Pct > 10.0 {
		tp1Amount = price * 0.10
		tp1Pct = 10.0
	}
	target1 := price + tp1Amount
	target1Pct := tp1Pct
	tp2Amount := atr * 5.0
	tp2Pct := tp2Amount / price * 100
	if tp2Pct > 15.0 {
		tp2Amount = price * 0.15
		tp2Pct = 15.0
	}
	target2 := price + tp2Amount
	tp2Pct = tp2Amount / price * 100
	rrRatio := tp1Amount / (price - stopLoss)

	pctAboveSMA := (qqqPrice - qqqSMA200) / qqqSMA200 * 100
	reason := fmt.Sprintf("[TQQQ/SMA] QQQ $%.2f > SMA200 $%.2f (+%.1f%%), holding TQQQ",
		qqqPrice, qqqSMA200, pctAboveSMA)

	return &Signal{
		Stock:       stock,
		Type:        SignalBuy,
		Strategy:    s.Name(),
		Strength:    65,
		Probability: 74,
		Reason:      reason,
		Details: map[string]float64{
			"qqq_price":       qqqPrice,
			"qqq_sma200":      qqqSMA200,
			"pct_above_sma":   pctAboveSMA,
			"regime":          0,
		},
		Guide: &TradeGuide{
			EntryPrice:      price,
			EntryType:       "market",
			StopLoss:        stopLoss,
			StopLossPct:     stopLossPct,
			Target1:         target1,
			Target1Pct:      target1Pct,
			Target2:         target2,
			Target2Pct:      tp2Pct,
			RiskRewardRatio: rrRatio,
			EntryATR:        atr,
		},
		Candles: trimCandles(tqqqCandles, 60),
	}, nil
}

// analyzeKRTiming — KODEX leverage when KOSPI200 > 200-day SMA, else inverse or cash.
func (s *ETFMomentumStrategy) analyzeKRTiming(ctx context.Context, stock model.Stock) (*Signal, error) {
	sym := stock.Symbol
	if sym != "069500" && sym != "114800" {
		return nil, nil
	}

	// KODEX 200 (069500) 기준 200일 SMA
	benchCandles, err := s.provider.GetDailyCandles(ctx, "069500", 210)
	if err != nil {
		return nil, fmt.Errorf("KODEX200 data: %w", err)
	}
	if len(benchCandles) < 200 {
		return nil, fmt.Errorf("KODEX200 insufficient data: %d < 200", len(benchCandles))
	}

	benchPrice := benchCandles[len(benchCandles)-1].Close
	benchSMA200 := CalculateMA(benchCandles, 200)
	isAboveSMA := benchPrice > benchSMA200

	// 추세 강도 확인: SMA200 대비 거리
	pctFromSMA := (benchPrice - benchSMA200) / benchSMA200 * 100

	// 상승장: KODEX 200(069500), 하락장: 인버스(114800)
	var target string
	if isAboveSMA {
		target = "069500"
	} else {
		target = "114800"
	}

	if sym != target {
		return nil, nil
	}

	// 추세 강도 필터: SMA200 근처(±2%)에서는 진입 보류 (whipsaw 방지)
	if math.Abs(pctFromSMA) < 2.0 {
		log.Printf("[KR-ETF] KODEX200 ₩%.0f too close to SMA200 ₩%.0f (%.1f%%), skip", benchPrice, benchSMA200, pctFromSMA)
		return nil, nil
	}

	// 타겟 ETF 데이터
	var candles []model.Candle
	if sym == "069500" {
		candles = benchCandles
	} else {
		candles, err = s.provider.GetDailyCandles(ctx, sym, 60)
		if err != nil {
			return nil, fmt.Errorf("%s data: %w", sym, err)
		}
		if len(candles) == 0 {
			return nil, fmt.Errorf("%s no data", sym)
		}
	}

	price := candles[len(candles)-1].Close
	atr := CalculateATR(candles, 14)

	// ETF SL: 시그널 역전(SMA200 이탈)이 주 청산 기준
	// 가격 SL은 극단적 폭락 방어용 safety net (7%)
	stopLoss := price * 0.93
	stopLossPct := 7.0

	tp1Amount := atr * 3.0
	tp1Pct := tp1Amount / price * 100
	// ETF TP 상한: TP1 5%, TP2 8% (비레버리지 ETF)
	if tp1Pct > 5.0 {
		tp1Amount = price * 0.05
		tp1Pct = 5.0
	}
	target1 := price + tp1Amount
	target1Pct := tp1Pct
	tp2Amount := atr * 5.0
	tp2Pct := tp2Amount / price * 100
	if tp2Pct > 8.0 {
		tp2Amount = price * 0.08
		tp2Pct = 8.0
	}
	target2 := price + tp2Amount
	tp2Pct = tp2Amount / price * 100
	rrRatio := tp1Amount / (price - stopLoss)

	var direction string
	if isAboveSMA {
		direction = "상승 (KODEX 200)"
	} else {
		direction = "하락 (인버스)"
	}
	reason := fmt.Sprintf("[KR-ETF] KODEX200 ₩%.0f vs SMA200 ₩%.0f (%+.1f%%) → %s, ATR=₩%.0f",
		benchPrice, benchSMA200, pctFromSMA, direction, atr)

	return &Signal{
		Stock:       stock,
		Type:        SignalBuy,
		Strategy:    s.Name(),
		Strength:    70,
		Probability: 60,
		Reason:      reason,
		Details: map[string]float64{
			"kodex200_price":  benchPrice,
			"kodex200_sma200": benchSMA200,
			"pct_from_sma":    pctFromSMA,
			"regime":          0,
		},
		Guide: &TradeGuide{
			EntryPrice:      price,
			EntryType:       "market",
			StopLoss:        stopLoss,
			StopLossPct:     stopLossPct,
			Target1:         target1,
			Target1Pct:      target1Pct,
			Target2:         target2,
			Target2Pct:      tp2Pct,
			RiskRewardRatio: rrRatio,
			EntryATR:        atr,
		},
		Candles: trimCandles(candles, 60),
	}, nil
}

// calcReturn calculates N-day return for a symbol.
// Returns (return, latestPrice, candles, error).
func (s *ETFMomentumStrategy) calcReturn(ctx context.Context, symbol string, days int) (float64, float64, []model.Candle, error) {
	candles, err := s.provider.GetDailyCandles(ctx, symbol, days+10) // buffer
	if err != nil {
		return 0, 0, nil, err
	}
	if len(candles) < days {
		return 0, 0, candles, fmt.Errorf("insufficient data for %s: %d < %d", symbol, len(candles), days)
	}

	latest := candles[len(candles)-1].Close
	oldest := candles[len(candles)-days].Close
	if oldest == 0 {
		return 0, latest, candles, nil
	}
	ret := (latest - oldest) / oldest
	return ret, latest, candles, nil
}

// trimCandles returns the last N candles for chart display
func trimCandles(candles []model.Candle, n int) []model.Candle {
	if len(candles) <= n {
		return candles
	}
	return candles[len(candles)-n:]
}

// IsLastTradingDayOfMonth checks if today is the last trading day of the month.
// Simple heuristic: next calendar day is in a different month.
func IsLastTradingDayOfMonth() bool {
	now := time.Now()
	tomorrow := now.AddDate(0, 0, 1)
	return now.Month() != tomorrow.Month()
}

// GetCapitalTier determines the strategy tier based on market and capital.
// Capital=0 means unknown/unspecified → returns "full" (backward compatible).
func GetCapitalTier(market string, capital float64) string {
	if capital <= 0 {
		return "full" // 자본 미지정 시 기존 동작 유지
	}

	switch market {
	case "kr":
		switch {
		case capital < 500000:
			return "etf"
		case capital < 5000000:
			return "hybrid"
		default:
			return "full"
		}
	case "crypto":
		switch {
		case capital < 200000:
			return "btc-only"
		case capital < 1000000:
			return "extended"
		default:
			return "full"
		}
	default: // us
		switch {
		case capital < 500:
			return "etf"
		case capital < 5000:
			return "hybrid"
		default:
			return "full"
		}
	}
}
