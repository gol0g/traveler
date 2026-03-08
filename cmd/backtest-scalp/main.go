package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"traveler/internal/provider"
	"traveler/internal/strategy"
	"traveler/pkg/model"
)

// backtestTrade records a completed trade.
type backtestTrade struct {
	Symbol     string
	EntryTime  time.Time
	ExitTime   time.Time
	EntryPrice float64
	ExitPrice  float64
	PnLPct     float64
	NetPnLPct  float64
	RSIAtEntry float64
	ExitReason string
	BarsHeld   int
}

// backtestResult summarizes the full backtest.
type backtestResult struct {
	Period       string
	TotalTrades  int
	Wins         int
	Losses       int
	WinRate      float64
	GrossReturn  float64
	NetReturn    float64
	AvgWinPct    float64
	AvgLossPct   float64
	BestTrade    float64
	WorstTrade   float64
	AvgBarsHeld  float64
	MaxDrawdown  float64
	ProfitFactor float64
	SharpeRatio  float64
	Commission   float64

	// By exit reason
	ExitReasons map[string]int

	// Monthly breakdown
	Monthly []monthlyReturn

	// Per-pair breakdown
	PerPair []pairStats

	// Config for display
	CandleInterval int
}

type pairStats struct {
	Symbol    string
	Trades    int
	Wins      int
	WinRate   float64
	NetPnL    float64
	AvgPnL    float64
	PF        float64
	AvgBars   float64
}

type monthlyReturn struct {
	Month    string
	Trades   int
	WinRate  float64
	NetPnL   float64
}

func main() {
	days := 90
	optimize := false
	candleInterval := 0 // 0 = use default from config
	customPairs := ""
	if len(os.Args) > 1 {
		fmt.Sscanf(os.Args[1], "%d", &days)
	}
	for _, arg := range os.Args[1:] {
		if arg == "--optimize" {
			optimize = true
		}
		if strings.HasPrefix(arg, "--interval=") {
			fmt.Sscanf(arg, "--interval=%d", &candleInterval)
		}
		if strings.HasPrefix(arg, "--days=") {
			fmt.Sscanf(arg, "--days=%d", &days)
		}
		if strings.HasPrefix(arg, "--pairs=") {
			customPairs = strings.TrimPrefix(arg, "--pairs=")
		}
	}

	cfg := strategy.DefaultScalpConfig()
	if customPairs != "" {
		cfg.Pairs = strings.Split(customPairs, ",")
	}
	if candleInterval > 0 {
		cfg.CandleInterval = candleInterval
		// Adjust CandleCount for shorter intervals (need same time window)
		cfg.CandleCount = 100 * 15 / candleInterval
		// Adjust MaxHoldBars proportionally
		cfg.MaxHoldBars = 32 * 15 / candleInterval
	}

	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Printf(" CRYPTO SCALPING BACKTEST - RSI Mean-Reversion (%dmin)\n", cfg.CandleInterval)
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println()

	ctx := context.Background()
	p := provider.NewUpbitProvider()

	// Fetch historical candles for each pair
	fmt.Println("Fetching historical data...")

	candlesPerDay := 24 * 60 / cfg.CandleInterval
	totalCandles := days * candlesPerDay
	fetchCount := totalCandles + cfg.CandleCount

	allData := make(map[string][]model.Candle)
	for _, symbol := range cfg.Pairs {
		fmt.Printf("  %s: fetching %d candles (%d days of %dmin)...", symbol, fetchCount, days, cfg.CandleInterval)
		candles, err := p.GetRecentMinuteCandles(ctx, symbol, cfg.CandleInterval, fetchCount)
		if err != nil {
			fmt.Printf(" ERROR: %v\n", err)
			continue
		}
		fmt.Printf(" got %d candles\n", len(candles))
		allData[symbol] = candles
		time.Sleep(10 * time.Second) // Upbit rate limit: 600/min, need ~44 req per pair
	}

	fmt.Println()

	if optimize {
		runOptimization(cfg, allData, days)
		return
	}

	fmt.Printf(" Period:          %d days\n", days)
	fmt.Printf(" Candle:          %d min\n", cfg.CandleInterval)
	fmt.Printf(" RSI:             period=%d, entry<%.0f, exit>%.0f\n", cfg.RSIPeriod, cfg.RSIEntry, cfg.RSIExit)
	fmt.Printf(" Volume:          >%.1fx avg (period=%d)\n", cfg.VolumeMin, cfg.VolumePeriod)
	fmt.Printf(" TP/SL:           +%.1f%% / -%.1f%%\n", cfg.TakeProfitPct, cfg.StopLossPct)
	fmt.Printf(" MaxHold:         %d bars (%.1f hours)\n", cfg.MaxHoldBars, float64(cfg.MaxHoldBars)*float64(cfg.CandleInterval)/60)
	fmt.Printf(" Commission:      %.2f%% per side\n", cfg.CommissionPct)
	fmt.Printf(" Pairs:           %d coins\n", len(cfg.Pairs))
	fmt.Println()

	fmt.Println("Running simulation...")
	result := runBacktest(cfg, allData)
	printResults(result)
}

