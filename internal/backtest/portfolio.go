package backtest

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// PortfolioPosition represents an open position
type PortfolioPosition struct {
	Symbol     string
	EntryDate  time.Time
	EntryPrice float64
	StopLoss   float64
	Target     float64
	Shares     int
	DaysHeld   int
}

// DailySnapshot represents portfolio state at end of day
type DailySnapshot struct {
	Date          time.Time
	Equity        float64
	Cash          float64
	PositionValue float64
	Positions     int
	DayPnL        float64
	DayReturn     float64
}

// PortfolioBacktestResult contains full portfolio simulation results
type PortfolioBacktestResult struct {
	// Config
	Strategy        string  `json:"strategy"`
	Period          string  `json:"period"`
	InitialCapital  float64 `json:"initial_capital"`
	MaxPositions    int     `json:"max_positions"`

	// Summary
	FinalEquity     float64 `json:"final_equity"`
	TotalReturn     float64 `json:"total_return"`
	TotalReturnPct  float64 `json:"total_return_pct"`
	CAGR            float64 `json:"cagr"` // Compound Annual Growth Rate

	// Trades
	TotalTrades     int     `json:"total_trades"`
	WinningTrades   int     `json:"winning_trades"`
	LosingTrades    int     `json:"losing_trades"`
	WinRate         float64 `json:"win_rate"`

	// Returns
	AvgWin          float64 `json:"avg_win"`
	AvgLoss         float64 `json:"avg_loss"`
	AvgWinPct       float64 `json:"avg_win_pct"`
	AvgLossPct      float64 `json:"avg_loss_pct"`
	LargestWin      float64 `json:"largest_win"`
	LargestLoss     float64 `json:"largest_loss"`

	// Risk
	RiskRewardRatio float64 `json:"risk_reward_ratio"`
	ProfitFactor    float64 `json:"profit_factor"`
	Expectancy      float64 `json:"expectancy"`
	ExpectancyR     float64 `json:"expectancy_r"`
	MaxDrawdown     float64 `json:"max_drawdown"`
	MaxDrawdownDays int     `json:"max_drawdown_days"`
	SharpeRatio     float64 `json:"sharpe_ratio"`
	SortinoRatio    float64 `json:"sortino_ratio"`

	// Kelly
	KellyOptimal    float64 `json:"kelly_optimal"`
	KellyHalf       float64 `json:"kelly_half"`

	// Activity
	TradingDays     int     `json:"trading_days"`
	AvgPositions    float64 `json:"avg_positions"`
	MaxPositionsHit int     `json:"max_positions_hit"`
	SignalsSkipped  int     `json:"signals_skipped"` // Due to max positions

	// Details
	Trades          []Trade         `json:"trades"`
	DailySnapshots  []DailySnapshot `json:"daily_snapshots"`
}

// PortfolioBacktestConfig holds configuration
type PortfolioBacktestConfig struct {
	InitialCapital  float64
	RiskPerTrade    float64 // e.g., 0.01 = 1%
	MaxPositions    int     // Maximum simultaneous positions
	StopLossPct     float64 // e.g., 0.02 = 2%
	TargetRMultiple float64 // e.g., 2.0 = 2R target
	MaxHoldDays     int     // Maximum days to hold
	Commission      float64 // Per trade commission rate
	Slippage        float64 // Expected slippage
}

// DefaultPortfolioConfig returns default configuration
func DefaultPortfolioConfig() PortfolioBacktestConfig {
	return PortfolioBacktestConfig{
		InitialCapital:  10000000, // 1000만원
		RiskPerTrade:    0.01,     // 1%
		MaxPositions:    5,        // Max 5 positions at once
		StopLossPct:     0.02,     // 2%
		TargetRMultiple: 2.0,      // 2R target
		MaxHoldDays:     5,        // 5 trading days
		Commission:      0.00015,  // 0.015%
		Slippage:        0.001,    // 0.1%
	}
}

// PortfolioBacktester simulates full portfolio trading
type PortfolioBacktester struct {
	config   PortfolioBacktestConfig
	provider provider.Provider
}

// NewPortfolioBacktester creates a new portfolio backtester
func NewPortfolioBacktester(cfg PortfolioBacktestConfig, p provider.Provider) *PortfolioBacktester {
	return &PortfolioBacktester{
		config:   cfg,
		provider: p,
	}
}

