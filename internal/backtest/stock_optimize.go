package backtest

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"traveler/internal/strategy"
	"traveler/internal/trader"
	"traveler/pkg/model"
)

// OptimizeResult holds the result of a single configuration backtest
type OptimizeResult struct {
	ConfigName   string
	Config       strategy.StockMetaConfig
	TotalReturn  float64
	TotalRetPct  float64
	WinRate      float64
	ProfitFactor float64
	MaxDrawdown  float64
	SharpeRatio  float64
	SortinoRatio float64
	CalmarRatio  float64
	TotalTrades  int
	Expectancy   float64
	Score        float64 // composite ranking score
}

// GetOptimizationConfigs returns the predefined configurations to test
func GetOptimizationConfigs(market string) []strategy.StockMetaConfig {
	benchmark := "SPY"
	if market == "kr" {
		benchmark = "069500"
	}

	all4 := []string{"breakout", "pullback", "mean-reversion", "oversold"}

	configs := []strategy.StockMetaConfig{
		{
			Name:         "baseline",
			Market:       market,
			BenchmarkSym: benchmark,
			Bull:         all4,
			Sideways:     all4,
			Bear:         all4,
		},
		{
			Name:         "regime-split",
			Market:       market,
			BenchmarkSym: benchmark,
			Bull:         []string{"breakout", "pullback"},
			Sideways:     []string{"mean-reversion", "pullback", "oversold"},
			Bear:         []string{"oversold"},
		},
		{
			Name:         "trend-focus",
			Market:       market,
			BenchmarkSym: benchmark,
			Bull:         []string{"breakout", "pullback"},
			Sideways:     []string{"mean-reversion", "oversold"},
			Bear:         nil, // no trading in bear
		},
		{
			Name:         "breakout-bull",
			Market:       market,
			BenchmarkSym: benchmark,
			Bull:         []string{"breakout"},
			Sideways:     []string{"mean-reversion", "pullback", "oversold"},
			Bear:         []string{"oversold"},
		},
		{
			Name:         "no-bear",
			Market:       market,
			BenchmarkSym: benchmark,
			Bull:         []string{"breakout", "pullback"},
			Sideways:     []string{"mean-reversion", "pullback", "oversold"},
			Bear:         nil, // skip bear entirely
		},
		{
			Name:         "aggressive",
			Market:       market,
			BenchmarkSym: benchmark,
			Bull:         []string{"breakout", "pullback", "oversold"},
			Sideways:     []string{"breakout", "mean-reversion"},
			Bear:         []string{"oversold"},
		},
		{
			Name:         "conservative",
			Market:       market,
			BenchmarkSym: benchmark,
			Bull:         []string{"pullback", "mean-reversion", "oversold"},
			Sideways:     []string{"mean-reversion", "oversold"},
			Bear:         nil,
		},
		{
			Name:         "extended-hold",
			Market:       market,
			BenchmarkSym: benchmark,
			Bull:         []string{"breakout", "pullback"},
			Sideways:     []string{"mean-reversion", "pullback", "oversold"},
			Bear:         []string{"oversold"},
			MaxHoldOverride: map[string]int{
				"breakout": 20, // extend from 15 to 20 in all regimes
			},
		},
	}

	return configs
}

// RunOptimization runs all configurations and returns sorted results
func RunOptimization(ctx context.Context, allCandles map[string][]model.Candle,
	simCfg StockSimConfig, sizerCfg trader.SizerConfig, syms []string) []OptimizeResult {

	configs := GetOptimizationConfigs(simCfg.Market)
	results := make([]OptimizeResult, 0, len(configs))

	// Run each configuration silently
	origVerbose := simCfg.Verbose
	simCfg.Verbose = false

	for i, cfg := range configs {
		fmt.Printf("  [%d/%d] Testing: %-20s ", i+1, len(configs), cfg.Name)

		// Fresh provider for each run (state isolation)
		btProvider := NewBacktestProvider(allCandles)

		// Create meta strategy with this config
		meta := strategy.NewStockMetaStrategy(cfg, btProvider)
		strats := []strategy.Strategy{meta}

		// Run simulation
		sim := NewStockSimulator(simCfg, btProvider, strats, sizerCfg, syms)
		result := sim.Run(ctx)

		opt := OptimizeResult{
			ConfigName:   cfg.Name,
			Config:       cfg,
			TotalReturn:  result.TotalReturn,
			TotalRetPct:  result.TotalReturnPct,
			WinRate:      result.WinRate,
			ProfitFactor: result.ProfitFactor,
			MaxDrawdown:  result.MaxDrawdown,
			SharpeRatio:  result.SharpeRatio,
			SortinoRatio: result.SortinoRatio,
			CalmarRatio:  result.CalmarRatio,
			TotalTrades:  result.TotalTrades,
			Expectancy:   result.Expectancy,
		}

		// Composite score: Sharpe × sqrt(trades) × (1 - MDD/100)
		if opt.TotalTrades > 0 && opt.SharpeRatio > 0 {
			opt.Score = opt.SharpeRatio * math.Sqrt(float64(opt.TotalTrades)) * (1 - opt.MaxDrawdown/100)
		}

		results = append(results, opt)
		fmt.Printf("→ %+.1f%%, PF %.2f, Sharpe %.2f, %d trades\n",
			opt.TotalRetPct, opt.ProfitFactor, opt.SharpeRatio, opt.TotalTrades)
	}

	simCfg.Verbose = origVerbose

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

// PrintOptimizationResults prints a comparison table
func PrintOptimizationResults(results []OptimizeResult, market string, days int) {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════════════════════════")
	fmt.Printf("  Optimization Results (%s, %d days)\n", strings.ToUpper(market), days)
	fmt.Println("═══════════════════════════════════════════════════════════════════════════════")
	fmt.Println()

	// Header
	fmt.Printf("  %-4s %-20s %8s %8s %6s %7s %7s %8s %8s %7s %8s\n",
		"#", "Config", "Return", "WinRate", "PF", "MDD", "Sharpe", "Sortino", "Calmar", "Trades", "Score")
	fmt.Println("  " + strings.Repeat("─", 100))

	bestIdx := 0
	for i, r := range results {
		marker := "  "
		if i == 0 && r.Score > 0 {
			marker = "★ "
			bestIdx = i
		}

		currSign := "$"
		if market == "kr" {
			currSign = "₩"
		}

		fmt.Printf("  %s%-20s %+7.1f%% %7.1f%% %5.2f %6.1f%% %6.2f %7.2f %7.2f %6d %7.1f",
			marker, r.ConfigName,
			r.TotalRetPct, r.WinRate, r.ProfitFactor,
			r.MaxDrawdown, r.SharpeRatio, r.SortinoRatio, r.CalmarRatio,
			r.TotalTrades, r.Score)

		if i == 0 && r.Score > 0 {
			fmt.Printf("  ← BEST")
		}
		fmt.Println()

		_ = currSign
	}

	fmt.Println()

	// Print best config details
	if len(results) > 0 {
		best := results[bestIdx]
		fmt.Println("── Best Configuration ──")
		fmt.Printf("  Name: %s\n", best.ConfigName)
		fmt.Printf("  Bull:     %v\n", best.Config.Bull)
		fmt.Printf("  Sideways: %v\n", best.Config.Sideways)
		fmt.Printf("  Bear:     %v\n", best.Config.Bear)
		if len(best.Config.MaxHoldOverride) > 0 {
			fmt.Printf("  MaxHoldOverride: %v\n", best.Config.MaxHoldOverride)
		}
		fmt.Println()
	}
}
