package backtest

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"traveler/internal/provider"
	"traveler/internal/strategy"
	"traveler/pkg/model"
)

// CryptoRSIConfig holds RSI contrarian backtest parameters
type CryptoRSIConfig struct {
	RSIThreshold   float64 // RSI buy threshold (default 20 for bear)
	RSIPeriod      int     // RSI lookback (default 14)
	BBPeriod       int     // Bollinger period (default 20)
	MinDropFromMA  float64 // Min drop % from MA20 (default 5.0)
	StopMultiplier float64 // Stop = 20d low × this (default 0.95 = 5% below)
	MaxHoldDays    int     // Force close after N days (default 10)

	InitialCapital float64
	RiskPerTrade   float64 // fraction (default 0.02)
	MaxPositionPct float64 // fraction (default 0.20)
	MinRiskReward  float64 // (default 1.5)
	Commission     float64 // Round-trip (default 0.001)
	MaxPositions   int     // (default 3)
}

// DefaultCryptoRSIConfig returns bear-market defaults
func DefaultCryptoRSIConfig() CryptoRSIConfig {
	return CryptoRSIConfig{
		RSIThreshold:   20,
		RSIPeriod:      14,
		BBPeriod:       20,
		MinDropFromMA:  5.0,
		StopMultiplier: 0.95,
		MaxHoldDays:    10,
		InitialCapital: 100000,
		RiskPerTrade:   0.02,
		MaxPositionPct: 0.20,
		MinRiskReward:  1.5,
		Commission:     0.001,
		MaxPositions:   3,
	}
}

type rsiActiveTrade struct {
	symbol     string
	entryTime  time.Time
	entryPrice float64
	stop       float64
	target1    float64 // MA20 mean reversion
	target2    float64
	qty        float64
	origQty    float64
	t1Hit      bool
	riskPerUnit float64
	daysHeld   int
}

// CryptoRSIBacktester backtests RSI contrarian strategy on daily candles
type CryptoRSIBacktester struct {
	config   CryptoRSIConfig
	provider provider.Provider
}

// NewCryptoRSIBacktester creates a new RSI backtester
func NewCryptoRSIBacktester(cfg CryptoRSIConfig, p provider.Provider) *CryptoRSIBacktester {
	return &CryptoRSIBacktester{config: cfg, provider: p}
}