func runBacktest(cfg strategy.ScalpConfig, allData map[string][]model.Candle) *backtestResult {
	warmup := cfg.CandleCount // need this many candles for indicators

	// Find the common start index (after warmup)
	minLen := math.MaxInt64
	for _, candles := range allData {
		if len(candles) < minLen {
			minLen = len(candles)
		}
	}

	if minLen <= warmup {
		fmt.Println("Insufficient data for backtest")
		return &backtestResult{ExitReasons: make(map[string]int), CandleInterval: cfg.CandleInterval}
	}

	var trades []backtestTrade
	activePositions := make(map[string]*simPosition)
	exitReasons := make(map[string]int)

	// Minimum candles needed for entry
	minIndicatorCandles := cfg.EMAPeriod + 1

	// Iterate through each bar
	for bar := warmup; bar < minLen; bar++ {
		// 1. Check exits for active positions
		for symbol, pos := range activePositions {
			candles := allData[symbol]
			if bar >= len(candles) {
				continue
			}

			current := candles[bar]
			pnlPct := (current.Close - pos.entryPrice) / pos.entryPrice * 100

			shouldExit := false
			reason := ""

			// Check intra-bar SL/TP using high/low
			lowPnl := (current.Low - pos.entryPrice) / pos.entryPrice * 100
			highPnl := (current.High - pos.entryPrice) / pos.entryPrice * 100

			if lowPnl <= -cfg.StopLossPct {
				shouldExit = true
				reason = "stop_loss"
				pnlPct = -cfg.StopLossPct
			} else if highPnl >= cfg.TakeProfitPct {
				shouldExit = true
				reason = "take_profit"
				pnlPct = cfg.TakeProfitPct
			}

			// RSI exit (mean reverted)
			if !shouldExit && bar-warmup >= cfg.RSIPeriod+1 {
				lookback := candles[:bar+1]
				if len(lookback) > cfg.CandleCount {
					lookback = lookback[len(lookback)-cfg.CandleCount:]
				}
				rsi := strategy.CalculateRSI(lookback, cfg.RSIPeriod)
				if rsi >= cfg.RSIExit {
					shouldExit = true
					reason = "rsi_exit"
				}
			}

			// Max hold
			if !shouldExit && (bar-pos.entryBar) >= cfg.MaxHoldBars {
				shouldExit = true
				reason = "max_hold"
			}

			if shouldExit {
				exitPrice := pos.entryPrice * (1 + pnlPct/100)
				netPnlPct := pnlPct - 2*cfg.CommissionPct

				trades = append(trades, backtestTrade{
					Symbol:     symbol,
					EntryTime:  pos.entryTime,
					ExitTime:   current.Time,
					EntryPrice: pos.entryPrice,
					ExitPrice:  exitPrice,
					PnLPct:     pnlPct,
					NetPnLPct:  netPnlPct,
					RSIAtEntry: pos.rsiAtEntry,
					ExitReason: reason,
					BarsHeld:   bar - pos.entryBar,
				})

				exitReasons[reason]++
				delete(activePositions, symbol)
			}
		}

		// 2. Check entries (if slots available)
		if len(activePositions) >= cfg.MaxPositions {
			continue
		}

		for _, symbol := range cfg.Pairs {
			if _, held := activePositions[symbol]; held {
				continue
			}
			if len(activePositions) >= cfg.MaxPositions {
				break
			}

			candles := allData[symbol]
			if bar >= len(candles) || bar < cfg.CandleCount {
				continue
			}

			// Use lookback window for indicator calculation
			lookback := candles[:bar+1]
			if len(lookback) > cfg.CandleCount {
				lookback = lookback[len(lookback)-cfg.CandleCount:]
			}

			if len(lookback) < minIndicatorCandles {
				continue
			}

			latest := lookback[len(lookback)-1]

			// RSI check
			rsi := strategy.CalculateRSI(lookback, cfg.RSIPeriod)
			if rsi >= cfg.RSIEntry {
				continue
			}

			// Volume filter
			avgVol := strategy.CalculateAvgVolume(lookback[:len(lookback)-1], cfg.VolumePeriod)
			currentVol := float64(latest.Volume)
			if avgVol <= 0 || currentVol/avgVol < cfg.VolumeMin {
				continue
			}

			// EMA trend filter — price must be above EMA
			ema := strategy.CalculateEMA(lookback, cfg.EMAPeriod)
			if ema <= 0 || latest.Close <= ema {
				continue
			}

			// Entry! (execute on NEXT bar open)
			if bar+1 < len(candles) {
				entryBar := bar + 1
				entryPrice := candles[entryBar].Open // next bar's open

				activePositions[symbol] = &simPosition{
					entryPrice: entryPrice,
					entryBar:   entryBar,
					entryTime:  candles[entryBar].Time,
					rsiAtEntry: rsi,
				}
			}
		}
	}

	// Calculate results
	result := &backtestResult{
		ExitReasons:    exitReasons,
		CandleInterval: cfg.CandleInterval,
	}

	if len(trades) == 0 {
		return result
	}

	// Period
	firstTrade := trades[0]
	lastTrade := trades[len(trades)-1]
	result.Period = fmt.Sprintf("%s ~ %s",
		firstTrade.EntryTime.Format("2006-01-02"),
		lastTrade.ExitTime.Format("2006-01-02"))

	result.TotalTrades = len(trades)

	var totalWinPct, totalLossPct float64
	var totalBarsHeld int
	var grossPnL, netPnL float64
	var grossWin, grossLoss float64

	equityCurve := make([]float64, 0, len(trades))
	cumPnL := 0.0

	for _, t := range trades {
		cumPnL += t.NetPnLPct
		equityCurve = append(equityCurve, cumPnL)

		grossPnL += t.PnLPct
		netPnL += t.NetPnLPct
		totalBarsHeld += t.BarsHeld

		if t.NetPnLPct > 0 {
			result.Wins++
			totalWinPct += t.NetPnLPct
			grossWin += t.NetPnLPct
		} else {
			result.Losses++
			totalLossPct += t.NetPnLPct
			grossLoss += math.Abs(t.NetPnLPct)
		}

		if t.NetPnLPct > result.BestTrade {
			result.BestTrade = t.NetPnLPct
		}
		if t.NetPnLPct < result.WorstTrade {
			result.WorstTrade = t.NetPnLPct
		}
	}

	result.GrossReturn = grossPnL
	result.NetReturn = netPnL
	result.Commission = grossPnL - netPnL
	result.WinRate = float64(result.Wins) / float64(result.TotalTrades) * 100
	result.AvgBarsHeld = float64(totalBarsHeld) / float64(result.TotalTrades)

	if result.Wins > 0 {
		result.AvgWinPct = totalWinPct / float64(result.Wins)
	}
	if result.Losses > 0 {
		result.AvgLossPct = totalLossPct / float64(result.Losses)
	}

	// Profit factor
	if grossLoss > 0 {
		result.ProfitFactor = grossWin / grossLoss
	}

	// Max drawdown on equity curve
	peak := 0.0
	maxDD := 0.0
	for _, eq := range equityCurve {
		if eq > peak {
			peak = eq
		}
		dd := peak - eq
		if dd > maxDD {
			maxDD = dd
		}
	}
	result.MaxDrawdown = maxDD

	// Sharpe ratio (annualized from trade returns)
	if len(trades) > 1 {
		mean := netPnL / float64(len(trades))
		var sumSq float64
		for _, t := range trades {
			diff := t.NetPnLPct - mean
			sumSq += diff * diff
		}
		stdDev := math.Sqrt(sumSq / float64(len(trades)-1))
		if stdDev > 0 {
			tradesPerYear := 5.0 * 365
			result.SharpeRatio = (mean / stdDev) * math.Sqrt(tradesPerYear)
		}
	}

	// Monthly breakdown
	monthMap := make(map[string]*monthlyReturn)
	for _, t := range trades {
		m := t.ExitTime.Format("2006-01")
		if _, ok := monthMap[m]; !ok {
			monthMap[m] = &monthlyReturn{Month: m}
		}
		mr := monthMap[m]
		mr.Trades++
		mr.NetPnL += t.NetPnLPct
		if t.NetPnLPct > 0 {
			mr.WinRate++
		}
	}
	for _, mr := range monthMap {
		if mr.Trades > 0 {
			mr.WinRate = mr.WinRate / float64(mr.Trades) * 100
		}
		result.Monthly = append(result.Monthly, *mr)
	}
	sort.Slice(result.Monthly, func(i, j int) bool {
		return result.Monthly[i].Month < result.Monthly[j].Month
	})

	// Per-pair breakdown
	pairMap := make(map[string]*pairStats)
	for _, t := range trades {
		ps, ok := pairMap[t.Symbol]
		if !ok {
			ps = &pairStats{Symbol: t.Symbol}
			pairMap[t.Symbol] = ps
		}
		ps.Trades++
		ps.NetPnL += t.NetPnLPct
		ps.AvgBars += float64(t.BarsHeld)
		if t.NetPnLPct > 0 {
			ps.Wins++
		}
	}
	for _, ps := range pairMap {
		if ps.Trades > 0 {
			ps.WinRate = float64(ps.Wins) / float64(ps.Trades) * 100
			ps.AvgPnL = ps.NetPnL / float64(ps.Trades)
			ps.AvgBars = ps.AvgBars / float64(ps.Trades)

			var grossWinP, grossLossP float64
			for _, t := range trades {
				if t.Symbol == ps.Symbol {
					if t.NetPnLPct > 0 {
						grossWinP += t.NetPnLPct
					} else {
						grossLossP += math.Abs(t.NetPnLPct)
					}
				}
			}
			if grossLossP > 0 {
				ps.PF = grossWinP / grossLossP
			}
		}
		result.PerPair = append(result.PerPair, *ps)
	}
	// Sort by net PnL descending
	sort.Slice(result.PerPair, func(i, j int) bool {
		return result.PerPair[i].NetPnL > result.PerPair[j].NetPnL
	})

	return result
}

