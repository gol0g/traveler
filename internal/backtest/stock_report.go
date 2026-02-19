package backtest

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"traveler/pkg/model"
)

// StockBacktestResult holds the complete backtest results
type StockBacktestResult struct {
	Config         StockSimConfig
	Period         string
	InitialCapital float64
	FinalCapital   float64

	// Stats
	TotalTrades   int
	WinningTrades int
	LosingTrades  int
	WinRate       float64
	TotalReturn   float64
	TotalReturnPct float64
	ProfitFactor  float64
	MaxDrawdown   float64
	SharpeRatio   float64
	SortinoRatio  float64 // downside-only volatility
	CalmarRatio   float64 // annualized return / max drawdown
	MDDDuration   int     // max drawdown recovery in trading days
	TailRatio     float64 // 95th / abs(5th) percentile of daily returns
	RecoveryFactor float64 // total return / abs(max drawdown)
	Expectancy    float64
	ExpectancyR   float64
	AvgWin        float64
	AvgLoss       float64
	AvgHoldDays   float64
	MaxWinStreak  int
	MaxLoseStreak int

	// Detail
	Trades            []StockTrade
	EquityCurve       []float64
	StrategyBreakdown []StrategyStats
	RegimeBreakdown   []RegimeStats
	ExitReasons       map[string]int
}

// StrategyStats holds per-strategy performance
type StrategyStats struct {
	Strategy     string
	Trades       int
	Wins         int
	WinRate      float64
	TotalPnL     float64
	WinPnL       float64
	LossPnL      float64
	ProfitFactor float64
}

// RegimeStats holds per-regime performance
type RegimeStats struct {
	Regime       string
	Trades       int
	Wins         int
	WinRate      float64
	TotalPnL     float64
	WinPnL       float64
	LossPnL      float64
	ProfitFactor float64
}

