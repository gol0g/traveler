package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"
)

const futuresURL = "https://fapi.binance.com"

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: btc-research <fetch|backtest> [days]")
		fmt.Println("  fetch [days]    — fetch historical signals and save to CSV (default: 30)")
		fmt.Println("  backtest        — run backtest on fetched data")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "fetch":
		days := 30
		if len(os.Args) > 2 {
			days, _ = strconv.Atoi(os.Args[2])
		}
		fetchHistorical(days)
	case "backtest":
		runBacktest()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
	}
}

// ====================== DATA FETCH ======================

type HistRow struct {
	Time           time.Time
	Open           float64
	High           float64
	Low            float64
	Close          float64
	Volume         float64
	TakerBuyVol    float64 // from kline field[9]
	OI             float64
	OIChangePct    float64
	FundingRate    float64
	LongShortRatio float64
	TakerBuyRatio  float64
	// Derived
	RSI7           float64
	ATR14          float64
	EMA50          float64
	FutureReturn1h float64 // % return in next 1 hour (label)
	FutureReturn4h float64 // % return in next 4 hours (label)
}

var client = &http.Client{Timeout: 30 * time.Second}

func fetchHistorical(days int) {
	ctx := context.Background()
	log.Printf("Fetching %d days of historical data for BTCUSDT...", days)

	endTime := time.Now()
	startTime := endTime.AddDate(0, 0, -days)

	// 1. Fetch 15-min candles (includes taker buy volume)
	log.Println("[1/4] Fetching 15-min candles...")
	candles := fetchCandles(ctx, startTime, endTime)
	log.Printf("  Got %d candles", len(candles))

	// 2. Fetch OI history (5-min resolution available, we use 15m)
	log.Println("[2/4] Fetching Open Interest history...")
	oiMap := fetchOIHistory(ctx, startTime, endTime)
	log.Printf("  Got %d OI points", len(oiMap))

	// 3. Fetch funding rate history
	log.Println("[3/4] Fetching funding rate history...")
	fundingMap := fetchFundingHistory(ctx, startTime, endTime)
	log.Printf("  Got %d funding points", len(fundingMap))

	// 4. Fetch long/short ratio + taker ratio
	log.Println("[4/4] Fetching long/short & taker ratios...")
	lsMap := fetchRatioHistory(ctx, "topLongShortAccountRatio", startTime, endTime)
	takerMap := fetchRatioHistory(ctx, "takerlongshortRatio", startTime, endTime)
	log.Printf("  Got %d LS, %d taker points", len(lsMap), len(takerMap))

	// Build rows aligned to candle timestamps
	rows := buildRows(candles, oiMap, fundingMap, lsMap, takerMap)
	log.Printf("Built %d aligned rows", len(rows))

	// Calculate indicators
	calculateIndicators(rows)

	// Calculate future returns (labels for analysis)
	calculateFutureReturns(rows)

	// Save to CSV
	filename := fmt.Sprintf("btc_historical_%dd.csv", days)
	saveCSV(rows, filename)
	log.Printf("Saved to %s", filename)

	// Print summary stats
	printStats(rows)
}

func fetchCandles(ctx context.Context, start, end time.Time) []HistRow {
	var all []HistRow
	current := start

	for current.Before(end) {
		rateLimitSleep()
		limit := 1500
		u := fmt.Sprintf("%s/fapi/v1/klines?symbol=BTCUSDT&interval=15m&startTime=%d&limit=%d",
			futuresURL, current.UnixMilli(), limit)

		body, err := httpGet(ctx, u)
		if err != nil {
			log.Printf("  candle fetch error: %v", err)
			break
		}

		var raw [][]json.RawMessage
		json.Unmarshal(body, &raw)

		if len(raw) == 0 {
			break
		}

		for _, k := range raw {
			if len(k) < 11 {
				continue
			}
			var openTimeMs int64
			json.Unmarshal(k[0], &openTimeMs)

			t := time.UnixMilli(openTimeMs)
			if t.After(end) {
				break
			}

			var oStr, hStr, lStr, cStr, vStr, tbvStr string
			json.Unmarshal(k[1], &oStr)
			json.Unmarshal(k[2], &hStr)
			json.Unmarshal(k[3], &lStr)
			json.Unmarshal(k[4], &cStr)
			json.Unmarshal(k[5], &vStr)
			json.Unmarshal(k[9], &tbvStr) // taker buy base asset volume

			o, _ := strconv.ParseFloat(oStr, 64)
			h, _ := strconv.ParseFloat(hStr, 64)
			l, _ := strconv.ParseFloat(lStr, 64)
			c, _ := strconv.ParseFloat(cStr, 64)
			v, _ := strconv.ParseFloat(vStr, 64)
			tbv, _ := strconv.ParseFloat(tbvStr, 64)

			all = append(all, HistRow{
				Time: t, Open: o, High: h, Low: l, Close: c,
				Volume: v, TakerBuyVol: tbv,
			})
		}

		last := raw[len(raw)-1]
		var lastTimeMs int64
		json.Unmarshal(last[0], &lastTimeMs)
		current = time.UnixMilli(lastTimeMs).Add(15 * time.Minute)
	}

	return all
}

