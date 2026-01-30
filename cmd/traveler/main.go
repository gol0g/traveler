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
	"traveler/internal/symbols"
	"traveler/pkg/model"
)

var (
	cfgFile     string
	days        int
	workers     int
	dropPct     float64
	risePct     float64
	reboundPct  float64
	symbolList  string
	format      string
	verbose     bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "traveler",
		Short: "Detect stocks with morning-dip → closing-rise patterns",
		Long: `Traveler scans US stocks (NYSE/NASDAQ) for a specific intraday pattern:
- Morning dip: Stock drops significantly in the first hour of trading
- Closing rise: Stock recovers and closes higher than the open

This pattern, when occurring for multiple consecutive days, may indicate
interesting trading opportunities.`,
		RunE: run,
	}

	// Flags
	rootCmd.Flags().StringVar(&cfgFile, "config", "config.yaml", "config file path")
	rootCmd.Flags().IntVar(&days, "days", 3, "minimum consecutive days with pattern")
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

	fmt.Printf("Scanning %d US stocks for %d-day morning-dip pattern...\n\n", len(stocks), cfg.Pattern.ConsecutiveDays)

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