// PrintReport prints the backtest report to stdout
func (r *StockBacktestResult) PrintReport(verbose bool) {
	market := strings.ToUpper(r.Config.Market)
	currSign := "$"
	if r.Config.Market == "kr" {
		currSign = "₩"
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Stock Backtest Report (%s)\n", market)
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("Period:    %s (%d trading days)\n", r.Period, len(r.EquityCurve))
	fmt.Printf("Capital:   %s%s → %s%s (%+.1f%%)\n",
		currSign, formatNum(r.InitialCapital),
		currSign, formatNum(r.FinalCapital),
		r.TotalReturnPct)
	fmt.Println()

	if r.TotalTrades == 0 {
		fmt.Println("  No trades executed.")
		fmt.Println()
		return
	}

	fmt.Println("── Performance ──")
	fmt.Printf("  Total Trades:  %-10d Win Rate: %.1f%%\n", r.TotalTrades, r.WinRate)
	fmt.Printf("  Wins: %-5d Losses: %-5d\n", r.WinningTrades, r.LosingTrades)
	fmt.Printf("  Profit Factor: %-10.2f Expectancy: %s%.2f/trade\n", r.ProfitFactor, currSign, r.Expectancy)
	fmt.Printf("  Max Drawdown:  %-10.1f%% Sharpe: %.2f\n", r.MaxDrawdown, r.SharpeRatio)
	fmt.Printf("  Sortino: %-10.2f       Calmar: %.2f\n", r.SortinoRatio, r.CalmarRatio)
	fmt.Printf("  Recovery: %.1fx          MDD Duration: %d days\n", r.RecoveryFactor, r.MDDDuration)
	fmt.Printf("  Tail Ratio: %.2f\n", r.TailRatio)
	fmt.Printf("  Avg Hold:      %.1f days\n", r.AvgHoldDays)
	fmt.Printf("  Avg Win:       %s%s   Avg Loss: %s%s\n",
		currSign, formatNum(r.AvgWin), currSign, formatNum(r.AvgLoss))
	fmt.Printf("  Streaks:       Win %d  Lose %d\n", r.MaxWinStreak, r.MaxLoseStreak)
	fmt.Println()

	// Strategy breakdown
	if len(r.StrategyBreakdown) > 0 {
		fmt.Println("── Strategy Breakdown ──")
		for _, ss := range r.StrategyBreakdown {
			fmt.Printf("  %-18s %2d trades, %5.1f%% win, PF %.2f, PnL %s%s\n",
				ss.Strategy+":", ss.Trades, ss.WinRate, ss.ProfitFactor,
				currSign, formatNumSigned(ss.TotalPnL))
		}
		fmt.Println()
	}

	// Regime breakdown
	if len(r.RegimeBreakdown) > 0 {
		fmt.Println("── Regime Breakdown ──")
		for _, rs := range r.RegimeBreakdown {
			fmt.Printf("  %-12s %2d trades, %5.1f%% win, PF %.2f, PnL %s%s\n",
				rs.Regime+":", rs.Trades, rs.WinRate, rs.ProfitFactor,
				currSign, formatNumSigned(rs.TotalPnL))
		}
		fmt.Println()
	}

	// Exit reasons
	if len(r.ExitReasons) > 0 {
		fmt.Println("── Exit Reasons ──")
		fmt.Print("  ")
		parts := make([]string, 0, len(r.ExitReasons))
		for reason, count := range r.ExitReasons {
			parts = append(parts, fmt.Sprintf("%s: %d", reason, count))
		}
		fmt.Println(strings.Join(parts, "  "))
		fmt.Println()
	}

	// Individual trades (verbose)
	if verbose && len(r.Trades) > 0 {
		fmt.Println("── Trades ──")
		for _, t := range r.Trades {
			sign := "+"
			if t.PnL < 0 {
				sign = ""
			}
			fmt.Printf("  %s  %-6s %s%7s × %3.0f  [%-16s %-8s]  → %s %s%5.1f%% (%s%.2f)  hold:%dd\n",
				t.EntryDate.Format("01-02"),
				t.Symbol,
				currSign, formatNum(t.EntryPrice),
				t.Quantity,
				t.Strategy, t.Regime,
				t.ExitDate.Format("01-02"),
				sign, t.PnLPct,
				sign, t.PnL,
				t.HoldDays)
		}
		fmt.Println()
	}
}

func formatNum(v float64) string {
	abs := math.Abs(v)
	if abs >= 1000000 {
		return fmt.Sprintf("%.1fM", v/1000000)
	}
	if abs >= 10000 {
		return fmt.Sprintf("%.0f", v)
	}
	if abs >= 100 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

func formatNumSigned(v float64) string {
	if v >= 0 {
		return "+" + formatNum(v)
	}
	return "-" + formatNum(math.Abs(v))
}

// ──────────────────────────────────────────────
// Candle cache helpers
// ──────────────────────────────────────────────

func loadCachedCandles(cacheDir, symbol, today string) ([]model.Candle, error) {
	path := filepath.Join(cacheDir, fmt.Sprintf("daily_%s_%s.json", symbol, today))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var candles []model.Candle
	if err := json.Unmarshal(data, &candles); err != nil {
		return nil, err
	}
	return candles, nil
}

func saveCachedCandles(cacheDir, symbol, today string, candles []model.Candle) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return
	}

	// Clean old caches for this symbol (different dates)
	pattern := filepath.Join(cacheDir, fmt.Sprintf("daily_%s_*.json", symbol))
	if matches, err := filepath.Glob(pattern); err == nil {
		target := fmt.Sprintf("daily_%s_%s.json", symbol, today)
		for _, m := range matches {
			if filepath.Base(m) != target {
				os.Remove(m)
			}
		}
	}

	data, err := json.Marshal(candles)
	if err != nil {
		return
	}

	path := filepath.Join(cacheDir, fmt.Sprintf("daily_%s_%s.json", symbol, today))
	os.WriteFile(path, data, 0644)
}
