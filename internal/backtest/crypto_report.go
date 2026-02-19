package backtest

import (
	"fmt"
	"strings"
)

// CryptoBacktestResult holds all backtest outputs
type CryptoBacktestResult struct {
	Config          CryptoORBConfig
	Period          string
	InitialCapital  float64
	FinalCapital    float64
	TotalTrades     int
	WinningTrades   int
	LosingTrades    int
	WinRate         float64
	TotalReturn     float64
	TotalReturnPct  float64
	AvgWin          float64
	AvgLoss         float64
	LargestWin      float64
	LargestLoss     float64
	RiskRewardRatio float64
	Expectancy      float64
	ExpectancyR     float64
	ProfitFactor    float64
	MaxDrawdown     float64
	SharpeRatio     float64
	KellyOptimal    float64
	KellyHalf       float64
	MaxWinStreak    int
	MaxLoseStreak   int
	AvgHoldMin      float64
	Trades          []CryptoTrade
	EquityCurve     []float64
	SymbolBreakdown []SymbolStats
	ExitReasons     map[string]int
}

// SymbolStats tracks per-symbol performance
type SymbolStats struct {
	Symbol   string  `json:"symbol"`
	Trades   int     `json:"trades"`
	Wins     int     `json:"wins"`
	WinRate  float64 `json:"win_rate"`
	TotalPnL float64 `json:"total_pnl"`
}

// PrintReport outputs the backtest result as a formatted table
func (r *CryptoBacktestResult) PrintReport(verbose bool) {
	strategy := "ORB"
	if r.Config.EnableDipBuy && r.Config.EnableORB {
		strategy = "ORB + DipBuy"
	} else if r.Config.EnableDipBuy {
		strategy = "DipBuy"
	}

	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf(" CRYPTO %s BACKTEST\n", strategy)
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf(" Period:    %s\n", r.Period)
	fmt.Printf(" Strategy:  %s\n", strategy)
	fmt.Printf(" Capital:   %s\n", fmtKRW(r.InitialCapital))

	fmt.Println()
	fmt.Println("--- Performance ---")
	fmt.Printf(" Trades:       %d (%dW / %dL)\n", r.TotalTrades, r.WinningTrades, r.LosingTrades)
	fmt.Printf(" Win Rate:     %.1f%%\n", r.WinRate)
	fmt.Printf(" Return:       %s (%.2f%%)\n", fmtKRWSign(r.TotalReturn), r.TotalReturnPct)
	fmt.Printf(" Final:        %s\n", fmtKRW(r.FinalCapital))
	fmt.Printf(" Avg Win:      %s\n", fmtKRWSign(r.AvgWin))
	fmt.Printf(" Avg Loss:     %s\n", fmtKRWSign(-r.AvgLoss))
	fmt.Printf(" Largest Win:  %s\n", fmtKRWSign(r.LargestWin))
	fmt.Printf(" Largest Loss: %s\n", fmtKRWSign(r.LargestLoss))

	fmt.Println()
	fmt.Println("--- Risk Metrics ---")
	fmt.Printf(" Profit Factor: %.2f\n", r.ProfitFactor)
	fmt.Printf(" Max Drawdown:  %.2f%%\n", r.MaxDrawdown)
	fmt.Printf(" Sharpe Ratio:  %.2f\n", r.SharpeRatio)
	fmt.Printf(" R/R Ratio:     %.2f\n", r.RiskRewardRatio)
	fmt.Printf(" Expectancy:    %s/trade\n", fmtKRWSign(r.Expectancy))
	fmt.Printf(" Expectancy R:  %.2fR/trade\n", r.ExpectancyR)
	fmt.Printf(" Kelly:         %.1f%% (half: %.1f%%)\n", r.KellyOptimal*100, r.KellyHalf*100)

	fmt.Println()
	fmt.Println("--- Streaks ---")
	fmt.Printf(" Max Win Streak:  %d\n", r.MaxWinStreak)
	fmt.Printf(" Max Lose Streak: %d\n", r.MaxLoseStreak)
	fmt.Printf(" Avg Hold:        %d min\n", int(r.AvgHoldMin))

	// Exit reason breakdown
	if len(r.ExitReasons) > 0 {
		fmt.Println()
		fmt.Println("--- Exit Reasons ---")
		total := float64(r.TotalTrades)
		order := []string{"stop", "target1", "target2", "eod", "daily_limit"}
		for _, reason := range order {
			count, ok := r.ExitReasons[reason]
			if !ok {
				continue
			}
			pct := float64(count) / total * 100
			label := exitLabel(reason)
			fmt.Printf(" %-14s %3d  (%4.1f%%)\n", label, count, pct)
		}
	}

	// Symbol breakdown
	if len(r.SymbolBreakdown) > 0 {
		fmt.Println()
		fmt.Println("--- Per-Symbol ---")
		fmt.Printf(" %-12s %6s %7s %10s\n", "Symbol", "Trades", "WinRate", "PnL")
		fmt.Println(" " + strings.Repeat("-", 40))
		for _, ss := range r.SymbolBreakdown {
			fmt.Printf(" %-12s %6d %6.1f%% %10s\n",
				ss.Symbol, ss.Trades, ss.WinRate, fmtKRWSign(ss.TotalPnL))
		}
	}

	fmt.Println(strings.Repeat("=", 60))

	// Verbose: individual trades
	if verbose && len(r.Trades) > 0 {
		fmt.Println()
		fmt.Println("--- All Trades ---")
		fmt.Printf(" %-12s %-16s %-16s %8s %8s %8s %6s %5s\n",
			"Symbol", "Entry", "Exit", "EntryP", "ExitP", "PnL", "R", "Reason")
		fmt.Println(" " + strings.Repeat("-", 85))
		for _, t := range r.Trades {
			fmt.Printf(" %-12s %-16s %-16s %8.0f %8.0f %8s %5.1fR  %s\n",
				t.Symbol,
				t.EntryTime.Format("01-02 15:04"),
				t.ExitTime.Format("01-02 15:04"),
				t.EntryPrice, t.ExitPrice,
				fmtKRWSign(t.PnL),
				t.RMultiple,
				exitLabel(t.ExitReason),
			)
		}
	}
}

