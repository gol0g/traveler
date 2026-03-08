package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"traveler/internal/collector"
)

func main() {
	dataDir := flag.String("data-dir", "", "Data directory (default: ~/.traveler)")
	binanceSyms := flag.String("binance", "BTCUSDT,ETHUSDT,SOLUSDT,XRPUSDT", "Binance Futures symbols (comma-separated)")
	upbitSyms := flag.String("upbit", "KRW-BTC,KRW-ETH,KRW-SOL,KRW-XRP,KRW-AVAX,KRW-LINK,KRW-ADA,KRW-DOGE", "Upbit symbols (comma-separated)")
	krSyms := flag.String("kr", "005930,000660,373220,005380,035420,000270,068270,035720,051910,006400", "KR stock symbols (comma-separated)")
	retentionDays := flag.Int("retention", 90, "Data retention days")
	flag.Parse()

	// Resolve data directory
	dir := *dataDir
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home + "/.traveler"
	}

	// Load .env if exists
	loadEnvFile(dir + "/.env")

	cfg := collector.Config{
		DataDir:       dir,
		RetentionDays: *retentionDays,
	}

	if *binanceSyms != "" {
		cfg.BinanceSymbols = strings.Split(*binanceSyms, ",")
	}
	if *upbitSyms != "" {
		cfg.UpbitSymbols = strings.Split(*upbitSyms, ",")
	}
	if *krSyms != "" {
		cfg.KRSymbols = strings.Split(*krSyms, ",")
	}

	log.Printf("=== Traveler Data Collector ===")
	log.Printf("Data dir: %s", dir)
	log.Printf("Binance: %v", cfg.BinanceSymbols)
	log.Printf("Upbit:   %v", cfg.UpbitSymbols)
	log.Printf("KR:      %v", cfg.KRSymbols)
	log.Printf("Retention: %d days", cfg.RetentionDays)

	c, err := collector.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create collector: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received %v, shutting down...", sig)
		cancel()
	}()

	if err := c.Run(ctx); err != nil {
		log.Fatalf("Collector error: %v", err)
	}
}

// loadEnvFile loads a simple KEY=VALUE .env file.
func loadEnvFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			// Remove quotes
			val = strings.Trim(val, `"'`)
			if os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
	}
}

var _ = fmt.Sprintf // keep import
