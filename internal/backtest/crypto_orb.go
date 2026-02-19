package backtest

import (
	"fmt"
	"math"
	"sort"
	"time"

	"traveler/pkg/model"
)

// CryptoORBConfig holds all backtester parameters
type CryptoORBConfig struct {
	// ORB strategy
	ORCollectMin   int     // OR collection minutes (default 30)
	ORMinRange     float64 // Min range % (default 0.3)
	ORMaxRange     float64 // Max range % (default 5.0)
	MaxBreakoutPct float64 // Max late entry % (default 2.0)

	// DipBuy strategy
	DipBuyEnabled  bool
	DipMinDrop     float64 // Min drop % (default -3.0)
	DipLookbackMin int     // Lookback window minutes (default 30)
	DipStopPct     float64 // Stop loss % (default 1.5)
	DipTargetPct   float64 // Target % (default 2.0)

	// Position management
	MaxPositions int     // Max concurrent (default 3)
	MaxDailyLoss float64 // Daily loss limit % (default 3.0)

	// Sizing
	InitialCapital float64 // (default 100000)
	RiskPerTrade   float64 // fraction (default 0.02)
	MaxPositionPct float64 // fraction (default 0.20)
	MinRiskReward  float64 // (default 1.5)

	// Costs
	Commission float64 // Round-trip fraction (default 0.001)

	// Execution
	CandleInterval int // Minutes per candle (default 5)

	// Strategy selection
	EnableORB    bool
	EnableDipBuy bool
}

// DefaultCryptoORBConfig returns production-matching defaults
func DefaultCryptoORBConfig() CryptoORBConfig {
	return CryptoORBConfig{
		ORCollectMin:   30,
		ORMinRange:     0.3,
		ORMaxRange:     5.0,
		MaxBreakoutPct: 2.0,
		DipBuyEnabled:  false,
		DipMinDrop:     -3.0,
		DipLookbackMin: 30,
		DipStopPct:     1.5,
		DipTargetPct:   2.0,
		MaxPositions:   3,
		MaxDailyLoss:   3.0,
		InitialCapital: 100000,
		RiskPerTrade:   0.02,
		MaxPositionPct: 0.20,
		MinRiskReward:  1.5,
		Commission:     0.001,
		CandleInterval: 5,
		EnableORB:      true,
		EnableDipBuy:   false,
	}
}

// CryptoTrade represents a single backtested trade
type CryptoTrade struct {
	Symbol     string    `json:"symbol"`
	Strategy   string    `json:"strategy"` // "orb" or "dipbuy"
	EntryTime  time.Time `json:"entry_time"`
	ExitTime   time.Time `json:"exit_time"`
	EntryPrice float64   `json:"entry_price"`
	ExitPrice  float64   `json:"exit_price"`
	StopLoss   float64   `json:"stop_loss"`
	Target1    float64   `json:"target1"`
	Target2    float64   `json:"target2"`
	Quantity   float64   `json:"quantity"`
	PnL        float64   `json:"pnl"`
	PnLPct     float64   `json:"pnl_pct"`
	RMultiple  float64   `json:"r_multiple"`
	Commission float64   `json:"commission"`
	IsWin      bool      `json:"is_win"`
	ExitReason string    `json:"exit_reason"` // "stop", "target1", "target2", "eod", "daily_limit"
	ORHigh     float64   `json:"or_high"`
	ORLow      float64   `json:"or_low"`
	HoldMin    int       `json:"hold_min"`
}

// activeTrade tracks an open position during simulation
type activeTrade struct {
	symbol     string
	strategy   string
	entryTime  time.Time
	entryPrice float64
	stop       float64
	target1    float64
	target2    float64
	qty        float64
	origQty    float64
	t1Hit      bool
	orHigh     float64
	orLow      float64
	riskPerUnit float64
}

// CryptoBacktester runs ORB/DipBuy backtests on historical crypto data
type CryptoBacktester struct {
	config CryptoORBConfig
}

// NewCryptoBacktester creates a new backtester
func NewCryptoBacktester(cfg CryptoORBConfig) *CryptoBacktester {
	return &CryptoBacktester{config: cfg}
}

// Run executes the backtest across all symbols and dates.
// dayData: map[symbol]map[dateStr][]Candle (5-min candles per day, sorted ascending)
func (bt *CryptoBacktester) Run(dayData map[string]map[string][]model.Candle) *CryptoBacktestResult {
	cfg := bt.config
	capital := cfg.InitialCapital
	equity := []float64{capital}
	peakCapital := capital

	var allTrades []CryptoTrade

	// Collect all dates across all symbols
	dateSet := make(map[string]bool)
	for _, dates := range dayData {
		for d := range dates {
			dateSet[d] = true
		}
	}
	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	// Simulate day by day
	for _, dateStr := range dates {
		dayTrades := bt.simulateDay(dateStr, dayData, capital)
		for _, t := range dayTrades {
			capital += t.PnL - t.Commission
			allTrades = append(allTrades, t)
			equity = append(equity, capital)
			if capital > peakCapital {
				peakCapital = capital
			}
		}
	}

	result := bt.buildResult(allTrades, equity, dates)
	return result
}