func fetchOIHistory(ctx context.Context, start, end time.Time) map[int64]float64 {
	result := make(map[int64]float64)
	current := end

	for current.After(start) {
		rateLimitSleep()
		u := fmt.Sprintf("%s/futures/data/openInterestHist?symbol=BTCUSDT&period=15m&endTime=%d&limit=500",
			futuresURL, current.UnixMilli())

		body, err := httpGet(ctx, u)
		if err != nil {
			log.Printf("  OI fetch error: %v", err)
			break
		}

		var data []struct {
			Timestamp       int64  `json:"timestamp"`
			SumOpenInterest string `json:"sumOpenInterest"`
		}
		json.Unmarshal(body, &data)

		if len(data) == 0 {
			break
		}

		for _, d := range data {
			oi, _ := strconv.ParseFloat(d.SumOpenInterest, 64)
			bucket := (d.Timestamp / (15 * 60 * 1000)) * (15 * 60 * 1000)
			result[bucket] = oi
		}

		// Move backwards
		earliest := data[0].Timestamp
		if time.UnixMilli(earliest).Before(start) {
			break
		}
		current = time.UnixMilli(earliest).Add(-1 * time.Millisecond)
	}

	return result
}

func fetchFundingHistory(ctx context.Context, start, end time.Time) map[int64]float64 {
	result := make(map[int64]float64)
	current := start

	for current.Before(end) {
		rateLimitSleep()
		u := fmt.Sprintf("%s/fapi/v1/fundingRate?symbol=BTCUSDT&startTime=%d&endTime=%d&limit=1000",
			futuresURL, current.UnixMilli(), end.UnixMilli())

		body, err := httpGet(ctx, u)
		if err != nil {
			log.Printf("  funding fetch error: %v", err)
			break
		}

		var data []struct {
			FundingTime int64  `json:"fundingTime"`
			FundingRate string `json:"fundingRate"`
		}
		json.Unmarshal(body, &data)

		if len(data) == 0 {
			break
		}

		for _, d := range data {
			rate, _ := strconv.ParseFloat(d.FundingRate, 64)
			result[d.FundingTime] = rate
		}

		current = time.UnixMilli(data[len(data)-1].FundingTime).Add(time.Second)
	}

	return result
}

func fetchRatioHistory(ctx context.Context, endpoint string, start, end time.Time) map[int64]float64 {
	result := make(map[int64]float64)
	current := end

	for current.After(start) {
		rateLimitSleep()
		u := fmt.Sprintf("%s/futures/data/%s?symbol=BTCUSDT&period=15m&endTime=%d&limit=500",
			futuresURL, endpoint, current.UnixMilli())

		body, err := httpGet(ctx, u)
		if err != nil {
			log.Printf("  %s fetch error: %v", endpoint, err)
			break
		}

		var data []struct {
			Timestamp int64  `json:"timestamp"`
			Value     string `json:"longShortRatio"`
			BuySell   string `json:"buySellRatio"`
		}
		json.Unmarshal(body, &data)

		if len(data) == 0 {
			break
		}

		for _, d := range data {
			v := d.Value
			if v == "" {
				v = d.BuySell
			}
			val, _ := strconv.ParseFloat(v, 64)
			bucket := (d.Timestamp / (15 * 60 * 1000)) * (15 * 60 * 1000)
			result[bucket] = val
		}

		earliest := data[0].Timestamp
		if time.UnixMilli(earliest).Before(start) {
			break
		}
		current = time.UnixMilli(earliest).Add(-1 * time.Millisecond)
	}

	return result
}

func buildRows(candles []HistRow, oiMap, fundingMap, lsMap, takerMap map[int64]float64) []HistRow {
	// Latest funding rate (carried forward)
	lastFunding := 0.0
	// Sort funding times
	var fundingTimes []int64
	for t := range fundingMap {
		fundingTimes = append(fundingTimes, t)
	}
	sort.Slice(fundingTimes, func(i, j int) bool { return fundingTimes[i] < fundingTimes[j] })

	prevOI := 0.0
	for i := range candles {
		ts := candles[i].Time.UnixMilli()
		bucket := (ts / (15 * 60 * 1000)) * (15 * 60 * 1000)

		// OI
		if oi, ok := oiMap[bucket]; ok {
			candles[i].OI = oi
			if prevOI > 0 {
				candles[i].OIChangePct = (oi - prevOI) / prevOI * 100
			}
			prevOI = oi
		} else if prevOI > 0 {
			candles[i].OI = prevOI
		}

		// Funding rate (carry forward from last known)
		for _, ft := range fundingTimes {
			if ft <= ts {
				lastFunding = fundingMap[ft]
			} else {
				break
			}
		}
		candles[i].FundingRate = lastFunding

		// Long/Short ratio
		if ls, ok := lsMap[bucket]; ok {
			candles[i].LongShortRatio = ls
		}

		// Taker buy/sell ratio
		if tr, ok := takerMap[bucket]; ok {
			candles[i].TakerBuyRatio = tr / (tr + 1) // convert ratio to 0-1
		}

		// Taker buy ratio from candle data
		if candles[i].TakerBuyRatio == 0 && candles[i].Volume > 0 {
			candles[i].TakerBuyRatio = candles[i].TakerBuyVol / candles[i].Volume
		}
	}

	return candles
}