// Run fetches daily candles and simulates RSI contrarian strategy
func (bt *CryptoRSIBacktester) Run(ctx context.Context, symbols []string, days int) *CryptoBacktestResult {
	cfg := bt.config
	capital := cfg.InitialCapital
	equity := []float64{capital}

	// Fetch daily candles for all symbols (need extra 55 days for indicators)
	fetchDays := days + 60
	symbolCandles := make(map[string][]model.Candle)

	fmt.Println("Fetching daily candles...")
	for _, sym := range symbols {
		candles, err := bt.provider.GetDailyCandles(ctx, sym, fetchDays)
		if err != nil {
			fmt.Printf("  %s: error %v\n", sym, err)
			continue
		}
		if len(candles) >= 55 {
			symbolCandles[sym] = candles
			fmt.Printf("  %s: %d candles\n", sym, len(candles))
		}
		time.Sleep(150 * time.Millisecond)
	}

	if len(symbolCandles) == 0 {
		return &CryptoBacktestResult{
			Config:         CryptoORBConfig{InitialCapital: cfg.InitialCapital},
			InitialCapital: cfg.InitialCapital,
			FinalCapital:   cfg.InitialCapital,
		}
	}

	// Find common date range (last `days` trading days)
	// Use BTC dates as reference, or first available symbol
	var refCandles []model.Candle
	for _, c := range symbolCandles {
		if len(c) > len(refCandles) {
			refCandles = c
		}
	}

	startIdx := len(refCandles) - days
	if startIdx < 55 {
		startIdx = 55
	}

	var allTrades []CryptoTrade
	var active []rsiActiveTrade
	var dates []string

	// Day-by-day simulation
	for i := startIdx; i < len(refCandles); i++ {
		today := refCandles[i].Time
		dateStr := today.Format("2006-01-02")
		dates = append(dates, dateStr)

		// --- Check exits for active trades ---
		newActive := make([]rsiActiveTrade, 0, len(active))
		for _, a := range active {
			candles, ok := symbolCandles[a.symbol]
			if !ok {
				newActive = append(newActive, a)
				continue
			}

			// Find today's candle for this symbol
			todayCandle := findCandle(candles, today)
			if todayCandle == nil {
				a.daysHeld++
				newActive = append(newActive, a)
				continue
			}

			a.daysHeld++
			closed := false

			// Stop loss
			if todayCandle.Low <= a.stop {
				t := bt.makeClosedTrade(&a, today, a.stop, "stop")
				allTrades = append(allTrades, t)
				capital += t.PnL - t.Commission
				equity = append(equity, capital)
				closed = true
			}

			// T1 partial (MA20 mean reversion)
			if !closed && !a.t1Hit && todayCandle.High >= a.target1 {
				halfQty := a.origQty * 0.5
				pnl := halfQty * (a.target1 - a.entryPrice)
				comm := halfQty * a.entryPrice * cfg.Commission

				allTrades = append(allTrades, CryptoTrade{
					Symbol: a.symbol, Strategy: "rsi-contrarian",
					EntryTime: a.entryTime, ExitTime: today,
					EntryPrice: a.entryPrice, ExitPrice: a.target1,
					StopLoss: a.stop, Target1: a.target1, Target2: a.target2,
					Quantity: halfQty, PnL: pnl - comm, Commission: comm,
					PnLPct:    (pnl - comm) / (halfQty * a.entryPrice) * 100,
					RMultiple: (a.target1 - a.entryPrice) / a.riskPerUnit,
					IsWin: true, ExitReason: "target1",
					HoldMin: a.daysHeld * 24 * 60,
				})
				capital += pnl - comm
				equity = append(equity, capital)

				a.qty -= halfQty
				a.t1Hit = true
				a.stop = a.entryPrice // breakeven
				if a.qty <= 0 {
					closed = true
				}
			}

			// T2
			if !closed && a.t1Hit && todayCandle.High >= a.target2 {
				t := bt.makeClosedTrade(&a, today, a.target2, "target2")
				allTrades = append(allTrades, t)
				capital += t.PnL - t.Commission
				equity = append(equity, capital)
				closed = true
			}

			// Max hold timeout
			if !closed && a.daysHeld >= cfg.MaxHoldDays {
				t := bt.makeClosedTrade(&a, today, todayCandle.Close, "timeout")
				allTrades = append(allTrades, t)
				capital += t.PnL - t.Commission
				equity = append(equity, capital)
				closed = true
			}

			if !closed {
				newActive = append(newActive, a)
			}
		}
		active = newActive

		// --- Check new entries ---
		if len(active) >= cfg.MaxPositions {
			continue
		}

		// Check already holding symbols
		holdingSyms := make(map[string]bool)
		for _, a := range active {
			holdingSyms[a.symbol] = true
		}

		for sym, candles := range symbolCandles {
			if holdingSyms[sym] || len(active) >= cfg.MaxPositions {
				continue
			}

			// Find index of today in this symbol's candles
			idx := findCandleIdx(candles, today)
			if idx < 55 {
				continue
			}

			// Use candles up to today (no look-ahead)
			window := candles[:idx+1]
			sig := bt.checkRSISignal(window)
			if sig == nil {
				continue
			}

			// Size position
			stopDist := sig.entry - sig.stop
			if stopDist <= 0 {
				continue
			}
			rr := (sig.target1 - sig.entry) / stopDist
			if rr < cfg.MinRiskReward {
				continue
			}

			riskBudget := capital * cfg.RiskPerTrade
			qty := riskBudget / stopDist
			maxQty := capital * cfg.MaxPositionPct / sig.entry
			qty = math.Min(qty, maxQty)
			if qty <= 0 {
				continue
			}

			active = append(active, rsiActiveTrade{
				symbol: sym, entryTime: today,
				entryPrice: sig.entry, stop: sig.stop,
				target1: sig.target1, target2: sig.target2,
				qty: qty, origQty: qty,
				riskPerUnit: stopDist,
			})
		}
	}

	// Force close remaining
	for _, a := range active {
		candles := symbolCandles[a.symbol]
		closePrice := candles[len(candles)-1].Close
		t := bt.makeClosedTrade(&a, refCandles[len(refCandles)-1].Time, closePrice, "eod")
		allTrades = append(allTrades, t)
		capital += t.PnL - t.Commission
		equity = append(equity, capital)
	}

	// Sort trades by time
	sort.Slice(allTrades, func(i, j int) bool {
		return allTrades[i].EntryTime.Before(allTrades[j].EntryTime)
	})

	return bt.buildResult(allTrades, equity, dates)
}