// simulateDay runs a single day simulation across all symbols.
func (bt *CryptoBacktester) simulateDay(dateStr string, dayData map[string]map[string][]model.Candle, capital float64) []CryptoTrade {
	cfg := bt.config
	var closedTrades []CryptoTrade
	var active []activeTrade
	dailyPnL := 0.0
	orbTriggered := make(map[string]bool) // per symbol dedup
	dipTriggered := make(map[string]bool)

	// Collect candles per symbol for this day, determine OR boundaries
	type symbolDay struct {
		symbol  string
		candles []model.Candle
		orHigh  float64
		orLow   float64
		orValid bool
	}

	var symDays []symbolDay
	orCandleCount := cfg.ORCollectMin / cfg.CandleInterval // e.g. 30/5 = 6

	for sym, dates := range dayData {
		candles, ok := dates[dateStr]
		if !ok || len(candles) <= orCandleCount {
			continue
		}

		// Collect OR from first N candles
		orHigh := 0.0
		orLow := math.MaxFloat64
		for i := 0; i < orCandleCount && i < len(candles); i++ {
			if candles[i].High > orHigh {
				orHigh = candles[i].High
			}
			if candles[i].Low < orLow {
				orLow = candles[i].Low
			}
		}

		rangePct := 0.0
		orValid := false
		if orLow > 0 {
			rangePct = (orHigh - orLow) / orLow * 100
			orValid = rangePct >= cfg.ORMinRange && rangePct <= cfg.ORMaxRange
		}
		_ = rangePct

		symDays = append(symDays, symbolDay{
			symbol: sym, candles: candles,
			orHigh: orHigh, orLow: orLow, orValid: orValid,
		})
	}

	if len(symDays) == 0 {
		return nil
	}

	// Find max candle count for time stepping
	maxCandles := 0
	for _, sd := range symDays {
		if len(sd.candles) > maxCandles {
			maxCandles = len(sd.candles)
		}
	}

	// DipBuy price history per symbol (sliding window of recent close prices)
	dipHistory := make(map[string][]pricePoint)

	// Step through candles after OR period
	for candleIdx := orCandleCount; candleIdx < maxCandles; candleIdx++ {
		// Check daily loss limit
		if capital > 0 && (-dailyPnL/capital*100) >= cfg.MaxDailyLoss {
			// Force close all active
			for _, a := range active {
				for _, sd := range symDays {
					if sd.symbol == a.symbol && candleIdx < len(sd.candles) {
						closePrice := sd.candles[candleIdx].Close
						t := bt.closeTrade(&a, sd.candles[candleIdx].Time, closePrice, "daily_limit")
						closedTrades = append(closedTrades, t)
						dailyPnL += t.PnL - t.Commission
						break
					}
				}
			}
			active = nil
			break
		}

		for _, sd := range symDays {
			if candleIdx >= len(sd.candles) {
				continue
			}
			candle := sd.candles[candleIdx]

			// --- Check exits for active trades on this symbol ---
			newActive := make([]activeTrade, 0, len(active))
			for i := range active {
				a := &active[i]
				if a.symbol != sd.symbol {
					newActive = append(newActive, *a)
					continue
				}

				closed := false

				// Stop loss check
				if candle.Low <= a.stop {
					closePrice := a.stop
					t := bt.closeTrade(a, candle.Time, closePrice, "stop")
					closedTrades = append(closedTrades, t)
					dailyPnL += t.PnL - t.Commission
					closed = true
				}

				// T1 partial exit
				if !closed && !a.t1Hit && candle.High >= a.target1 {
					halfQty := a.origQty * 0.5
					pnlPartial := halfQty * (a.target1 - a.entryPrice)
					commPartial := halfQty * a.entryPrice * cfg.Commission
					a.qty -= halfQty
					a.t1Hit = true
					a.stop = a.entryPrice // move stop to breakeven

					closedTrades = append(closedTrades, CryptoTrade{
						Symbol: sd.symbol, Strategy: a.strategy,
						EntryTime: a.entryTime, ExitTime: candle.Time,
						EntryPrice: a.entryPrice, ExitPrice: a.target1,
						StopLoss: a.stop, Target1: a.target1, Target2: a.target2,
						Quantity: halfQty, PnL: pnlPartial, Commission: commPartial,
						PnLPct:    pnlPartial / (halfQty * a.entryPrice) * 100,
						RMultiple: (a.target1 - a.entryPrice) / a.riskPerUnit,
						IsWin: true, ExitReason: "target1",
						ORHigh: a.orHigh, ORLow: a.orLow,
						HoldMin: int(candle.Time.Sub(a.entryTime).Minutes()),
					})
					dailyPnL += pnlPartial - commPartial

					if a.qty <= 0 {
						closed = true
					}
				}

				// T2 full exit
				if !closed && a.t1Hit && candle.High >= a.target2 {
					t := bt.closeTrade(a, candle.Time, a.target2, "target2")
					closedTrades = append(closedTrades, t)
					dailyPnL += t.PnL - t.Commission
					closed = true
				}

				if !closed {
					newActive = append(newActive, *a)
				}
			}
			active = newActive

			// --- Check new entries ---
			// ORB entry
			if cfg.EnableORB && sd.orValid && !orbTriggered[sd.symbol] && len(active) < cfg.MaxPositions {
				if sig := bt.checkORBSignal(sd, candle); sig != nil {
					if trade := bt.openTrade(sig, candle.Time, capital); trade != nil {
						active = append(active, *trade)
						orbTriggered[sd.symbol] = true
					}
				}
			}

			// DipBuy entry
			if cfg.EnableDipBuy && !dipTriggered[sd.symbol] && len(active) < cfg.MaxPositions {
				dipHistory[sd.symbol] = append(dipHistory[sd.symbol], pricePoint{candle.Time, candle.Close})
				// Trim to lookback window
				cutoff := candle.Time.Add(-time.Duration(cfg.DipLookbackMin) * time.Minute)
				trimmed := dipHistory[sd.symbol]
				startIdx := 0
				for i, p := range trimmed {
					if p.t.After(cutoff) {
						startIdx = i
						break
					}
				}
				if startIdx > 0 {
					dipHistory[sd.symbol] = trimmed[startIdx:]
				}

				if sig := bt.checkDipBuySignal(sd.symbol, dipHistory[sd.symbol], candle, sd.candles, candleIdx); sig != nil {
					if trade := bt.openTrade(sig, candle.Time, capital); trade != nil {
						active = append(active, *trade)
						dipTriggered[sd.symbol] = true
					}
				}
			}
		}

		// Force close at end of day (last candle)
		if candleIdx == maxCandles-1 {
			for _, a := range active {
				for _, sd := range symDays {
					if sd.symbol == a.symbol && candleIdx < len(sd.candles) {
						closePrice := sd.candles[candleIdx].Close
						t := bt.closeTrade(&a, sd.candles[candleIdx].Time, closePrice, "eod")
						closedTrades = append(closedTrades, t)
						dailyPnL += t.PnL - t.Commission
						break
					}
				}
			}
			active = nil
		}
	}

	return closedTrades
}

