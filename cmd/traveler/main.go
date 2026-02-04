package main

import (
	"bufio"
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
	"traveler/internal/broker/kis"
	"traveler/internal/config"
	"traveler/internal/provider"
	"traveler/internal/scanner"
	"traveler/internal/strategy"
	"traveler/internal/symbols"
	"traveler/internal/trader"
	"traveler/internal/web"
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
	webMode        bool
	webPort        int

	// Auto-trade flags
	autoTrade    bool
	dryRun       bool
	marketOrder  bool
	monitorMode  bool
	adaptiveMode bool // 적응형 자동 스캔
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
	rootCmd.Flags().StringVar(&universe, "universe", "", "stock universe: test, dow30, nasdaq100, sp500, midcap, russell")
	rootCmd.Flags().StringVarP(&outputFile, "output", "o", "", "save report to file (auto-generates filename if empty)")
	rootCmd.Flags().BoolVar(&webMode, "web", false, "start web UI server")
	rootCmd.Flags().IntVar(&webPort, "port", 8080, "web server port")

	// Auto-trade flags
	rootCmd.Flags().BoolVar(&autoTrade, "auto-trade", false, "enable auto-trading via KIS API")
	rootCmd.Flags().BoolVar(&dryRun, "dry-run", true, "dry-run mode (no actual orders)")
	rootCmd.Flags().BoolVar(&marketOrder, "market-order", false, "use market orders instead of limit orders")
	rootCmd.Flags().BoolVar(&monitorMode, "monitor", false, "position monitoring mode only")
	rootCmd.Flags().BoolVar(&adaptiveMode, "adaptive", false, "adaptive mode: auto-select universe based on balance")

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

	// Web mode - start web server
	if webMode {
		return runWebServer(cfg, fallbackProvider)
	}

	// Monitor mode - only monitor existing positions
	if monitorMode {
		return runMonitorMode(cfg)
	}

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())

	// Auto-trade mode: fetch account balance before scanning
	if autoTrade && cfg.KIS.AppKey != "" {
		creds := kis.Credentials{
			AppKey:    cfg.KIS.AppKey,
			AppSecret: cfg.KIS.AppSecret,
			AccountNo: cfg.KIS.AccountNo,
		}
		kisBroker := kis.NewClient(creds)
		if kisBroker.IsReady() {
			if balance, err := kisBroker.GetBalance(ctx); err == nil && balance.TotalEquity > 0 {
				accountBalance = balance.TotalEquity
				fmt.Printf("KIS Account Balance: %s\n", formatUSD(accountBalance))
			}
		}
	}
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
	} else if universe != "" {
		// Use predefined universe
		universeSymbols := symbols.GetUniverse(symbols.Universe(universe))
		if universeSymbols == nil {
			return fmt.Errorf("unknown universe: %s (use: test, dow30, nasdaq100, sp500, midcap, russell)", universe)
		}
		fmt.Printf("Loading %s universe (%d stocks)...\n", universe, len(universeSymbols))
		stocks, err = loader.LoadSymbols(ctx, universeSymbols)
		if err != nil {
			return fmt.Errorf("loading universe symbols: %w", err)
		}
	} else {
		// Load all US stocks
		fmt.Println("Loading US stock list...")
		stocks, err = loader.LoadUSStocks(ctx)
		if err != nil {
			return fmt.Errorf("loading US stocks: %w", err)
		}
	}

	// Adaptive mode: auto-select universe based on balance
	if adaptiveMode && strategyName == "pullback" {
		return runAdaptiveScan(ctx, fallbackProvider, cfg, loader)
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

func runWebServer(cfg *config.Config, p *provider.FallbackProvider) error {
	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	server := web.NewServer(cfg, p, accountBalance, universe)

	go func() {
		<-sigChan
		fmt.Println("\nShutting down web server...")
		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 5*time.Second)
		defer shutdownCancel()
		server.Shutdown(shutdownCtx)
	}()

	return server.Start(webPort)
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

	// Get strategy from registry
	strat, err := strategy.Get("pullback", fallbackProvider)
	if err != nil {
		return fmt.Errorf("strategy not found: %w", err)
	}

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
			// Always fetch candle data for chart visualization (needed for JSON report & web UI)
			candles, candleErr := fallbackProvider.GetDailyCandles(ctx, stock.Symbol, 100)
			if candleErr == nil {
				signal.Candles = candles
			}
			signals = append(signals, *signal)
		}
		bar.Set(i + 1)
	}

	bar.Finish()
	fmt.Println()

	// Calculate position sizing using the new Sizer
	if len(signals) > 0 {
		// Sort by probability first to prioritize best signals
		sort.Slice(signals, func(i, j int) bool {
			return signals[i].Probability > signals[j].Probability
		})

		// Use balance-adjusted sizer config
		sizerCfg := trader.AdjustConfigForBalance(accountBalance)
		sizer := trader.NewPositionSizer(sizerCfg)

		// Apply sizing and filter
		signals = sizer.ApplyToSignals(signals)

		if len(signals) == 0 {
			fmt.Printf("\nNo affordable signals found (max position value: %s)\n", formatUSD(accountBalance*0.2))
		}
	}

	scanTime := time.Since(startTime)

	// Output results
	if format == "json" {
		return outputSignalsJSON(signals, len(stocks), scanTime)
	}

	if err := outputSignalsTable(signals, len(stocks), scanTime, accountBalance); err != nil {
		return err
	}

	// Auto-trade mode
	if autoTrade && len(signals) > 0 {
		return executeAutoTrade(ctx, signals, cfg)
	}

	return nil
}