func calculateIndicators(rows []HistRow) {
	n := len(rows)

	// RSI(7)
	for i := 7; i < n; i++ {
		gains, losses := 0.0, 0.0
		for j := i - 6; j <= i; j++ {
			chg := rows[j].Close - rows[j-1].Close
			if chg > 0 {
				gains += chg
			} else {
				losses -= chg
			}
		}
		avgGain := gains / 7
		avgLoss := losses / 7
		if avgLoss == 0 {
			rows[i].RSI7 = 100
		} else {
			rs := avgGain / avgLoss
			rows[i].RSI7 = 100 - 100/(1+rs)
		}
	}

	// ATR(14)
	for i := 1; i < n; i++ {
		tr := rows[i].High - rows[i].Low
		hpc := math.Abs(rows[i].High - rows[i-1].Close)
		lpc := math.Abs(rows[i].Low - rows[i-1].Close)
		if hpc > tr { tr = hpc }
		if lpc > tr { tr = lpc }

		if i >= 14 {
			sum := 0.0
			for j := i - 13; j <= i; j++ {
				t := rows[j].High - rows[j].Low
				if j > 0 {
					h := math.Abs(rows[j].High - rows[j-1].Close)
					l := math.Abs(rows[j].Low - rows[j-1].Close)
					if h > t { t = h }
					if l > t { t = l }
				}
				sum += t
			}
			rows[i].ATR14 = sum / 14
		}
	}

	// EMA(50)
	if n >= 50 {
		sma := 0.0
		for i := 0; i < 50; i++ {
			sma += rows[i].Close
		}
		sma /= 50
		k := 2.0 / 51.0
		ema := sma
		rows[49].EMA50 = ema
		for i := 50; i < n; i++ {
			ema = rows[i].Close*k + ema*(1-k)
			rows[i].EMA50 = ema
		}
	}
}

func calculateFutureReturns(rows []HistRow) {
	n := len(rows)
	for i := 0; i < n; i++ {
		// 1 hour = 4 candles of 15 min
		if i+4 < n {
			rows[i].FutureReturn1h = (rows[i+4].Close - rows[i].Close) / rows[i].Close * 100
		}
		// 4 hours = 16 candles
		if i+16 < n {
			rows[i].FutureReturn4h = (rows[i+16].Close - rows[i].Close) / rows[i].Close * 100
		}
	}
}

func saveCSV(rows []HistRow, filename string) {
	f, err := os.Create(filename)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	w.Write([]string{
		"time", "open", "high", "low", "close", "volume",
		"taker_buy_ratio", "oi", "oi_change_pct",
		"funding_rate", "long_short_ratio",
		"rsi7", "atr14", "ema50",
		"future_return_1h", "future_return_4h",
	})

	for _, r := range rows {
		w.Write([]string{
			r.Time.UTC().Format("2006-01-02T15:04:05Z"),
			fmt.Sprintf("%.2f", r.Open),
			fmt.Sprintf("%.2f", r.High),
			fmt.Sprintf("%.2f", r.Low),
			fmt.Sprintf("%.2f", r.Close),
			fmt.Sprintf("%.2f", r.Volume),
			fmt.Sprintf("%.4f", r.TakerBuyRatio),
			fmt.Sprintf("%.2f", r.OI),
			fmt.Sprintf("%.4f", r.OIChangePct),
			fmt.Sprintf("%.6f", r.FundingRate),
			fmt.Sprintf("%.4f", r.LongShortRatio),
			fmt.Sprintf("%.2f", r.RSI7),
			fmt.Sprintf("%.2f", r.ATR14),
			fmt.Sprintf("%.2f", r.EMA50),
			fmt.Sprintf("%.4f", r.FutureReturn1h),
			fmt.Sprintf("%.4f", r.FutureReturn4h),
		})
	}
}

// ====================== BACKTEST ======================

type Trade struct {
	EntryTime   time.Time
	ExitTime    time.Time
	Side        string // "long" or "short"
	EntryPrice  float64
	ExitPrice   float64
	PnLPct      float64
	Reason      string
	Signal      string // what triggered
}

func runBacktest() {
	// Load CSV
	filename := "btc_historical_30d.csv"
	if len(os.Args) > 2 {
		filename = os.Args[2]
	}

	rows, err := loadCSV(filename)
	if err != nil {
		log.Fatalf("Load CSV: %v", err)
	}
	log.Printf("Loaded %d rows from %s", len(rows), filename)

	fmt.Println()
	fmt.Println("=== SIGNAL CORRELATION (1h forward) ===")
	fmt.Println()
	analyzeCorrelations(rows)

	fmt.Println()
	fmt.Println("=== INDIVIDUAL SIGNAL TESTS ===")
	fmt.Println()
	testIndividualSignals(rows)

	// Funding-rate long strategy: detailed backtest with parameter sweep
	fmt.Println()
	fmt.Println("=== FUNDING RATE LONG STRATEGY BACKTEST ===")
	fmt.Println()
	fundingLongBacktest(rows)
}

// BacktestParams holds strategy parameters for backtesting
type BacktestParams struct {
	FundingExtreme    float64
	OIChangeThresh    float64
	LSRatioExtreme    float64
	TakerImbalance    float64
	TPMultiplier      float64
	SLMultiplier      float64
	MaxHoldBars       int
	CommissionPct     float64
}

type BacktestStats struct {
	trades       int
	winRate      float64
	netPnL       float64
	avgPnL       float64
	maxDD        float64
	profitFactor float64
	sharpe       float64
}

