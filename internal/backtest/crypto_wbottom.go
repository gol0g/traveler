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

// CryptoWBottomConfig holds W-Bottom strategy backtest parameters
type CryptoWBottomConfig struct {
	// W-Bottom detection
	WBottomTolerance float64 // Max % difference between two lows (default 3.0)
	WBottomMinDays   int     // Min days between two lows (default 5)
	WBottomMaxDays   int     // Max days between two lows (default 25)
	NecklineBreakPct float64 // Min % above neckline for entry (default 0.5)

	// Confluence requirements
	MinConfluence int // Min confluence score to enter (default 3 of 6)

	// Stops
	ATRStopMultiple float64 // ATR(14) × this = stop distance (default 2.5)

	// Targets
	PatternTargetMul float64 // T1 = entry + pattern_height × this (default 1.0)
	ExtendedMul      float64 // T2 = entry + pattern_height × this (default 1.5)

	// Position sizing (bear-market conservative)
	InitialCapital float64
	RiskPerTrade   float64 // fraction (default 0.005 = 0.5% for bear)
	MaxPositionPct float64 // fraction (default 0.15)
	MaxPositions   int     // (default 3)
	MaxHoldDays    int     // Force close after N days (default 15)

	// Costs
	Commission float64 // Round-trip fraction (default 0.001)
}

// DefaultCryptoWBottomConfig returns bear-market conservative defaults
func DefaultCryptoWBottomConfig() CryptoWBottomConfig {
	return CryptoWBottomConfig{
		WBottomTolerance: 5.0,
		WBottomMinDays:   3,
		WBottomMaxDays:   30,
		NecklineBreakPct: 0.5,
		MinConfluence:    2,
		ATRStopMultiple:  2.5,
		PatternTargetMul: 0.5,
		ExtendedMul:      0.8,
		InitialCapital:   100000,
		RiskPerTrade:     0.005,
		MaxPositionPct:   0.15,
		MaxPositions:     3,
		MaxHoldDays:      20,
		Commission:       0.001,
	}
}

// wBottomSignal represents a detected W-bottom with entry levels
type wBottomSignal struct {
	entry     float64
	stop      float64
	target1   float64
	target2   float64
	neckline  float64
	low1Price float64
	low2Price float64
	low1Day   int
	low2Day   int
	score     int    // confluence score
	reasons   string // what signals confirmed
}

// wBottomActiveTrade tracks an open W-bottom position
type wBottomActiveTrade struct {
	symbol     string
	entryTime  time.Time
	entryPrice float64
	stop       float64
	target1    float64
	target2    float64
	qty        float64
	origQty    float64
	t1Hit      bool
	riskPerUnit float64
	daysHeld   int
}

// CryptoWBottomBacktester backtests W-Bottom strategy on daily candles
type CryptoWBottomBacktester struct {
	config   CryptoWBottomConfig
	provider provider.Provider
}

// NewCryptoWBottomBacktester creates a new W-Bottom backtester
func NewCryptoWBottomBacktester(cfg CryptoWBottomConfig, p provider.Provider) *CryptoWBottomBacktester {
	return &CryptoWBottomBacktester{config: cfg, provider: p}
}