// symbolData holds historical data for one symbol
type symbolData struct {
	Symbol  string
	Candles []model.Candle
}

// Run executes the portfolio backtest
func (pb *PortfolioBacktester) Run(ctx context.Context, symbols []string, days int) (*PortfolioBacktestResult, error) {
	return pb.RunWithProgress(ctx, symbols, days, nil)
}

// ProgressCallback reports loading progress
type ProgressCallback func(loaded, total int, symbol string)

// RunWithProgress executes the portfolio backtest with progress callback
func (pb *PortfolioBacktester) RunWithProgress(ctx context.Context, symbols []string, days int, progress ProgressCallback) (*PortfolioBacktestResult, error) {
	// Fetch historical data for all symbols
	fmt.Printf("Loading historical data for %d symbols...\n", len(symbols))

	allData := make(map[string][]model.Candle)
	for i, sym := range symbols {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		candles, err := pb.provider.GetDailyCandles(ctx, sym, days+60) // Extra for MA calculation
		if err != nil || len(candles) < 60 {
			if progress != nil {
				progress(i+1, len(symbols), sym+" (skipped)")
			}
			continue
		}
		allData[sym] = candles

		if progress != nil {
			progress(i+1, len(symbols), sym)
		}
	}

	if len(allData) == 0 {
		return nil, fmt.Errorf("no valid data for any symbol")
	}

	fmt.Printf("Loaded data for %d/%d symbols\n", len(allData), len(symbols))

	// Find common date range
	dates := pb.findCommonDates(allData, days)
	if len(dates) < 20 {
		return nil, fmt.Errorf("insufficient common trading days: %d", len(dates))
	}

	fmt.Printf("Simulating %d trading days...\n\n", len(dates))

	// Initialize portfolio
	result := &PortfolioBacktestResult{
		Strategy:       "pullback",
		InitialCapital: pb.config.InitialCapital,
		MaxPositions:   pb.config.MaxPositions,
		Trades:         make([]Trade, 0),
		DailySnapshots: make([]DailySnapshot, 0),
	}

	cash := pb.config.InitialCapital
	positions := make(map[string]*PortfolioPosition)
	peakEquity := cash

	// Simulate each trading day
	for dayIdx, date := range dates {
		// 1. Check exits for existing positions
		closedPositions := make([]string, 0)

		for sym, pos := range positions {
			candles := allData[sym]
			dayCandle := pb.findCandle(candles, date)
			if dayCandle == nil {
				continue
			}

			pos.DaysHeld++

			// Check stop loss
			if dayCandle.Low <= pos.StopLoss {
				exitPrice := pos.StopLoss * (1 - pb.config.Slippage)
				trade := pb.closeTrade(pos, date, exitPrice, "stop")
				result.Trades = append(result.Trades, trade)
				cash += float64(pos.Shares)*exitPrice - pb.calcCommission(pos.Shares, exitPrice)
				closedPositions = append(closedPositions, sym)
				continue
			}

			// Check target
			if dayCandle.High >= pos.Target {
				exitPrice := pos.Target * (1 - pb.config.Slippage)
				trade := pb.closeTrade(pos, date, exitPrice, "target")
				result.Trades = append(result.Trades, trade)
				cash += float64(pos.Shares)*exitPrice - pb.calcCommission(pos.Shares, exitPrice)
				closedPositions = append(closedPositions, sym)
				continue
			}

			// Check timeout
			if pos.DaysHeld >= pb.config.MaxHoldDays {
				exitPrice := dayCandle.Close * (1 - pb.config.Slippage)
				trade := pb.closeTrade(pos, date, exitPrice, "timeout")
				result.Trades = append(result.Trades, trade)
				cash += float64(pos.Shares)*exitPrice - pb.calcCommission(pos.Shares, exitPrice)
				closedPositions = append(closedPositions, sym)
			}
		}

		for _, sym := range closedPositions {
			delete(positions, sym)
		}

		// 2. Scan for new signals (if we have capacity)
		if len(positions) < pb.config.MaxPositions {
			signals := pb.scanForSignals(allData, date, dayIdx)

			for _, sig := range signals {
				if len(positions) >= pb.config.MaxPositions {
					result.SignalsSkipped++
					break
				}

				// Skip if already holding this symbol
				if _, exists := positions[sig.Symbol]; exists {
					continue
				}

				// Calculate position size
				riskAmount := (cash + pb.calcPositionValue(positions, allData, date)) * pb.config.RiskPerTrade
				entryPrice := sig.EntryPrice * (1 + pb.config.Slippage)
				stopLoss := entryPrice * (1 - pb.config.StopLossPct)
				riskPerShare := entryPrice - stopLoss
				shares := int(riskAmount / riskPerShare)

				if shares <= 0 {
					continue
				}

				cost := float64(shares)*entryPrice + pb.calcCommission(shares, entryPrice)
				if cost > cash {
					shares = int((cash - 1000) / entryPrice) // Leave some buffer
					if shares <= 0 {
						continue
					}
					cost = float64(shares)*entryPrice + pb.calcCommission(shares, entryPrice)
				}

				// Open position
				positions[sig.Symbol] = &PortfolioPosition{
					Symbol:     sig.Symbol,
					EntryDate:  date,
					EntryPrice: entryPrice,
					StopLoss:   stopLoss,
					Target:     entryPrice + riskPerShare*pb.config.TargetRMultiple,
					Shares:     shares,
					DaysHeld:   0,
				}

				cash -= cost
			}
		} else if len(positions) == pb.config.MaxPositions {
			result.MaxPositionsHit++
		}

		// 3. Record daily snapshot
		posValue := pb.calcPositionValue(positions, allData, date)
		equity := cash + posValue

		var prevEquity float64
		if len(result.DailySnapshots) > 0 {
			prevEquity = result.DailySnapshots[len(result.DailySnapshots)-1].Equity
		} else {
			prevEquity = pb.config.InitialCapital
		}

		dayPnL := equity - prevEquity
		dayReturn := 0.0
		if prevEquity > 0 {
			dayReturn = dayPnL / prevEquity * 100
		}

		result.DailySnapshots = append(result.DailySnapshots, DailySnapshot{
			Date:          date,
			Equity:        equity,
			Cash:          cash,
			PositionValue: posValue,
			Positions:     len(positions),
			DayPnL:        dayPnL,
			DayReturn:     dayReturn,
		})

		// Track drawdown
		if equity > peakEquity {
			peakEquity = equity
		}
	}

	// Close any remaining positions at last day's close
	lastDate := dates[len(dates)-1]
	for sym, pos := range positions {
		candles := allData[sym]
		dayCandle := pb.findCandle(candles, lastDate)
		if dayCandle == nil {
			continue
		}
		exitPrice := dayCandle.Close * (1 - pb.config.Slippage)
		trade := pb.closeTrade(pos, lastDate, exitPrice, "end")
		result.Trades = append(result.Trades, trade)
		cash += float64(pos.Shares)*exitPrice - pb.calcCommission(pos.Shares, exitPrice)
	}

	// Calculate final statistics
	result.Period = dates[0].Format("2006-01-02") + " ~ " + dates[len(dates)-1].Format("2006-01-02")
	result.FinalEquity = cash
	result.TotalReturn = cash - pb.config.InitialCapital
	result.TotalReturnPct = result.TotalReturn / pb.config.InitialCapital * 100
	result.TradingDays = len(dates)

	// CAGR
	years := float64(len(dates)) / 252.0
	if years > 0 && result.FinalEquity > 0 {
		result.CAGR = (math.Pow(result.FinalEquity/pb.config.InitialCapital, 1/years) - 1) * 100
	}

	pb.calculateTradeStats(result)
	pb.calculateRiskMetrics(result)

	return result, nil
}

