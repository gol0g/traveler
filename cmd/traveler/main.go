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
	"traveler/internal/backtest"
	"traveler/internal/config"
	"traveler/internal/provider"
	"traveler/internal/scanner"
	"traveler/internal/strategy"
	"traveler/internal/symbols"
	"traveler/pkg/model"
)

var (
	cfgFile        string
	days           int
	workers        int
	dropPct        float64
	risePct        float64
	reboundPct     float64
	symbolList     string
	format         string
	verbose        bool
	strategyName   string
	accountBalance float64
	runBacktest    bool
	backtestDays   int
	universe       string
	outputFile     string
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
	rootCmd.Flags().Float64Var(&accountBalance, "capital", 100000, "account balance in USD for position sizing")
	rootCmd.Flags().BoolVar(&runBacktest, "backtest", false, "run backtest on historical data")
	rootCmd.Flags().IntVar(&backtestDays, "backtest-days", 365, "number of days for backtest")
	rootCmd.Flags().StringVar(&universe, "universe", "", "stock universe for backtest: sp500, nasdaq100, test")
	rootCmd.Flags().StringVarP(&outputFile, "output", "o", "", "save report to file (auto-generates filename if empty)")

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
	// Run backtest if requested
	if runBacktest && len(stocks) > 0 {
		return runPullbackBacktest(ctx, stocks[0].Symbol, fallbackProvider)
	}

	fmt.Printf("Scanning %d stocks for pullback opportunities...\n", len(stocks))
	fmt.Printf("Account: %s\n\n", formatUSD(accountBalance))

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

	// Scan stocks - first pass to collect all signals
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

	// Calculate position sizing based on number of signals
	if len(signals) > 0 {
		// Sort by probability first to prioritize best signals
		sort.Slice(signals, func(i, j int) bool {
			return signals[i].Probability > signals[j].Probability
		})

		// Limit to top 5 positions max
		maxPositions := 5
		if len(signals) > maxPositions {
			signals = signals[:maxPositions]
		}

		// Calculate allocation per position
		allocationPerPosition := accountBalance / float64(len(signals))
		riskPerPosition := accountBalance * 0.01 / float64(len(signals)) // Total 1% risk split

		for i := range signals {
			if signals[i].Guide != nil {
				g := signals[i].Guide
				riskPerShare := g.EntryPrice - g.StopLoss
				if riskPerShare > 0 {
					// Calculate shares based on risk limit
					sharesByRisk := int(riskPerPosition / riskPerShare)
					// Calculate shares based on allocation limit
					sharesByAllocation := int(allocationPerPosition / g.EntryPrice)
					// Use the smaller of the two
					g.PositionSize = sharesByRisk
					if sharesByAllocation < sharesByRisk {
						g.PositionSize = sharesByAllocation
					}
					if g.PositionSize < 1 {
						g.PositionSize = 1
					}
					g.InvestAmount = float64(g.PositionSize) * g.EntryPrice
					g.RiskAmount = float64(g.PositionSize) * riskPerShare
					g.RiskPct = g.RiskAmount / accountBalance * 100
					g.AllocationPct = g.InvestAmount / accountBalance * 100
				}
			}
		}
	}

	scanTime := time.Since(startTime)

	// Output results
	if format == "json" {
		return outputSignalsJSON(signals, len(stocks), scanTime)
	}
	return outputSignalsTable(signals, len(stocks), scanTime, accountBalance)
}