type rsiSignal struct {
	entry   float64
	stop    float64
	target1 float64
	target2 float64
}

// checkRSISignal replicates RSIContrarianStrategy.Analyze on a candle window
func (bt *CryptoRSIBacktester) checkRSISignal(candles []model.Candle) *rsiSignal {
	if len(candles) < 55 {
		return nil
	}

	ind := strategy.CalculateIndicators(candles)
	current := candles[len(candles)-1]
	price := current.Close

	// RSI < threshold
	if ind.RSI14 >= bt.config.RSIThreshold {
		return nil
	}

	// Price below BB lower
	if ind.BBLower <= 0 || price > ind.BBLower {
		return nil
	}

	// Volume >= 50% of avg
	if ind.AvgVol > 0 && float64(current.Volume)/ind.AvgVol < 0.5 {
		return nil
	}

	// Not in free-fall: close in upper 30% or green candle
	candleRange := current.High - current.Low
	if candleRange > 0 {
		closePos := (current.Close - current.Low) / candleRange
		isGreen := current.Close > current.Open
		if closePos < 0.3 && !isGreen {
			return nil
		}
	}

	// Drop from MA20 >= 5%
	if ind.MA20 > 0 {
		drop := (ind.MA20 - price) / ind.MA20 * 100
		if drop < bt.config.MinDropFromMA {
			return nil
		}
	}

	// Calculate levels
	low20 := strategy.CalculateLowestLow(candles, 20)
	if low20 <= 0 {
		low20 = current.Low
	}
	stop := low20 * bt.config.StopMultiplier

	target1 := ind.MA20
	if target1 <= price {
		target1 = price * 1.03
	}

	target2 := ind.MA20
	if ind.BBUpper > 0 && ind.MA20 > 0 {
		target2 = (ind.MA20 + ind.BBUpper) / 2
	}
	if target2 <= target1 {
		target2 = target1 * 1.03
	}

	if price-stop <= 0 {
		return nil
	}

	return &rsiSignal{
		entry: price, stop: stop,
		target1: target1, target2: target2,
	}
}

func (bt *CryptoRSIBacktester) makeClosedTrade(a *rsiActiveTrade, exitTime time.Time, exitPrice float64, reason string) CryptoTrade {
	pnl := a.qty * (exitPrice - a.entryPrice)
	comm := a.qty * a.entryPrice * bt.config.Commission
	return CryptoTrade{
		Symbol: a.symbol, Strategy: "rsi-contrarian",
		EntryTime: a.entryTime, ExitTime: exitTime,
		EntryPrice: a.entryPrice, ExitPrice: exitPrice,
		StopLoss: a.stop, Target1: a.target1, Target2: a.target2,
		Quantity: a.qty, PnL: pnl - comm, Commission: comm,
		PnLPct:    (pnl - comm) / (a.qty * a.entryPrice) * 100,
		RMultiple: (exitPrice - a.entryPrice) / a.riskPerUnit,
		IsWin:     pnl-comm > 0,
		ExitReason: reason,
		HoldMin: a.daysHeld * 24 * 60,
	}
}

