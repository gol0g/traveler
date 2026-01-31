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
	rootCmd.Flags().Float64Var(&accountBalance, "capital", 10000000, "account balance in KRW for position sizing")
	rootCmd.Flags().BoolVar(&runBacktest, "backtest", false, "run backtest on historical data")
	rootCmd.Flags().IntVar(&backtestDays, "backtest-days", 365, "number of days for backtest")

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
	fmt.Printf("Account: %s | Risk per trade: 1%%\n\n", formatKRW(accountBalance))

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
			// Calculate position size based on account balance
			if signal.Guide != nil {
				riskAmount := accountBalance * 0.01 // 1% risk
				riskPerShare := signal.Guide.EntryPrice - signal.Guide.StopLoss
				if riskPerShare > 0 {
					signal.Guide.PositionSize = int(riskAmount / riskPerShare)
					signal.Guide.InvestAmount = float64(signal.Guide.PositionSize) * signal.Guide.EntryPrice
					signal.Guide.RiskAmount = riskAmount
					signal.Guide.RiskPct = 1.0
				}
			}
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

func runPullbackBacktest(ctx context.Context, symbol string, p provider.Provider) error {
	// If single symbol, use single backtest; otherwise portfolio backtest
	symbols := strings.Split(symbolList, ",")
	if len(symbols) > 1 {
		return runPortfolioBacktest(ctx, symbols, p)
	}

	fmt.Printf("Running single-stock backtest for %s (%d days)...\n", symbol, backtestDays)
	fmt.Println("TIP: Use --symbols with multiple tickers for full portfolio simulation\n")

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

func runPortfolioBacktest(ctx context.Context, symbols []string, p provider.Provider) error {
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println(" PORTFOLIO BACKTEST")
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Printf("\n Symbols:    %d stocks\n", len(symbols))
	fmt.Printf(" Period:     %d days\n", backtestDays)
	fmt.Printf(" Capital:    %s\n", formatKRW(accountBalance))
	fmt.Printf(" Max Positions: 5 simultaneous\n")
	fmt.Printf(" Risk/Trade: 1%%\n\n")

	cfg := backtest.DefaultPortfolioConfig()
	cfg.InitialCapital = accountBalance

	bt := backtest.NewPortfolioBacktester(cfg, p)
	result, err := bt.Run(ctx, symbols, backtestDays)
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
	fmt.Printf(" Initial Capital: %s\n", formatKRW(initialCapital))
	fmt.Printf(" Final Capital:   %s\n", formatKRW(initialCapital+result.TotalReturn))

	fmt.Println("\n--- Performance ---")
	fmt.Printf(" Total Trades:    %d\n", result.TotalTrades)
	fmt.Printf(" Win Rate:        %.1f%% (%d wins / %d losses)\n",
		result.WinRate, result.WinningTrades, result.LosingTrades)
	fmt.Printf(" Total Return:    %s (%.1f%%)\n",
		formatKRW(result.TotalReturn), result.TotalReturnPct)

	fmt.Println("\n--- Risk Metrics ---")
	fmt.Printf(" Avg Win:         %s (%.2f%%)\n", formatKRW(result.AvgWin), result.AvgWinPct)
	fmt.Printf(" Avg Loss:        %s (%.2f%%)\n", formatKRW(result.AvgLoss), result.AvgLossPct)
	fmt.Printf(" Risk/Reward:     1:%.2f\n", result.RiskRewardRatio)
	fmt.Printf(" Profit Factor:   %.2f\n", result.ProfitFactor)
	fmt.Printf(" Max Drawdown:    %.1f%%\n", result.MaxDrawdown)

	fmt.Println("\n--- Expectancy ---")
	fmt.Printf(" Per Trade:       %s\n", formatKRW(result.Expectancy))
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
	fmt.Printf(" Initial Capital: %s\n", formatKRW(result.InitialCapital))
	fmt.Printf(" Final Equity:    %s\n", formatKRW(result.FinalEquity))
	fmt.Printf(" Total Return:    %s (%.1f%%)\n", formatKRW(result.TotalReturn), result.TotalReturnPct)
	fmt.Printf(" CAGR:            %.1f%%\n", result.CAGR)

	fmt.Println("\n--- Trade Statistics ---")
	fmt.Printf(" Total Trades:    %d\n", result.TotalTrades)
	fmt.Printf(" Win Rate:        %.1f%% (%d W / %d L)\n",
		result.WinRate, result.WinningTrades, result.LosingTrades)
	fmt.Printf(" Avg Win:         %s (+%.2f%%)\n", formatKRW(result.AvgWin), result.AvgWinPct)
	fmt.Printf(" Avg Loss:        %s (%.2f%%)\n", formatKRW(result.AvgLoss), result.AvgLossPct)
	fmt.Printf(" Largest Win:     %s\n", formatKRW(result.LargestWin))
	fmt.Printf(" Largest Loss:    %s\n", formatKRW(result.LargestLoss))

	fmt.Println("\n--- Risk Metrics ---")
	fmt.Printf(" Risk/Reward:     1:%.2f\n", result.RiskRewardRatio)
	fmt.Printf(" Profit Factor:   %.2f\n", result.ProfitFactor)
	fmt.Printf(" Max Drawdown:    %.1f%% (%d days)\n", result.MaxDrawdown, result.MaxDrawdownDays)
	fmt.Printf(" Sharpe Ratio:    %.2f\n", result.SharpeRatio)
	fmt.Printf(" Sortino Ratio:   %.2f\n", result.SortinoRatio)

	fmt.Println("\n--- Expectancy ---")
	fmt.Printf(" Per Trade:       %s\n", formatKRW(result.Expectancy))
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

	// Print actionable trade guides for top signals
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println(" TRADE GUIDE - Top Opportunities")
	fmt.Println(strings.Repeat("=", 60))

	count := 0
	for _, s := range signals {
		if count >= 3 { // Top 3 only for actionable guide
			break
		}

		fmt.Printf("\n[%d] %s (%s)\n", count+1, s.Stock.Symbol, s.Stock.Name)
		fmt.Println(strings.Repeat("-", 50))

		// Signal info
		fmt.Printf("  Signal: %s\n", s.Reason)
		fmt.Printf("  Win Probability: %.0f%% | Breakeven: 33%%\n", s.Probability)

		if s.Guide != nil {
			g := s.Guide

			// Entry/Exit Guide
			fmt.Println("\n  [ENTRY]")
			fmt.Printf("    Price:    $%.2f (%s order)\n", g.EntryPrice, g.EntryType)

			fmt.Println("\n  [EXIT - Stop Loss]")
			fmt.Printf("    Price:    $%.2f (%.1f%% below entry)\n", g.StopLoss, g.StopLossPct)

			fmt.Println("\n  [EXIT - Targets]")
			fmt.Printf("    Target 1: $%.2f (+%.1f%%) - Take 50%% profit\n", g.Target1, g.Target1Pct)
			fmt.Printf("    Target 2: $%.2f (+%.1f%%) - Take remaining\n", g.Target2, g.Target2Pct)

			// Position Sizing
			fmt.Println("\n  [POSITION SIZE]")
			fmt.Printf("    Shares:   %d shares\n", g.PositionSize)
			fmt.Printf("    Amount:   %s\n", formatKRW(g.InvestAmount))
			fmt.Printf("    At Risk:  %s (%.1f%% of portfolio)\n", formatKRW(g.RiskAmount), g.RiskPct)

			// Risk/Reward
			fmt.Println("\n  [RISK/REWARD]")
			fmt.Printf("    Ratio:    1:%.1f (target 2R)\n", g.RiskRewardRatio)
			if g.KellyFraction > 0 {
				fmt.Printf("    Kelly:    %.1f%% optimal, %.1f%% recommended (half)\n",
					g.KellyFraction*100, g.KellyFraction*50)
			}
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

		count++
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println(" DISCLAIMER: This is not financial advice. Always do your")
	fmt.Println(" own research. Past performance doesn't guarantee future results.")
	fmt.Println(strings.Repeat("=", 60))

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

func formatKRW(amount float64) string {
	if amount >= 100000000 { // 1억 이상
		return fmt.Sprintf("%.1f억원", amount/100000000)
	} else if amount >= 10000 { // 1만 이상
		return fmt.Sprintf("%.0f만원", amount/10000)
	}
	return fmt.Sprintf("%.0f원", amount)
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
