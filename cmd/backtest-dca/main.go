package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"traveler/internal/dca"
	"traveler/internal/provider"
	"traveler/internal/strategy"
	"traveler/pkg/model"
)

// backtest simulation state
type simAsset struct {
	totalInvested  float64 // cumulative buys (cost basis of remaining position)
	totalQuantity  float64
	avgCost        float64
	triggeredTiers map[float64]bool
}

type simState struct {
	assets        map[string]*simAsset
	totalBought   float64 // cumulative KRW spent on buys (never decreases)
	totalSold     float64 // cumulative KRW received from sells
}

type dailySnapshot struct {
	Date         time.Time
	TotalBought  float64 // cumulative buys
	TotalSold    float64 // cumulative sells
	PortValue    float64 // current portfolio market value
	TotalReturn  float64 // (portValue + totalSold - totalBought) / totalBought * 100
	FearGreed    int
}

func main() {
	days := flag.Int("days", 365, "Backtest period in days (from today going back)")
	baseAmount := flag.Float64("amount", 50000, "Base DCA amount per day (KRW)")
	noFG := flag.Bool("no-fg", false, "Disable Fear & Greed multiplier (fixed DCA)")
	noEMA := flag.Bool("no-ema", false, "Disable EMA50 overlay")
	noTP := flag.Bool("no-tp", false, "Disable take-profit")
	flag.Parse()

	ctx := context.Background()
	cfg := dca.DefaultConfig()
	cfg.BaseDCAAmount = *baseAmount

	fmt.Println("============================================================")
	fmt.Println(" DCA BACKTEST — Fear & Greed + EMA50 + Take-Profit")
	fmt.Println("============================================================")
	fmt.Printf("  Period:     %d days\n", *days)
	fmt.Printf("  Base DCA:   ₩%.0f/day\n", cfg.BaseDCAAmount)
	fmt.Printf("  F&G Tiers:  %v\n", !*noFG)
	fmt.Printf("  EMA50:      %v\n", !*noEMA)
	fmt.Printf("  Take-Profit:%v\n", !*noTP)
	fmt.Printf("  Coins:      BTC 40%%, ETH 20%%, SOL 15%%, XRP 10%%\n")
	fmt.Println()

	if *noFG {
		cfg.FearGreedEnabled = false
	}
	if *noEMA {
		cfg.EMA50Enabled = false
	}
	if *noTP {
		cfg.TakeProfitEnabled = false
	}

	// 1. Fetch historical Fear & Greed data
	fmt.Println("Fetching Fear & Greed history...")
	fgMap, err := fetchFearGreedHistory(ctx, *days+60)
	if err != nil {
		fmt.Fprintf(os.Stderr, "F&G fetch failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Got %d F&G data points\n", len(fgMap))

	// 2. Fetch historical price data
	upbit := provider.NewUpbitProvider()
	symbols := []string{"KRW-BTC", "KRW-ETH", "KRW-SOL", "KRW-XRP"}
	priceData := make(map[string][]model.Candle)

	fetchDays := *days + 60
	for _, sym := range symbols {
		fmt.Printf("Fetching %s (%d days)...\n", sym, fetchDays)
		candles, err := upbit.GetDailyCandles(ctx, sym, fetchDays)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s fetch failed: %v\n", sym, err)
			os.Exit(1)
		}
		priceData[sym] = candles
		fmt.Printf("  Got %d candles\n", len(candles))
		time.Sleep(1 * time.Second)
	}

	// 3. Determine date range
	loc, _ := time.LoadLocation("Asia/Seoul")
	if loc == nil {
		loc = time.FixedZone("KST", 9*60*60)
	}
	endDate := time.Now().In(loc).AddDate(0, 0, -1)
	startDate := endDate.AddDate(0, 0, -(*days)+1)
	fmt.Printf("\nBacktest: %s ~ %s\n\n", startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))

	// Build date-indexed price maps
	type dayPrice struct {
		close float64
		idx   int
	}
	priceMaps := make(map[string]map[string]dayPrice)
	for sym, candles := range priceData {
		m := make(map[string]dayPrice)
		for i, c := range candles {
			dateKey := c.Time.In(loc).Format("2006-01-02")
			m[dateKey] = dayPrice{close: c.Close, idx: i}
		}
		priceMaps[sym] = m
	}

	// 4. Run Smart DCA simulation
	state := &simState{assets: make(map[string]*simAsset)}
	var snapshots []dailySnapshot
	commRate := 0.0005

	for d := startDate; !d.After(endDate); d = d.AddDate(0, 0, 1) {
		dateKey := d.Format("2006-01-02")

		allAvailable := true
		for _, sym := range symbols {
			if _, ok := priceMaps[sym][dateKey]; !ok {
				allAvailable = false
				break
			}
		}
		if !allAvailable {
			continue
		}

		fgValue := 50
		if fg, ok := fgMap[dateKey]; ok {
			fgValue = fg
		}

		fgMult := 1.0
		if cfg.FearGreedEnabled {
			fgMult = getFGMultiplier(cfg.FGTiers, fgValue)
		}

		// Buy
		for _, target := range cfg.Targets {
			dp, ok := priceMaps[target.Symbol][dateKey]
			if !ok {
				continue
			}

			ema50Mult := 1.0
			if cfg.EMA50Enabled {
				candles := priceData[target.Symbol]
				if dp.idx >= 49 {
					subset := candles[:dp.idx+1]
					ema50 := strategy.CalculateEMA(subset, 50)
					if dp.close < ema50 {
						ema50Mult = cfg.EMA50BuyMultiplier
					} else {
						ema50Mult = cfg.EMA50SellMultiplier
					}
				}
			}

			amount := math.Floor(cfg.BaseDCAAmount * target.TargetPct * fgMult * ema50Mult)
			if amount >= cfg.MinOrderAmount {
				cost := amount * (1 + commRate)
				qty := amount / dp.close
				simBuy(state, target.Symbol, cost, qty)
			}
		}

		// Extreme Greed sell
		if fgValue >= 75 && cfg.ExtGreedSellPct > 0 {
			for _, target := range cfg.Targets {
				dp := priceMaps[target.Symbol][dateKey]
				a := state.assets[target.Symbol]
				if a == nil || a.totalQuantity <= 0 {
					continue
				}
				sellQty := a.totalQuantity * cfg.ExtGreedSellPct
				proceeds := sellQty * dp.close * (1 - commRate)
				simSell(state, target.Symbol, sellQty, proceeds)
			}
		}

		// Take-profit
		if cfg.TakeProfitEnabled {
			for _, target := range cfg.Targets {
				dp := priceMaps[target.Symbol][dateKey]
				a := state.assets[target.Symbol]
				if a == nil || a.totalQuantity <= 0 || a.avgCost <= 0 {
					continue
				}
				pnlPct := (dp.close - a.avgCost) / a.avgCost * 100

				if pnlPct < cfg.TakeProfitTiers[0].PctThreshold {
					a.triggeredTiers = make(map[float64]bool)
					continue
				}

				for _, tier := range cfg.TakeProfitTiers {
					if pnlPct < tier.PctThreshold {
						break
					}
					if a.triggeredTiers[tier.PctThreshold] {
						continue
					}
					sellQty := a.totalQuantity * tier.SellPct
					proceeds := sellQty * dp.close * (1 - commRate)
					simSell(state, target.Symbol, sellQty, proceeds)
					a.triggeredTiers[tier.PctThreshold] = true
				}
			}
		}

		// Daily snapshot
		var portValue float64
		for _, target := range cfg.Targets {
			dp := priceMaps[target.Symbol][dateKey]
			a := state.assets[target.Symbol]
			if a != nil {
				portValue += a.totalQuantity * dp.close
			}
		}

		totalReturn := 0.0
		if state.totalBought > 0 {
			totalReturn = (portValue + state.totalSold - state.totalBought) / state.totalBought * 100
		}

		snapshots = append(snapshots, dailySnapshot{
			Date:        d,
			TotalBought: state.totalBought,
			TotalSold:   state.totalSold,
			PortValue:   portValue,
			TotalReturn: totalReturn,
			FearGreed:   fgValue,
		})
	}

	// 5. Simple DCA for comparison
	simpleTotalBought := 0.0
	simpleAssets := make(map[string]*simAsset)
	for d := startDate; !d.After(endDate); d = d.AddDate(0, 0, 1) {
		dateKey := d.Format("2006-01-02")
		allAvailable := true
		for _, sym := range symbols {
			if _, ok := priceMaps[sym][dateKey]; !ok {
				allAvailable = false
				break
			}
		}
		if !allAvailable {
			continue
		}
		for _, target := range cfg.Targets {
			dp := priceMaps[target.Symbol][dateKey]
			amount := math.Floor(cfg.BaseDCAAmount * target.TargetPct)
			if amount < cfg.MinOrderAmount {
				continue
			}
			cost := amount * (1 + commRate)
			qty := amount / dp.close
			a, ok := simpleAssets[target.Symbol]
			if !ok {
				a = &simAsset{triggeredTiers: make(map[float64]bool)}
				simpleAssets[target.Symbol] = a
			}
			a.totalInvested += cost
			a.totalQuantity += qty
			a.avgCost = a.totalInvested / a.totalQuantity
			simpleTotalBought += cost
		}
	}

	lastDateKey := endDate.Format("2006-01-02")
	simpleFinalValue := 0.0
	for _, target := range cfg.Targets {
		dp := priceMaps[target.Symbol][lastDateKey]
		a := simpleAssets[target.Symbol]
		if a != nil {
			simpleFinalValue += a.totalQuantity * dp.close
		}
	}
	simplePnL := simpleFinalValue - simpleTotalBought
	simplePnLPct := 0.0
	if simpleTotalBought > 0 {
		simplePnLPct = simplePnL / simpleTotalBought * 100
	}

	// 6. Print results
	final := snapshots[len(snapshots)-1]
	smartProfit := final.PortValue + final.TotalSold - final.TotalBought
	tradingDays := len(snapshots)
	years := float64(tradingDays) / 365.0
	annualizedSmart := 0.0
	if years > 0 && final.TotalBought > 0 {
		totalReturnRatio := (final.PortValue + final.TotalSold) / final.TotalBought
		if totalReturnRatio > 0 {
			annualizedSmart = (math.Pow(totalReturnRatio, 1.0/years) - 1) * 100
		}
	}
	annualizedSimple := 0.0
	if years > 0 && simpleTotalBought > 0 {
		totalReturnRatio := simpleFinalValue / simpleTotalBought
		if totalReturnRatio > 0 {
			annualizedSimple = (math.Pow(totalReturnRatio, 1.0/years) - 1) * 100
		}
	}

	fmt.Println("============================================================")
	fmt.Println(" RESULTS")
	fmt.Println("============================================================")

	fmt.Println()
	fmt.Println(">> Smart DCA (F&G + EMA50 + Take-Profit)")
	fmt.Printf("   Total Bought:     ₩%s (누적 매수)\n", formatKRW(final.TotalBought))
	fmt.Printf("   Total Sold:       ₩%s (누적 매도)\n", formatKRW(final.TotalSold))
	fmt.Printf("   Portfolio Value:  ₩%s (현재 보유 가치)\n", formatKRW(final.PortValue))
	fmt.Printf("   Total Profit:     ₩%s\n", formatKRW(smartProfit))
	fmt.Printf("   Total Return:     %+.1f%%\n", final.TotalReturn)
	fmt.Printf("   Annualized:       %+.1f%%/yr\n", annualizedSmart)

	fmt.Println()
	fmt.Println(">> Simple Fixed DCA (no F&G, no EMA50, no TP)")
	fmt.Printf("   Total Bought:     ₩%s\n", formatKRW(simpleTotalBought))
	fmt.Printf("   Final Value:      ₩%s\n", formatKRW(simpleFinalValue))
	fmt.Printf("   Total Profit:     ₩%s\n", formatKRW(simplePnL))
	fmt.Printf("   Total Return:     %+.1f%%\n", simplePnLPct)
	fmt.Printf("   Annualized:       %+.1f%%/yr\n", annualizedSimple)

	fmt.Println()
	fmt.Printf(">> Alpha: %+.1f%%p (총수익률), %+.1f%%p/yr (연환산)\n",
		final.TotalReturn-simplePnLPct, annualizedSmart-annualizedSimple)

	// Per-asset breakdown
	fmt.Println()
	fmt.Println("--- Per-Asset (Smart DCA, 잔여 포지션) ---")
	fmt.Printf("%-10s %12s %12s %10s %8s\n", "Coin", "Cost Basis", "Value", "PnL", "PnL%")
	for _, target := range cfg.Targets {
		a := state.assets[target.Symbol]
		if a == nil {
			continue
		}
		dp := priceMaps[target.Symbol][lastDateKey]
		value := a.totalQuantity * dp.close
		pnl := value - a.totalInvested
		pnlPct := 0.0
		if a.totalInvested > 0 {
			pnlPct = pnl / a.totalInvested * 100
		}
		fmt.Printf("%-10s %12s %12s %10s %+7.1f%%\n",
			target.Symbol, formatKRW(a.totalInvested), formatKRW(value), formatKRW(pnl), pnlPct)
	}

	// Risk metrics
	maxDD, maxDDDate := calcMaxDrawdown(snapshots)
	fmt.Println()
	fmt.Printf("--- Risk Metrics ---\n")
	fmt.Printf("  Max Drawdown:      %.1f%% (on %s)\n", maxDD, maxDDDate)
	fmt.Printf("  Trading Days:      %d\n", tradingDays)
	fmt.Printf("  Avg Daily Invest:  ₩%s\n", formatKRW(final.TotalBought/float64(tradingDays)))

	// Monthly total return
	fmt.Println()
	fmt.Println("--- Monthly Total Return ---")
	printMonthlyReturns(snapshots)

	// Projected expected returns
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println(" EXPECTED RETURN PROJECTION (₩50,000/day base)")
	fmt.Println("============================================================")
	fmt.Println()
	fmt.Printf("  Based on %d-day backtest (annualized Smart: %+.1f%%/yr)\n", *days, annualizedSmart)
	fmt.Println()
	projections := []struct {
		label string
		years float64
	}{
		{"1 Year", 1},
		{"2 Years", 2},
		{"3 Years", 3},
		{"5 Years", 5},
	}
	avgDailySpend := final.TotalBought / float64(tradingDays)
	for _, p := range projections {
		totalDays := p.years * 365
		totalInvest := avgDailySpend * totalDays
		// Compound: rough estimate assuming constant return rate
		// Simple approximation: invested evenly, average age = half the period
		// Use annualized return on average-age capital
		avgReturn := annualizedSmart / 100
		// DCA grows linearly, so average capital is invested for half the period
		expectedProfit := totalInvest * avgReturn * p.years / 2
		expectedTotal := totalInvest + expectedProfit
		fmt.Printf("  %s:\n", p.label)
		fmt.Printf("    Est. Invested:  ₩%s\n", formatKRW(totalInvest))
		fmt.Printf("    Est. Value:     ₩%s\n", formatKRW(expectedTotal))
		fmt.Printf("    Est. Profit:    ₩%s (%+.1f%%)\n", formatKRW(expectedProfit), expectedProfit/totalInvest*100)
		fmt.Println()
	}
}