type pricePoint struct {
	t     time.Time
	price float64
}

type signalInfo struct {
	symbol   string
	strategy string
	entry    float64
	stop     float64
	target1  float64
	target2  float64
	rr       float64
	orHigh   float64
	orLow    float64
}

// checkORBSignal replicates production checkORB logic
func (bt *CryptoBacktester) checkORBSignal(sd struct {
	symbol  string
	candles []model.Candle
	orHigh  float64
	orLow   float64
	orValid bool
}, candle model.Candle) *signalInfo {

	if candle.Close <= sd.orHigh {
		return nil
	}

	breakoutPct := (candle.Close - sd.orHigh) / sd.orHigh * 100
	if breakoutPct > bt.config.MaxBreakoutPct {
		return nil
	}

	entry := candle.Close
	rangeWidth := sd.orHigh - sd.orLow
	stop := sd.orLow + rangeWidth*0.5
	target1 := entry + rangeWidth
	target2 := entry + rangeWidth*1.5

	risk := entry - stop
	reward := target1 - entry
	if risk <= 0 || reward/risk < 1.0 {
		return nil
	}

	rr := reward / risk
	if rr < bt.config.MinRiskReward {
		return nil
	}

	return &signalInfo{
		symbol: sd.symbol, strategy: "orb",
		entry: entry, stop: stop, target1: target1, target2: target2,
		rr: rr, orHigh: sd.orHigh, orLow: sd.orLow,
	}
}

