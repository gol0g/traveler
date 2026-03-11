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
	"time"

	"traveler/pkg/model"
)

// FundingRate represents a single funding rate entry.
type FundingRate struct {
	Time time.Time
	Rate float64
}

// TradeResult represents a completed backtest trade.
type TradeResult struct {
	EntryTime  time.Time
	ExitTime   time.Time
	EntryPrice float64
	ExitPrice  float64
	PnLPct     float64
	Reason     string
	Funding    float64
	RSI        float64
	ATR        float64
}

// Config holds backtest parameters.
type Config struct {
	FundingThreshold float64
	RSIMin           float64
	RSIMax           float64 // 0 = no upper limit
	RSIPeriod        int
	TPAtrMult        float64
	SLAtrMult        float64
	ATRPeriod        int
	MaxHoldBars      int
	CommissionPct    float64
	CooldownBars     int     // bars to wait after exit before re-entry (0 = no cooldown)
	MinATR           float64 // minimum ATR to enter (0 = no minimum)
}

func main() {
	days := 180
	if len(os.Args) > 1 {
		if d, err := strconv.Atoi(os.Args[1]); err == nil {
			days = d
		}
	}

	ctx := context.Background()
	fmt.Printf("Fetching %d days of BTCUSDT 15m candles...\n", days)

	candles := fetchCandles(ctx, "BTCUSDT", 15, days)
	fmt.Printf("Got %d candles\n", len(candles))

	fmt.Println("Fetching funding rates...")
	fundingRates := fetchFundingRates(ctx, "BTCUSDT", days)
	fmt.Printf("Got %d funding rates\n\n", len(fundingRates))

	// Debug: check funding rate coverage
	if len(fundingRates) > 0 && len(candles) > 0 {
		fmt.Printf("Candle range: %s ~ %s\n", candles[0].Time.Format("2006-01-02"), candles[len(candles)-1].Time.Format("2006-01-02"))
		fmt.Printf("Funding range: %s ~ %s\n", fundingRates[0].Time.Format("2006-01-02"), fundingRates[len(fundingRates)-1].Time.Format("2006-01-02"))
		negCount := 0
		for _, r := range fundingRates {
			if r.Rate < 0 {
				negCount++
			}
		}
		fmt.Printf("Negative funding: %d/%d (%.1f%%)\n", negCount, len(fundingRates), float64(negCount)/float64(len(fundingRates))*100)

		// Check how many candles have negative funding
		candlesWithNegFunding := 0
		for _, c := range candles[50:] {
			f := getFundingAt(fundingRates, c.Time)
			if f < 0 {
				candlesWithNegFunding++
			}
		}
		fmt.Printf("Candles with negative funding: %d/%d\n\n", candlesWithNegFunding, len(candles)-50)
	}

	// Parameter grid search: cooldown + RSI max + minATR
	var configs []Config
	rsiMaxes := []float64{0, 75, 80, 85}           // 0 = no limit
	cooldowns := []int{0, 3, 6, 12}                // bars (0/45m/90m/3h)
	minATRs := []float64{0, 200, 300}              // minimum ATR filter

	for _, rsiMax := range rsiMaxes {
		for _, cd := range cooldowns {
			for _, minATR := range minATRs {
				configs = append(configs, Config{
					FundingThreshold: -0.00005,
					RSIMin:           40,
					RSIMax:           rsiMax,
					RSIPeriod:        7,
					TPAtrMult:        2.5,
					SLAtrMult:        1.5,
					ATRPeriod:        14,
					MaxHoldBars:      48,
					CommissionPct:    0.04,
					CooldownBars:     cd,
					MinATR:           minATR,
				})
			}
		}
	}
	fmt.Printf("Grid search: %d combinations\n\n", len(configs))

	type Result struct {
		Cfg     Config
		Trades  int
		Wins    int
		WR      float64
		NetPnL  float64
		PF      float64
		MaxDD   float64
		Sharpe  float64
	}

	var results []Result

	for _, cfg := range configs {
		trades := runBacktest(candles, fundingRates, cfg)
		r := Result{Cfg: cfg, Trades: len(trades)}

		if len(trades) == 0 {
			results = append(results, r)
			continue
		}

		var grossWin, grossLoss float64
		var pnls []float64
		cumPnL := 0.0
		peak := 0.0
		maxDD := 0.0

		for _, t := range trades {
			net := t.PnLPct - 2*cfg.CommissionPct
			pnls = append(pnls, net)
			r.NetPnL += net
			if net > 0 {
				r.Wins++
				grossWin += net
			} else {
				grossLoss += math.Abs(net)
			}
			cumPnL += net
			if cumPnL > peak {
				peak = cumPnL
			}
			dd := peak - cumPnL
			if dd > maxDD {
				maxDD = dd
			}
		}

		r.WR = float64(r.Wins) / float64(r.Trades) * 100
		if grossLoss > 0 {
			r.PF = grossWin / grossLoss
		}
		r.MaxDD = maxDD

		// Sharpe (using trade returns)
		if len(pnls) > 1 {
			mean := r.NetPnL / float64(len(pnls))
			var sumSq float64
			for _, p := range pnls {
				sumSq += (p - mean) * (p - mean)
			}
			std := math.Sqrt(sumSq / float64(len(pnls)-1))
			if std > 0 {
				r.Sharpe = mean / std * math.Sqrt(float64(len(pnls)))
			}
		}

		results = append(results, r)
	}

	// Sort by NetPnL
	sort.Slice(results, func(i, j int) bool {
		return results[i].NetPnL > results[j].NetPnL
	})

	fmt.Printf("%-5s %-5s %-4s %-6s %-6s %-8s %-6s %-6s %-6s %-6s\n",
		"RSIx", "CD", "mATR", "Trades", "Net%", "WR%", "PF", "MDD%", "Sharpe", "")
	fmt.Println("------------------------------------------------------------------------")
	for i, r := range results {
		rsiMaxStr := "none"
		if r.Cfg.RSIMax > 0 {
			rsiMaxStr = fmt.Sprintf("%.0f", r.Cfg.RSIMax)
		}
		minATRStr := "0"
		if r.Cfg.MinATR > 0 {
			minATRStr = fmt.Sprintf("%.0f", r.Cfg.MinATR)
		}
		marker := ""
		if i == 0 {
			marker = " ← BEST"
		}
		fmt.Printf("%-5s %-5d %-4s %-6d %-8.2f %-6.1f %-6.2f %-6.2f %-6.2f%s\n",
			rsiMaxStr, r.Cfg.CooldownBars, minATRStr,
			r.Trades, r.NetPnL, r.WR, r.PF, r.MaxDD, r.Sharpe, marker)
	}

	// Print best config details
	if len(results) > 0 && results[0].Trades > 0 {
		best := results[0]
		fmt.Printf("\n=== Best Config ===\n")
		rsiMaxLabel := "none"
		if best.Cfg.RSIMax > 0 {
			rsiMaxLabel = fmt.Sprintf("%.0f", best.Cfg.RSIMax)
		}
		fmt.Printf("Funding < %.4f%%, RSI > %.0f < %s, TP=ATR*%.1f, SL=ATR*%.1f, MaxBars=%d\n",
			best.Cfg.FundingThreshold*100, best.Cfg.RSIMin, rsiMaxLabel,
			best.Cfg.TPAtrMult, best.Cfg.SLAtrMult, best.Cfg.MaxHoldBars)
		fmt.Printf("Cooldown=%d bars, MinATR=%.0f\n", best.Cfg.CooldownBars, best.Cfg.MinATR)
		fmt.Printf("Trades=%d, WR=%.1f%%, Net=%.2f%%, PF=%.2f, MDD=%.2f%%, Sharpe=%.2f\n",
			best.Trades, best.WR, best.NetPnL, best.PF, best.MaxDD, best.Sharpe)
	}
}

