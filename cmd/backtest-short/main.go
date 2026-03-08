package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"traveler/internal/strategy"
	"traveler/pkg/model"
)

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
	ExitReasons  map[string]int
	Monthly      []monthlyReturn
	PerPair      []pairStats
}

type pairStats struct {
	Symbol  string
	Trades  int
	Wins    int
	WinRate float64
	NetPnL  float64
	AvgPnL  float64
	PF      float64
	AvgBars float64
}

type monthlyReturn struct {
	Month   string
	Trades  int
	WinRate float64
	NetPnL  float64
}

// shortParams holds tunable parameters for the short scalp backtest.
type shortParams struct {
	RSIEntry      float64 // RSI above this → short entry
	RSIExit       float64 // RSI below this → close short
	TakeProfitPct float64
	StopLossPct   float64
	VolumeMin     float64
	MaxHoldBars   int
	EMAPeriod     int
	RSIPeriod     int
	CommissionPct float64 // per side
	MaxPositions  int

	// EMA bounce mode: use EMA20 proximity instead of strict RSI>70
	UseEMABounce   bool
	EMABouncePeriod int     // e.g., 20
	EMABounceMaxPct float64 // max distance from EMA to qualify (e.g., 0.5%)
}

func defaultShortParams() shortParams {
	return shortParams{
		RSIEntry:      70.0,
		RSIExit:       40.0,
		TakeProfitPct: 2.0,
		StopLossPct:   2.5,
		VolumeMin:     1.5,
		MaxHoldBars:   32,
		EMAPeriod:     50,
		RSIPeriod:     7,
		CommissionPct: 0.04,
		MaxPositions:  3,

		UseEMABounce:    false,
		EMABouncePeriod: 20,
		EMABounceMaxPct: 0.5,
	}
}

type simPosition struct {
	entryPrice float64
	entryBar   int
	entryTime  time.Time
	rsiAtEntry float64
}

func main() {
	days := 90
	optimize := false
	customPairs := ""
	for _, arg := range os.Args[1:] {
		if arg == "--optimize" {
			optimize = true
		}
		if strings.HasPrefix(arg, "--days=") {
			fmt.Sscanf(arg, "--days=%d", &days)
		}
		if strings.HasPrefix(arg, "--pairs=") {
			customPairs = strings.TrimPrefix(arg, "--pairs=")
		}
	}

	pairs := []string{"ETHUSDT", "SOLUSDT", "XRPUSDT"}
	if customPairs != "" {
		pairs = strings.Split(customPairs, ",")
	}

	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println(" BINANCE SHORT SCALP BACKTEST - 15min")
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println()

	ctx := context.Background()

	// Fetch paginated historical data (Binance limit: 1500 per request)
	candlesPerDay := 24 * 60 / 15 // 96
	totalNeeded := days*candlesPerDay + 100
	allData := make(map[string][]model.Candle)

	for _, symbol := range pairs {
		fmt.Printf("  %s: fetching %d days of 15min candles...", symbol, days)
		candles, err := fetchPaginated(ctx, symbol, 15, totalNeeded)
		if err != nil {
			fmt.Printf(" ERROR: %v\n", err)
			continue
		}
		fmt.Printf(" got %d candles\n", len(candles))
		allData[symbol] = candles
	}
	fmt.Println()

	if optimize {
		runOptimization(defaultShortParams(), pairs, allData, days)
		return
	}

	// Run 3 strategy variants
	fmt.Println("━━━ Strategy A: Current (RSI>70, EMA50 below) ━━━")
	paramsA := defaultShortParams()
	resultA := runBacktest(paramsA, allData)
	printResults("A: RSI>70", resultA)

	fmt.Println("\n━━━ Strategy B: Relaxed RSI (RSI>60, EMA50 below) ━━━")
	paramsB := defaultShortParams()
	paramsB.RSIEntry = 60.0
	resultB := runBacktest(paramsB, allData)
	printResults("B: RSI>60", resultB)

	fmt.Println("\n━━━ Strategy C: EMA20 Bounce Short (RSI>55, near EMA20, below EMA50) ━━━")
	paramsC := defaultShortParams()
	paramsC.RSIEntry = 55.0
	paramsC.UseEMABounce = true
	paramsC.EMABouncePeriod = 20
	paramsC.EMABounceMaxPct = 1.0
	resultC := runBacktest(paramsC, allData)
	printResults("C: EMA20 Bounce", resultC)

	// Comparison
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println(" STRATEGY COMPARISON")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf(" %-20s %6s %6s %8s %6s %6s\n", "Strategy", "Trades", "Win%", "Net%", "PF", "MDD%")
	fmt.Println(" " + strings.Repeat("-", 56))
	for _, row := range []struct {
		name string
		r    *backtestResult
	}{
		{"A: RSI>70 (current)", resultA},
		{"B: RSI>60", resultB},
		{"C: EMA20 Bounce", resultC},
	} {
		fmt.Printf(" %-20s %6d %5.0f%% %+7.1f%% %5.2f %5.1f%%\n",
			row.name, row.r.TotalTrades, row.r.WinRate, row.r.NetReturn, row.r.ProfitFactor, row.r.MaxDrawdown)
	}
	fmt.Println()
}