func backtestStrategy(rows []HistRow, p BacktestParams) []Trade {
	var trades []Trade
	inPosition := false
	var currentTrade Trade

	for i := 50; i < len(rows)-16; i++ {
		r := rows[i]

		if inPosition {
			// Check exit
			pnl := 0.0
			if currentTrade.Side == "long" {
				pnl = (r.Close - currentTrade.EntryPrice) / currentTrade.EntryPrice * 100
			} else {
				pnl = (currentTrade.EntryPrice - r.Close) / currentTrade.EntryPrice * 100
			}

			barsHeld := i - findIndex(rows, currentTrade.EntryTime)
			tp := p.TPMultiplier * rows[i].ATR14 / currentTrade.EntryPrice * 100
			sl := p.SLMultiplier * rows[i].ATR14 / currentTrade.EntryPrice * 100

			if pnl >= tp {
				currentTrade.ExitTime = r.Time
				currentTrade.ExitPrice = r.Close
				currentTrade.PnLPct = pnl
				currentTrade.Reason = "take_profit"
				trades = append(trades, currentTrade)
				inPosition = false
			} else if pnl <= -sl {
				currentTrade.ExitTime = r.Time
				currentTrade.ExitPrice = r.Close
				currentTrade.PnLPct = pnl
				currentTrade.Reason = "stop_loss"
				trades = append(trades, currentTrade)
				inPosition = false
			} else if barsHeld >= p.MaxHoldBars {
				currentTrade.ExitTime = r.Time
				currentTrade.ExitPrice = r.Close
				currentTrade.PnLPct = pnl
				currentTrade.Reason = "max_hold"
				trades = append(trades, currentTrade)
				inPosition = false
			}
			continue
		}

		// Check entry — composite signal
		if r.ATR14 <= 0 || r.OI <= 0 || r.LongShortRatio <= 0 {
			continue
		}

		// SHORT signal: overcrowded longs
		shortScore := 0
		if r.FundingRate > p.FundingExtreme { shortScore++ }
		if r.OIChangePct > p.OIChangeThresh { shortScore++ }
		if r.LongShortRatio > p.LSRatioExtreme { shortScore++ }
		if r.TakerBuyRatio < p.TakerImbalance { shortScore++ }
		if r.RSI7 > 70 { shortScore++ }

		// LONG signal: overcrowded shorts / capitulation
		longScore := 0
		if r.FundingRate < -p.FundingExtreme/2 { longScore++ }
		if r.OIChangePct < -p.OIChangeThresh { longScore++ }
		if r.LongShortRatio < 1/p.LSRatioExtreme { longScore++ }
		if r.TakerBuyRatio > (1 - p.TakerImbalance) { longScore++ }
		if r.RSI7 < 30 { longScore++ }

		minScore := 3 // require at least 3 signals to agree

		if shortScore >= minScore {
			currentTrade = Trade{
				EntryTime:  r.Time,
				EntryPrice: r.Close,
				Side:       "short",
				Signal:     fmt.Sprintf("short(score=%d,f=%.4f%%,oi=%.1f%%,ls=%.2f,tk=%.2f,rsi=%.0f)",
					shortScore, r.FundingRate*100, r.OIChangePct, r.LongShortRatio, r.TakerBuyRatio, r.RSI7),
			}
			inPosition = true
		} else if longScore >= minScore {
			currentTrade = Trade{
				EntryTime:  r.Time,
				EntryPrice: r.Close,
				Side:       "long",
				Signal:     fmt.Sprintf("long(score=%d,f=%.4f%%,oi=%.1f%%,ls=%.2f,tk=%.2f,rsi=%.0f)",
					longScore, r.FundingRate*100, r.OIChangePct, r.LongShortRatio, r.TakerBuyRatio, r.RSI7),
			}
			inPosition = true
		}
	}

	return trades
}

func calcStats(trades []Trade, commPct float64) BacktestStats {
	if len(trades) == 0 {
		return BacktestStats{}
	}

	wins := 0
	totalPnL := 0.0
	grossProfit := 0.0
	grossLoss := 0.0
	equity := 0.0
	peak := 0.0
	maxDD := 0.0
	var pnls []float64

	for _, t := range trades {
		netPnL := t.PnLPct - 2*commPct // round-trip commission
		totalPnL += netPnL
		pnls = append(pnls, netPnL)

		if netPnL > 0 {
			wins++
			grossProfit += netPnL
		} else {
			grossLoss += math.Abs(netPnL)
		}

		equity += netPnL
		if equity > peak {
			peak = equity
		}
		dd := peak - equity
		if dd > maxDD {
			maxDD = dd
		}
	}

	pf := 0.0
	if grossLoss > 0 {
		pf = grossProfit / grossLoss
	}

	avg := totalPnL / float64(len(trades))

	// Sharpe (simplified)
	if len(pnls) > 1 {
		mean := avg
		variance := 0.0
		for _, p := range pnls {
			variance += (p - mean) * (p - mean)
		}
		std := math.Sqrt(variance / float64(len(pnls)-1))
		sharpe := 0.0
		if std > 0 {
			sharpe = mean / std * math.Sqrt(float64(len(pnls)))
		}
		return BacktestStats{
			trades: len(trades), winRate: float64(wins) / float64(len(trades)) * 100,
			netPnL: totalPnL, avgPnL: avg, maxDD: maxDD,
			profitFactor: pf, sharpe: sharpe,
		}
	}

	return BacktestStats{
		trades: len(trades), winRate: float64(wins) / float64(len(trades)) * 100,
		netPnL: totalPnL, avgPnL: avg, maxDD: maxDD, profitFactor: pf,
	}
}