func runBacktest(candles []model.Candle, fundingRates []FundingRate, cfg Config) []TradeResult {
	if len(candles) < cfg.RSIPeriod+cfg.ATRPeriod+2 {
		return nil
	}

	var trades []TradeResult
	var inPosition bool
	var entryPrice, sl, tp, entryFunding, entryRSI, entryATR float64
	var entryTime time.Time
	var entryBar int
	lastExitBar := -999 // for cooldown

	for i := 50; i < len(candles); i++ {
		c := candles[i]

		if inPosition {
			barsHeld := i - entryBar
			pnl := (c.Close - entryPrice) / entryPrice * 100

			shouldExit := false
			reason := ""

			if c.Low <= sl {
				shouldExit = true
				reason = "stop_loss"
				pnl = (sl - entryPrice) / entryPrice * 100
			} else if c.High >= tp {
				shouldExit = true
				reason = "take_profit"
				pnl = (tp - entryPrice) / entryPrice * 100
			} else if barsHeld >= cfg.MaxHoldBars {
				shouldExit = true
				reason = "max_hold"
			}

			if shouldExit {
				exitPrice := c.Close
				if reason == "stop_loss" {
					exitPrice = sl
				} else if reason == "take_profit" {
					exitPrice = tp
				}
				trades = append(trades, TradeResult{
					EntryTime: entryTime, ExitTime: c.Time,
					EntryPrice: entryPrice, ExitPrice: exitPrice,
					PnLPct: pnl, Reason: reason,
					Funding: entryFunding, RSI: entryRSI, ATR: entryATR,
				})
				inPosition = false
				lastExitBar = i
			}
			continue
		}

		// Cooldown: wait N bars after last exit
		if cfg.CooldownBars > 0 && (i-lastExitBar) < cfg.CooldownBars {
			continue
		}

		// Check entry
		funding := getFundingAt(fundingRates, c.Time)
		if funding >= cfg.FundingThreshold {
			continue
		}

		rsi := calcRSI(candles[:i+1], cfg.RSIPeriod)
		if rsi > 0 && rsi < cfg.RSIMin {
			continue
		}
		if cfg.RSIMax > 0 && rsi > cfg.RSIMax {
			continue
		}

		atr := calcATR(candles[:i+1], cfg.ATRPeriod)
		if atr <= 0 {
			continue
		}

		// MinATR filter: skip if ATR too small (SL too tight)
		if cfg.MinATR > 0 && atr < cfg.MinATR {
			continue
		}

		// Enter long
		inPosition = true
		entryPrice = c.Close
		entryTime = c.Time
		entryBar = i
		entryFunding = funding
		entryRSI = rsi
		entryATR = atr
		tp = entryPrice + atr*cfg.TPAtrMult
		sl = entryPrice - atr*cfg.SLAtrMult
	}

	return trades
}