// checkDipBuySignal replicates production checkDipBuy logic
func (bt *CryptoBacktester) checkDipBuySignal(symbol string, history []pricePoint, candle model.Candle, allCandles []model.Candle, idx int) *signalInfo {
	if len(history) < 6 {
		return nil
	}

	// Find recent high
	recentHigh := 0.0
	for _, p := range history {
		if p.price > recentHigh {
			recentHigh = p.price
		}
	}
	if recentHigh <= 0 {
		return nil
	}

	dropPct := (candle.Close - recentHigh) / recentHigh * 100
	if dropPct > bt.config.DipMinDrop {
		return nil
	}

	// Bottoming: current > prev, prev <= prev-1
	if idx < 2 {
		return nil
	}
	curr := candle.Close
	prev1 := allCandles[idx-1].Close
	prev2 := allCandles[idx-2].Close
	if !(curr > prev1 && prev1 <= prev2) {
		return nil
	}

	// Daily change filter (use first candle as prev close proxy)
	prevClose := allCandles[0].Open
	if prevClose > 0 {
		dailyChange := (curr - prevClose) / prevClose * 100
		if dailyChange < -8 {
			return nil
		}
	}

	entry := curr
	stop := entry * (1 - bt.config.DipStopPct/100)
	target1 := entry * (1 + bt.config.DipTargetPct/100)
	target2 := entry * (1 + bt.config.DipTargetPct*1.5/100)

	risk := entry - stop
	reward := target1 - entry
	if risk <= 0 || reward/risk < bt.config.MinRiskReward {
		return nil
	}

	return &signalInfo{
		symbol: symbol, strategy: "dipbuy",
		entry: entry, stop: stop, target1: target1, target2: target2,
		rr: reward / risk,
	}
}

// openTrade sizes and creates an active trade (fractional qty for crypto)
func (bt *CryptoBacktester) openTrade(sig *signalInfo, t time.Time, capital float64) *activeTrade {
	cfg := bt.config
	stopDist := sig.entry - sig.stop
	if stopDist <= 0 {
		return nil
	}

	riskBudget := capital * cfg.RiskPerTrade
	qtyByRisk := riskBudget / stopDist

	maxPosValue := capital * cfg.MaxPositionPct
	qtyByAlloc := maxPosValue / sig.entry

	qty := math.Min(qtyByRisk, qtyByAlloc)
	if qty <= 0 {
		return nil
	}

	return &activeTrade{
		symbol: sig.symbol, strategy: sig.strategy,
		entryTime: t, entryPrice: sig.entry,
		stop: sig.stop, target1: sig.target1, target2: sig.target2,
		qty: qty, origQty: qty,
		orHigh: sig.orHigh, orLow: sig.orLow,
		riskPerUnit: stopDist,
	}
}

// closeTrade creates a CryptoTrade from closing an active trade
func (bt *CryptoBacktester) closeTrade(a *activeTrade, exitTime time.Time, exitPrice float64, reason string) CryptoTrade {
	grossPnL := a.qty * (exitPrice - a.entryPrice)
	comm := a.qty * a.entryPrice * bt.config.Commission
	netPnL := grossPnL - comm

	return CryptoTrade{
		Symbol: a.symbol, Strategy: a.strategy,
		EntryTime: a.entryTime, ExitTime: exitTime,
		EntryPrice: a.entryPrice, ExitPrice: exitPrice,
		StopLoss: a.stop, Target1: a.target1, Target2: a.target2,
		Quantity: a.qty, PnL: netPnL, Commission: comm,
		PnLPct:    netPnL / (a.qty * a.entryPrice) * 100,
		RMultiple: (exitPrice - a.entryPrice) / a.riskPerUnit,
		IsWin:     netPnL > 0,
		ExitReason: reason,
		ORHigh: a.orHigh, ORLow: a.orLow,
		HoldMin: int(exitTime.Sub(a.entryTime).Minutes()),
	}
}

// buildResult computes all stats from trades
func (bt *CryptoBacktester) buildResult(trades []CryptoTrade, equity []float64, dates []string) *CryptoBacktestResult {
	result := &CryptoBacktestResult{
		Config:         bt.config,
		Period:         fmt.Sprintf("%s ~ %s (%d days)", dates[0], dates[len(dates)-1], len(dates)),
		InitialCapital: bt.config.InitialCapital,
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

		// Per-symbol
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

	// Max drawdown
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

	// Kelly
	if result.AvgLoss > 0 {
		winProb := result.WinRate / 100
		b := result.AvgWin / result.AvgLoss
		result.KellyOptimal = math.Max(0, (winProb*b-(1-winProb))/b)
		result.KellyHalf = result.KellyOptimal / 2
	}

	// Sharpe (annualized, using trade returns)
	if len(trades) > 1 {
		returns := make([]float64, len(trades))
		for i, t := range trades {
			returns[i] = t.PnLPct
		}
		avg := cryptoAvg(returns)
		std := cryptoStddev(returns)
		if std > 0 {
			result.SharpeRatio = (avg / std) * math.Sqrt(365) // crypto = 365 days
		}
	}

	result.AvgHoldMin = float64(totalHold) / float64(len(trades))

	// Symbol breakdown
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

	// Exit reasons
	result.ExitReasons = exitReasons

	return result
}

func cryptoAvg(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func cryptoStddev(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	a := cryptoAvg(v)
	s := 0.0
	for _, x := range v {
		s += (x - a) * (x - a)
	}
	return math.Sqrt(s / float64(len(v)-1))
}