// Run fetches daily candles and simulates W-Bottom strategy
func (bt *CryptoWBottomBacktester) Run(ctx context.Context, symbols []string, days int) *CryptoBacktestResult {
	cfg := bt.config
	capital := cfg.InitialCapital
	equity := []float64{capital}

	// Need extra history for pattern detection + indicators
	fetchDays := days + 80
	symbolCandles := make(map[string][]model.Candle)

	fmt.Println("Fetching daily candles...")
	for _, sym := range symbols {
		candles, err := bt.provider.GetDailyCandles(ctx, sym, fetchDays)
		if err != nil {
			fmt.Printf("  %s: error %v\n", sym, err)
			continue
		}
		if len(candles) >= 60 {
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

	// Reference candles for date iteration
	var refCandles []model.Candle
	for _, c := range symbolCandles {
		if len(c) > len(refCandles) {
			refCandles = c
		}
	}

	startIdx := len(refCandles) - days
	if startIdx < 60 {
		startIdx = 60
	}

	var allTrades []CryptoTrade
	var active []wBottomActiveTrade
	var dates []string

	// Day-by-day simulation
	for i := startIdx; i < len(refCandles); i++ {
		today := refCandles[i].Time
		dateStr := today.Format("2006-01-02")
		dates = append(dates, dateStr)

		// --- Check exits for active trades ---
		newActive := make([]wBottomActiveTrade, 0, len(active))
		for _, a := range active {
			candles, ok := symbolCandles[a.symbol]
			if !ok {
				newActive = append(newActive, a)
				continue
			}

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

			// T1 partial
			if !closed && !a.t1Hit && todayCandle.High >= a.target1 {
				halfQty := a.origQty * 0.5
				pnl := halfQty * (a.target1 - a.entryPrice)
				comm := halfQty * a.entryPrice * cfg.Commission

				allTrades = append(allTrades, CryptoTrade{
					Symbol: a.symbol, Strategy: "wbottom",
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

		holdingSyms := make(map[string]bool)
		for _, a := range active {
			holdingSyms[a.symbol] = true
		}

		for sym, candles := range symbolCandles {
			if holdingSyms[sym] || len(active) >= cfg.MaxPositions {
				continue
			}

			idx := findCandleIdx(candles, today)
			if idx < 60 {
				continue
			}

			window := candles[:idx+1]
			sig := bt.detectWBottom(window)
			if sig == nil {
				continue
			}

			// Size position with ATR-based stop
			stopDist := sig.entry - sig.stop
			if stopDist <= 0 {
				continue
			}

			riskBudget := capital * cfg.RiskPerTrade
			qty := riskBudget / stopDist
			maxQty := capital * cfg.MaxPositionPct / sig.entry
			qty = math.Min(qty, maxQty)
			if qty <= 0 {
				continue
			}

			active = append(active, wBottomActiveTrade{
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

	sort.Slice(allTrades, func(i, j int) bool {
		return allTrades[i].EntryTime.Before(allTrades[j].EntryTime)
	})

	return bt.buildResult(allTrades, equity, dates)
}

// detectWBottom scans for W-bottom pattern with confluence scoring
func (bt *CryptoWBottomBacktester) detectWBottom(candles []model.Candle) *wBottomSignal {
	cfg := bt.config
	n := len(candles)
	if n < 40 {
		return nil
	}

	current := candles[n-1]
	price := current.Close

	// Calculate indicators on full window
	ind := strategy.CalculateIndicators(candles)
	atr := ind.ATR14
	if atr <= 0 {
		return nil
	}

	// Trend filters for bear market:
	// 1. MA20 slope not strongly negative (decline is slowing)
	if ind.MA20Slope < -5.0 {
		return nil
	}
	// 2. RSI shows recovery from oversold (not still in free fall)
	if ind.RSI14 < 30 {
		return nil
	}
	// 3. Price higher than 5 days ago (short-term uptrend confirmation)
	if n > 5 && price <= candles[n-6].Close {
		return nil
	}

	// Look for two swing lows in the lookback window
	// A swing low: candle[i].Low < candle[i-1].Low AND candle[i].Low < candle[i+1].Low
	lookback := cfg.WBottomMaxDays + 10
	if lookback > n-5 {
		lookback = n - 5
	}

	type swingLow struct {
		idx   int
		price float64
		vol   int64
		rsi   float64
	}

	var swingLows []swingLow
	startScan := n - lookback
	if startScan < 3 {
		startScan = 3
	}

	// Pre-calculate RSI series for divergence detection
	rsiSeries := strategy.CalculateRSISeries(candles, 14)

	for i := startScan; i < n-1; i++ {
		// Swing low: lower than both neighbors
		if candles[i].Low <= candles[i-1].Low && candles[i].Low <= candles[i+1].Low {
			rsi := 50.0
			rsiIdx := i - 15 // rsiSeries starts at candle index 15
			if rsiSeries != nil && rsiIdx >= 0 && rsiIdx < len(rsiSeries) {
				rsi = rsiSeries[rsiIdx]
			}
			swingLows = append(swingLows, swingLow{
				idx:   i,
				price: candles[i].Low,
				vol:   candles[i].Volume,
				rsi:   rsi,
			})
		}
	}

	if len(swingLows) < 2 {
		return nil
	}

	// Find the best W-bottom pair (most recent valid pair)
	var bestSig *wBottomSignal
	_ = cfg.NecklineBreakPct // used for future refinement

	for i := len(swingLows) - 1; i >= 1; i-- {
		low2 := swingLows[i]
		for j := i - 1; j >= 0; j-- {
			low1 := swingLows[j]

			daysBetween := low2.idx - low1.idx
			if daysBetween < cfg.WBottomMinDays || daysBetween > cfg.WBottomMaxDays {
				continue
			}

			// Two lows within tolerance %
			pctDiff := math.Abs(low2.price-low1.price) / low1.price * 100
			if pctDiff > cfg.WBottomTolerance {
				continue
			}

			// Ascending bottom preference: second low should be same or higher
			// (selling pressure decreasing, not a lower-low downtrend)
			if low2.price < low1.price*0.97 {
				continue
			}

			// Find neckline (highest high between the two lows)
			neckline := 0.0
			for k := low1.idx + 1; k < low2.idx; k++ {
				if candles[k].High > neckline {
					neckline = candles[k].High
				}
			}
			if neckline <= 0 {
				continue
			}

			// Price should be recovering from second low toward neckline
			// In bear markets, don't require full neckline break — enter on recovery
			bottomLevel := math.Min(low1.price, low2.price)
			recovery := neckline - bottomLevel
			if recovery <= 0 {
				continue
			}
			recoveryPct := (price - bottomLevel) / recovery
			// Require at least 30% recovery toward neckline
			if recoveryPct < 0.3 {
				continue
			}
			// Must be above second low (confirming bounce)
			if price <= low2.price*1.01 {
				continue
			}
			necklineBreakLevel := neckline * (1 + cfg.NecklineBreakPct/100)
			_ = necklineBreakLevel

			// --- Confluence scoring ---
			score := 0
			var reasons []string

			// 1. RSI divergence: second low has higher RSI than first low (seller exhaustion)
			if low2.rsi > low1.rsi+1 {
				score++
				reasons = append(reasons, "RSI-div")
			}

			// 2. Volume divergence: declining volume on second low (less selling pressure)
			if low2.vol < low1.vol {
				score++
				reasons = append(reasons, "Vol-div")
			}

			// 3. MACD turning: histogram improving (less negative or turning positive)
			macdHist := strategy.CalculateMACDSeries(candles, 12, 26, 9)
			if macdHist != nil && len(macdHist) >= 3 {
				recent := macdHist[len(macdHist)-1]
				prev := macdHist[len(macdHist)-3]
				if recent > prev {
					score++
					reasons = append(reasons, "MACD-turn")
				}
			}

			// 4. BB reclaim: price was near/below BB lower, now recovering
			if ind.BBLower > 0 && (low2.price < ind.BBLower*1.02) && price > ind.BBLower {
				score++
				reasons = append(reasons, "BB-reclaim")
			}

			// 5. Reversal candle: today is green (buyer presence)
			if current.Close > current.Open {
				score++
				reasons = append(reasons, "reversal")
			}

			// 6. Volume spike: today's volume > 1.3× average (accumulation)
			if ind.AvgVol > 0 && float64(current.Volume) > ind.AvgVol*1.3 {
				score++
				reasons = append(reasons, "vol-spike")
			}

			if score < cfg.MinConfluence {
				continue
			}

			// For low-confluence signals (score=2), require RSI has recovered above 30
			// to avoid entering during active selling
			if score <= 2 && ind.RSI14 < 30 {
				continue
			}

			// Calculate levels
			entry := price

			// ATR-based stop
			stop := entry - atr*cfg.ATRStopMultiple
			if stop > bottomLevel*0.99 {
				stop = bottomLevel * 0.99 // Don't set stop above pattern lows
			}

			// Pattern height projection for targets
			patternHeight := neckline - bottomLevel
			target1 := entry + patternHeight*cfg.PatternTargetMul
			target2 := entry + patternHeight*cfg.ExtendedMul

			stopDist := entry - stop
			if stopDist <= 0 {
				continue
			}
			rr := (target1 - entry) / stopDist
			if rr < 1.0 {
				continue
			}
			_ = rr

			reasonStr := ""
			for k, r := range reasons {
				if k > 0 {
					reasonStr += "+"
				}
				reasonStr += r
			}

			sig := &wBottomSignal{
				entry:     entry,
				stop:      stop,
				target1:   target1,
				target2:   target2,
				neckline:  neckline,
				low1Price: low1.price,
				low2Price: low2.price,
				low1Day:   low1.idx,
				low2Day:   low2.idx,
				score:     score,
				reasons:   reasonStr,
			}

			if bestSig == nil || sig.score > bestSig.score {
				bestSig = sig
			}

			break // Take the most recent low1 for this low2
		}

		if bestSig != nil {
			break // Take the most recent W-bottom
		}
	}

	return bestSig
}

func (bt *CryptoWBottomBacktester) makeClosedTrade(a *wBottomActiveTrade, exitTime time.Time, exitPrice float64, reason string) CryptoTrade {
	pnl := a.qty * (exitPrice - a.entryPrice)
	comm := a.qty * a.entryPrice * bt.config.Commission
	return CryptoTrade{
		Symbol: a.symbol, Strategy: "wbottom",
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

func (bt *CryptoWBottomBacktester) buildResult(trades []CryptoTrade, equity []float64, dates []string) *CryptoBacktestResult {
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