func getFundingAt(rates []FundingRate, t time.Time) float64 {
	// Find the most recent funding rate before time t
	best := 0.0
	for _, r := range rates {
		if r.Time.Before(t) || r.Time.Equal(t) {
			best = r.Rate
		} else {
			break
		}
	}
	return best
}

func calcRSI(candles []model.Candle, period int) float64 {
	if len(candles) < period+1 {
		return 0
	}
	var gainSum, lossSum float64
	for i := len(candles) - period; i < len(candles); i++ {
		diff := candles[i].Close - candles[i-1].Close
		if diff > 0 {
			gainSum += diff
		} else {
			lossSum -= diff
		}
	}
	if lossSum == 0 {
		return 100
	}
	rs := (gainSum / float64(period)) / (lossSum / float64(period))
	return 100 - 100/(1+rs)
}

func calcATR(candles []model.Candle, period int) float64 {
	if len(candles) < period+1 {
		return 0
	}
	var sum float64
	for i := len(candles) - period; i < len(candles); i++ {
		tr := candles[i].High - candles[i].Low
		d1 := math.Abs(candles[i].High - candles[i-1].Close)
		d2 := math.Abs(candles[i].Low - candles[i-1].Close)
		if d1 > tr {
			tr = d1
		}
		if d2 > tr {
			tr = d2
		}
		sum += tr
	}
	return sum / float64(period)
}