func simBuy(state *simState, symbol string, cost, qty float64) {
	a, ok := state.assets[symbol]
	if !ok {
		a = &simAsset{triggeredTiers: make(map[float64]bool)}
		state.assets[symbol] = a
	}
	a.totalInvested += cost
	a.totalQuantity += qty
	a.avgCost = a.totalInvested / a.totalQuantity
	state.totalBought += cost
}

func simSell(state *simState, symbol string, qty, proceeds float64) {
	a := state.assets[symbol]
	if a == nil {
		return
	}
	costBasis := qty * a.avgCost
	a.totalInvested -= costBasis
	if a.totalInvested < 0 {
		a.totalInvested = 0
	}
	a.totalQuantity -= qty
	if a.totalQuantity < 0 {
		a.totalQuantity = 0
	}
	if a.totalQuantity > 0 {
		a.avgCost = a.totalInvested / a.totalQuantity
	}
	state.totalSold += proceeds
}

func getFGMultiplier(tiers []dca.FGTier, value int) float64 {
	for _, tier := range tiers {
		if value >= tier.MinValue && value <= tier.MaxValue {
			return tier.Multiplier
		}
	}
	return 1.0
}

func calcMaxDrawdown(snapshots []dailySnapshot) (float64, string) {
	// For DCA, use total return % for drawdown (not absolute value)
	peak := -999.0
	maxDD := 0.0
	maxDDDate := ""

	for _, s := range snapshots {
		if s.TotalReturn > peak {
			peak = s.TotalReturn
		}
		dd := peak - s.TotalReturn
		if dd > maxDD {
			maxDD = dd
			maxDDDate = s.Date.Format("2006-01-02")
		}
	}
	return maxDD, maxDDDate
}

