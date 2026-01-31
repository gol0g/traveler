package backtest

import (
	"context"
	"math"
	"sort"
	"time"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// Trade represents a single completed trade
type Trade struct {
	Symbol     string    `json:"symbol"`
	EntryDate  time.Time `json:"entry_date"`
	ExitDate   time.Time `json:"exit_date"`
	EntryPrice float64   `json:"entry_price"`
	ExitPrice  float64   `json:"exit_price"`
	StopLoss   float64   `json:"stop_loss"`
	Target     float64   `json:"target"`
	Shares     int       `json:"shares"`
	PnL        float64   `json:"pnl"`        // Profit/Loss in dollars
	PnLPct     float64   `json:"pnl_pct"`    // Profit/Loss percentage
	RMultiple  float64   `json:"r_multiple"` // Return in R (risk units)
	IsWin      bool      `json:"is_win"`
	ExitReason string    `json:"exit_reason"` // "target", "stop", "timeout"
}

// BacktestResult contains the complete backtest results
type BacktestResult struct {
	// Summary
	Strategy        string        `json:"strategy"`
	Period          string        `json:"period"`
	TotalTrades     int           `json:"total_trades"`
	WinningTrades   int           `json:"winning_trades"`
	LosingTrades    int           `json:"losing_trades"`
	WinRate         float64       `json:"win_rate"`

	// Returns
	TotalReturn     float64       `json:"total_return"`
	TotalReturnPct  float64       `json:"total_return_pct"`
	AvgWin          float64       `json:"avg_win"`
	AvgLoss         float64       `json:"avg_loss"`
	AvgWinPct       float64       `json:"avg_win_pct"`
	AvgLossPct      float64       `json:"avg_loss_pct"`
	LargestWin      float64       `json:"largest_win"`
	LargestLoss     float64       `json:"largest_loss"`

	// Risk metrics
	RiskRewardRatio float64       `json:"risk_reward_ratio"`
	Expectancy      float64       `json:"expectancy"`     // Expected $ per trade
	ExpectancyR     float64       `json:"expectancy_r"`   // Expected R per trade
	ProfitFactor    float64       `json:"profit_factor"`  // Gross profit / Gross loss
	MaxDrawdown     float64       `json:"max_drawdown"`   // Maximum drawdown %
	MaxDrawdownDays int           `json:"max_drawdown_days"`
	SharpeRatio     float64       `json:"sharpe_ratio"`

	// Kelly
	KellyOptimal    float64       `json:"kelly_optimal"`
	KellyHalf       float64       `json:"kelly_half"`

	// Streaks
	MaxWinStreak    int           `json:"max_win_streak"`
	MaxLoseStreak   int           `json:"max_lose_streak"`
	CurrentStreak   int           `json:"current_streak"`

	// Individual trades
	Trades          []Trade       `json:"trades"`

	// Equity curve
	EquityCurve     []float64     `json:"equity_curve"`
}

// BacktestConfig holds backtest parameters
type BacktestConfig struct {
	InitialCapital  float64
	RiskPerTrade    float64   // e.g., 0.01 = 1%
	StopLossPct     float64   // e.g., 0.02 = 2%
	TargetRMultiple float64   // e.g., 2.0 = 2R target
	MaxHoldDays     int       // Maximum days to hold
	Commission      float64   // Per trade commission rate
	Slippage        float64   // Expected slippage
}

// DefaultBacktestConfig returns default configuration
func DefaultBacktestConfig() BacktestConfig {
	return BacktestConfig{
		InitialCapital:  10000000, // 1000만원
		RiskPerTrade:    0.01,     // 1%
		StopLossPct:     0.02,     // 2%
		TargetRMultiple: 2.0,      // 2R target
		MaxHoldDays:     5,        // 5 trading days max
		Commission:      0.00015,  // 0.015%
		Slippage:        0.001,    // 0.1%
	}
}

// Backtester runs backtests on historical data
type Backtester struct {
	config   BacktestConfig
	provider provider.Provider
}

// NewBacktester creates a new backtester
func NewBacktester(cfg BacktestConfig, p provider.Provider) *Backtester {
	return &Backtester{
		config:   cfg,
		provider: p,
	}
}

// RunPullbackBacktest backtests the pullback strategy
func (b *Backtester) RunPullbackBacktest(ctx context.Context, symbol string, days int) (*BacktestResult, error) {
	// Get historical daily data
	candles, err := b.provider.GetDailyCandles(ctx, symbol, days)
	if err != nil {
		return nil, err
	}

	if len(candles) < 60 {
		return nil, nil // Not enough data
	}

	result := &BacktestResult{
		Strategy: "pullback",
		Period:   candles[0].Time.Format("2006-01-02") + " ~ " + candles[len(candles)-1].Time.Format("2006-01-02"),
		Trades:   make([]Trade, 0),
	}

	capital := b.config.InitialCapital
	equity := []float64{capital}
	peakEquity := capital

	// Simulate trading
	for i := 60; i < len(candles)-b.config.MaxHoldDays; i++ {
		// Check for pullback signal
		signal := b.checkPullbackSignal(candles[:i+1])
		if !signal {
			equity = append(equity, capital)
			continue
		}

		// Entry next day at open
		entryCandle := candles[i+1]
		entryPrice := entryCandle.Open * (1 + b.config.Slippage) // Add slippage

		// Calculate position size
		riskAmount := capital * b.config.RiskPerTrade
		stopLoss := entryPrice * (1 - b.config.StopLossPct)
		riskPerShare := entryPrice - stopLoss
		shares := int(riskAmount / riskPerShare)

		if shares <= 0 {
			equity = append(equity, capital)
			continue
		}

		target := entryPrice + (riskPerShare * b.config.TargetRMultiple)

		// Simulate holding period
		trade := Trade{
			Symbol:     symbol,
			EntryDate:  entryCandle.Time,
			EntryPrice: entryPrice,
			StopLoss:   stopLoss,
			Target:     target,
			Shares:     shares,
		}

		// Check each day for exit
		for j := i + 1; j <= i+b.config.MaxHoldDays && j < len(candles); j++ {
			dayCandle := candles[j]

			// Check stop loss (using low)
			if dayCandle.Low <= stopLoss {
				trade.ExitDate = dayCandle.Time
				trade.ExitPrice = stopLoss * (1 - b.config.Slippage)
				trade.ExitReason = "stop"
				break
			}

			// Check target (using high)
			if dayCandle.High >= target {
				trade.ExitDate = dayCandle.Time
				trade.ExitPrice = target * (1 - b.config.Slippage)
				trade.ExitReason = "target"
				break
			}

			// Timeout - exit at close on last day
			if j == i+b.config.MaxHoldDays || j == len(candles)-1 {
				trade.ExitDate = dayCandle.Time
				trade.ExitPrice = dayCandle.Close * (1 - b.config.Slippage)
				trade.ExitReason = "timeout"
				break
			}
		}

		// Calculate P&L
		grossPnL := float64(trade.Shares) * (trade.ExitPrice - trade.EntryPrice)
		commission := float64(trade.Shares) * trade.EntryPrice * b.config.Commission * 2
		trade.PnL = grossPnL - commission
		trade.PnLPct = trade.PnL / (float64(trade.Shares) * trade.EntryPrice) * 100
		trade.RMultiple = (trade.ExitPrice - trade.EntryPrice) / riskPerShare
		trade.IsWin = trade.PnL > 0

		capital += trade.PnL
		equity = append(equity, capital)

		// Track drawdown
		if capital > peakEquity {
			peakEquity = capital
		}

		result.Trades = append(result.Trades, trade)

		// Skip ahead past this trade
		i = i + b.config.MaxHoldDays
	}

	// Calculate statistics
	b.calculateStats(result, equity, b.config.InitialCapital)

	return result, nil
}

// checkPullbackSignal checks for pullback entry signal
func (b *Backtester) checkPullbackSignal(candles []model.Candle) bool {
	if len(candles) < 50 {
		return false
	}

	// Calculate MA20 and MA50
	ma20 := calculateMA(candles, 20)
	ma50 := calculateMA(candles, 50)

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

	if !bullish && !longShadow {
		return false
	}

	// Condition 4: Volume check (simplified - below average)
	avgVol := calculateAvgVolume(candles, 20)
	if float64(latest.Volume) > avgVol*1.2 {
		return false // Too much volume = selling pressure
	}

	return true
}

func calculateMA(candles []model.Candle, period int) float64 {
	if len(candles) < period {
		return 0
	}
	var sum float64
	for i := len(candles) - period; i < len(candles); i++ {
		sum += candles[i].Close
	}
	return sum / float64(period)
}

func calculateAvgVolume(candles []model.Candle, period int) float64 {
	if len(candles) < period {
		return 0
	}
	var sum int64
	for i := len(candles) - period; i < len(candles); i++ {
		sum += candles[i].Volume
	}
	return float64(sum) / float64(period)
}

// calculateStats computes all statistics from trades
func (b *Backtester) calculateStats(result *BacktestResult, equity []float64, initialCapital float64) {
	if len(result.Trades) == 0 {
		return
	}

	result.TotalTrades = len(result.Trades)
	result.EquityCurve = equity

	var totalWin, totalLoss float64
	var winCount, lossCount int
	var winStreak, loseStreak, maxWinStreak, maxLoseStreak int
	var totalR float64

	for _, t := range result.Trades {
		totalR += t.RMultiple

		if t.IsWin {
			winCount++
			totalWin += t.PnL
			if t.PnL > result.LargestWin {
				result.LargestWin = t.PnL
			}

			winStreak++
			loseStreak = 0
			if winStreak > maxWinStreak {
				maxWinStreak = winStreak
			}
		} else {
			lossCount++
			totalLoss += math.Abs(t.PnL)
			if t.PnL < result.LargestLoss {
				result.LargestLoss = t.PnL
			}

			loseStreak++
			winStreak = 0
			if loseStreak > maxLoseStreak {
				maxLoseStreak = loseStreak
			}
		}
	}

	result.WinningTrades = winCount
	result.LosingTrades = lossCount
	result.WinRate = float64(winCount) / float64(result.TotalTrades) * 100
	result.MaxWinStreak = maxWinStreak
	result.MaxLoseStreak = maxLoseStreak

	// Returns
	result.TotalReturn = totalWin - totalLoss
	result.TotalReturnPct = result.TotalReturn / initialCapital * 100

	if winCount > 0 {
		result.AvgWin = totalWin / float64(winCount)
		result.AvgWinPct = result.AvgWin / initialCapital * 100
	}
	if lossCount > 0 {
		result.AvgLoss = totalLoss / float64(lossCount)
		result.AvgLossPct = result.AvgLoss / initialCapital * 100
	}

	// Risk/Reward
	if result.AvgLoss > 0 {
		result.RiskRewardRatio = result.AvgWin / result.AvgLoss
	}

	// Expectancy
	result.Expectancy = (result.WinRate/100*result.AvgWin) - ((100-result.WinRate)/100*result.AvgLoss)
	result.ExpectancyR = totalR / float64(result.TotalTrades)

	// Profit Factor
	if totalLoss > 0 {
		result.ProfitFactor = totalWin / totalLoss
	}

	// Max Drawdown
	peak := equity[0]
	var maxDD float64
	for _, e := range equity {
		if e > peak {
			peak = e
		}
		dd := (peak - e) / peak * 100
		if dd > maxDD {
			maxDD = dd
		}
	}
	result.MaxDrawdown = maxDD

	// Kelly Criterion
	if result.AvgLoss > 0 {
		winProb := result.WinRate / 100
		b := result.AvgWin / result.AvgLoss
		result.KellyOptimal = (winProb*b - (1 - winProb)) / b
		result.KellyOptimal = math.Max(0, result.KellyOptimal)
		result.KellyHalf = result.KellyOptimal / 2
	}

	// Sharpe Ratio (simplified, annualized)
	if len(result.Trades) > 1 {
		returns := make([]float64, len(result.Trades))
		for i, t := range result.Trades {
			returns[i] = t.PnLPct
		}
		avgReturn := average(returns)
		stdReturn := stdDev(returns)
		if stdReturn > 0 {
			result.SharpeRatio = (avgReturn / stdReturn) * math.Sqrt(252) // Annualized
		}
	}
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func stdDev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	avg := average(values)
	var sumSquares float64
	for _, v := range values {
		sumSquares += (v - avg) * (v - avg)
	}
	return math.Sqrt(sumSquares / float64(len(values)-1))
}

// MonteCarloResult contains Monte Carlo simulation results
type MonteCarloResult struct {
	Simulations     int       `json:"simulations"`
	MedianReturn    float64   `json:"median_return"`
	WorstCase       float64   `json:"worst_case"`      // 5th percentile
	BestCase        float64   `json:"best_case"`       // 95th percentile
	RuinProbability float64   `json:"ruin_probability"` // % of sims that went bust
	MaxDrawdowns    []float64 `json:"max_drawdowns"`
}

// RunMonteCarlo runs Monte Carlo simulation on trade results
func RunMonteCarlo(trades []Trade, initialCapital float64, simulations int) *MonteCarloResult {
	if len(trades) == 0 {
		return nil
	}

	result := &MonteCarloResult{
		Simulations: simulations,
	}

	// Extract R-multiples from trades
	rMultiples := make([]float64, len(trades))
	for i, t := range trades {
		rMultiples[i] = t.RMultiple
	}

	finalReturns := make([]float64, simulations)
	maxDDs := make([]float64, simulations)
	ruinCount := 0

	riskPerTrade := initialCapital * 0.01 // 1% risk

	for sim := 0; sim < simulations; sim++ {
		capital := initialCapital
		peak := capital

		// Shuffle and simulate
		shuffled := shuffleFloat64(rMultiples)

		for _, r := range shuffled {
			pnl := riskPerTrade * r
			capital += pnl

			if capital > peak {
				peak = capital
			}

			dd := (peak - capital) / peak * 100
			if dd > maxDDs[sim] {
				maxDDs[sim] = dd
			}

			if capital <= 0 {
				ruinCount++
				break
			}
		}

		finalReturns[sim] = (capital - initialCapital) / initialCapital * 100
	}

	sort.Float64s(finalReturns)
	sort.Float64s(maxDDs)

	result.MedianReturn = finalReturns[simulations/2]
	result.WorstCase = finalReturns[simulations/20]        // 5th percentile
	result.BestCase = finalReturns[simulations*19/20]      // 95th percentile
	result.RuinProbability = float64(ruinCount) / float64(simulations) * 100
	result.MaxDrawdowns = maxDDs

	return result
}

func shuffleFloat64(slice []float64) []float64 {
	result := make([]float64, len(slice))
	copy(result, slice)

	// Simple Fisher-Yates shuffle using time as seed
	seed := time.Now().UnixNano()
	for i := len(result) - 1; i > 0; i-- {
		seed = (seed*1103515245 + 12345) & 0x7fffffff
		j := int(seed) % (i + 1)
		result[i], result[j] = result[j], result[i]
	}

	return result
}