func fetchCandles(ctx context.Context, symbol string, interval, days int) []model.Candle {
	client := &http.Client{Timeout: 30 * time.Second}
	var allCandles []model.Candle

	endTime := time.Now().UnixMilli()
	batchSize := 1500
	intervStr := fmt.Sprintf("%dm", interval)
	batchDuration := int64(batchSize) * int64(interval) * 60 * 1000

	startLimit := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()

	for {
		startTime := endTime - batchDuration
		if startTime < startLimit {
			startTime = startLimit
		}

		url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/klines?symbol=%s&interval=%s&startTime=%d&endTime=%d&limit=%d",
			symbol, intervStr, startTime, endTime, batchSize)

		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			break
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var raw [][]json.RawMessage
		json.Unmarshal(body, &raw)

		if len(raw) == 0 {
			break
		}

		for _, k := range raw {
			if len(k) < 6 {
				continue
			}
			var openTimeMs int64
			json.Unmarshal(k[0], &openTimeMs)
			var o, h, l, c, v string
			json.Unmarshal(k[1], &o)
			json.Unmarshal(k[2], &h)
			json.Unmarshal(k[3], &l)
			json.Unmarshal(k[4], &c)
			json.Unmarshal(k[5], &v)
			op, _ := strconv.ParseFloat(o, 64)
			hi, _ := strconv.ParseFloat(h, 64)
			lo, _ := strconv.ParseFloat(l, 64)
			cl, _ := strconv.ParseFloat(c, 64)
			vol, _ := strconv.ParseFloat(v, 64)
			allCandles = append(allCandles, model.Candle{
				Time: time.UnixMilli(openTimeMs), Open: op, High: hi, Low: lo, Close: cl,
				Volume: int64(vol * 1e6),
			})
		}

		// Move window back
		var firstTimeMs int64
		json.Unmarshal(raw[0][0], &firstTimeMs)
		endTime = firstTimeMs - 1

		if endTime <= startLimit {
			break
		}

		time.Sleep(200 * time.Millisecond)
	}

	sort.Slice(allCandles, func(i, j int) bool {
		return allCandles[i].Time.Before(allCandles[j].Time)
	})

	// Deduplicate
	if len(allCandles) > 1 {
		dedup := []model.Candle{allCandles[0]}
		for i := 1; i < len(allCandles); i++ {
			if !allCandles[i].Time.Equal(allCandles[i-1].Time) {
				dedup = append(dedup, allCandles[i])
			}
		}
		allCandles = dedup
	}

	return allCandles
}

func fetchFundingRates(ctx context.Context, symbol string, days int) []FundingRate {
	client := &http.Client{Timeout: 30 * time.Second}
	var allRates []FundingRate

	startTime := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()

	for {
		url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/fundingRate?symbol=%s&startTime=%d&limit=1000",
			symbol, startTime)

		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := client.Do(req)
		if err != nil {
			break
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var raw []struct {
			FundingTime int64  `json:"fundingTime"`
			FundingRate string `json:"fundingRate"`
		}
		json.Unmarshal(body, &raw)

		if len(raw) == 0 {
			break
		}

		for _, r := range raw {
			rate, _ := strconv.ParseFloat(r.FundingRate, 64)
			allRates = append(allRates, FundingRate{
				Time: time.UnixMilli(r.FundingTime),
				Rate: rate,
			})
		}

		// Move forward
		startTime = raw[len(raw)-1].FundingTime + 1
		if startTime >= time.Now().UnixMilli() {
			break
		}

		time.Sleep(200 * time.Millisecond)
	}

	sort.Slice(allRates, func(i, j int) bool {
		return allRates[i].Time.Before(allRates[j].Time)
	})

	return allRates
}
