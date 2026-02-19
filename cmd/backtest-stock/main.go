package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"traveler/internal/backtest"
	"traveler/internal/provider"
	"traveler/internal/strategy"
	"traveler/internal/symbols"
	"traveler/internal/trader"
	"traveler/pkg/model"
)

type cliConfig struct {
	market   string
	days     int
	capital  float64
	universe string
	verbose  bool
	noCache  bool
	dataDir  string
	optimize bool
}

func main() {
	cfg := cliConfig{}

	flag.StringVar(&cfg.market, "market", "us", "Market: us or kr")
	flag.IntVar(&cfg.days, "days", 120, "Backtest period in trading days")
	flag.Float64Var(&cfg.capital, "capital", 0, "Initial capital (0 = default: $5000 US, ₩5M KR)")
	flag.StringVar(&cfg.universe, "universe", "", "Universe: test, dow30, nasdaq100, sp500, kospi30, etc. (empty = adaptive)")
	flag.BoolVar(&cfg.verbose, "verbose", false, "Print individual trades")
	flag.BoolVar(&cfg.noCache, "no-cache", false, "Skip cache, fetch fresh data")
	flag.StringVar(&cfg.dataDir, "data-dir", "", "Data directory (default: ~/.traveler)")
	flag.BoolVar(&cfg.optimize, "optimize", false, "Run optimization across multiple regime-strategy configurations")
	flag.Parse()

	// Defaults
	if cfg.capital == 0 {
		if cfg.market == "kr" {
			cfg.capital = 5000000
		} else {
			cfg.capital = 5000
		}
	}
	if cfg.dataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.dataDir = filepath.Join(home, ".traveler")
		} else {
			cfg.dataDir = "."
		}
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Stock Backtester (%s)\n", strings.ToUpper(cfg.market))
	fmt.Println("═══════════════════════════════════════════")
	if cfg.market == "kr" {
		fmt.Printf("Capital: ₩%.0f | Days: %d | Universe: %s\n", cfg.capital, cfg.days, univLabel(cfg))
	} else {
		fmt.Printf("Capital: $%.0f | Days: %d | Universe: %s\n", cfg.capital, cfg.days, univLabel(cfg))
	}
	fmt.Println()

	ctx := context.Background()

	// 1. Resolve universe symbols
	syms := resolveSymbols(cfg)
	if len(syms) == 0 {
		log.Fatal("No symbols to backtest")
	}

	// Add benchmark symbol
	benchmark := "SPY"
	if cfg.market == "kr" {
		benchmark = "069500"
	}
	if !contains(syms, benchmark) {
		syms = append([]string{benchmark}, syms...)
	}

	// Deduplicate
	syms = dedupStrings(syms)
	log.Printf("[INIT] %d symbols (including benchmark %s)", len(syms), benchmark)

	// 2. Fetch data from Yahoo Finance
	yahoo := provider.NewYahooProvider()
	lookback := cfg.days + 260 // strategy needs up to MA200 + buffer
	if lookback < 370 {
		lookback = 370
	}

	// KR symbols need Yahoo suffix (.KS or .KQ)
	fetchSyms := syms
	var krSuffix map[string]string // original → yahoo symbol
	if cfg.market == "kr" {
		fetchSyms, krSuffix = convertKRSymbols(syms)
	}

	log.Printf("[DATA] Fetching %d symbols × %d days from Yahoo Finance...", len(fetchSyms), lookback)
	allCandles, err := backtest.FetchStockData(ctx, yahoo, fetchSyms, lookback, cfg.dataDir, cfg.noCache)
	if err != nil {
		log.Fatalf("Failed to fetch data: %v", err)
	}

	// Map KR Yahoo symbols back to original codes
	if cfg.market == "kr" {
		mapped := make(map[string][]model.Candle)
		for yahooSym, candles := range allCandles {
			if orig, ok := krSuffix[yahooSym]; ok {
				mapped[orig] = candles
			} else {
				mapped[yahooSym] = candles
			}
		}
		allCandles = mapped
	}

	if len(allCandles) < 5 {
		log.Fatalf("Too few symbols with data: %d", len(allCandles))
	}

	// Filter syms to those with actual data
	var validSyms []string
	for _, s := range syms {
		if _, ok := allCandles[s]; ok {
			validSyms = append(validSyms, s)
		}
	}
	syms = validSyms
	log.Printf("[DATA] %d symbols with valid data", len(syms))

	// 3. Sizer config
	var sizerCfg trader.SizerConfig
	if cfg.market == "kr" {
		sizerCfg = trader.AdjustConfigForKRBalance(cfg.capital)
	} else {
		sizerCfg = trader.AdjustConfigForBalance(cfg.capital)
	}

	simCfg := backtest.StockSimConfig{
		Market:         cfg.market,
		Days:           cfg.days,
		InitialCapital: cfg.capital,
		MaxPositions:   sizerCfg.MaxPositions,
		Commission:     sizerCfg.CommissionRate,
		Verbose:        cfg.verbose,
	}

	// 4. Optimize mode or single run
	if cfg.optimize {
		fmt.Println("Running optimization across 8 configurations...")
		fmt.Println()
		results := backtest.RunOptimization(ctx, allCandles, simCfg, sizerCfg, syms)
		backtest.PrintOptimizationResults(results, cfg.market, cfg.days)
		return
	}

	// Single backtest with default meta strategy
	btProvider := backtest.NewBacktestProvider(allCandles)
	metaCfg := strategy.DefaultStockMetaConfig(cfg.market)
	meta := strategy.NewStockMetaStrategy(metaCfg, btProvider)
	strategies := []strategy.Strategy{meta}

	sim := backtest.NewStockSimulator(simCfg, btProvider, strategies, sizerCfg, syms)
	result := sim.Run(ctx)
	result.PrintReport(cfg.verbose)
}

