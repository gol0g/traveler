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

// DipConfig holds backtest parameters.
type DipConfig struct {
	DipMinDrop  float64 // min drop % to trigger (e.g. -3.0)
	LookbackMin int     // lookback window in candles (e.g. 6 = 30min at 5min candles)
	SLPct       float64 // stop loss %
	TPPct       float64 // take profit %
	MaxHoldBars int     // force close after N bars
	CommPct     float64 // per side commission %
}

type Trade struct {
	Symbol     string
	EntryTime  time.Time
	ExitTime   time.Time
	EntryPrice float64
	ExitPrice  float64
	PnLPct     float64
	Reason     string
}

func main() {
	days := 90
	if len(os.Args) > 1 {
		if d, err := strconv.Atoi(os.Args[1]); err == nil {
			days = d
		}
	}

	ctx := context.Background()
	// Use Binance Futures 5-min candles for KR-like intraday simulation
	// (KIS doesn't provide bulk historical minute data)
	symbols := []string{"ETHUSDT", "SOLUSDT", "XRPUSDT", "BTCUSDT"}

	candleMap := make(map[string][]model.Candle)
	for _, sym := range symbols {
		fmt.Printf("Fetching %d days of %s 5m candles...\n", days, sym)
		candles := fetchCandles(ctx, sym, 5, days)
		candleMap[sym] = candles
		fmt.Printf("  Got %d candles\n", len(candles))
	}

	// Parameter grid
	configs := []DipConfig{
		// Drop threshold sweep
		{-2.0, 6, 1.5, 3.0, 36, 0.04},
		{-2.5, 6, 1.5, 3.0, 36, 0.04},
		{-3.0, 6, 1.5, 3.0, 36, 0.04},
		{-3.5, 6, 1.5, 3.0, 36, 0.04},
		{-4.0, 6, 1.5, 3.0, 36, 0.04},
		{-5.0, 6, 1.5, 3.0, 36, 0.04},

		// TP/SL sweep with -3%
		{-3.0, 6, 1.0, 2.0, 36, 0.04},
		{-3.0, 6, 1.5, 2.0, 36, 0.04},
		{-3.0, 6, 2.0, 3.0, 36, 0.04},
		{-3.0, 6, 2.0, 4.0, 36, 0.04},
		{-3.0, 6, 1.0, 3.0, 36, 0.04},
		{-3.0, 6, 2.5, 5.0, 36, 0.04},

		// Lookback window sweep
		{-3.0, 3, 1.5, 3.0, 36, 0.04},  // 15min lookback
		{-3.0, 12, 1.5, 3.0, 36, 0.04}, // 60min lookback
		{-3.0, 18, 1.5, 3.0, 36, 0.04}, // 90min lookback

		// MaxHold sweep
		{-3.0, 6, 1.5, 3.0, 12, 0.04}, // 1 hour
		{-3.0, 6, 1.5, 3.0, 24, 0.04}, // 2 hours
		{-3.0, 6, 1.5, 3.0, 72, 0.04}, // 6 hours

		// Wider drop + tight SL (knife catching safeguard)
		{-4.0, 6, 1.0, 2.0, 24, 0.04},
		{-5.0, 6, 1.0, 2.0, 24, 0.04},
		{-4.0, 6, 1.5, 2.0, 24, 0.04},

		// KR stock-like commissions (0.25% per side)
		{-3.0, 6, 1.5, 3.0, 36, 0.25},
		{-3.0, 6, 2.0, 4.0, 36, 0.25},
		{-4.0, 6, 1.5, 3.0, 36, 0.25},
		{-5.0, 6, 1.5, 3.0, 36, 0.25},
	}

	type Result struct {
		Cfg    DipConfig
		Trades int
		Wins   int
		WR     float64
		NetPnL float64
		PF     float64
		MaxDD  float64
	}

	var results []Result

	for _, cfg := range configs {
		var allTrades []Trade
		for _, sym := range symbols {
			trades := runBacktest(candleMap[sym], sym, cfg)
			allTrades = append(allTrades, trades...)
		}

		r := Result{Cfg: cfg, Trades: len(allTrades)}
		if len(allTrades) == 0 {
			results = append(results, r)
			continue
		}

		var grossWin, grossLoss float64
		cumPnL := 0.0
		peak := 0.0
		maxDD := 0.0

		for _, t := range allTrades {
			net := t.PnLPct - 2*cfg.CommPct
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
		results = append(results, r)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].NetPnL > results[j].NetPnL
	})

	fmt.Printf("\n%-6s %-4s %-4s %-4s %-5s %-5s | %-6s %-8s %-6s %-6s %-6s\n",
		"Drop", "Lk", "SL", "TP", "Hold", "Comm",
		"Trades", "Net%", "WR%", "PF", "MDD%")
	fmt.Println("--------------------------------------------------------------")
	for _, r := range results {
		fmt.Printf("%-6.1f %-4d %-4.1f %-4.1f %-5d %-5.2f | %-6d %-8.2f %-6.1f %-6.2f %-6.2f\n",
			r.Cfg.DipMinDrop, r.Cfg.LookbackMin, r.Cfg.SLPct, r.Cfg.TPPct,
			r.Cfg.MaxHoldBars, r.Cfg.CommPct,
			r.Trades, r.NetPnL, r.WR, r.PF, r.MaxDD)
	}

	// Best details
	if len(results) > 0 && results[0].Trades > 0 {
		best := results[0]
		fmt.Printf("\n=== Best Config ===\n")
		fmt.Printf("Drop<%.1f%%, Lookback=%d bars, SL=%.1f%%, TP=%.1f%%, Hold=%d, Comm=%.2f%%\n",
			best.Cfg.DipMinDrop, best.Cfg.LookbackMin, best.Cfg.SLPct, best.Cfg.TPPct,
			best.Cfg.MaxHoldBars, best.Cfg.CommPct)
		fmt.Printf("Trades=%d, WR=%.1f%%, Net=%.2f%%, PF=%.2f, MDD=%.2f%%\n",
			best.Trades, best.WR, best.NetPnL, best.PF, best.MaxDD)

		// Print per-symbol breakdown
		for _, sym := range symbols {
			trades := runBacktest(candleMap[sym], sym, best.Cfg)
			if len(trades) == 0 {
				continue
			}
			var net float64
			wins := 0
			for _, t := range trades {
				n := t.PnLPct - 2*best.Cfg.CommPct
				net += n
				if n > 0 {
					wins++
				}
			}
			fmt.Printf("  %s: %d trades, WR=%.0f%%, net=%.2f%%\n",
				sym, len(trades), float64(wins)/float64(len(trades))*100, net)
		}
	}
}