// fetchPaginated fetches candles in chunks of 1500 using Binance endTime pagination.
func fetchPaginated(ctx context.Context, symbol string, interval, total int) ([]model.Candle, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var all []model.Candle
	endTimeMs := time.Now().UnixMilli()

	for len(all) < total {
		need := total - len(all)
		if need > 1500 {
			need = 1500
		}

		candles, err := fetchKlines(ctx, client, symbol, interval, need, endTimeMs)
		if err != nil {
			return nil, err
		}
		if len(candles) == 0 {
			break
		}

		all = append(candles, all...) // prepend (older data first)
		endTimeMs = candles[0].Time.UnixMilli() - 1
		time.Sleep(150 * time.Millisecond)
	}

	return all, nil
}

func fetchKlines(ctx context.Context, client *http.Client, symbol string, interval, limit int, endTimeMs int64) ([]model.Candle, error) {
	u := fmt.Sprintf("https://fapi.binance.com/fapi/v1/klines?symbol=%s&interval=%dm&limit=%d&endTime=%d",
		symbol, interval, limit, endTimeMs)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	var raw [][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	candles := make([]model.Candle, 0, len(raw))
	for _, k := range raw {
		if len(k) < 6 {
			continue
		}
		var openTimeMs int64
		json.Unmarshal(k[0], &openTimeMs)

		var oS, hS, lS, cS, vS string
		json.Unmarshal(k[1], &oS)
		json.Unmarshal(k[2], &hS)
		json.Unmarshal(k[3], &lS)
		json.Unmarshal(k[4], &cS)
		json.Unmarshal(k[5], &vS)

		o, _ := strconv.ParseFloat(oS, 64)
		h, _ := strconv.ParseFloat(hS, 64)
		l, _ := strconv.ParseFloat(lS, 64)
		c, _ := strconv.ParseFloat(cS, 64)
		v, _ := strconv.ParseFloat(vS, 64)

		candles = append(candles, model.Candle{
			Time:   time.UnixMilli(openTimeMs),
			Open:   o,
			High:   h,
			Low:    l,
			Close:  c,
			Volume: int64(v * 1e6),
		})
	}
	return candles, nil
}

func runBacktest(params shortParams, allData map[string][]model.Candle) *backtestResult {
	warmup := 100

	minLen := math.MaxInt64
	for _, candles := range allData {
		if len(candles) < minLen {
			minLen = len(candles)
		}
	}

	if minLen <= warmup {
		return &backtestResult{ExitReasons: make(map[string]int)}
	}

	var trades []backtestTrade
	activePositions := make(map[string]*simPosition)
	exitReasons := make(map[string]int)

	minIndicatorCandles := params.EMAPeriod + 1

	for bar := warmup; bar < minLen; bar++ {
		// 1. Check exits
		for symbol, pos := range activePositions {
			candles := allData[symbol]
			if bar >= len(candles) {
				continue
			}

			current := candles[bar]
			// SHORT PnL: profit when price drops
			pnlPct := (pos.entryPrice - current.Close) / pos.entryPrice * 100

			shouldExit := false
			reason := ""

			// Intra-bar: high = worst for shorts, low = best for shorts
			worstPnl := (pos.entryPrice - current.High) / pos.entryPrice * 100
			bestPnl := (pos.entryPrice - current.Low) / pos.entryPrice * 100

			// Stop loss: price went UP against short
			if worstPnl <= -params.StopLossPct {
				shouldExit = true
				reason = "stop_loss"
				pnlPct = -params.StopLossPct
			} else if bestPnl >= params.TakeProfitPct {
				// Take profit: price went DOWN in our favor
				shouldExit = true
				reason = "take_profit"
				pnlPct = params.TakeProfitPct
			}

			// RSI exit: RSI dropped back (mean reverted)
			if !shouldExit {
				lookback := candles[:bar+1]
				if len(lookback) > 100 {
					lookback = lookback[len(lookback)-100:]
				}
				if len(lookback) >= params.RSIPeriod+1 {
					rsi := strategy.CalculateRSI(lookback, params.RSIPeriod)
					if rsi <= params.RSIExit {
						shouldExit = true
						reason = "rsi_exit"
						// use close price PnL
					}
				}
			}

			// Max hold
			if !shouldExit && (bar-pos.entryBar) >= params.MaxHoldBars {
				shouldExit = true
				reason = "max_hold"
			}

			if shouldExit {
				netPnlPct := pnlPct - 2*params.CommissionPct

				trades = append(trades, backtestTrade{
					Symbol:     symbol,
					EntryTime:  pos.entryTime,
					ExitTime:   current.Time,
					EntryPrice: pos.entryPrice,
					ExitPrice:  pos.entryPrice * (1 - pnlPct/100),
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

		// 2. Check entries
		if len(activePositions) >= params.MaxPositions {
			continue
		}

		for _, symbol := range getPairs(allData) {
			if _, held := activePositions[symbol]; held {
				continue
			}
			if len(activePositions) >= params.MaxPositions {
				break
			}

			candles := allData[symbol]
			if bar >= len(candles) || bar < warmup {
				continue
			}

			lookback := candles[:bar+1]
			if len(lookback) > 100 {
				lookback = lookback[len(lookback)-100:]
			}
			if len(lookback) < minIndicatorCandles {
				continue
			}

			latest := lookback[len(lookback)-1]
			price := latest.Close

			// RSI check: must be OVERBOUGHT
			rsi := strategy.CalculateRSI(lookback, params.RSIPeriod)
			if rsi <= params.RSIEntry {
				continue
			}

			// Volume filter
			avgVol := strategy.CalculateAvgVolume(lookback[:len(lookback)-1], 20)
			currentVol := float64(latest.Volume)
			if avgVol <= 0 || currentVol/avgVol < params.VolumeMin {
				continue
			}

			// EMA50 filter: price must be BELOW EMA50 (bearish trend)
			ema50 := strategy.CalculateEMA(lookback, params.EMAPeriod)
			if ema50 <= 0 || price >= ema50 {
				continue
			}

			// EMA bounce mode: also require price near EMA20
			if params.UseEMABounce {
				ema20 := strategy.CalculateEMA(lookback, params.EMABouncePeriod)
				if ema20 <= 0 {
					continue
				}
				distPct := math.Abs(price-ema20) / ema20 * 100
				if distPct > params.EMABounceMaxPct {
					continue
				}
			}

			// Entry on NEXT bar open
			if bar+1 < len(candles) {
				entryBar := bar + 1
				entryPrice := candles[entryBar].Open
				activePositions[symbol] = &simPosition{
					entryPrice: entryPrice,
					entryBar:   entryBar,
					entryTime:  candles[entryBar].Time,
					rsiAtEntry: rsi,
				}
			}
		}
	}

	return calculateResults(trades, exitReasons)
}

func getPairs(allData map[string][]model.Candle) []string {
	var pairs []string
	for k := range allData {
		pairs = append(pairs, k)
	}
	sort.Strings(pairs)
	return pairs
}

func calculateResults(trades []backtestTrade, exitReasons map[string]int) *backtestResult {
	result := &backtestResult{ExitReasons: exitReasons}

	if len(trades) == 0 {
		return result
	}

	result.Period = fmt.Sprintf("%s ~ %s",
		trades[0].EntryTime.Format("2006-01-02"),
		trades[len(trades)-1].ExitTime.Format("2006-01-02"))
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
	if grossLoss > 0 {
		result.ProfitFactor = grossWin / grossLoss
	}

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

	// Monthly
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

	// Per-pair
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
			var gw, gl float64
			for _, t := range trades {
				if t.Symbol == ps.Symbol {
					if t.NetPnLPct > 0 {
						gw += t.NetPnLPct
					} else {
						gl += math.Abs(t.NetPnLPct)
					}
				}
			}
			if gl > 0 {
				ps.PF = gw / gl
			}
		}
		result.PerPair = append(result.PerPair, *ps)
	}
	sort.Slice(result.PerPair, func(i, j int) bool {
		return result.PerPair[i].NetPnL > result.PerPair[j].NetPnL
	})

	return result
}

func printResults(name string, r *backtestResult) {
	fmt.Printf("\n [%s]\n", name)

	if r.TotalTrades == 0 {
		fmt.Println("  No trades generated.")
		return
	}

	fmt.Printf(" Period:          %s\n", r.Period)
	fmt.Printf(" Total Trades:    %d\n", r.TotalTrades)
	fmt.Printf(" Win Rate:        %.1f%% (%d W / %d L)\n", r.WinRate, r.Wins, r.Losses)
	fmt.Printf(" Net Return:      %+.2f%% (commission: %.2f%%)\n", r.NetReturn, r.Commission)
	fmt.Printf(" Avg Win:         +%.3f%%  Avg Loss: %.3f%%\n", r.AvgWinPct, r.AvgLossPct)
	fmt.Printf(" Profit Factor:   %.2f\n", r.ProfitFactor)
	fmt.Printf(" Max Drawdown:    %.2f%%\n", r.MaxDrawdown)
	fmt.Printf(" Sharpe:          %.2f\n", r.SharpeRatio)
	fmt.Printf(" Avg Hold:        %.1f bars (%.1f hours)\n", r.AvgBarsHeld, r.AvgBarsHeld*15/60)

	fmt.Printf(" Exit reasons:    ")
	for _, reason := range []string{"take_profit", "rsi_exit", "stop_loss", "max_hold"} {
		count := r.ExitReasons[reason]
		if count > 0 {
			fmt.Printf("%s=%d ", reason, count)
		}
	}
	fmt.Println()

	if len(r.PerPair) > 0 {
		fmt.Printf(" %-10s %5s %6s %8s %6s\n", "Pair", "Trd", "Win%", "Net%", "PF")
		for _, ps := range r.PerPair {
			fmt.Printf(" %-10s %5d %5.0f%% %+7.2f%% %5.2f\n",
				ps.Symbol, ps.Trades, ps.WinRate, ps.NetPnL, ps.PF)
		}
	}
}

func runOptimization(base shortParams, pairs []string, allData map[string][]model.Candle, days int) {
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println(" SHORT SCALP PARAMETER OPTIMIZATION")
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println()

	type optResult struct {
		RSIEntry  float64
		RSIExit   float64
		TP        float64
		SL        float64
		VolMin    float64
		MaxBars   int
		EMABounce bool
		Trades    int
		WinRate   float64
		NetReturn float64
		PF        float64
		MaxDD     float64
		EV        float64
	}

	rsiEntries := []float64{55, 60, 65, 70, 75}
	rsiExits := []float64{30, 35, 40, 45}
	tps := []float64{1.5, 2.0, 2.5, 3.0}
	sls := []float64{1.5, 2.0, 2.5, 3.0}
	volMins := []float64{1.0, 1.5}
	maxBarsOpts := []int{24, 32, 48}
	emaBounceOpts := []bool{false, true}

	var results []optResult
	total := len(rsiEntries) * len(rsiExits) * len(tps) * len(sls) * len(volMins) * len(maxBarsOpts) * len(emaBounceOpts)
	count := 0

	for _, rsiE := range rsiEntries {
		for _, rsiX := range rsiExits {
			for _, tp := range tps {
				for _, sl := range sls {
					for _, vol := range volMins {
						for _, mb := range maxBarsOpts {
							for _, bounce := range emaBounceOpts {
								count++
								if count%200 == 0 {
									fmt.Printf("\r  Progress: %d/%d (%.0f%%)...", count, total, float64(count)/float64(total)*100)
								}

								p := base
								p.RSIEntry = rsiE
								p.RSIExit = rsiX
								p.TakeProfitPct = tp
								p.StopLossPct = sl
								p.VolumeMin = vol
								p.MaxHoldBars = mb
								p.UseEMABounce = bounce
								if bounce {
									p.EMABounceMaxPct = 1.0
								}

								r := runBacktest(p, allData)
								if r.TotalTrades < 10 {
									continue
								}

								ev := r.NetReturn / float64(r.TotalTrades)
								results = append(results, optResult{
									RSIEntry:  rsiE,
									RSIExit:   rsiX,
									TP:        tp,
									SL:        sl,
									VolMin:    vol,
									MaxBars:   mb,
									EMABounce: bounce,
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
	}

	fmt.Printf("\r  Tested %d combinations, %d viable\n\n", count, len(results))

	// Sort by net return
	sort.Slice(results, func(i, j int) bool {
		return results[i].NetReturn > results[j].NetReturn
	})

	fmt.Println(" Top 20 by Net Return:")
	fmt.Printf(" %-5s %-5s %-5s %-5s %-4s %-4s %-6s | %5s %6s %8s %6s %6s\n",
		"RSIe", "RSIx", "TP%", "SL%", "Vol", "Bar", "Bounce",
		"Trd", "Win%", "Net%", "PF", "MDD%")
	fmt.Println(" " + strings.Repeat("-", 78))

	limit := 20
	if len(results) < limit {
		limit = len(results)
	}
	for i := 0; i < limit; i++ {
		r := results[i]
		bounce := "N"
		if r.EMABounce {
			bounce = "Y"
		}
		fmt.Printf(" %-5.0f %-5.0f %-5.1f %-5.1f %-4.1f %-4d %-6s | %5d %5.0f%% %+7.1f%% %5.2f %5.1f%%\n",
			r.RSIEntry, r.RSIExit, r.TP, r.SL, r.VolMin, r.MaxBars, bounce,
			r.Trades, r.WinRate, r.NetReturn, r.PF, r.MaxDD)
	}

	// Risk-adjusted
	fmt.Println("\n Top 10 by Risk-Adjusted (Net%/MDD%):")
	sort.Slice(results, func(i, j int) bool {
		ri := results[i].NetReturn / math.Max(results[i].MaxDD, 0.01)
		rj := results[j].NetReturn / math.Max(results[j].MaxDD, 0.01)
		return ri > rj
	})

	fmt.Printf(" %-5s %-5s %-5s %-5s %-4s %-4s %-6s | %5s %6s %8s %6s %6s\n",
		"RSIe", "RSIx", "TP%", "SL%", "Vol", "Bar", "Bounce",
		"Trd", "Win%", "Net%", "PF", "MDD%")
	fmt.Println(" " + strings.Repeat("-", 78))
	limit = 10
	if len(results) < limit {
		limit = len(results)
	}
	for i := 0; i < limit; i++ {
		r := results[i]
		bounce := "N"
		if r.EMABounce {
			bounce = "Y"
		}
		fmt.Printf(" %-5.0f %-5.0f %-5.1f %-5.1f %-4.1f %-4d %-6s | %5d %5.0f%% %+7.1f%% %5.2f %5.1f%%\n",
			r.RSIEntry, r.RSIExit, r.TP, r.SL, r.VolMin, r.MaxBars, bounce,
			r.Trades, r.WinRate, r.NetReturn, r.PF, r.MaxDD)
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
}