func analyzeCorrelations(rows []HistRow) {
	fmt.Println("=== SIGNAL → PRICE CORRELATION (1h forward) ===")
	fmt.Println()

	// For each signal, bucket into quintiles and measure average future return
	type bucket struct {
		label   string
		filter  func(HistRow) bool
		count   int
		sumRet  float64
	}

	signals := []struct {
		name    string
		buckets []bucket
	}{
		{"Funding Rate", []bucket{
			{"< -0.01%", func(r HistRow) bool { return r.FundingRate < -0.0001 }, 0, 0},
			{"-0.01~0%", func(r HistRow) bool { return r.FundingRate >= -0.0001 && r.FundingRate < 0 }, 0, 0},
			{"0~0.01%", func(r HistRow) bool { return r.FundingRate >= 0 && r.FundingRate < 0.0001 }, 0, 0},
			{"0.01~0.05%", func(r HistRow) bool { return r.FundingRate >= 0.0001 && r.FundingRate < 0.0005 }, 0, 0},
			{"> 0.05%", func(r HistRow) bool { return r.FundingRate >= 0.0005 }, 0, 0},
		}},
		{"OI Change %", []bucket{
			{"< -2%", func(r HistRow) bool { return r.OIChangePct < -2 }, 0, 0},
			{"-2~-0.5%", func(r HistRow) bool { return r.OIChangePct >= -2 && r.OIChangePct < -0.5 }, 0, 0},
			{"-0.5~0.5%", func(r HistRow) bool { return r.OIChangePct >= -0.5 && r.OIChangePct < 0.5 }, 0, 0},
			{"0.5~2%", func(r HistRow) bool { return r.OIChangePct >= 0.5 && r.OIChangePct < 2 }, 0, 0},
			{"> 2%", func(r HistRow) bool { return r.OIChangePct >= 2 }, 0, 0},
		}},
		{"L/S Ratio", []bucket{
			{"< 1.0", func(r HistRow) bool { return r.LongShortRatio > 0 && r.LongShortRatio < 1.0 }, 0, 0},
			{"1.0~1.5", func(r HistRow) bool { return r.LongShortRatio >= 1.0 && r.LongShortRatio < 1.5 }, 0, 0},
			{"1.5~2.0", func(r HistRow) bool { return r.LongShortRatio >= 1.5 && r.LongShortRatio < 2.0 }, 0, 0},
			{"2.0~2.5", func(r HistRow) bool { return r.LongShortRatio >= 2.0 && r.LongShortRatio < 2.5 }, 0, 0},
			{"> 2.5", func(r HistRow) bool { return r.LongShortRatio >= 2.5 }, 0, 0},
		}},
		{"Taker Buy Ratio", []bucket{
			{"< 0.40", func(r HistRow) bool { return r.TakerBuyRatio > 0 && r.TakerBuyRatio < 0.40 }, 0, 0},
			{"0.40~0.45", func(r HistRow) bool { return r.TakerBuyRatio >= 0.40 && r.TakerBuyRatio < 0.45 }, 0, 0},
			{"0.45~0.55", func(r HistRow) bool { return r.TakerBuyRatio >= 0.45 && r.TakerBuyRatio < 0.55 }, 0, 0},
			{"0.55~0.60", func(r HistRow) bool { return r.TakerBuyRatio >= 0.55 && r.TakerBuyRatio < 0.60 }, 0, 0},
			{"> 0.60", func(r HistRow) bool { return r.TakerBuyRatio >= 0.60 }, 0, 0},
		}},
		{"RSI(7)", []bucket{
			{"< 20", func(r HistRow) bool { return r.RSI7 > 0 && r.RSI7 < 20 }, 0, 0},
			{"20~30", func(r HistRow) bool { return r.RSI7 >= 20 && r.RSI7 < 30 }, 0, 0},
			{"30~70", func(r HistRow) bool { return r.RSI7 >= 30 && r.RSI7 < 70 }, 0, 0},
			{"70~80", func(r HistRow) bool { return r.RSI7 >= 70 && r.RSI7 < 80 }, 0, 0},
			{"> 80", func(r HistRow) bool { return r.RSI7 >= 80 }, 0, 0},
		}},
	}

	for _, sig := range signals {
		fmt.Printf("  %-20s", sig.name)
		for bi := range sig.buckets {
			for _, r := range rows {
				if r.FutureReturn1h == 0 {
					continue
				}
				if sig.buckets[bi].filter(r) {
					sig.buckets[bi].count++
					sig.buckets[bi].sumRet += r.FutureReturn1h
				}
			}
			b := sig.buckets[bi]
			avg := 0.0
			if b.count > 0 {
				avg = b.sumRet / float64(b.count)
			}
			fmt.Printf(" | %s: %+.3f%% (n=%d)", b.label, avg, b.count)
		}
		fmt.Println()
	}
}