type simPosition struct {
	entryPrice float64
	entryBar   int
	entryTime  time.Time
	rsiAtEntry float64
}

func printResults(r *backtestResult) {
	candleMin := r.CandleInterval
	if candleMin == 0 {
		candleMin = 15
	}

	fmt.Println()
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Printf(" BACKTEST RESULTS (%dmin candles)\n", candleMin)
	fmt.Println("=" + strings.Repeat("=", 59))

	if r.TotalTrades == 0 {
		fmt.Println("\n  No trades generated. Try a longer period or relaxed parameters.")
		return
	}

	fmt.Printf("\n Period:          %s\n", r.Period)
	fmt.Printf(" Total Trades:    %d\n", r.TotalTrades)
	fmt.Printf(" Win Rate:        %.1f%% (%d W / %d L)\n", r.WinRate, r.Wins, r.Losses)

	fmt.Println("\n--- Returns ---")
	fmt.Printf(" Gross Return:    %+.2f%% (sum of all trade %%)\n", r.GrossReturn)
	fmt.Printf(" Net Return:      %+.2f%% (after %.2f%% commission)\n", r.NetReturn, r.Commission)

	fmt.Println("\n--- Per-Trade ---")
	fmt.Printf(" Avg Win:         +%.3f%%\n", r.AvgWinPct)
	fmt.Printf(" Avg Loss:        %.3f%%\n", r.AvgLossPct)
	fmt.Printf(" Best Trade:      %+.3f%%\n", r.BestTrade)
	fmt.Printf(" Worst Trade:     %+.3f%%\n", r.WorstTrade)
	fmt.Printf(" Avg Hold:        %.1f bars (%.1f hours)\n", r.AvgBarsHeld, r.AvgBarsHeld*float64(candleMin)/60)

	fmt.Println("\n--- Risk ---")
	fmt.Printf(" Profit Factor:   %.2f\n", r.ProfitFactor)
	fmt.Printf(" Max Drawdown:    %.2f%%\n", r.MaxDrawdown)
	fmt.Printf(" Sharpe Ratio:    %.2f (annualized)\n", r.SharpeRatio)

	fmt.Println("\n--- Exit Reasons ---")
	reasons := []string{"take_profit", "rsi_exit", "stop_loss", "max_hold"}
	for _, reason := range reasons {
		count, ok := r.ExitReasons[reason]
		if !ok {
			count = 0
		}
		pct := float64(count) / float64(r.TotalTrades) * 100
		fmt.Printf(" %-14s:  %d (%.0f%%)\n", reason, count, pct)
	}

	if len(r.Monthly) > 0 {
		fmt.Println("\n--- Monthly Breakdown ---")
		fmt.Printf(" %-10s %6s %8s %10s\n", "Month", "Trades", "WinRate", "Net PnL%")
		fmt.Println(" " + strings.Repeat("-", 38))
		for _, m := range r.Monthly {
			fmt.Printf(" %-10s %6d %7.0f%% %+9.2f%%\n", m.Month, m.Trades, m.WinRate, m.NetPnL)
		}
	}

	// Per-pair breakdown
	if len(r.PerPair) > 0 {
		fmt.Println("\n--- Per-Pair Breakdown (sorted by Net PnL) ---")
		fmt.Printf(" %-10s %5s %5s %7s %9s %8s %6s %6s\n",
			"Pair", "Trd", "Win", "WinR%", "NetPnL%", "AvgPnL%", "PF", "Bars")
		fmt.Println(" " + strings.Repeat("-", 65))
		for _, ps := range r.PerPair {
			fmt.Printf(" %-10s %5d %5d %6.0f%% %+8.2f%% %+7.4f%% %5.2f %5.1f\n",
				ps.Symbol, ps.Trades, ps.Wins, ps.WinRate, ps.NetPnL, ps.AvgPnL, ps.PF, ps.AvgBars)
		}
	}

	// Expected value per trade
	ev := r.NetReturn / float64(r.TotalTrades)
	tradesPerDay := float64(r.TotalTrades) / 90.0
	fmt.Println("\n--- Projection ---")
	fmt.Printf(" EV per trade:    %+.4f%%\n", ev)
	fmt.Printf(" Est. trades/day: %.1f\n", tradesPerDay)
	fmt.Printf(" Est. daily:      %+.3f%%\n", ev*tradesPerDay)
	fmt.Printf(" Est. monthly:    %+.2f%%\n", ev*tradesPerDay*30)

	// Capital projection
	orderKRW := 50000.0
	evKRW := orderKRW * ev / 100.0
	fmt.Printf("\n At ₩%.0f/trade:\n", orderKRW)
	fmt.Printf(" EV per trade:    ₩%+.0f\n", evKRW)
	fmt.Printf(" Est. daily:      ₩%+.0f\n", evKRW*tradesPerDay)
	fmt.Printf(" Est. monthly:    ₩%+.0f\n", evKRW*tradesPerDay*30)

	fmt.Println("\n" + strings.Repeat("=", 60))
}