func (bt *CryptoRSIBacktester) buildResult(trades []CryptoTrade, equity []float64, dates []string) *CryptoBacktestResult {
	cfg := bt.config
	result := &CryptoBacktestResult{
		Config: CryptoORBConfig{
			InitialCapital: cfg.InitialCapital,
			EnableORB:      false,
			EnableDipBuy:   false,
		},
		Period:         fmt.Sprintf("%s ~ %s (%d days)", dates[0], dates[len(dates)-1], len(dates)),
		InitialCapital: cfg.InitialCapital,
		FinalCapital:   equity[len(equity)-1],
		Trades:         trades,
		EquityCurve:    equity,
	}

	if len(trades) == 0 {
		return result
	}

	result.TotalTrades = len(trades)
	result.TotalReturn = result.FinalCapital - result.InitialCapital
	result.TotalReturnPct = result.TotalReturn / result.InitialCapital * 100

	var totalWin, totalLoss float64
	var winStreak, loseStreak int
	var totalR float64
	var totalHold int

	symStats := make(map[string]*SymbolStats)
	exitReasons := make(map[string]int)

	for _, t := range trades {
		totalR += t.RMultiple
		totalHold += t.HoldMin
		exitReasons[t.ExitReason]++

		ss, ok := symStats[t.Symbol]
		if !ok {
			ss = &SymbolStats{Symbol: t.Symbol}
			symStats[t.Symbol] = ss
		}
		ss.Trades++
		ss.TotalPnL += t.PnL

		if t.IsWin {
			result.WinningTrades++
			totalWin += t.PnL
			if t.PnL > result.LargestWin {
				result.LargestWin = t.PnL
			}
			ss.Wins++
			winStreak++
			loseStreak = 0
			if winStreak > result.MaxWinStreak {
				result.MaxWinStreak = winStreak
			}
		} else {
			result.LosingTrades++
			totalLoss += math.Abs(t.PnL)
			if t.PnL < result.LargestLoss {
				result.LargestLoss = t.PnL
			}
			loseStreak++
			winStreak = 0
			if loseStreak > result.MaxLoseStreak {
				result.MaxLoseStreak = loseStreak
			}
		}
	}

	result.WinRate = float64(result.WinningTrades) / float64(result.TotalTrades) * 100
	if result.WinningTrades > 0 {
		result.AvgWin = totalWin / float64(result.WinningTrades)
	}
	if result.LosingTrades > 0 {
		result.AvgLoss = totalLoss / float64(result.LosingTrades)
	}
	if result.AvgLoss > 0 {
		result.RiskRewardRatio = result.AvgWin / result.AvgLoss
	}
	result.Expectancy = (result.WinRate/100*result.AvgWin) - ((100-result.WinRate)/100*result.AvgLoss)
	result.ExpectancyR = totalR / float64(result.TotalTrades)
	if totalLoss > 0 {
		result.ProfitFactor = totalWin / totalLoss
	}

	peak := equity[0]
	for _, e := range equity {
		if e > peak {
			peak = e
		}
		dd := (peak - e) / peak * 100
		if dd > result.MaxDrawdown {
			result.MaxDrawdown = dd
		}
	}

	if result.AvgLoss > 0 {
		winProb := result.WinRate / 100
		b := result.AvgWin / result.AvgLoss
		result.KellyOptimal = math.Max(0, (winProb*b-(1-winProb))/b)
		result.KellyHalf = result.KellyOptimal / 2
	}

	if len(trades) > 1 {
		returns := make([]float64, len(trades))
		for i, t := range trades {
			returns[i] = t.PnLPct
		}
		a := cryptoAvg(returns)
		s := cryptoStddev(returns)
		if s > 0 {
			result.SharpeRatio = (a / s) * math.Sqrt(365)
		}
	}

	if len(trades) > 0 {
		result.AvgHoldMin = float64(totalHold) / float64(len(trades))
	}

	result.SymbolBreakdown = make([]SymbolStats, 0, len(symStats))
	for _, ss := range symStats {
		if ss.Trades > 0 {
			ss.WinRate = float64(ss.Wins) / float64(ss.Trades) * 100
		}
		result.SymbolBreakdown = append(result.SymbolBreakdown, *ss)
	}
	sort.Slice(result.SymbolBreakdown, func(i, j int) bool {
		return result.SymbolBreakdown[i].TotalPnL > result.SymbolBreakdown[j].TotalPnL
	})

	result.ExitReasons = exitReasons
	return result
}

func findCandle(candles []model.Candle, day time.Time) *model.Candle {
	dateStr := day.Format("2006-01-02")
	for i := range candles {
		if candles[i].Time.Format("2006-01-02") == dateStr {
			return &candles[i]
		}
	}
	return nil
}

func findCandleIdx(candles []model.Candle, day time.Time) int {
	dateStr := day.Format("2006-01-02")
	for i := range candles {
		if candles[i].Time.Format("2006-01-02") == dateStr {
			return i
		}
	}
	return -1
}