// scanForSignals finds pullback signals for a given date
func (pb *PortfolioBacktester) scanForSignals(allData map[string][]model.Candle, date time.Time, dayIdx int) []struct {
	Symbol     string
	EntryPrice float64
	Score      float64
} {
	var signals []struct {
		Symbol     string
		EntryPrice float64
		Score      float64
	}

	for sym, candles := range allData {
		// Find index for this date
		idx := -1
		for i, c := range candles {
			if c.Time.Year() == date.Year() && c.Time.YearDay() == date.YearDay() {
				idx = i
				break
			}
		}

		if idx < 50 { // Need enough history
			continue
		}

		// Check pullback signal using data up to this day
		historyCandles := candles[:idx+1]
		if pb.checkPullbackSignal(historyCandles) {
			// Entry next day at open (we'll use close as proxy)
			signals = append(signals, struct {
				Symbol     string
				EntryPrice float64
				Score      float64
			}{
				Symbol:     sym,
				EntryPrice: candles[idx].Close,
				Score:      1.0,
			})
		}
	}

	// Sort by score (for now all equal, but can be enhanced)
	sort.Slice(signals, func(i, j int) bool {
		return signals[i].Score > signals[j].Score
	})

	return signals
}

// checkPullbackSignal checks if pullback conditions are met
func (pb *PortfolioBacktester) checkPullbackSignal(candles []model.Candle) bool {
	if len(candles) < 50 {
		return false
	}

	// Calculate MA20 and MA50
	ma20 := calcMA(candles, 20)
	ma50 := calcMA(candles, 50)

	latest := candles[len(candles)-1]

	// Condition 1: Price above MA50 (uptrend)
	if latest.Close <= ma50 {
		return false
	}

	// Condition 2: Low touched MA20 (pullback)
	tolerance := ma20 * 0.02
	if latest.Low > ma20+tolerance || latest.Low < ma20-tolerance*2 {
		return false
	}

	// Condition 3: Bullish candle or long lower shadow
	bodySize := math.Abs(latest.Close - latest.Open)
	lowerShadow := math.Min(latest.Open, latest.Close) - latest.Low

	bullish := latest.Close > latest.Open
	longShadow := lowerShadow > bodySize*1.5

	return bullish || longShadow
}