// adaptiveStockLoader implements trader.StockLoader
type adaptiveStockLoader struct {
	loader *symbols.Loader
}

func (l *adaptiveStockLoader) LoadUniverse(ctx context.Context, u symbols.Universe) ([]model.Stock, error) {
	syms := symbols.GetUniverse(u)
	if syms == nil {
		return nil, fmt.Errorf("unknown universe: %s", u)
	}
	return l.loader.LoadSymbols(ctx, syms)
}

func runAdaptiveScan(ctx context.Context, fallbackProvider *provider.FallbackProvider, cfg *config.Config, loader *symbols.Loader) error {
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println(" ADAPTIVE SCAN - Auto Universe Selection")
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Printf("\n Account Balance: %s\n", formatUSD(accountBalance))

	// Get strategy from registry
	strat, err := strategy.Get("pullback", fallbackProvider)
	if err != nil {
		return fmt.Errorf("strategy not found: %w", err)
	}

	// Create sizer config based on balance
	sizerCfg := trader.AdjustConfigForBalance(accountBalance)
	fmt.Printf(" Risk per Trade:  %.1f%%\n", sizerCfg.RiskPerTrade*100)
	fmt.Printf(" Max Positions:   %d\n", sizerCfg.MaxPositions)
	fmt.Printf(" Min R/R:         %.1f\n", sizerCfg.MinRiskReward)
	fmt.Println()

	// Determine universe tiers
	tiers := trader.GetUniverseTiers(accountBalance)
	tierNames := make([]string, 0)
	for _, t := range tiers {
		if t.Priority == 1 {
			tierNames = append(tierNames, t.Name)
		}
	}
	fmt.Printf(" 1st Tier:        %s\n", strings.Join(tierNames, ", "))
	fmt.Println()

	// Create adaptive scanner
	adaptiveCfg := trader.DefaultAdaptiveConfig()
	adaptiveCfg.Verbose = verbose

	// Create scan function
	scanFunc := func(ctx context.Context, stocks []model.Stock) ([]strategy.Signal, error) {
		var signals []strategy.Signal

		// Progress bar
		bar := progressbar.NewOptions(len(stocks),
			progressbar.OptionEnableColorCodes(true),
			progressbar.OptionShowCount(),
			progressbar.OptionSetWidth(30),
			progressbar.OptionSetDescription("Scanning"),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "[green]█[reset]",
				SaucerHead:    "[green]█[reset]",
				SaucerPadding: "░",
				BarStart:      "[",
				BarEnd:        "]",
			}),
		)

		for i, stock := range stocks {
			select {
			case <-ctx.Done():
				bar.Finish()
				return signals, ctx.Err()
			default:
			}

			signal, err := strat.Analyze(ctx, stock)
			if err == nil && signal != nil {
				candles, candleErr := fallbackProvider.GetDailyCandles(ctx, stock.Symbol, 100)
				if candleErr == nil {
					signal.Candles = candles
				}
				signals = append(signals, *signal)
			}
			bar.Set(i + 1)
		}
		bar.Finish()
		fmt.Println()

		return signals, nil
	}

	scanner := trader.NewAdaptiveScanner(adaptiveCfg, sizerCfg, scanFunc)

	// Run adaptive scan
	result, err := scanner.Scan(ctx, &adaptiveStockLoader{loader: loader})
	if err != nil {
		return fmt.Errorf("adaptive scan failed: %w", err)
	}

	// Print results
	fmt.Println()
	fmt.Printf("Scan Complete:\n")
	fmt.Printf("  Universes:    %s\n", strings.Join(result.UniversesUsed, " → "))
	fmt.Printf("  Stocks:       %d scanned\n", result.ScannedCount)
	fmt.Printf("  Signals:      %d found\n", result.Quality.SignalCount)
	fmt.Printf("  Avg Prob:     %.1f%%\n", result.Quality.AvgProb)
	fmt.Printf("  Expansions:   %d\n", result.Expansions)
	fmt.Printf("  Decision:     %s\n", result.Decision)
	fmt.Println()

	if len(result.Signals) == 0 {
		fmt.Println("No trading opportunities found today.")
		return nil
	}

	// Apply position sizing
	sizer := trader.NewPositionSizer(sizerCfg)
	signals := sizer.ApplyToSignals(result.Signals)

	if len(signals) == 0 {
		fmt.Println("No affordable signals after sizing.")
		return nil
	}

	// Output results
	scanTime := time.Duration(0) // Already shown in adaptive output
	if format == "json" {
		return outputSignalsJSON(signals, result.ScannedCount, scanTime)
	}

	if err := outputSignalsTable(signals, result.ScannedCount, scanTime, accountBalance); err != nil {
		return err
	}

	// Auto-trade mode
	if autoTrade && len(signals) > 0 {
		return executeAutoTrade(ctx, signals, cfg)
	}

	return nil
}