// runOptimization sweeps multiple parameter combinations
func runOptimization(baseCfg strategy.ScalpConfig, allData map[string][]model.Candle, days int) {
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Printf(" PARAMETER OPTIMIZATION SWEEP (%dmin candles)\n", baseCfg.CandleInterval)
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println()

	type paramSet struct {
		RSIEntry float64
		RSIExit  float64
		TP       float64
		SL       float64
		VolMin   float64
		MaxBars  int
	}

	type optResult struct {
		Params    paramSet
		Trades    int
		WinRate   float64
		NetReturn float64
		PF        float64
		MaxDD     float64
		EV        float64
	}

	// Parameter grid
	rsiEntries := []float64{20, 25, 30, 35}
	rsiExits := []float64{50, 55, 60, 65, 70}
	tps := []float64{1.0, 1.5, 2.0, 2.5}
	sls := []float64{1.5, 2.0, 2.5, 3.0}
	volMins := []float64{1.0, 1.5, 2.0}
	maxBarsMultiples := []int{16, 32, 48}

	// Adjust maxBars for candle interval
	scaledMaxBars := make([]int, len(maxBarsMultiples))
	for i, mb := range maxBarsMultiples {
		// Scale: 32 bars at 15min = 8h. For 5min, 96 bars = 8h.
		scaledMaxBars[i] = mb * 15 / baseCfg.CandleInterval
	}

	var results []optResult
	total := len(rsiEntries) * len(rsiExits) * len(tps) * len(sls) * len(volMins) * len(scaledMaxBars)
	count := 0

	for _, rsiE := range rsiEntries {
		for _, rsiX := range rsiExits {
			for _, tp := range tps {
				for _, sl := range sls {
					for _, vol := range volMins {
						for _, mb := range scaledMaxBars {
							count++
							if count%100 == 0 {
								fmt.Printf("\r  Progress: %d/%d (%.0f%%)...", count, total, float64(count)/float64(total)*100)
							}

							cfg := baseCfg
							cfg.RSIEntry = rsiE
							cfg.RSIExit = rsiX
							cfg.TakeProfitPct = tp
							cfg.StopLossPct = sl
							cfg.VolumeMin = vol
							cfg.MaxHoldBars = mb

							r := runBacktest(cfg, allData)
							if r.TotalTrades < 20 {
								continue // too few trades
							}

							ev := r.NetReturn / float64(r.TotalTrades)
							results = append(results, optResult{
								Params: paramSet{
									RSIEntry: rsiE,
									RSIExit:  rsiX,
									TP:       tp,
									SL:       sl,
									VolMin:   vol,
									MaxBars:  mb,
								},
								Trades:    r.TotalTrades,
								WinRate:   r.WinRate,
								NetReturn: r.NetReturn,
								PF:        r.ProfitFactor,
								MaxDD:     r.MaxDrawdown,
								EV:        ev,
							})
						}
					}
				}
			}
		}
	}

	fmt.Printf("\r  Tested %d combinations, %d viable\n\n", count, len(results))

	// Sort by net return (best first)
	sort.Slice(results, func(i, j int) bool {
		return results[i].NetReturn > results[j].NetReturn
	})

	// Print top 20
	fmt.Printf(" %-5s %-5s %-5s %-5s %-5s %-5s | %5s %6s %8s %6s %6s %8s\n",
		"RSIe", "RSIx", "TP%", "SL%", "Vol", "Bars",
		"Trd", "Win%", "Net%", "PF", "MDD%", "EV%")
	fmt.Println(" " + strings.Repeat("-", 85))

	limit := 20
	if len(results) < limit {
		limit = len(results)
	}

	for i := 0; i < limit; i++ {
		r := results[i]
		fmt.Printf(" %-5.0f %-5.0f %-5.1f %-5.1f %-5.1f %-5d | %5d %5.0f%% %+7.1f%% %5.2f %5.1f%% %+7.4f%%\n",
			r.Params.RSIEntry, r.Params.RSIExit,
			r.Params.TP, r.Params.SL, r.Params.VolMin, r.Params.MaxBars,
			r.Trades, r.WinRate, r.NetReturn, r.PF, r.MaxDD, r.EV)
	}

	// Best by Sharpe-like metric (return / drawdown)
	fmt.Println("\n Top by Risk-Adjusted Return (Net%/MDD%):")
	sort.Slice(results, func(i, j int) bool {
		ri := results[i].NetReturn / math.Max(results[i].MaxDD, 0.01)
		rj := results[j].NetReturn / math.Max(results[j].MaxDD, 0.01)
		return ri > rj
	})

	fmt.Printf(" %-5s %-5s %-5s %-5s %-5s %-5s | %5s %6s %8s %6s %6s %8s\n",
		"RSIe", "RSIx", "TP%", "SL%", "Vol", "Bars",
		"Trd", "Win%", "Net%", "PF", "MDD%", "EV%")
	fmt.Println(" " + strings.Repeat("-", 85))

	limit = 10
	if len(results) < limit {
		limit = len(results)
	}
	for i := 0; i < limit; i++ {
		r := results[i]
		fmt.Printf(" %-5.0f %-5.0f %-5.1f %-5.1f %-5.1f %-5d | %5d %5.0f%% %+7.1f%% %5.2f %5.1f%% %+7.4f%%\n",
			r.Params.RSIEntry, r.Params.RSIExit,
			r.Params.TP, r.Params.SL, r.Params.VolMin, r.Params.MaxBars,
			r.Trades, r.WinRate, r.NetReturn, r.PF, r.MaxDD, r.EV)
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
}