func calcMA(candles []model.Candle, period int) float64 {
	if len(candles) < period {
		return 0
	}
	var sum float64
	for i := len(candles) - period; i < len(candles); i++ {
		sum += candles[i].Close
	}
	return sum / float64(period)
}

// Helper functions
func (pb *PortfolioBacktester) findCommonDates(allData map[string][]model.Candle, maxDays int) []time.Time {
	// Get all unique dates
	dateSet := make(map[string]int)
	for _, candles := range allData {
		for _, c := range candles {
			key := c.Time.Format("2006-01-02")
			dateSet[key]++
		}
	}

	// Filter to dates with sufficient coverage
	minCoverage := len(allData) / 2
	var dates []time.Time
	for dateStr, count := range dateSet {
		if count >= minCoverage {
			t, _ := time.Parse("2006-01-02", dateStr)
			dates = append(dates, t)
		}
	}

	sort.Slice(dates, func(i, j int) bool {
		return dates[i].Before(dates[j])
	})

	// Return most recent N days
	if len(dates) > maxDays {
		dates = dates[len(dates)-maxDays:]
	}

	return dates
}

func (pb *PortfolioBacktester) findCandle(candles []model.Candle, date time.Time) *model.Candle {
	for i := range candles {
		if candles[i].Time.Year() == date.Year() && candles[i].Time.YearDay() == date.YearDay() {
			return &candles[i]
		}
	}
	return nil
}

func (pb *PortfolioBacktester) calcCommission(shares int, price float64) float64 {
	return float64(shares) * price * pb.config.Commission
}

func (pb *PortfolioBacktester) calcPositionValue(positions map[string]*PortfolioPosition, allData map[string][]model.Candle, date time.Time) float64 {
	var total float64
	for sym, pos := range positions {
		candles := allData[sym]
		dayCandle := pb.findCandle(candles, date)
		if dayCandle != nil {
			total += float64(pos.Shares) * dayCandle.Close
		}
	}
	return total
}

func (pb *PortfolioBacktester) closeTrade(pos *PortfolioPosition, exitDate time.Time, exitPrice float64, reason string) Trade {
	riskPerShare := pos.EntryPrice - pos.StopLoss
	pnl := float64(pos.Shares) * (exitPrice - pos.EntryPrice)

	return Trade{
		Symbol:     pos.Symbol,
		EntryDate:  pos.EntryDate,
		ExitDate:   exitDate,
		EntryPrice: pos.EntryPrice,
		ExitPrice:  exitPrice,
		StopLoss:   pos.StopLoss,
		Target:     pos.Target,
		Shares:     pos.Shares,
		PnL:        pnl,
		PnLPct:     pnl / (float64(pos.Shares) * pos.EntryPrice) * 100,
		RMultiple:  (exitPrice - pos.EntryPrice) / riskPerShare,
		IsWin:      pnl > 0,
		ExitReason: reason,
	}
}