func runPullbackBacktest(ctx context.Context, symbol string, p provider.Provider) error {
	// Check for universe-based backtest
	if universe != "" {
		universeSymbols := symbols.GetUniverse(symbols.Universe(universe))
		if universeSymbols == nil {
			return fmt.Errorf("unknown universe: %s (use: test, dow30, nasdaq100, sp500, midcap, russell)", universe)
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

		// Also save JSON report for web UI
		jsonFilename := fmt.Sprintf("report_%s.json", time.Now().Format("2006-01-02_150405"))
		if err := saveJSONReport(jsonFilename, signals, capital, totalScanned, scanTime); err != nil {
			fmt.Printf("Warning: failed to save JSON report: %v\n", err)
		} else {
			fmt.Printf("JSON report saved to: %s (for Web UI)\n", jsonFilename)
		}
	}

	return nil
}

func outputSignalsJSON(signals []strategy.Signal, totalScanned int, scanTime time.Duration) error {
	// Calculate totals
	var totalInvest, totalRisk float64
	for _, s := range signals {
		if s.Guide != nil {
			totalInvest += s.Guide.InvestAmount
			totalRisk += s.Guide.RiskAmount
		}
	}

	result := strategy.ScanResult{
		Strategy:     "pullback",
		TotalScanned: totalScanned,
		SignalsFound: len(signals),
		Signals:      signals,
		ScanTime:     scanTime.String(),
		Capital:      accountBalance,
		TotalInvest:  totalInvest,
		TotalRisk:    totalRisk,
		GeneratedAt:  time.Now().Format(time.RFC3339),
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

func saveJSONReport(filename string, signals []strategy.Signal, capital float64, totalScanned int, scanTime time.Duration) error {
	// Calculate totals
	var totalInvest, totalRisk float64
	for _, s := range signals {
		if s.Guide != nil {
			totalInvest += s.Guide.InvestAmount
			totalRisk += s.Guide.RiskAmount
		}
	}

	result := strategy.ScanResult{
		Strategy:     "pullback",
		TotalScanned: totalScanned,
		SignalsFound: len(signals),
		Signals:      signals,
		ScanTime:     scanTime.String(),
		Capital:      capital,
		TotalInvest:  totalInvest,
		TotalRisk:    totalRisk,
		GeneratedAt:  time.Now().Format(time.RFC3339),
	}

	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
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

func executeAutoTrade(ctx context.Context, signals []strategy.Signal, cfg *config.Config) error {
	// Check KIS config
	if cfg.KIS.AppKey == "" || cfg.KIS.AppSecret == "" {
		return fmt.Errorf("KIS API credentials not configured. Set KIS_APP_KEY, KIS_APP_SECRET, KIS_ACCOUNT_NO environment variables or add to config.yaml")
	}
	if cfg.KIS.AccountNo == "" {
		return fmt.Errorf("KIS account number not configured")
	}

	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println(" AUTO-TRADE MODE")
	fmt.Println(strings.Repeat("=", 60))

	// Warning for live trading
	if !dryRun {
		fmt.Println()
		fmt.Println(strings.Repeat("!", 60))
		fmt.Println("  WARNING: LIVE TRADING MODE")
		fmt.Println("  This will execute REAL orders with REAL money!")
		fmt.Println("  Account: " + cfg.KIS.AccountNo)
		fmt.Println(strings.Repeat("!", 60))
		fmt.Print("\nType 'CONFIRM' to proceed: ")

		reader := bufio.NewReader(os.Stdin)
		confirm, _ := reader.ReadString('\n')
		confirm = strings.TrimSpace(confirm)
		if confirm != "CONFIRM" {
			fmt.Println("Trading cancelled by user")
			return nil
		}
	}

	// Create KIS client
	creds := kis.Credentials{
		AppKey:    cfg.KIS.AppKey,
		AppSecret: cfg.KIS.AppSecret,
		AccountNo: cfg.KIS.AccountNo,
	}
	kisBroker := kis.NewClient(creds)

	fmt.Println("\nConnecting to KIS API...")
	if !kisBroker.IsReady() {
		return fmt.Errorf("failed to connect to KIS API - check your credentials")
	}
	fmt.Println("Connected successfully!")

	// 실제 계좌 잔고 조회
	fmt.Println("Fetching account balance...")
	balance, err := kisBroker.GetBalance(ctx)
	if err != nil {
		fmt.Printf("Warning: failed to get balance: %v\n", err)
		fmt.Printf("Using CLI capital: %s\n", formatUSD(accountBalance))
	} else {
		// 실제 잔고 사용
		actualCapital := balance.TotalEquity
		if actualCapital > 0 {
			fmt.Printf("Account Balance: %s\n", formatUSD(actualCapital))
			accountBalance = actualCapital
		} else {
			fmt.Printf("Using CLI capital: %s\n", formatUSD(accountBalance))
		}

		// 현재 보유 포지션 표시
		if len(balance.Positions) > 0 {
			fmt.Printf("Current Positions: %d\n", len(balance.Positions))
			for _, p := range balance.Positions {
				pnlSign := "+"
				if p.UnrealizedPnL < 0 {
					pnlSign = ""
				}
				fmt.Printf("  - %s: %d shares (P&L: %s%.2f)\n",
					p.Symbol, p.Quantity, pnlSign, p.UnrealizedPnL)
			}
		}
	}

	// Create AutoTrader
	traderCfg := trader.Config{
		DryRun:          dryRun,
		MaxPositions:    cfg.Trader.MaxPositions,
		MaxPositionPct:  cfg.Trader.MaxPositionPct,
		TotalCapital:    accountBalance,
		RiskPerTrade:    cfg.Trader.RiskPerTrade,
		MonitorInterval: time.Duration(cfg.Trader.MonitorInterval) * time.Second,
	}

	autoTrader := trader.NewAutoTrader(traderCfg, kisBroker, marketOrder)

	// Execute signals
	fmt.Printf("\nExecuting %d signals...\n", len(signals))
	results, err := autoTrader.ExecuteSignals(ctx, signals)
	if err != nil {
		return fmt.Errorf("execute signals: %w", err)
	}

	// Print execution results
	fmt.Println()
	fmt.Println(strings.Repeat("-", 60))
	fmt.Println(" EXECUTION RESULTS")
	fmt.Println(strings.Repeat("-", 60))

	successCount := 0
	for _, r := range results {
		status := "FAILED"
		if r.Success {
			status = "OK"
			successCount++
		}
		fmt.Printf(" [%s] %s: %s %d @ $%.2f",
			status, r.Signal.Stock.Symbol, r.Order.Side, r.Order.Quantity, r.Order.LimitPrice)
		if r.Result != nil {
			fmt.Printf(" (Order: %s)", r.Result.OrderID)
		}
		if r.Error != "" {
			fmt.Printf(" - %s", r.Error)
		}
		fmt.Println()
	}

	fmt.Printf("\nTotal: %d/%d orders executed\n", successCount, len(results))

	// Start monitoring if not dry-run
	if !dryRun && successCount > 0 {
		fmt.Println()
		fmt.Println(strings.Repeat("=", 60))
		fmt.Println(" POSITION MONITORING")
		fmt.Println(strings.Repeat("=", 60))
		fmt.Println("\nMonitoring positions for stop loss and take profit...")
		fmt.Println("Press Ctrl+C to stop monitoring")
		fmt.Println()

		go autoTrader.StartMonitoring(ctx)

		// Wait for interrupt
		<-ctx.Done()
		autoTrader.StopMonitoring()
		fmt.Println("\nMonitoring stopped.")
	}

	return nil
}

func runMonitorMode(cfg *config.Config) error {
	if cfg.KIS.AppKey == "" || cfg.KIS.AppSecret == "" {
		return fmt.Errorf("KIS API credentials not configured")
	}

	fmt.Println("Starting position monitor...")

	// Setup context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nStopping monitor...")
		cancel()
	}()

	// Create KIS client
	creds := kis.Credentials{
		AppKey:    cfg.KIS.AppKey,
		AppSecret: cfg.KIS.AppSecret,
		AccountNo: cfg.KIS.AccountNo,
	}
	broker := kis.NewClient(creds)

	if !broker.IsReady() {
		return fmt.Errorf("failed to connect to KIS API")
	}

	// Show current positions
	positions, err := broker.GetPositions(ctx)
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}

	fmt.Println()
	fmt.Println("Current Positions:")
	fmt.Println(strings.Repeat("-", 60))
	if len(positions) == 0 {
		fmt.Println("  No positions found")
	} else {
		for _, p := range positions {
			pnlSign := "+"
			if p.UnrealizedPnL < 0 {
				pnlSign = ""
			}
			fmt.Printf("  %s: %d shares @ $%.2f (P&L: %s$%.2f / %s%.1f%%)\n",
				p.Symbol, p.Quantity, p.AvgCost, pnlSign, p.UnrealizedPnL, pnlSign, p.UnrealizedPct)
		}
	}
	fmt.Println(strings.Repeat("-", 60))

	// Create trader for monitoring
	traderCfg := trader.Config{
		DryRun:          dryRun,
		MaxPositions:    cfg.Trader.MaxPositions,
		TotalCapital:    accountBalance,
		MonitorInterval: time.Duration(cfg.Trader.MonitorInterval) * time.Second,
	}

	autoTrader := trader.NewAutoTrader(traderCfg, broker, false)

	fmt.Println("\nMonitoring started. Press Ctrl+C to stop.")
	autoTrader.StartMonitoring(ctx)

	return nil
}
