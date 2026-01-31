package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"

	"traveler/internal/analyzer"
	"traveler/internal/config"
	"traveler/internal/provider"
	"traveler/internal/scanner"
	"traveler/internal/strategy"
	"traveler/internal/symbols"
	"traveler/pkg/model"
)

var (
	cfgFile      string
	days         int
	workers      int
	dropPct      float64
	risePct      float64
	reboundPct   float64
	symbolList   string
	format       string
	verbose      bool
	strategyName string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "traveler",
		Short: "Stock pattern scanner with multiple strategies",
		Long: `Traveler scans US stocks (NYSE/NASDAQ) using various trading strategies:

Strategies:
  morning-dip  - Detect stocks that dip in morning and recover by close (counter-trend)
  pullback     - Detect uptrending stocks pulling back to MA20 (trend-following)

Examples:
  traveler --strategy morning-dip --days 3
  traveler --strategy pullback --symbols AAPL,MSFT,GOOGL`,
		RunE: run,
	}

	// Flags
	rootCmd.Flags().StringVar(&cfgFile, "config", "config.yaml", "config file path")
	rootCmd.Flags().StringVar(&strategyName, "strategy", "morning-dip", "strategy: morning-dip, pullback")
	rootCmd.Flags().IntVar(&days, "days", 1, "minimum consecutive days with pattern (morning-dip)")
	rootCmd.Flags().IntVar(&workers, "workers", 10, "number of parallel workers")
	rootCmd.Flags().Float64Var(&dropPct, "drop", -1.0, "minimum morning drop percentage (negative value)")
	rootCmd.Flags().Float64Var(&risePct, "rise", 0.5, "minimum close rise percentage")
	rootCmd.Flags().Float64Var(&reboundPct, "rebound", 2.0, "minimum rebound from morning low percentage")
	rootCmd.Flags().StringVar(&symbolList, "symbols", "", "comma-separated list of symbols to scan (default: all US stocks)")
	rootCmd.Flags().StringVar(&format, "format", "table", "output format: table, json")
	rootCmd.Flags().BoolVar(&verbose, "verbose", false, "show detailed output")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	// Load configuration
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Override config with CLI flags
	if days > 0 {
		cfg.Pattern.ConsecutiveDays = days
	}
	if workers > 0 {
		cfg.Scanner.Workers = workers
	}
	if cmd.Flags().Changed("drop") {
		cfg.Pattern.MorningDropThreshold = dropPct
	}
	if cmd.Flags().Changed("rise") {
		cfg.Pattern.CloseRiseThreshold = risePct
	}
	if cmd.Flags().Changed("rebound") {
		cfg.Pattern.ReboundThreshold = reboundPct
	}

	// Create providers with fallback
	providers := createProviders(cfg)
	if len(providers) == 0 {
		return fmt.Errorf("no API providers available. Set FINNHUB_API_KEY or ALPHAVANTAGE_API_KEY environment variable")
	}

	fallbackProvider := provider.NewFallbackProvider(providers...)
	if !fallbackProvider.IsAvailable() {
		return fmt.Errorf("no available data providers")
	}

	if verbose {
		fmt.Printf("Using providers: ")
		for i, p := range fallbackProvider.Providers() {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Print(p.Name())
		}
		fmt.Println()
	}

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nInterrupted. Stopping scan...")
		cancel()
	}()

	// Load symbols
	loader := symbols.NewLoader(fallbackProvider)
	var stocks []model.Stock

	if symbolList != "" {
		// Use specified symbols
		syms := strings.Split(symbolList, ",")
		stocks, err = loader.LoadSymbols(ctx, syms)
		if err != nil {
			return fmt.Errorf("loading symbols: %w", err)
		}
	} else {
		// Load all US stocks
		fmt.Println("Loading US stock list...")
		stocks, err = loader.LoadUSStocks(ctx)
		if err != nil {
			return fmt.Errorf("loading US stocks: %w", err)
		}
	}

	if len(stocks) == 0 {
		return fmt.Errorf("no stocks to scan")
	}

	// Route to appropriate strategy
	switch strategyName {
	case "pullback":
		return runPullbackStrategy(ctx, stocks, fallbackProvider, cfg)
	case "morning-dip":
		fallthrough
	default:
		return runMorningDipStrategy(ctx, stocks, fallbackProvider, cfg)
	}
}