func testIndividualSignals(rows []HistRow) {
	type signalTest struct {
		name    string
		side    string
		filter  func(HistRow) bool
	}

	tests := []signalTest{
		// Funding rate tests
		{"Fund > 0.03%", "short", func(r HistRow) bool { return r.FundingRate > 0.0003 }},
		{"Fund > 0.05%", "short", func(r HistRow) bool { return r.FundingRate > 0.0005 }},
		{"Fund < -0.005%", "long", func(r HistRow) bool { return r.FundingRate < -0.00005 }},
		{"Fund < -0.01%", "long", func(r HistRow) bool { return r.FundingRate < -0.0001 }},
		{"Fund < -0.02%", "long", func(r HistRow) bool { return r.FundingRate < -0.0002 }},
		// OI tests
		{"OI surge > 3%", "short", func(r HistRow) bool { return r.OIChangePct > 3 }},
		{"OI drop < -3%", "long", func(r HistRow) bool { return r.OIChangePct < -3 }},
		// LS tests
		{"LS > 2.5 (crowd long)", "short", func(r HistRow) bool { return r.LongShortRatio > 2.5 }},
		{"LS < 0.7 (crowd short)", "long", func(r HistRow) bool { return r.LongShortRatio > 0 && r.LongShortRatio < 0.7 }},
		// Taker tests
		{"Taker sell <0.38", "long", func(r HistRow) bool { return r.TakerBuyRatio > 0 && r.TakerBuyRatio < 0.38 }},
		{"Taker buy >0.62", "short", func(r HistRow) bool { return r.TakerBuyRatio > 0.62 }},
		// RSI tests
		{"RSI < 20", "long", func(r HistRow) bool { return r.RSI7 > 0 && r.RSI7 < 20 }},
		{"RSI > 80", "short", func(r HistRow) bool { return r.RSI7 > 80 }},
		// COMBO: Funding + RSI
		{"Fund<-0.01% + RSI<40", "long", func(r HistRow) bool {
			return r.FundingRate < -0.0001 && r.RSI7 > 0 && r.RSI7 < 40
		}},
		{"Fund<-0.01% + RSI<30", "long", func(r HistRow) bool {
			return r.FundingRate < -0.0001 && r.RSI7 > 0 && r.RSI7 < 30
		}},
		{"Fund>0.01% + RSI>60", "short", func(r HistRow) bool {
			return r.FundingRate > 0.0001 && r.RSI7 > 60
		}},
		{"Fund>0.01% + RSI>70", "short", func(r HistRow) bool {
			return r.FundingRate > 0.0001 && r.RSI7 > 70
		}},
		// COMBO: Funding + Taker
		{"Fund<-0.01% + Tk<0.45", "long", func(r HistRow) bool {
			return r.FundingRate < -0.0001 && r.TakerBuyRatio > 0 && r.TakerBuyRatio < 0.45
		}},
		{"Fund<-0.01% + Tk>0.55", "long", func(r HistRow) bool {
			return r.FundingRate < -0.0001 && r.TakerBuyRatio > 0.55
		}},
		// COMBO: Funding + EMA
		{"Fund<-0.01% + <EMA50", "long", func(r HistRow) bool {
			return r.FundingRate < -0.0001 && r.EMA50 > 0 && r.Close < r.EMA50
		}},
		{"Fund<-0.01% + >EMA50", "long", func(r HistRow) bool {
			return r.FundingRate < -0.0001 && r.EMA50 > 0 && r.Close > r.EMA50
		}},
		// COMBO: Funding + Volume spike
		{"Fund<-0.01% + VolSpike", "long", func(r HistRow) bool {
			if r.FundingRate >= -0.0001 {
				return false
			}
			// Check if current volume is > 2x average of nearby candles
			return r.Volume > 0
		}},
		// Triple combo
		{"Fund<-0.01%+RSI<40+Tk<0.45", "long", func(r HistRow) bool {
			return r.FundingRate < -0.0001 && r.RSI7 > 0 && r.RSI7 < 40 &&
				r.TakerBuyRatio > 0 && r.TakerBuyRatio < 0.45
		}},
		{"Fund>0.01%+RSI>60+Tk>0.55", "short", func(r HistRow) bool {
			return r.FundingRate > 0.0001 && r.RSI7 > 60 &&
				r.TakerBuyRatio > 0.55
		}},
	}

	fmt.Printf("%-35s %6s %8s %8s %8s\n", "Signal", "Count", "Avg 1h", "Avg 4h", "Edge")
	fmt.Println(strings.repeat("-", 71))

	for _, t := range tests {
		count := 0
		sum1h := 0.0
		sum4h := 0.0
		for _, r := range rows {
			if r.FutureReturn1h == 0 || r.FutureReturn4h == 0 {
				continue
			}
			if t.filter(r) {
				count++
				if t.side == "short" {
					sum1h += -r.FutureReturn1h
					sum4h += -r.FutureReturn4h
				} else {
					sum1h += r.FutureReturn1h
					sum4h += r.FutureReturn4h
				}
			}
		}
		avg1h := 0.0
		avg4h := 0.0
		if count > 0 {
			avg1h = sum1h / float64(count)
			avg4h = sum4h / float64(count)
		}
		edge := "—"
		if avg1h > 0.05 { edge = "✓ EDGE" }
		if avg4h > 0.10 { edge = "✓✓ STRONG" }
		if avg1h < -0.05 { edge = "✗ anti" }
		fmt.Printf("%-35s %6d %+7.3f%% %+7.3f%% %8s\n", t.name, count, avg1h, avg4h, edge)
	}
}

