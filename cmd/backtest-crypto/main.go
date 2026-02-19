package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"traveler/internal/backtest"
	"traveler/internal/provider"
	"traveler/internal/symbols"
)

func main() {
	cfg := parseFlags()

	// Resolve data directory
	dataDir := cfg.dataDir
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".traveler")
	}

	// Resolve symbols
	symList := resolveSymbols(cfg.symbols, cfg.universe)

	fmt.Printf("Crypto %s Backtest\n", strings.ToUpper(cfg.strategy))
	fmt.Printf("  Symbols:  %d (%s)\n", len(symList), strings.Join(symList, ", "))
	fmt.Printf("  Days:     %d\n", cfg.days)
	fmt.Printf("  Capital:  %.0f\n", cfg.capital)
	fmt.Println()

	ctx := context.Background()

	// RSI contrarian uses daily candles — different path
	if cfg.strategy == "rsi" {
		runRSIBacktest(ctx, cfg, symList)
		return
	}

	// W-Bottom uses daily candles
	if cfg.strategy == "wbottom" {
		runWBottomBacktest(ctx, cfg, symList)
		return
	}

	// ORB / DipBuy uses intraday minute candles
	fmt.Printf("  Interval: %d min\n\n", cfg.interval)

	loc, _ := time.LoadLocation("Asia/Seoul")
	now := time.Now().In(loc)
	endDate := now.AddDate(0, 0, -1)
	startDate := endDate.AddDate(0, 0, -cfg.days+1)

	fmt.Println("Fetching data...")
	cache := backtest.NewCandleCache(dataDir)
	dayData, err := cache.FetchAllData(ctx, symList, startDate, endDate, cfg.interval, cfg.noCache)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching data: %v\n", err)
		os.Exit(1)
	}

	totalDays := 0
	for _, dates := range dayData {
		totalDays += len(dates)
	}
	fmt.Printf("  Loaded %d symbol-days of data\n\n", totalDays)

	btConfig := backtest.DefaultCryptoORBConfig()
	btConfig.InitialCapital = cfg.capital
	btConfig.CandleInterval = cfg.interval

	switch cfg.strategy {
	case "orb":
		btConfig.EnableORB = true
		btConfig.EnableDipBuy = false
	case "dipbuy":
		btConfig.EnableORB = false
		btConfig.EnableDipBuy = true
	case "both":
		btConfig.EnableORB = true
		btConfig.EnableDipBuy = true
	}

	fmt.Println("Running simulation...")
	bt := backtest.NewCryptoBacktester(btConfig)
	result := bt.Run(dayData)
	result.PrintReport(cfg.verbose)
}

func runWBottomBacktest(ctx context.Context, cfg cliConfig, symList []string) {
	wbCfg := backtest.DefaultCryptoWBottomConfig()
	wbCfg.InitialCapital = cfg.capital

	p := provider.NewUpbitProvider()
	bt := backtest.NewCryptoWBottomBacktester(wbCfg, p)

	fmt.Println()
	result := bt.Run(ctx, symList, cfg.days)
	result.PrintWBottomReport(cfg.verbose)
}

func runRSIBacktest(ctx context.Context, cfg cliConfig, symList []string) {
	rsiCfg := backtest.DefaultCryptoRSIConfig()
	rsiCfg.InitialCapital = cfg.capital

	p := provider.NewUpbitProvider()
	bt := backtest.NewCryptoRSIBacktester(rsiCfg, p)

	fmt.Println()
	result := bt.Run(ctx, symList, cfg.days)

	// Override strategy name for report
	result.Config.EnableORB = false
	result.Config.EnableDipBuy = false
	result.PrintRSIReport(cfg.verbose)
}

type cliConfig struct {
	symbols  string
	universe string
	days     int
	capital  float64
	strategy string
	interval int
	verbose  bool
	noCache  bool
	dataDir  string
}

func parseFlags() cliConfig {
	cfg := cliConfig{
		symbols:  "",
		universe: "crypto-top10",
		days:     60,
		capital:  100000,
		strategy: "orb",
		interval: 5,
		verbose:  false,
		noCache:  false,
		dataDir:  "",
	}

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--symbols":
			if i+1 < len(args) {
				cfg.symbols = args[i+1]
				i++
			}
		case "--universe":
			if i+1 < len(args) {
				cfg.universe = args[i+1]
				i++
			}
		case "--days":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &cfg.days)
				i++
			}
		case "--capital":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%f", &cfg.capital)
				i++
			}
		case "--strategy":
			if i+1 < len(args) {
				cfg.strategy = args[i+1]
				i++
			}
		case "--interval":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &cfg.interval)
				i++
			}
		case "--verbose", "-v":
			cfg.verbose = true
		case "--no-cache":
			cfg.noCache = true
		case "--data-dir":
			if i+1 < len(args) {
				cfg.dataDir = args[i+1]
				i++
			}
		case "--help", "-h":
			printUsage()
			os.Exit(0)
		}
	}
	return cfg
}

func resolveSymbols(syms, universe string) []string {
	if syms != "" {
		return strings.Split(syms, ",")
	}
	switch universe {
	case "crypto-top30":
		return symbols.CryptoTop30Symbols
	default:
		return symbols.CryptoTop10Symbols
	}
}

func printUsage() {
	fmt.Println(`Usage: backtest-crypto [flags]

Flags:
  --symbols     Comma-separated symbols (e.g., KRW-BTC,KRW-ETH)
  --universe    Symbol universe: crypto-top10 (default), crypto-top30
  --days        Backtest period in days (default: 60)
  --capital     Initial capital in KRW (default: 100000)
  --strategy    Strategy: orb, dipbuy, both, rsi, wbottom (default: orb)
  --interval    Candle interval in minutes (default: 5, for orb/dipbuy)
  --verbose     Show individual trade details
  --no-cache    Force re-fetch data (ignore cache)
  --data-dir    Data directory (default: ~/.traveler)
  --help        Show this help`)
}