func runMorningDipStrategy(ctx context.Context, stocks []model.Stock, fallbackProvider *provider.FallbackProvider, cfg *config.Config) error {
	fmt.Printf("Scanning %d stocks for %d-day morning-dip pattern...\n\n", len(stocks), cfg.Pattern.ConsecutiveDays)

	// Create pattern config
	patternCfg := analyzer.PatternConfig{
		ConsecutiveDays:      cfg.Pattern.ConsecutiveDays,
		MorningDropThreshold: cfg.Pattern.MorningDropThreshold,
		CloseRiseThreshold:   cfg.Pattern.CloseRiseThreshold,
		ReboundThreshold:     cfg.Pattern.ReboundThreshold,
		MorningWindow:        cfg.Pattern.MorningWindowMinutes,
		ClosingWindow:        cfg.Pattern.ClosingWindowMinutes,
	}

	// Create scanner
	s := scanner.NewScanner(fallbackProvider, patternCfg, cfg.Scanner.Workers, cfg.Scanner.Timeout)

	// Setup progress bar
	bar := progressbar.NewOptions(len(stocks),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetWidth(40),
		progressbar.OptionSetDescription("Scanning"),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "[green]█[reset]",
			SaucerHead:    "[green]█[reset]",
			SaucerPadding: "░",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	s.SetProgressCallback(func(scanned, total int) {
		bar.Set(scanned)
	})

	// Run scan
	result, err := s.Scan(ctx, stocks)
	if err != nil {
		return fmt.Errorf("scanning: %w", err)
	}

	bar.Finish()
	fmt.Println()

	// Output results
	if format == "json" {
		return outputJSON(result)
	}
	return outputTable(result, cfg.Pattern.ConsecutiveDays)
}

func runPullbackStrategy(ctx context.Context, stocks []model.Stock, fallbackProvider *provider.FallbackProvider, cfg *config.Config) error {
	fmt.Printf("Scanning %d stocks for pullback opportunities...\n\n", len(stocks))

	// Create pullback strategy
	pullbackCfg := strategy.DefaultPullbackConfig()
	strat := strategy.NewPullbackStrategy(pullbackCfg, fallbackProvider)

	// Setup progress bar
	bar := progressbar.NewOptions(len(stocks),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetWidth(40),
		progressbar.OptionSetDescription("Scanning"),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "[green]█[reset]",
			SaucerHead:    "[green]█[reset]",
			SaucerPadding: "░",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	// Scan stocks
	var signals []strategy.Signal
	startTime := time.Now()

	for i, stock := range stocks {
		select {
		case <-ctx.Done():
			bar.Finish()
			fmt.Println("\nScan interrupted")
			break
		default:
		}

		signal, err := strat.Analyze(ctx, stock)
		if err == nil && signal != nil {
			signals = append(signals, *signal)
		}
		bar.Set(i + 1)
	}

	bar.Finish()
	fmt.Println()

	scanTime := time.Since(startTime)

	// Output results
	if format == "json" {
		return outputSignalsJSON(signals, len(stocks), scanTime)
	}
	return outputSignalsTable(signals, len(stocks), scanTime)
}

func outputSignalsTable(signals []strategy.Signal, totalScanned int, scanTime time.Duration) error {
	if len(signals) == 0 {
		fmt.Println("No pullback opportunities found.")
		fmt.Printf("Scanned %d stocks in %s\n", totalScanned, scanTime.Round(time.Second))
		return nil
	}

	// Sort by probability descending
	sort.Slice(signals, func(i, j int) bool {
		return signals[i].Probability > signals[j].Probability
	})

	fmt.Printf("Found %d pullback opportunities:\n\n", len(signals))

	table := tablewriter.NewTable(os.Stdout,
		tablewriter.WithHeader([]string{"Symbol", "Name", "Strength", "Prob", "Reason"}),
	)

	for _, s := range signals {
		name := s.Stock.Name
		if len(name) > 18 {
			name = name[:18] + "..."
		}

		reason := s.Reason
		if len(reason) > 45 {
			reason = reason[:45] + "..."
		}

		table.Append([]string{
			s.Stock.Symbol,
			name,
			fmt.Sprintf("%.0f", s.Strength),
			fmt.Sprintf("%.0f%%", s.Probability),
			reason,
		})
	}

	table.Render()

	// Print details for top signals
	fmt.Println("\n--- Pullback Details ---")
	count := 0
	for _, s := range signals {
		if count >= 5 {
			break
		}

		fmt.Printf("\n[%s] %s\n", s.Stock.Symbol, s.Stock.Name)
		fmt.Printf("  %s\n", s.Reason)
		fmt.Printf("  Close: $%.2f | MA20: $%.2f | MA50: $%.2f\n",
			s.Details["close"], s.Details["ma20"], s.Details["ma50"])
		fmt.Printf("  Price vs MA50: %+.1f%% | Price vs MA20: %+.1f%%\n",
			s.Details["price_vs_ma50_pct"], s.Details["price_vs_ma20_pct"])
		if rsi, ok := s.Details["rsi14"]; ok && rsi > 0 {
			fmt.Printf("  RSI(14): %.1f | Volume: %.1fx avg\n", rsi, s.Details["volume_ratio"])
		}
		fmt.Printf("  >> Probability: %.0f%% | Strength: %.0f\n", s.Probability, s.Strength)

		count++
	}

	fmt.Printf("\nScanned %d stocks in %s\n", totalScanned, scanTime.Round(time.Second))
	return nil
}

func outputSignalsJSON(signals []strategy.Signal, totalScanned int, scanTime time.Duration) error {
	result := strategy.ScanResult{
		Strategy:     "pullback",
		TotalScanned: totalScanned,
		SignalsFound: len(signals),
		Signals:      signals,
		ScanTime:     scanTime.String(),
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func createProviders(cfg *config.Config) []provider.Provider {
	var providers []provider.Provider

	// Finnhub (primary - higher rate limit)
	if cfg.API.Finnhub.Key != "" {
		providers = append(providers, provider.NewFinnhubProvider(cfg.API.Finnhub.Key, cfg.API.Finnhub.RateLimit))
	}

	// Alpha Vantage (secondary)
	if cfg.API.AlphaVantage.Key != "" {
		providers = append(providers, provider.NewAlphaVantageProvider(cfg.API.AlphaVantage.Key, cfg.API.AlphaVantage.RateLimit))
	}

	// Yahoo Finance (fallback - always available)
	providers = append(providers, provider.NewYahooProvider())

	return providers
}

func outputTable(result *model.ScanResult, minDays int) error {
	if result.MatchingCount == 0 {
		fmt.Printf("No stocks found with %d-day consecutive morning-dip pattern.\n", minDays)
		fmt.Printf("Scanned %d stocks in %s\n", result.TotalScanned, result.ScanTime.Round(time.Second))
		return nil
	}

	// Sort results by continuation probability (descending), then by consecutive days
	results := result.Results
	sort.Slice(results, func(i, j int) bool {
		// First by recommendation strength
		recOrder := map[string]int{"strong": 4, "moderate": 3, "weak": 2, "avoid": 1, "": 0}
		ri, rj := "", ""
		if results[i].Technical != nil {
			ri = results[i].Technical.Recommendation
		}
		if results[j].Technical != nil {
			rj = results[j].Technical.Recommendation
		}
		if recOrder[ri] != recOrder[rj] {
			return recOrder[ri] > recOrder[rj]
		}
		// Then by consecutive days
		if results[i].ConsecutiveDays != results[j].ConsecutiveDays {
			return results[i].ConsecutiveDays > results[j].ConsecutiveDays
		}
		return results[i].AvgMorningDipPct < results[j].AvgMorningDipPct
	})

	fmt.Printf("Found %d stocks with %d+ day morning-dip pattern:\n\n", result.MatchingCount, minDays)

	// Main table
	table := tablewriter.NewTable(os.Stdout,
		tablewriter.WithHeader([]string{"Symbol", "Name", "Days", "Avg Dip", "Avg Rise", "Prob", "Signal"}),
	)

	for _, r := range results {
		name := r.Stock.Name
		if len(name) > 18 {
			name = name[:18] + "..."
		}

		prob := "-"
		signal := "-"
		if r.Technical != nil {
			prob = fmt.Sprintf("%.0f%%", r.Technical.ContinuationProb)
			signal = r.Technical.Recommendation
		}

		table.Append([]string{
			r.Stock.Symbol,
			name,
			fmt.Sprintf("%d", r.ConsecutiveDays),
			fmt.Sprintf("%.1f%%", r.AvgMorningDipPct),
			fmt.Sprintf("+%.1f%%", r.AvgCloseRisePct),
			prob,
			signal,
		})
	}

	table.Render()

	// Print detailed technical analysis for top results
	fmt.Println("\n--- Technical Analysis Details ---")
	count := 0
	for _, r := range results {
		if r.Technical == nil {
			continue
		}
		if count >= 5 { // Show top 5 only
			break
		}

		fmt.Printf("\n[%s] %s\n", r.Stock.Symbol, r.Stock.Name)
		fmt.Printf("  Pattern: %d consecutive days | Strength: %.0f | Consistency: %.0f\n",
			r.ConsecutiveDays, r.Technical.PatternStrength, r.Technical.ConsistencyScore)

		if r.Technical.RSI > 0 {
			fmt.Printf("  RSI(14): %.1f (%s)\n", r.Technical.RSI, r.Technical.RSISignal)
		}
		if r.Technical.VolumeRatio > 0 {
			fmt.Printf("  Volume: %.1fx avg (%s)\n", r.Technical.VolumeRatio, r.Technical.VolumeSignal)
		}
		fmt.Printf("  Trend: %s (MA5: %+.1f%%, MA20: %+.1f%%)\n",
			r.Technical.TrendSignal, r.Technical.PriceVsMA5, r.Technical.PriceVsMA20)
		fmt.Printf("  >> Continuation Probability: %.0f%% [%s]\n",
			r.Technical.ContinuationProb, strings.ToUpper(r.Technical.Recommendation))

		count++
	}

	fmt.Printf("\nScanned %d stocks in %s\n", result.TotalScanned, result.ScanTime.Round(time.Second))
	return nil
}

func outputJSON(result *model.ScanResult) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