func fmtKRW(v float64) string {
	if v >= 1000000 {
		return fmt.Sprintf("%.1fM", v/1000000)
	}
	return fmt.Sprintf("%.0f", v)
}

func fmtKRWSign(v float64) string {
	if v >= 0 {
		return fmt.Sprintf("+%.0f", v)
	}
	return fmt.Sprintf("%.0f", v)
}

// PrintRSIReport outputs RSI contrarian backtest result
func (r *CryptoBacktestResult) PrintRSIReport(verbose bool) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println(" CRYPTO RSI CONTRARIAN BACKTEST")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf(" Period:    %s\n", r.Period)
	fmt.Printf(" Strategy:  RSI Contrarian (RSI<%d, bear market)\n", 20)
	fmt.Printf(" Capital:   %s\n", fmtKRW(r.InitialCapital))
	fmt.Printf(" Hold Max:  10 days\n")

	fmt.Println()
	fmt.Println("--- Performance ---")
	fmt.Printf(" Trades:       %d (%dW / %dL)\n", r.TotalTrades, r.WinningTrades, r.LosingTrades)
	fmt.Printf(" Win Rate:     %.1f%%\n", r.WinRate)
	fmt.Printf(" Return:       %s (%.2f%%)\n", fmtKRWSign(r.TotalReturn), r.TotalReturnPct)
	fmt.Printf(" Final:        %s\n", fmtKRW(r.FinalCapital))
	fmt.Printf(" Avg Win:      %s\n", fmtKRWSign(r.AvgWin))
	fmt.Printf(" Avg Loss:     %s\n", fmtKRWSign(-r.AvgLoss))
	fmt.Printf(" Largest Win:  %s\n", fmtKRWSign(r.LargestWin))
	fmt.Printf(" Largest Loss: %s\n", fmtKRWSign(r.LargestLoss))

	fmt.Println()
	fmt.Println("--- Risk Metrics ---")
	fmt.Printf(" Profit Factor: %.2f\n", r.ProfitFactor)
	fmt.Printf(" Max Drawdown:  %.2f%%\n", r.MaxDrawdown)
	fmt.Printf(" Sharpe Ratio:  %.2f\n", r.SharpeRatio)
	fmt.Printf(" R/R Ratio:     %.2f\n", r.RiskRewardRatio)
	fmt.Printf(" Expectancy:    %s/trade\n", fmtKRWSign(r.Expectancy))
	fmt.Printf(" Kelly:         %.1f%% (half: %.1f%%)\n", r.KellyOptimal*100, r.KellyHalf*100)

	fmt.Println()
	fmt.Println("--- Streaks ---")
	fmt.Printf(" Max Win Streak:  %d\n", r.MaxWinStreak)
	fmt.Printf(" Max Lose Streak: %d\n", r.MaxLoseStreak)
	if r.AvgHoldMin > 0 {
		fmt.Printf(" Avg Hold:        %.1f days\n", r.AvgHoldMin/60/24)
	}

	if len(r.ExitReasons) > 0 {
		fmt.Println()
		fmt.Println("--- Exit Reasons ---")
		total := float64(r.TotalTrades)
		for _, reason := range []string{"stop", "target1", "target2", "timeout", "eod"} {
			count, ok := r.ExitReasons[reason]
			if !ok {
				continue
			}
			pct := float64(count) / total * 100
			fmt.Printf(" %-14s %3d  (%4.1f%%)\n", exitLabel(reason), count, pct)
		}
	}

	if len(r.SymbolBreakdown) > 0 {
		fmt.Println()
		fmt.Println("--- Per-Symbol ---")
		fmt.Printf(" %-12s %6s %7s %10s\n", "Symbol", "Trades", "WinRate", "PnL")
		fmt.Println(" " + strings.Repeat("-", 40))
		for _, ss := range r.SymbolBreakdown {
			fmt.Printf(" %-12s %6d %6.1f%% %10s\n",
				ss.Symbol, ss.Trades, ss.WinRate, fmtKRWSign(ss.TotalPnL))
		}
	}

	fmt.Println(strings.Repeat("=", 60))

	if verbose && len(r.Trades) > 0 {
		fmt.Println()
		fmt.Println("--- All Trades ---")
		fmt.Printf(" %-12s %-11s %-11s %10s %10s %8s %6s %s\n",
			"Symbol", "Entry", "Exit", "EntryP", "ExitP", "PnL", "R", "Reason")
		fmt.Println(" " + strings.Repeat("-", 80))
		for _, t := range r.Trades {
			fmt.Printf(" %-12s %-11s %-11s %10.0f %10.0f %8s %5.1fR  %s\n",
				t.Symbol,
				t.EntryTime.Format("01-02"),
				t.ExitTime.Format("01-02"),
				t.EntryPrice, t.ExitPrice,
				fmtKRWSign(t.PnL),
				t.RMultiple,
				exitLabel(t.ExitReason),
			)
		}
	}
}

