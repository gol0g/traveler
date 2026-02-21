package main

import (
	"context"
	"fmt"
	"time"

	"traveler/internal/provider"
	"traveler/internal/strategy"
)

func main() {
	p := provider.NewUpbitProvider()
	ctx := context.Background()

	syms := []string{"KRW-UNI", "KRW-XRP", "KRW-LINK", "KRW-NEAR", "KRW-OP"}

	for _, sym := range syms {
		candles, err := p.GetDailyCandles(ctx, sym, 30)
		if err != nil {
			fmt.Printf("%s: error %v\n", sym, err)
			continue
		}

		fmt.Printf("\n=== %s ===\n", sym)
		fmt.Printf("%-7s %10s %10s %10s %6s\n", "Date", "Close", "Low", "High", "RSI")

		for i, c := range candles {
			rsi := 0.0
			if i >= 15 {
				ind := strategy.CalculateIndicators(candles[:i+1])
				rsi = ind.RSI14
			}
			fmt.Printf("%-7s %10.0f %10.0f %10.0f %6.1f\n",
				c.Time.Format("01-02"), c.Close, c.Low, c.High, rsi)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