func runBacktest(candles []model.Candle, symbol string, cfg DipConfig) []Trade {
	if len(candles) < cfg.LookbackMin+3 {
		return nil
	}

	var trades []Trade
	var inPosition bool
	var entryPrice float64
	var entryTime time.Time
	var entryBar int

	for i := cfg.LookbackMin + 2; i < len(candles); i++ {
		c := candles[i]

		if inPosition {
			barsHeld := i - entryBar
			pnlPct := (c.Close - entryPrice) / entryPrice * 100

			shouldExit := false
			reason := ""
			exitPrice := c.Close

			// SL: use Low for intra-candle
			slPnl := (c.Low - entryPrice) / entryPrice * 100
			if slPnl <= -cfg.SLPct {
				shouldExit = true
				reason = "stop_loss"
				exitPrice = entryPrice * (1 - cfg.SLPct/100)
				pnlPct = -cfg.SLPct
			}

			// TP: use High for intra-candle
			tpPnl := (c.High - entryPrice) / entryPrice * 100
			if !shouldExit && tpPnl >= cfg.TPPct {
				shouldExit = true
				reason = "take_profit"
				exitPrice = entryPrice * (1 + cfg.TPPct/100)
				pnlPct = cfg.TPPct
			}

			// Max hold
			if !shouldExit && barsHeld >= cfg.MaxHoldBars {
				shouldExit = true
				reason = "max_hold"
			}

			if shouldExit {
				trades = append(trades, Trade{
					Symbol: symbol, EntryTime: entryTime, ExitTime: c.Time,
					EntryPrice: entryPrice, ExitPrice: exitPrice,
					PnLPct: pnlPct, Reason: reason,
				})
				inPosition = false
			}
			continue
		}

		// Entry check: dip buy
		// Find recent high in lookback window
		recentHigh := 0.0
		for j := i - cfg.LookbackMin; j < i; j++ {
			if candles[j].High > recentHigh {
				recentHigh = candles[j].High
			}
		}

		if recentHigh <= 0 {
			continue
		}

		price := c.Close
		dropPct := (price - recentHigh) / recentHigh * 100

		if dropPct > cfg.DipMinDrop {
			continue // not enough drop
		}

		// Bounce confirmation: current close > prev close, prev close <= prev-prev close
		if i < 2 {
			continue
		}
		prev1 := candles[i-1].Close
		prev2 := candles[i-2].Close
		isBottoming := price > prev1 && prev1 <= prev2
		if !isBottoming {
			continue
		}

		// Enter on next candle open
		if i+1 < len(candles) {
			inPosition = true
			entryPrice = candles[i+1].Open
			entryTime = candles[i+1].Time
			entryBar = i + 1
		}
	}

	return trades
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