func runPullbackBacktest(ctx context.Context, symbol string, p provider.Provider) error {
	// Check for universe-based backtest
	if universe != "" {
		universeSymbols := symbols.GetUniverse(symbols.Universe(universe))
		if universeSymbols == nil {
			return fmt.Errorf("unknown universe: %s (use: sp500, nasdaq100, test)", universe)
		}
		return runPortfolioBacktest(ctx, universeSymbols, p)
	}

	// If multiple symbols specified, use portfolio backtest
	if symbolList != "" {
		syms := strings.Split(symbolList, ",")
		if len(syms) > 1 {
			return runPortfolioBacktest(ctx, syms, p)
		}
	}

	fmt.Printf("Running single-stock backtest for %s (%d days)...\n", symbol, backtestDays)
	fmt.Println("TIP: Use --universe sp500 for full portfolio simulation with automatic stock discovery\n")

	cfg := backtest.DefaultBacktestConfig()
	cfg.InitialCapital = accountBalance

	bt := backtest.NewBacktester(cfg, p)
	result, err := bt.RunPullbackBacktest(ctx, symbol, backtestDays)
	if err != nil {
		return fmt.Errorf("backtest failed: %w", err)
	}

	if result == nil || result.TotalTrades == 0 {
		fmt.Println("No trades generated in backtest period.")
		return nil
	}

	outputSingleBacktest(result, cfg.InitialCapital)
	return nil
}