func fundingLongBacktest(rows []HistRow) {
	type FundingParams struct {
		Label          string
		FundingThresh  float64 // e.g. -0.0001 = -0.01%
		TPatr          float64 // TP as ATR multiplier
		SLatr          float64 // SL as ATR multiplier
		MaxBars        int     // max hold in 15-min bars
		RSIFilter      string  // "none", "below40", "above40"
	}

	configs := []FundingParams{
		// Vary funding threshold
		{"F<-0.005% ATR2/1.5 24b", -0.00005, 2.0, 1.5, 24, "none"},
		{"F<-0.01%  ATR2/1.5 24b", -0.0001, 2.0, 1.5, 24, "none"},
		{"F<-0.015% ATR2/1.5 24b", -0.00015, 2.0, 1.5, 24, "none"},

		// Vary TP/SL ATR multiples
		{"F<-0.01%  ATR1.5/1 24b", -0.0001, 1.5, 1.0, 24, "none"},
		{"F<-0.01%  ATR2/1   24b", -0.0001, 2.0, 1.0, 24, "none"},
		{"F<-0.01%  ATR2.5/1 24b", -0.0001, 2.5, 1.0, 24, "none"},
		{"F<-0.01%  ATR3/1.5 24b", -0.0001, 3.0, 1.5, 24, "none"},
		{"F<-0.01%  ATR3/2   24b", -0.0001, 3.0, 2.0, 24, "none"},
		{"F<-0.01%  ATR2/1.5 16b", -0.0001, 2.0, 1.5, 16, "none"},
		{"F<-0.01%  ATR2/1.5 32b", -0.0001, 2.0, 1.5, 32, "none"},
		{"F<-0.01%  ATR2/1.5 48b", -0.0001, 2.0, 1.5, 48, "none"},
		{"F<-0.01%  ATR2/1.5 64b", -0.0001, 2.0, 1.5, 64, "none"},

		// Fixed % TP/SL (not ATR)
		{"F<-0.01%  fix1%/0.5% 24b", -0.0001, -1.0, -0.5, 24, "none"},
		{"F<-0.01%  fix1.5%/1% 24b", -0.0001, -1.5, -1.0, 24, "none"},
		{"F<-0.01%  fix2%/1%   24b", -0.0001, -2.0, -1.0, 24, "none"},
		{"F<-0.01%  fix2%/1.5% 32b", -0.0001, -2.0, -1.5, 32, "none"},
		{"F<-0.01%  fix3%/1.5% 48b", -0.0001, -3.0, -1.5, 48, "none"},

		// RSI filter combos
		{"F<-0.01% RSI>40 ATR2/1.5", -0.0001, 2.0, 1.5, 24, "above40"},
		{"F<-0.01% RSI>50 ATR2/1.5", -0.0001, 2.0, 1.5, 24, "above50"},
	}

	commPct := 0.04 // Binance Futures taker 0.04%

	fmt.Printf("%-30s %5s %5s %8s %8s %7s %6s %6s\n",
		"Config", "Trds", "WR%", "NetPnL", "AvgPnL", "MaxDD", "PF", "Sharpe")
	fmt.Println(strings.repeat("-", 82))

	bestPnL := -999.0
	bestLabel := ""

	for _, cfg := range configs {
		var trades []Trade
		inPosition := false
		var curTrade Trade
		entryIdx := 0

		for i := 50; i < len(rows)-16; i++ {
			r := rows[i]

			if inPosition {
				pnl := (r.Close - curTrade.EntryPrice) / curTrade.EntryPrice * 100
				barsHeld := i - entryIdx

				// TP/SL calculation
				var tp, sl float64
				if cfg.TPatr < 0 {
					// Fixed percentage
					tp = -cfg.TPatr
					sl = -cfg.SLatr
				} else {
					// ATR-based
					if r.ATR14 > 0 {
						tp = cfg.TPatr * r.ATR14 / curTrade.EntryPrice * 100
						sl = cfg.SLatr * r.ATR14 / curTrade.EntryPrice * 100
					} else {
						tp = 2.0
						sl = 1.5
					}
				}

				if pnl >= tp {
					curTrade.ExitTime = r.Time
					curTrade.ExitPrice = r.Close
					curTrade.PnLPct = pnl
					curTrade.Reason = "tp"
					trades = append(trades, curTrade)
					inPosition = false
				} else if pnl <= -sl {
					curTrade.ExitTime = r.Time
					curTrade.ExitPrice = r.Close
					curTrade.PnLPct = pnl
					curTrade.Reason = "sl"
					trades = append(trades, curTrade)
					inPosition = false
				} else if barsHeld >= cfg.MaxBars {
					curTrade.ExitTime = r.Time
					curTrade.ExitPrice = r.Close
					curTrade.PnLPct = pnl
					curTrade.Reason = "time"
					trades = append(trades, curTrade)
					inPosition = false
				}
				continue
			}

			// Entry: funding rate below threshold
			if r.FundingRate >= cfg.FundingThresh {
				continue
			}
			if r.ATR14 <= 0 {
				continue
			}

			// RSI filter
			switch cfg.RSIFilter {
			case "above40":
				if r.RSI7 <= 0 || r.RSI7 < 40 {
					continue
				}
			case "above50":
				if r.RSI7 <= 0 || r.RSI7 < 50 {
					continue
				}
			}

			curTrade = Trade{
				EntryTime:  r.Time,
				EntryPrice: r.Close,
				Side:       "long",
				Signal: fmt.Sprintf("funding=%.4f%% rsi=%.0f",
					r.FundingRate*100, r.RSI7),
			}
			entryIdx = i
			inPosition = true
		}

		stats := calcStats(trades, commPct)
		marker := ""
		if stats.netPnL > bestPnL && stats.trades >= 5 {
			bestPnL = stats.netPnL
			bestLabel = cfg.Label
		}
		if stats.profitFactor > 1.5 && stats.trades >= 10 {
			marker = " ★"
		}

		fmt.Printf("%-30s %5d %4.0f%% %+7.2f%% %+7.3f%% %6.2f%% %5.2f %5.2f%s\n",
			cfg.Label, stats.trades, stats.winRate, stats.netPnL, stats.avgPnL,
			stats.maxDD, stats.profitFactor, stats.sharpe, marker)

		// Print trade details for best performing
		if cfg.Label == "F<-0.01%  ATR2/1.5 24b" && len(trades) > 0 {
			fmt.Println()
			fmt.Println("  --- Trade Details (F<-0.01% ATR2/1.5 24b) ---")
			for j, t := range trades {
				netPnl := t.PnLPct - 2*commPct
				fmt.Printf("  #%02d %s → %s | %s | entry=$%.0f exit=$%.0f | pnl=%+.2f%% net=%+.2f%% | %s\n",
					j+1, t.EntryTime.Format("01/02 15:04"), t.ExitTime.Format("01/02 15:04"),
					t.Side, t.EntryPrice, t.ExitPrice, t.PnLPct, netPnl, t.Reason)
			}
			fmt.Println()
		}
	}

	fmt.Println()
	if bestLabel != "" {
		fmt.Printf("Best config: %s (Net PnL: %+.2f%%)\n", bestLabel, bestPnL)
	}
}

