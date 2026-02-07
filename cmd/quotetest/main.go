package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"traveler/internal/broker/kis"
	"traveler/internal/config"
	"traveler/internal/provider"
	"traveler/internal/strategy"
	"traveler/pkg/model"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatal(err)
	}

	if cfg.KIS.Domestic.AppKey == "" {
		log.Fatal("No domestic credentials in config")
	}

	creds := kis.Credentials{
		AppKey:    cfg.KIS.Domestic.AppKey,
		AppSecret: cfg.KIS.Domestic.AppSecret,
		AccountNo: cfg.KIS.Domestic.AccountNo,
	}

	fmt.Println("=== KIS Domestic API Test ===")

	// 1. Provider test
	fmt.Println("\n[1] KIS Provider - GetDailyCandles for 005930 (삼성전자)")
	kisProv := provider.NewKISProvider(creds)
	ctx := context.Background()

	start := time.Now()
	candles, err := kisProv.GetDailyCandles(ctx, "005930", 100)
	elapsed := time.Since(start)
	if err != nil {
		fmt.Printf("    ERROR: %v\n", err)
	} else {
		fmt.Printf("    OK: %d candles in %s\n", len(candles), elapsed)
		if len(candles) > 0 {
			last := candles[len(candles)-1]
			fmt.Printf("    Last: %s O=%.0f H=%.0f L=%.0f C=%.0f V=%d\n",
				last.Time.Format("2006-01-02"), last.Open, last.High, last.Low, last.Close, last.Volume)
		}
	}

	// 2. Strategy test
	fmt.Println("\n[2] Strategy Analysis for 005930")
	stock := model.Stock{Symbol: "005930", Name: "삼성전자"}
	strategies := strategy.GetAll(kisProv)
	for _, strat := range strategies {
		sig, err := strat.Analyze(ctx, stock)
		if err != nil {
			fmt.Printf("    %s: ERROR - %v\n", strat.Name(), err)
		} else if sig != nil {
			fmt.Printf("    %s: SIGNAL! prob=%.0f%% strength=%.1f reason=%s\n",
				strat.Name(), sig.Probability, sig.Strength, sig.Reason)
		} else {
			fmt.Printf("    %s: no signal\n", strat.Name())
		}
	}

	// 3. Test a few more stocks
	testSymbols := []string{"000660", "035420", "035720", "373220", "068270"}
	fmt.Println("\n[3] Multi-stock scan test")
	for _, sym := range testSymbols {
		start := time.Now()
		c2, err := kisProv.GetDailyCandles(ctx, sym, 100)
		elapsed := time.Since(start)
		if err != nil {
			fmt.Printf("    %s: CANDLE ERROR - %v (%.1fs)\n", sym, err, elapsed.Seconds())
			continue
		}

		var bestStrat string
		var bestProb float64
		for _, strat := range strategies {
			sig, err := strat.Analyze(ctx, model.Stock{Symbol: sym, Name: sym})
			if err == nil && sig != nil && sig.Probability > bestProb {
				bestStrat = strat.Name()
				bestProb = sig.Probability
			}
		}

		last := c2[len(c2)-1]
		if bestStrat != "" {
			fmt.Printf("    %s: %d candles, last=%.0f, SIGNAL(%s, %.0f%%) (%.1fs)\n",
				sym, len(c2), last.Close, bestStrat, bestProb, elapsed.Seconds())
		} else {
			fmt.Printf("    %s: %d candles, last=%.0f, no signal (%.1fs)\n",
				sym, len(c2), last.Close, elapsed.Seconds())
		}
	}

	fmt.Println("\n=== Test Complete ===")
}