func resolveSymbols(cfg cliConfig) []string {
	if cfg.universe != "" {
		u := symbols.Universe(cfg.universe)
		syms := symbols.GetUniverse(u)
		if syms == nil {
			log.Fatalf("Unknown universe: %s", cfg.universe)
		}
		return syms
	}

	// Adaptive: pick universe by market and capital
	if cfg.market == "kr" {
		var syms []string
		syms = append(syms, symbols.GetUniverse(symbols.UniverseKospi30)...)
		syms = append(syms, symbols.GetUniverse(symbols.UniverseKosdaq30)...)
		if cfg.capital >= 5000000 {
			syms = append(syms, symbols.GetUniverse(symbols.UniverseKospi200)...)
		}
		return syms
	}

	// US: start with nasdaq100 + sp500
	var syms []string
	syms = append(syms, symbols.GetUniverse(symbols.UniverseNasdaq100)...)
	syms = append(syms, symbols.GetUniverse(symbols.UniverseSP500)...)
	return syms
}

// convertKRSymbols converts 6-digit KR codes to Yahoo Finance format
func convertKRSymbols(syms []string) ([]string, map[string]string) {
	yahooSyms := make([]string, 0, len(syms))
	mapping := make(map[string]string) // yahoo → original

	kosdaqSet := make(map[string]bool)
	for _, s := range symbols.Kosdaq30Symbols {
		kosdaqSet[s] = true
	}

	for _, s := range syms {
		if symbols.IsKoreanSymbol(s) {
			suffix := ".KS"
			if kosdaqSet[s] {
				suffix = ".KQ"
			}
			yahoo := s + suffix
			yahooSyms = append(yahooSyms, yahoo)
			mapping[yahoo] = s
		} else {
			yahooSyms = append(yahooSyms, s)
			mapping[s] = s
		}
	}
	return yahooSyms, mapping
}


func univLabel(cfg cliConfig) string {
	if cfg.universe != "" {
		return cfg.universe
	}
	if cfg.market == "kr" {
		return "kospi30+kosdaq30 (adaptive)"
	}
	return "nasdaq100+sp500 (adaptive)"
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func dedupStrings(ss []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