func runPortfolioBacktest(ctx context.Context, syms []string, p provider.Provider) error {
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println(" PORTFOLIO BACKTEST - Full Strategy Simulation")
	fmt.Println("=" + strings.Repeat("=", 59))

	universeLabel := "custom"
	if universe != "" {
		universeLabel = universe
	}

	fmt.Printf("\n Universe:      %s (%d symbols)\n", universeLabel, len(syms))
	fmt.Printf(" Period:        %d trading days\n", backtestDays)
	fmt.Printf(" Capital:       %s\n", formatUSD(accountBalance))
	fmt.Printf(" Max Positions: 5 simultaneous\n")
	fmt.Printf(" Risk/Trade:    1%%\n")
	fmt.Printf(" Stop Loss:     2%%\n")
	fmt.Printf(" Target:        2R (4%%)\n\n")

	fmt.Println(" This backtest simulates:")
	fmt.Println("   1. Daily scan of ALL symbols in universe")
	fmt.Println("   2. Automatic signal detection (no look-ahead)")
	fmt.Println("   3. Position entry on next day's open")
	fmt.Println("   4. Portfolio management with max 5 positions")
	fmt.Println()

	cfg := backtest.DefaultPortfolioConfig()
	cfg.InitialCapital = accountBalance

	bt := backtest.NewPortfolioBacktester(cfg, p)

	// Progress bar for loading
	bar := progressbar.NewOptions(len(syms),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionSetWidth(40),
		progressbar.OptionSetDescription("Loading data"),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "[cyan]█[reset]",
			SaucerHead:    "[cyan]█[reset]",
			SaucerPadding: "░",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	result, err := bt.RunWithProgress(ctx, syms, backtestDays, func(loaded, total int, sym string) {
		bar.Set(loaded)
	})
	bar.Finish()
	fmt.Println()
	if err != nil {
		return fmt.Errorf("portfolio backtest failed: %w", err)
	}

	if result == nil || result.TotalTrades == 0 {
		fmt.Println("No trades generated in backtest period.")
		return nil
	}

	outputPortfolioBacktest(result)

	// Monte Carlo
	if len(result.Trades) >= 10 {
		fmt.Println("\n--- Monte Carlo Simulation (1000 runs) ---")
		mc := backtest.RunMonteCarlo(result.Trades, cfg.InitialCapital, 1000)
		if mc != nil {
			fmt.Printf(" Median Return:    %.1f%%\n", mc.MedianReturn)
			fmt.Printf(" Best Case (95%%):  %.1f%%\n", mc.BestCase)
			fmt.Printf(" Worst Case (5%%):  %.1f%%\n", mc.WorstCase)
			fmt.Printf(" Ruin Probability: %.1f%%\n", mc.RuinProbability)
		}
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
	return nil
}

func outputSingleBacktest(result *backtest.BacktestResult, initialCapital float64) {
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Printf(" SINGLE STOCK BACKTEST\n")
	fmt.Println("=" + strings.Repeat("=", 59))

	fmt.Printf("\n Period: %s\n", result.Period)
	fmt.Printf(" Initial Capital: %s\n", formatUSD(initialCapital))
	fmt.Printf(" Final Capital:   %s\n", formatUSD(initialCapital+result.TotalReturn))

	fmt.Println("\n--- Performance ---")
	fmt.Printf(" Total Trades:    %d\n", result.TotalTrades)
	fmt.Printf(" Win Rate:        %.1f%% (%d wins / %d losses)\n",
		result.WinRate, result.WinningTrades, result.LosingTrades)
	fmt.Printf(" Total Return:    %s (%.1f%%)\n",
		formatUSD(result.TotalReturn), result.TotalReturnPct)

	fmt.Println("\n--- Risk Metrics ---")
	fmt.Printf(" Avg Win:         %s (%.2f%%)\n", formatUSD(result.AvgWin), result.AvgWinPct)
	fmt.Printf(" Avg Loss:        %s (%.2f%%)\n", formatUSD(result.AvgLoss), result.AvgLossPct)
	fmt.Printf(" Risk/Reward:     1:%.2f\n", result.RiskRewardRatio)
	fmt.Printf(" Profit Factor:   %.2f\n", result.ProfitFactor)
	fmt.Printf(" Max Drawdown:    %.1f%%\n", result.MaxDrawdown)

	fmt.Println("\n--- Expectancy ---")
	fmt.Printf(" Per Trade:       %s\n", formatUSD(result.Expectancy))
	fmt.Printf(" Per Trade (R):   %.2fR\n", result.ExpectancyR)

	fmt.Println("\n--- Kelly Criterion ---")
	fmt.Printf(" Optimal Size:    %.1f%% of capital\n", result.KellyOptimal*100)
	fmt.Printf(" Half-Kelly:      %.1f%% of capital (recommended)\n", result.KellyHalf*100)

	fmt.Println("\n--- Streaks ---")
	fmt.Printf(" Max Win Streak:  %d\n", result.MaxWinStreak)
	fmt.Printf(" Max Lose Streak: %d\n", result.MaxLoseStreak)

	// Monte Carlo simulation
	if len(result.Trades) >= 10 {
		fmt.Println("\n--- Monte Carlo Simulation (1000 runs) ---")
		mc := backtest.RunMonteCarlo(result.Trades, result.TotalReturn+10000000, 1000)
		if mc != nil {
			fmt.Printf(" Median Return:   %.1f%%\n", mc.MedianReturn)
			fmt.Printf(" Best Case (95%%): %.1f%%\n", mc.BestCase)
			fmt.Printf(" Worst Case (5%%): %.1f%%\n", mc.WorstCase)
			fmt.Printf(" Ruin Probability: %.1f%%\n", mc.RuinProbability)
		}
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
}

func outputPortfolioBacktest(result *backtest.PortfolioBacktestResult) {
	fmt.Println("\n--- RESULTS ---")
	fmt.Printf(" Period:          %s (%d trading days)\n", result.Period, result.TradingDays)
	fmt.Printf(" Initial Capital: %s\n", formatUSD(result.InitialCapital))
	fmt.Printf(" Final Equity:    %s\n", formatUSD(result.FinalEquity))
	fmt.Printf(" Total Return:    %s (%.1f%%)\n", formatUSD(result.TotalReturn), result.TotalReturnPct)
	fmt.Printf(" CAGR:            %.1f%%\n", result.CAGR)

	fmt.Println("\n--- Trade Statistics ---")
	fmt.Printf(" Total Trades:    %d\n", result.TotalTrades)
	fmt.Printf(" Win Rate:        %.1f%% (%d W / %d L)\n",
		result.WinRate, result.WinningTrades, result.LosingTrades)
	fmt.Printf(" Avg Win:         %s (+%.2f%%)\n", formatUSD(result.AvgWin), result.AvgWinPct)
	fmt.Printf(" Avg Loss:        %s (%.2f%%)\n", formatUSD(result.AvgLoss), result.AvgLossPct)
	fmt.Printf(" Largest Win:     %s\n", formatUSD(result.LargestWin))
	fmt.Printf(" Largest Loss:    %s\n", formatUSD(result.LargestLoss))

	fmt.Println("\n--- Risk Metrics ---")
	fmt.Printf(" Risk/Reward:     1:%.2f\n", result.RiskRewardRatio)
	fmt.Printf(" Profit Factor:   %.2f\n", result.ProfitFactor)
	fmt.Printf(" Max Drawdown:    %.1f%% (%d days)\n", result.MaxDrawdown, result.MaxDrawdownDays)
	fmt.Printf(" Sharpe Ratio:    %.2f\n", result.SharpeRatio)
	fmt.Printf(" Sortino Ratio:   %.2f\n", result.SortinoRatio)

	fmt.Println("\n--- Expectancy ---")
	fmt.Printf(" Per Trade:       %s\n", formatUSD(result.Expectancy))
	fmt.Printf(" Per Trade (R):   %.2fR\n", result.ExpectancyR)

	fmt.Println("\n--- Position Management ---")
	fmt.Printf(" Avg Positions:   %.1f\n", result.AvgPositions)
	fmt.Printf(" Max Pos Days:    %d\n", result.MaxPositionsHit)
	fmt.Printf(" Signals Skipped: %d (due to max positions)\n", result.SignalsSkipped)

	fmt.Println("\n--- Kelly Criterion ---")
	if result.KellyOptimal > 0 {
		fmt.Printf(" Optimal Size:    %.1f%% of capital\n", result.KellyOptimal*100)
		fmt.Printf(" Half-Kelly:      %.1f%% (recommended)\n", result.KellyHalf*100)
	} else {
		fmt.Println(" Optimal Size:    0% (strategy not profitable)")
		fmt.Println(" Recommendation:  Do NOT trade this strategy as-is")
	}
}

func outputSignalsTable(signals []strategy.Signal, totalScanned int, scanTime time.Duration, capital float64) error {
	if len(signals) == 0 {
		fmt.Println("No pullback opportunities found.")
		fmt.Printf("Scanned %d stocks in %s\n", totalScanned, scanTime.Round(time.Second))
		return nil
	}

	// Calculate portfolio summary
	var totalInvest, totalRisk float64
	for _, s := range signals {
		if s.Guide != nil {
			totalInvest += s.Guide.InvestAmount
			totalRisk += s.Guide.RiskAmount
		}
	}
	cashRemaining := capital - totalInvest

	// Portfolio Summary Header
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println(" PORTFOLIO ALLOCATION SUMMARY")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf(" Total Capital:     %s\n", formatUSD(capital))
	fmt.Printf(" Recommended Picks: %d stocks\n", len(signals))
	fmt.Printf(" Total Investment:  %s (%.1f%%)\n", formatUSD(totalInvest), totalInvest/capital*100)
	fmt.Printf(" Total Risk:        %s (%.2f%%)\n", formatUSD(totalRisk), totalRisk/capital*100)
	fmt.Printf(" Cash Remaining:    %s (%.1f%%)\n", formatUSD(cashRemaining), cashRemaining/capital*100)
	fmt.Println(strings.Repeat("=", 60))

	fmt.Printf("\nFound %d pullback opportunities (sorted by probability):\n\n", len(signals))

	table := tablewriter.NewTable(os.Stdout,
		tablewriter.WithHeader([]string{"#", "Symbol", "Price", "Shares", "Amount", "Alloc%", "Risk$"}),
	)

	for i, s := range signals {
		if s.Guide == nil {
			continue
		}
		g := s.Guide

		table.Append([]string{
			fmt.Sprintf("%d", i+1),
			s.Stock.Symbol,
			fmt.Sprintf("$%.2f", g.EntryPrice),
			fmt.Sprintf("%d", g.PositionSize),
			formatUSD(g.InvestAmount),
			fmt.Sprintf("%.1f%%", g.AllocationPct),
			formatUSD(g.RiskAmount),
		})
	}

	table.Render()

	// Print detailed trade guides
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println(" DETAILED TRADE GUIDE")
	fmt.Println(strings.Repeat("=", 60))

	for i, s := range signals {
		fmt.Printf("\n[%d] %s (%s)\n", i+1, s.Stock.Symbol, s.Stock.Name)
		fmt.Println(strings.Repeat("-", 50))

		// Signal info
		fmt.Printf("  Signal: %s\n", s.Reason)
		fmt.Printf("  Win Probability: %.0f%%\n", s.Probability)

		if s.Guide != nil {
			g := s.Guide

			// Entry/Exit Guide
			fmt.Println("\n  [ENTRY]")
			fmt.Printf("    Buy %d shares @ $%.2f = %s\n", g.PositionSize, g.EntryPrice, formatUSD(g.InvestAmount))
			fmt.Printf("    Allocation: %.1f%% of portfolio\n", g.AllocationPct)

			fmt.Println("\n  [EXIT - Stop Loss]")
			fmt.Printf("    Sell @ $%.2f (%.1f%% loss)\n", g.StopLoss, g.StopLossPct)
			fmt.Printf("    Max Loss: %s (%.2f%% of portfolio)\n", formatUSD(g.RiskAmount), g.RiskPct)

			fmt.Println("\n  [EXIT - Take Profit]")
			fmt.Printf("    Target 1: $%.2f (+%.1f%%) - Sell 50%%\n", g.Target1, g.Target1Pct)
			fmt.Printf("    Target 2: $%.2f (+%.1f%%) - Sell remaining\n", g.Target2, g.Target2Pct)
		}

		// Technical Details
		fmt.Println("\n  [TECHNICALS]")
		fmt.Printf("    Close: $%.2f | MA20: $%.2f | MA50: $%.2f\n",
			s.Details["close"], s.Details["ma20"], s.Details["ma50"])
		if rsi, ok := s.Details["rsi14"]; ok && rsi > 0 {
			rsiStatus := "neutral"
			if rsi < 30 {
				rsiStatus = "oversold"
			} else if rsi > 70 {
				rsiStatus = "overbought"
			}
			fmt.Printf("    RSI(14): %.1f (%s) | Volume: %.1fx avg\n", rsi, rsiStatus, s.Details["volume_ratio"])
		}
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println(" DISCLAIMER: This is not financial advice. Always do your")
	fmt.Println(" own research. Past performance doesn't guarantee future results.")
	fmt.Println(strings.Repeat("=", 60))

	fmt.Printf("\nScanned %d stocks in %s\n", totalScanned, scanTime.Round(time.Second))

	// Save report to file if requested
	if outputFile != "" || len(signals) > 0 {
		filename := outputFile
		if filename == "" {
			// Auto-generate filename with date
			filename = fmt.Sprintf("report_%s.txt", time.Now().Format("2006-01-02_150405"))
		}
		if err := saveReport(filename, signals, capital, totalScanned, scanTime); err != nil {
			fmt.Printf("Warning: failed to save report: %v\n", err)
		} else {
			fmt.Printf("Report saved to: %s\n", filename)
		}
	}

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

func saveReport(filename string, signals []strategy.Signal, capital float64, totalScanned int, scanTime time.Duration) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	// Calculate totals
	var totalInvest, totalRisk float64
	for _, s := range signals {
		if s.Guide != nil {
			totalInvest += s.Guide.InvestAmount
			totalRisk += s.Guide.RiskAmount
		}
	}

	// Write header
	fmt.Fprintf(f, "TRAVELER STOCK SCAN REPORT\n")
	fmt.Fprintf(f, "Generated: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(f, "%s\n\n", strings.Repeat("=", 60))

	// Portfolio Summary
	fmt.Fprintf(f, "PORTFOLIO ALLOCATION SUMMARY\n")
	fmt.Fprintf(f, "%s\n", strings.Repeat("-", 40))
	fmt.Fprintf(f, "Total Capital:     %s\n", formatUSD(capital))
	fmt.Fprintf(f, "Stocks Scanned:    %d\n", totalScanned)
	fmt.Fprintf(f, "Recommended Picks: %d\n", len(signals))
	fmt.Fprintf(f, "Total Investment:  %s (%.1f%%)\n", formatUSD(totalInvest), totalInvest/capital*100)
	fmt.Fprintf(f, "Total Risk:        %s (%.2f%%)\n", formatUSD(totalRisk), totalRisk/capital*100)
	fmt.Fprintf(f, "Cash Remaining:    %s (%.1f%%)\n", formatUSD(capital-totalInvest), (capital-totalInvest)/capital*100)
	fmt.Fprintf(f, "Scan Duration:     %s\n\n", scanTime.Round(time.Second))

	// Quick Reference Table
	fmt.Fprintf(f, "QUICK REFERENCE\n")
	fmt.Fprintf(f, "%s\n", strings.Repeat("-", 40))
	fmt.Fprintf(f, "%-6s %-10s %-8s %-10s %-8s %-10s\n", "#", "Symbol", "Price", "Shares", "Amount", "Risk")
	fmt.Fprintf(f, "%s\n", strings.Repeat("-", 60))
	for i, s := range signals {
		if s.Guide != nil {
			fmt.Fprintf(f, "%-6d %-10s $%-7.2f %-8d %-10s %-10s\n",
				i+1, s.Stock.Symbol, s.Guide.EntryPrice, s.Guide.PositionSize,
				formatUSD(s.Guide.InvestAmount), formatUSD(s.Guide.RiskAmount))
		}
	}
	fmt.Fprintf(f, "\n")

	// Detailed Trade Guide
	fmt.Fprintf(f, "DETAILED TRADE GUIDE\n")
	fmt.Fprintf(f, "%s\n\n", strings.Repeat("=", 60))

	for i, s := range signals {
		fmt.Fprintf(f, "[%d] %s (%s)\n", i+1, s.Stock.Symbol, s.Stock.Name)
		fmt.Fprintf(f, "%s\n", strings.Repeat("-", 50))
		fmt.Fprintf(f, "Signal: %s\n", s.Reason)
		fmt.Fprintf(f, "Win Probability: %.0f%%\n\n", s.Probability)

		if s.Guide != nil {
			g := s.Guide
			fmt.Fprintf(f, "[ENTRY]\n")
			fmt.Fprintf(f, "  Buy %d shares @ $%.2f = %s\n", g.PositionSize, g.EntryPrice, formatUSD(g.InvestAmount))
			fmt.Fprintf(f, "  Allocation: %.1f%% of portfolio\n\n", g.AllocationPct)

			fmt.Fprintf(f, "[STOP LOSS]\n")
			fmt.Fprintf(f, "  Sell @ $%.2f (%.1f%% loss)\n", g.StopLoss, g.StopLossPct)
			fmt.Fprintf(f, "  Max Loss: %s (%.2f%% of portfolio)\n\n", formatUSD(g.RiskAmount), g.RiskPct)

			fmt.Fprintf(f, "[TAKE PROFIT]\n")
			fmt.Fprintf(f, "  Target 1: $%.2f (+%.1f%%) - Sell 50%%\n", g.Target1, g.Target1Pct)
			fmt.Fprintf(f, "  Target 2: $%.2f (+%.1f%%) - Sell remaining\n\n", g.Target2, g.Target2Pct)
		}

		fmt.Fprintf(f, "[TECHNICALS]\n")
		fmt.Fprintf(f, "  Close: $%.2f | MA20: $%.2f | MA50: $%.2f\n",
			s.Details["close"], s.Details["ma20"], s.Details["ma50"])
		if rsi, ok := s.Details["rsi14"]; ok && rsi > 0 {
			fmt.Fprintf(f, "  RSI(14): %.1f | Volume: %.1fx avg\n", rsi, s.Details["volume_ratio"])
		}
		fmt.Fprintf(f, "\n%s\n\n", strings.Repeat("=", 60))
	}

	// Disclaimer
	fmt.Fprintf(f, "DISCLAIMER\n")
	fmt.Fprintf(f, "This is not financial advice. Always do your own research.\n")
	fmt.Fprintf(f, "Past performance doesn't guarantee future results.\n")

	return nil
}

func formatUSD(amount float64) string {
	if amount >= 1000000 {
		return fmt.Sprintf("$%.2fM", amount/1000000)
	} else if amount >= 1000 {
		return fmt.Sprintf("$%.1fK", amount/1000)
	}
	return fmt.Sprintf("$%.2f", amount)
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