// PrintWBottomReport outputs W-Bottom backtest result
func (r *CryptoBacktestResult) PrintWBottomReport(verbose bool) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println(" CRYPTO W-BOTTOM BACKTEST")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf(" Period:    %s\n", r.Period)
	fmt.Printf(" Strategy:  W-Bottom + RSI Divergence + Confluence\n")
	fmt.Printf(" Capital:   %s\n", fmtKRW(r.InitialCapital))
	fmt.Printf(" Risk:      0.5%%/trade (bear-market sizing)\n")

	fmt.Println()
	fmt.Println("--- Performance ---")
	fmt.Printf(" Trades:       %d (%dW / %dL)\n", r.TotalTrades, r.WinningTrades, r.LosingTrades)
	fmt.Printf(" Win Rate:     %.1f%%\n", r.WinRate)
	fmt.Printf(" Return:       %s (%.2f%%)\n", fmtKRWSign(r.TotalReturn), r.TotalReturnPct)
	fmt.Printf(" Final:        %s\n", fmtKRW(r.FinalCapital))
	fmt.Printf(" Avg Win:      %s\n", fmtKRWSign(r.AvgWin))
	fmt.Printf(" Avg Loss:     %s\n", fmtKRWSign(-r.AvgLoss))
	fmt.Printf(" Largest Win:  %s\n", fmtKRWSign(r.LargestWin))
	fmt.Printf(" Largest Loss: %s\n", fmtKRWSign(r.LargestLoss))

	fmt.Println()
	fmt.Println("--- Risk Metrics ---")
	fmt.Printf(" Profit Factor: %.2f\n", r.ProfitFactor)
	fmt.Printf(" Max Drawdown:  %.2f%%\n", r.MaxDrawdown)
	fmt.Printf(" Sharpe Ratio:  %.2f\n", r.SharpeRatio)
	fmt.Printf(" R/R Ratio:     %.2f\n", r.RiskRewardRatio)
	fmt.Printf(" Expectancy:    %s/trade\n", fmtKRWSign(r.Expectancy))
	fmt.Printf(" Expectancy R:  %.2fR/trade\n", r.ExpectancyR)
	fmt.Printf(" Kelly:         %.1f%% (half: %.1f%%)\n", r.KellyOptimal*100, r.KellyHalf*100)

	fmt.Println()
	fmt.Println("--- Streaks ---")
	fmt.Printf(" Max Win Streak:  %d\n", r.MaxWinStreak)
	fmt.Printf(" Max Lose Streak: %d\n", r.MaxLoseStreak)
	if r.AvgHoldMin > 0 {
		fmt.Printf(" Avg Hold:        %.1f days\n", r.AvgHoldMin/60/24)
	}

	if len(r.ExitReasons) > 0 {
		fmt.Println()
		fmt.Println("--- Exit Reasons ---")
		total := float64(r.TotalTrades)
		for _, reason := range []string{"stop", "target1", "target2", "timeout", "eod"} {
			count, ok := r.ExitReasons[reason]
			if !ok {
				continue
			}
			pct := float64(count) / total * 100
			fmt.Printf(" %-14s %3d  (%4.1f%%)\n", exitLabel(reason), count, pct)
		}
	}

	if len(r.SymbolBreakdown) > 0 {
		fmt.Println()
		fmt.Println("--- Per-Symbol ---")
		fmt.Printf(" %-12s %6s %7s %10s\n", "Symbol", "Trades", "WinRate", "PnL")
		fmt.Println(" " + strings.Repeat("-", 40))
		for _, ss := range r.SymbolBreakdown {
			fmt.Printf(" %-12s %6d %6.1f%% %10s\n",
				ss.Symbol, ss.Trades, ss.WinRate, fmtKRWSign(ss.TotalPnL))
		}
	}

	fmt.Println(strings.Repeat("=", 60))

	if verbose && len(r.Trades) > 0 {
		fmt.Println()
		fmt.Println("--- All Trades ---")
		fmt.Printf(" %-12s %-11s %-11s %10s %10s %8s %6s %s\n",
			"Symbol", "Entry", "Exit", "EntryP", "ExitP", "PnL", "R", "Reason")
		fmt.Println(" " + strings.Repeat("-", 80))
		for _, t := range r.Trades {
			fmt.Printf(" %-12s %-11s %-11s %10.0f %10.0f %8s %5.1fR  %s\n",
				t.Symbol,
				t.EntryTime.Format("01-02"),
				t.ExitTime.Format("01-02"),
				t.EntryPrice, t.ExitPrice,
				fmtKRWSign(t.PnL),
				t.RMultiple,
				exitLabel(t.ExitReason),
			)
		}
	}
}

func exitLabel(reason string) string {
	switch reason {
	case "stop":
		return "Stop Loss"
	case "target1":
		return "Target 1"
	case "target2":
		return "Target 2"
	case "eod":
		return "End of Day"
	case "daily_limit":
		return "Daily Limit"
	case "timeout":
		return "Timeout"
	default:
		return reason
	}
}