func (pb *PortfolioBacktester) calculateTradeStats(result *PortfolioBacktestResult) {
	if len(result.Trades) == 0 {
		return
	}

	result.TotalTrades = len(result.Trades)

	var totalWin, totalLoss float64
	var winPcts, lossPcts []float64

	for _, t := range result.Trades {
		if t.IsWin {
			result.WinningTrades++
			totalWin += t.PnL
			winPcts = append(winPcts, t.PnLPct)
			if t.PnL > result.LargestWin {
				result.LargestWin = t.PnL
			}
		} else {
			result.LosingTrades++
			totalLoss += math.Abs(t.PnL)
			lossPcts = append(lossPcts, t.PnLPct)
			if t.PnL < result.LargestLoss {
				result.LargestLoss = t.PnL
			}
		}
	}

	result.WinRate = float64(result.WinningTrades) / float64(result.TotalTrades) * 100

	if result.WinningTrades > 0 {
		result.AvgWin = totalWin / float64(result.WinningTrades)
		result.AvgWinPct = avg(winPcts)
	}
	if result.LosingTrades > 0 {
		result.AvgLoss = totalLoss / float64(result.LosingTrades)
		result.AvgLossPct = avg(lossPcts)
	}

	if result.AvgLoss > 0 {
		result.RiskRewardRatio = result.AvgWin / result.AvgLoss
	}
	if totalLoss > 0 {
		result.ProfitFactor = totalWin / totalLoss
	}

	result.Expectancy = (result.WinRate/100)*result.AvgWin - ((100-result.WinRate)/100)*result.AvgLoss

	var totalR float64
	for _, t := range result.Trades {
		totalR += t.RMultiple
	}
	result.ExpectancyR = totalR / float64(result.TotalTrades)

	// Kelly
	if result.AvgLoss > 0 {
		w := result.WinRate / 100
		b := result.AvgWin / result.AvgLoss
		result.KellyOptimal = (w*b - (1 - w)) / b
		result.KellyOptimal = math.Max(0, result.KellyOptimal)
		result.KellyHalf = result.KellyOptimal / 2
	}

	// Avg positions
	var totalPos float64
	for _, snap := range result.DailySnapshots {
		totalPos += float64(snap.Positions)
	}
	if len(result.DailySnapshots) > 0 {
		result.AvgPositions = totalPos / float64(len(result.DailySnapshots))
	}
}

func (pb *PortfolioBacktester) calculateRiskMetrics(result *PortfolioBacktestResult) {
	if len(result.DailySnapshots) < 2 {
		return
	}

	// Max Drawdown
	peak := result.DailySnapshots[0].Equity
	var maxDD float64
	var ddStart, ddEnd int

	for i, snap := range result.DailySnapshots {
		if snap.Equity > peak {
			peak = snap.Equity
			ddStart = i
		}
		dd := (peak - snap.Equity) / peak * 100
		if dd > maxDD {
			maxDD = dd
			ddEnd = i
		}
	}
	result.MaxDrawdown = maxDD
	result.MaxDrawdownDays = ddEnd - ddStart

	// Sharpe & Sortino
	returns := make([]float64, 0, len(result.DailySnapshots))
	var negReturns []float64

	for _, snap := range result.DailySnapshots {
		returns = append(returns, snap.DayReturn)
		if snap.DayReturn < 0 {
			negReturns = append(negReturns, snap.DayReturn)
		}
	}

	avgReturn := avg(returns)
	stdReturn := stddev(returns)
	if stdReturn > 0 {
		result.SharpeRatio = (avgReturn / stdReturn) * math.Sqrt(252)
	}

	if len(negReturns) > 0 {
		downDev := stddev(negReturns)
		if downDev > 0 {
			result.SortinoRatio = (avgReturn / downDev) * math.Sqrt(252)
		}
	}
}

func avg(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func stddev(vals []float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	mean := avg(vals)
	var sum float64
	for _, v := range vals {
		sum += (v - mean) * (v - mean)
	}
	return math.Sqrt(sum / float64(len(vals)-1))
}