func printMonthlyReturns(snapshots []dailySnapshot) {
	type monthData struct {
		endReturn float64
	}
	months := make(map[string]*monthData)
	var monthKeys []string

	for _, s := range snapshots {
		key := s.Date.Format("2006-01")
		md, ok := months[key]
		if !ok {
			md = &monthData{}
			months[key] = md
			monthKeys = append(monthKeys, key)
		}
		md.endReturn = s.TotalReturn
	}

	sort.Strings(monthKeys)
	for _, key := range monthKeys {
		md := months[key]
		bar := ""
		n := int(math.Abs(md.endReturn) / 2)
		if n > 30 {
			n = 30
		}
		for i := 0; i < n; i++ {
			if md.endReturn >= 0 {
				bar += "█"
			} else {
				bar += "░"
			}
		}
		fmt.Printf("  %s: %+7.1f%% %s\n", key, md.endReturn, bar)
	}
}

func formatKRW(v float64) string {
	neg := ""
	if v < 0 {
		neg = "-"
		v = -v
	}
	s := fmt.Sprintf("%.0f", v)
	n := len(s)
	if n <= 3 {
		return neg + s
	}
	result := ""
	for i, c := range s {
		if i > 0 && (n-i)%3 == 0 {
			result += ","
		}
		result += string(c)
	}
	return neg + result
}

func fetchFearGreedHistory(ctx context.Context, days int) (map[string]int, error) {
	url := fmt.Sprintf("https://api.alternative.me/fng/?limit=%d", days)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var fng struct {
		Data []struct {
			Value     string `json:"value"`
			Timestamp string `json:"timestamp"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fng); err != nil {
		return nil, err
	}

	result := make(map[string]int)
	for _, d := range fng.Data {
		val, _ := strconv.Atoi(d.Value)
		ts, _ := strconv.ParseInt(d.Timestamp, 10, 64)
		t := time.Unix(ts, 0).UTC()
		dateKey := t.Format("2006-01-02")
		result[dateKey] = val
	}

	return result, nil
}