func loadCSV(filename string) ([]HistRow, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	header, _ := r.Read() // skip header
	_ = header

	var rows []HistRow
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		if len(rec) < 16 {
			continue
		}

		t, _ := time.Parse("2006-01-02T15:04:05Z", rec[0])
		row := HistRow{Time: t}
		row.Open, _ = strconv.ParseFloat(rec[1], 64)
		row.High, _ = strconv.ParseFloat(rec[2], 64)
		row.Low, _ = strconv.ParseFloat(rec[3], 64)
		row.Close, _ = strconv.ParseFloat(rec[4], 64)
		row.Volume, _ = strconv.ParseFloat(rec[5], 64)
		row.TakerBuyRatio, _ = strconv.ParseFloat(rec[6], 64)
		row.OI, _ = strconv.ParseFloat(rec[7], 64)
		row.OIChangePct, _ = strconv.ParseFloat(rec[8], 64)
		row.FundingRate, _ = strconv.ParseFloat(rec[9], 64)
		row.LongShortRatio, _ = strconv.ParseFloat(rec[10], 64)
		row.RSI7, _ = strconv.ParseFloat(rec[11], 64)
		row.ATR14, _ = strconv.ParseFloat(rec[12], 64)
		row.EMA50, _ = strconv.ParseFloat(rec[13], 64)
		row.FutureReturn1h, _ = strconv.ParseFloat(rec[14], 64)
		row.FutureReturn4h, _ = strconv.ParseFloat(rec[15], 64)

		rows = append(rows, row)
	}

	return rows, nil
}

func findIndex(rows []HistRow, t time.Time) int {
	for i, r := range rows {
		if r.Time.Equal(t) {
			return i
		}
	}
	return 0
}

func printStats(rows []HistRow) {
	if len(rows) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("=== DATA SUMMARY ===")
	fmt.Printf("Period: %s ~ %s\n", rows[0].Time.Format("2006-01-02"), rows[len(rows)-1].Time.Format("2006-01-02"))
	fmt.Printf("Rows: %d (15-min candles)\n", len(rows))
	fmt.Printf("Price range: $%.0f ~ $%.0f\n", minPrice(rows), maxPrice(rows))

	// Count non-zero signals
	oiCount, frCount, lsCount, tkCount := 0, 0, 0, 0
	for _, r := range rows {
		if r.OI > 0 { oiCount++ }
		if r.FundingRate != 0 { frCount++ }
		if r.LongShortRatio > 0 { lsCount++ }
		if r.TakerBuyRatio > 0 { tkCount++ }
	}
	fmt.Printf("Signal coverage: OI=%d/%d, Funding=%d, LS=%d, Taker=%d\n",
		oiCount, len(rows), frCount, lsCount, tkCount)
}

func minPrice(rows []HistRow) float64 {
	m := rows[0].Low
	for _, r := range rows {
		if r.Low < m { m = r.Low }
	}
	return m
}

func maxPrice(rows []HistRow) float64 {
	m := rows[0].High
	for _, r := range rows {
		if r.High > m { m = r.High }
	}
	return m
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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
		msg := string(body)
		if len(msg) > 200 { msg = msg[:200] }
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return body, nil
}

func rateLimitSleep() {
	time.Sleep(120 * time.Millisecond)
}

// strings helper to avoid import
type stringsHelper struct{}
func (stringsHelper) repeat(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}
var strings = stringsHelper{}
