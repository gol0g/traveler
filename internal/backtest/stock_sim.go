package backtest

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"traveler/internal/provider"
	"traveler/internal/strategy"
	"traveler/internal/trader"
	"traveler/pkg/model"
)

// ──────────────────────────────────────────────
// BacktestProvider — provider.Provider 구현
// 날짜별로 캔들을 필터링하여 전략이 백테스트인지 모르게 함
// ──────────────────────────────────────────────

// BacktestProvider implements provider.Provider with date-filtered candle data.
// Strategies call GetDailyCandles() and receive only candles up to currentDate.
type BacktestProvider struct {
	allCandles  map[string][]model.Candle // symbol → all candles sorted ascending
	currentDate time.Time
}

// NewBacktestProvider creates a provider from pre-loaded candle data
func NewBacktestProvider(candles map[string][]model.Candle) *BacktestProvider {
	return &BacktestProvider{
		allCandles:  candles,
		currentDate: time.Now(),
	}
}

// SetDate sets the simulation "today" — all candle queries return data up to this date
func (p *BacktestProvider) SetDate(d time.Time) {
	p.currentDate = d
}

func (p *BacktestProvider) Name() string { return "backtest" }
func (p *BacktestProvider) IsAvailable() bool { return true }
func (p *BacktestProvider) RateLimit() int { return 9999 }

func (p *BacktestProvider) GetDailyCandles(_ context.Context, symbol string, days int) ([]model.Candle, error) {
	candles, ok := p.allCandles[symbol]
	if !ok || len(candles) == 0 {
		return nil, fmt.Errorf("no data for %s", symbol)
	}

	// Filter candles on or before currentDate
	endIdx := sort.Search(len(candles), func(i int) bool {
		return candles[i].Time.After(p.currentDate)
	})

	if endIdx == 0 {
		return nil, fmt.Errorf("no candles for %s before %s", symbol, p.currentDate.Format("2006-01-02"))
	}

	filtered := candles[:endIdx]
	if len(filtered) > days {
		filtered = filtered[len(filtered)-days:]
	}

	// Return a copy to avoid mutation
	result := make([]model.Candle, len(filtered))
	copy(result, filtered)
	return result, nil
}

func (p *BacktestProvider) GetIntradayData(_ context.Context, _ string, _ time.Time, _ int) (*model.IntradayData, error) {
	return nil, fmt.Errorf("intraday not supported in backtest")
}

func (p *BacktestProvider) GetMultiDayIntraday(_ context.Context, _ string, _ int, _ int) ([]model.IntradayData, error) {
	return nil, fmt.Errorf("intraday not supported in backtest")
}

func (p *BacktestProvider) GetSymbols(_ context.Context, _ string) ([]model.Stock, error) {
	return nil, fmt.Errorf("not supported in backtest")
}

// ──────────────────────────────────────────────
// StockSimulator — 일별 시뮬레이션 엔진
// ──────────────────────────────────────────────

// StockSimConfig holds backtest configuration
type StockSimConfig struct {
	Market         string  // "us" or "kr"
	Days           int     // backtest period in trading days
	InitialCapital float64
	MaxPositions   int
	Commission     float64 // round-trip (e.g., 0.005 = 0.5%)
	Verbose        bool
}

// DefaultStockSimConfig returns default config
func DefaultStockSimConfig(market string) StockSimConfig {
	cfg := StockSimConfig{
		Market:       market,
		Days:         120,
		MaxPositions: 5,
		Commission:   0.005, // 0.5% round-trip
		Verbose:      false,
	}
	if market == "kr" {
		cfg.InitialCapital = 5000000 // ₩500만
		cfg.Commission = 0.005       // 세금+수수료
	} else {
		cfg.InitialCapital = 5000 // $5,000
	}
	return cfg
}

// StockTrade records a completed trade
type StockTrade struct {
	Symbol     string
	Strategy   string
	EntryDate  time.Time
	ExitDate   time.Time
	EntryPrice float64
	ExitPrice  float64
	Quantity   float64
	StopLoss   float64
	Target1    float64
	Target2    float64
	PnL        float64
	PnLPct     float64
	Commission float64
	IsWin      bool
	ExitReason string // "stop", "target1", "target2", "timeout"
	Regime     string // "bull", "sideways", "bear"
	HoldDays   int
}

type activePosition struct {
	symbol     string
	strategy   string
	entryDate  time.Time
	entryPrice float64
	quantity   float64
	origQty    float64
	stopLoss   float64
	target1    float64
	target2    float64
	t1Hit      bool
	maxHold    int
	regime     string

	// Trailing stop (activated after T1 hit)
	useTrailing        bool
	trailingATR        float64
	trailingMultiplier float64
	highestSinceT1     float64
}

// regimeResetter is implemented by strategies with regime cache
type regimeResetter interface {
	ResetRegimeCache()
}

// StockSimulator runs a day-by-day backtest simulation replicating daemon behavior
type StockSimulator struct {
	config     StockSimConfig
	provider   *BacktestProvider
	strategies []strategy.Strategy
	sizerCfg   trader.SizerConfig
	symbols    []string

	// State
	capital   float64
	positions map[string]*activePosition
	trades    []StockTrade
	equity    []float64
	dailyDates []time.Time
}

// NewStockSimulator creates a new simulator
func NewStockSimulator(cfg StockSimConfig, prov *BacktestProvider, strats []strategy.Strategy, sizerCfg trader.SizerConfig, syms []string) *StockSimulator {
	return &StockSimulator{
		config:     cfg,
		provider:   prov,
		strategies: strats,
		sizerCfg:   sizerCfg,
		symbols:    syms,
		capital:    cfg.InitialCapital,
		positions:  make(map[string]*activePosition),
	}
}

// Run executes the backtest simulation and returns results
func (s *StockSimulator) Run(ctx context.Context) *StockBacktestResult {
	// Determine trading dates from benchmark candles
	tradingDates := s.getTradingDates()
	if len(tradingDates) == 0 {
		log.Println("[BACKTEST] No trading dates found")
		return &StockBacktestResult{Config: s.config}
	}

	// Use only the last N days as the backtest window
	if len(tradingDates) > s.config.Days {
		tradingDates = tradingDates[len(tradingDates)-s.config.Days:]
	}

	log.Printf("[BACKTEST] Simulation: %d trading days (%s ~ %s), %d symbols",
		len(tradingDates),
		tradingDates[0].Format("2006-01-02"),
		tradingDates[len(tradingDates)-1].Format("2006-01-02"),
		len(s.symbols))

	for i, date := range tradingDates {
		s.provider.SetDate(date)
		s.dailyDates = append(s.dailyDates, date)

		// 1. Reset strategy regime caches (includes StockMetaStrategy's internal RegimeDetector)
		for _, strat := range s.strategies {
			if rr, ok := strat.(regimeResetter); ok {
				rr.ResetRegimeCache()
			}
		}

		// 2. Check exits for open positions
		s.checkExits(date)

		// 3. Scan for new entries if room available
		if len(s.positions) < s.config.MaxPositions {
			s.scanAndEnter(ctx, date)
		}

		// 4. Record equity
		equity := s.calculateEquity(date)
		s.equity = append(s.equity, equity)

		// Progress logging
		if (i+1)%20 == 0 || i == len(tradingDates)-1 {
			log.Printf("[BACKTEST] Day %d/%d (%s): capital=$%.2f, positions=%d, trades=%d",
				i+1, len(tradingDates), date.Format("2006-01-02"),
				s.capital, len(s.positions), len(s.trades))
		}
	}

	// Force close remaining positions at last day's close
	lastDate := tradingDates[len(tradingDates)-1]
	s.forceCloseAll(lastDate, "backtest_end")

	return s.buildResult(tradingDates)
}

// getTradingDates extracts unique dates from benchmark symbol candles
func (s *StockSimulator) getTradingDates() []time.Time {
	benchmarkSym := "SPY"
	if s.config.Market == "kr" {
		benchmarkSym = "069500"
	}

	candles, ok := s.provider.allCandles[benchmarkSym]
	if !ok || len(candles) == 0 {
		// Fallback: use first available symbol
		for _, c := range s.provider.allCandles {
			candles = c
			break
		}
	}

	dates := make([]time.Time, 0, len(candles))
	for _, c := range candles {
		dates = append(dates, c.Time)
	}
	return dates
}

// checkExits checks all open positions for stop/target/timeout exits
func (s *StockSimulator) checkExits(date time.Time) {
	for sym, pos := range s.positions {
		candle := s.getCandle(sym, date)
		if candle == nil {
			continue
		}

		// Count hold days (trading days)
		holdDays := s.countTradingDays(pos.entryDate, date)

		// Priority: stop loss > target > timeout (conservative: stop first)

		// Stop loss check (includes trailing stop from previous day's ratchet)
		if candle.Low <= pos.stopLoss {
			reason := "stop"
			if pos.t1Hit && pos.useTrailing && pos.stopLoss > pos.entryPrice {
				reason = "trailing_stop"
			}
			s.closePosition(pos, pos.stopLoss, date, reason, holdDays)
			delete(s.positions, sym)
			continue
		}

		// Target1 partial exit (if not yet hit)
		if !pos.t1Hit && candle.High >= pos.target1 {
			if pos.quantity > 1 {
				// Partial sell: 50%
				sellQty := math.Floor(pos.quantity / 2)
				if sellQty < 1 {
					sellQty = 1
				}
				s.recordPartialSell(pos, pos.target1, date, sellQty, "target1", holdDays)
				pos.quantity -= sellQty
				pos.t1Hit = true
				pos.stopLoss = pos.entryPrice // move stop to breakeven
				pos.highestSinceT1 = candle.High // initialize tracking
			} else {
				// Only 1 share: full exit at T1
				s.closePosition(pos, pos.target1, date, "target1", holdDays)
				delete(s.positions, sym)
				continue
			}
		}

		// Post-T1: T2 profit taking + trailing stop update
		if pos.t1Hit {
			// T2 check always applies (fixed profit target)
			if candle.High >= pos.target2 {
				s.closePosition(pos, pos.target2, date, "target2", holdDays)
				delete(s.positions, sym)
				continue
			}

			// Trailing stop: update for NEXT day's stop check (not same-candle)
			if pos.useTrailing {
				if candle.High > pos.highestSinceT1 {
					pos.highestSinceT1 = candle.High
				}
				trailingStop := pos.highestSinceT1 - pos.trailingATR*pos.trailingMultiplier
				if trailingStop < pos.entryPrice {
					trailingStop = pos.entryPrice // never below breakeven
				}
				if trailingStop > pos.stopLoss {
					pos.stopLoss = trailingStop // ratchet up only
				}
			}
		}

		// Time stop
		if holdDays >= pos.maxHold {
			s.closePosition(pos, candle.Close, date, "timeout", holdDays)
			delete(s.positions, sym)
			continue
		}
	}
}

// scanAndEnter scans universe and enters new positions.
// Regime detection and strategy selection are handled internally by StockMetaStrategy.
func (s *StockSimulator) scanAndEnter(ctx context.Context, date time.Time) {
	var signals []strategy.Signal

	for _, sym := range s.symbols {
		// Skip if already have position
		if _, has := s.positions[sym]; has {
			continue
		}

		stock := model.Stock{Symbol: sym, Name: sym}

		// Run all strategies (or meta strategy), keep strongest
		var best *strategy.Signal
		for _, strat := range s.strategies {
			sig, err := strat.Analyze(ctx, stock)
			if err == nil && sig != nil {
				if best == nil || sig.Strength > best.Strength {
					best = sig
				}
			}
		}

		if best != nil {
			signals = append(signals, *best)
		}
	}

	if len(signals) == 0 {
		return
	}

	// Sort by probability descending
	sort.Slice(signals, func(i, j int) bool {
		return signals[i].Probability > signals[j].Probability
	})

	// Position sizing with current capital
	sizerCfg := s.sizerCfg
	sizerCfg.TotalCapital = s.capital
	sizer := trader.NewPositionSizer(sizerCfg)
	sized := sizer.ApplyToSignals(signals)

	// Enter positions (up to available slots)
	for _, sig := range sized {
		if len(s.positions) >= s.config.MaxPositions {
			break
		}
		if sig.Guide == nil {
			continue
		}

		// Check if we can afford this
		investAmount := sig.Guide.PositionSize * sig.Guide.EntryPrice
		commission := investAmount * s.config.Commission
		if investAmount+commission > s.capital {
			continue
		}

		maxHold := trader.GetMaxHoldDays(sig.Strategy)
		// Check for meta strategy max hold override
		if sig.Details != nil {
			if override, ok := sig.Details["max_hold_override"]; ok && override > 0 {
				maxHold = int(override)
			}
		}

		// Extract regime from signal details (injected by meta strategy)
		regimeStr := "unknown"
		if sig.Details != nil {
			switch sig.Details["regime"] {
			case 1:
				regimeStr = "bull"
			case 0:
				regimeStr = "sideways"
			case -1:
				regimeStr = "bear"
			}
		}

		pos := &activePosition{
			symbol:     sig.Stock.Symbol,
			strategy:   sig.Strategy,
			entryDate:  date,
			entryPrice: sig.Guide.EntryPrice,
			quantity:   sig.Guide.PositionSize,
			origQty:    sig.Guide.PositionSize,
			stopLoss:   sig.Guide.StopLoss,
			target1:    sig.Guide.Target1,
			target2:    sig.Guide.Target2,
			maxHold:    maxHold,
			regime:     regimeStr,

			// Trailing stop from strategy guide
			useTrailing:        sig.Guide.UseTrailingStop,
			trailingATR:        sig.Guide.EntryATR,
			trailingMultiplier: sig.Guide.TrailingMultiplier,
		}

		s.positions[sig.Stock.Symbol] = pos
		s.capital -= investAmount + commission

		if s.config.Verbose {
			log.Printf("  [BUY] %s %s @ $%.2f × %.0f  [%s/%s] stop=$%.2f T1=$%.2f T2=$%.2f",
				date.Format("2006-01-02"), sig.Stock.Symbol,
				sig.Guide.EntryPrice, sig.Guide.PositionSize,
				sig.Strategy, regimeStr,
				sig.Guide.StopLoss, sig.Guide.Target1, sig.Guide.Target2)
		}
	}
}

// closePosition closes a full position and records the trade
func (s *StockSimulator) closePosition(pos *activePosition, exitPrice float64, date time.Time, reason string, holdDays int) {
	commission := pos.quantity * exitPrice * s.config.Commission
	pnl := pos.quantity*(exitPrice-pos.entryPrice) - commission
	pnlPct := (exitPrice - pos.entryPrice) / pos.entryPrice * 100

	trade := StockTrade{
		Symbol:     pos.symbol,
		Strategy:   pos.strategy,
		EntryDate:  pos.entryDate,
		ExitDate:   date,
		EntryPrice: pos.entryPrice,
		ExitPrice:  exitPrice,
		Quantity:   pos.quantity,
		StopLoss:   pos.stopLoss,
		Target1:    pos.target1,
		Target2:    pos.target2,
		PnL:        pnl,
		PnLPct:     pnlPct,
		Commission: commission,
		IsWin:      pnl > 0,
		ExitReason: reason,
		Regime:     pos.regime,
		HoldDays:   holdDays,
	}

	s.trades = append(s.trades, trade)
	s.capital += pos.quantity*exitPrice - commission

	if s.config.Verbose {
		sign := "+"
		if pnl < 0 {
			sign = ""
		}
		log.Printf("  [SELL] %s %s @ $%.2f (%s%.1f%%) %s hold:%dd",
			date.Format("2006-01-02"), pos.symbol,
			exitPrice, sign, pnlPct, reason, holdDays)
	}
}

// recordPartialSell records a T1 partial sell
func (s *StockSimulator) recordPartialSell(pos *activePosition, exitPrice float64, date time.Time, qty float64, reason string, holdDays int) {
	commission := qty * exitPrice * s.config.Commission
	pnl := qty*(exitPrice-pos.entryPrice) - commission
	pnlPct := (exitPrice - pos.entryPrice) / pos.entryPrice * 100

	trade := StockTrade{
		Symbol:     pos.symbol,
		Strategy:   pos.strategy,
		EntryDate:  pos.entryDate,
		ExitDate:   date,
		EntryPrice: pos.entryPrice,
		ExitPrice:  exitPrice,
		Quantity:   qty,
		StopLoss:   pos.stopLoss,
		Target1:    pos.target1,
		Target2:    pos.target2,
		PnL:        pnl,
		PnLPct:     pnlPct,
		Commission: commission,
		IsWin:      pnl > 0,
		ExitReason: reason,
		Regime:     pos.regime,
		HoldDays:   holdDays,
	}

	s.trades = append(s.trades, trade)
	s.capital += qty*exitPrice - commission

	if s.config.Verbose {
		log.Printf("  [PARTIAL] %s %s %.0f shares @ $%.2f (+%.1f%%) %s",
			date.Format("2006-01-02"), pos.symbol, qty, exitPrice, pnlPct, reason)
	}
}

// forceCloseAll closes all remaining positions
func (s *StockSimulator) forceCloseAll(date time.Time, reason string) {
	for sym, pos := range s.positions {
		candle := s.getCandle(sym, date)
		exitPrice := pos.entryPrice // fallback
		if candle != nil {
			exitPrice = candle.Close
		}
		holdDays := s.countTradingDays(pos.entryDate, date)
		s.closePosition(pos, exitPrice, date, reason, holdDays)
	}
	s.positions = make(map[string]*activePosition)
}

// getCandle returns the candle for a symbol on a specific date
func (s *StockSimulator) getCandle(symbol string, date time.Time) *model.Candle {
	candles, ok := s.provider.allCandles[symbol]
	if !ok {
		return nil
	}

	dateStr := date.Format("2006-01-02")
	for i := range candles {
		if candles[i].Time.Format("2006-01-02") == dateStr {
			return &candles[i]
		}
	}
	return nil
}

// countTradingDays counts weekday days between two dates
func (s *StockSimulator) countTradingDays(from, to time.Time) int {
	fromDate := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())
	toDate := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, to.Location())

	if !toDate.After(fromDate) {
		return 0
	}

	days := 0
	current := fromDate
	for current.Before(toDate) {
		current = current.AddDate(0, 0, 1)
		wd := current.Weekday()
		if wd != time.Saturday && wd != time.Sunday {
			days++
		}
	}
	return days
}

// calculateEquity returns total equity (cash + positions marked to market)
func (s *StockSimulator) calculateEquity(date time.Time) float64 {
	equity := s.capital
	for sym, pos := range s.positions {
		candle := s.getCandle(sym, date)
		if candle != nil {
			equity += pos.quantity * candle.Close
		} else {
			equity += pos.quantity * pos.entryPrice
		}
	}
	return equity
}

// buildResult compiles backtest results
func (s *StockSimulator) buildResult(tradingDates []time.Time) *StockBacktestResult {
	result := &StockBacktestResult{
		Config:         s.config,
		InitialCapital: s.config.InitialCapital,
		FinalCapital:   s.capital,
		Trades:         s.trades,
		EquityCurve:    s.equity,
		ExitReasons:    make(map[string]int),
	}

	if len(tradingDates) >= 2 {
		result.Period = fmt.Sprintf("%s ~ %s",
			tradingDates[0].Format("2006-01-02"),
			tradingDates[len(tradingDates)-1].Format("2006-01-02"))
	}

	result.TotalTrades = len(s.trades)
	if result.TotalTrades == 0 {
		return result
	}

	// Win/loss stats
	var totalWinPnL, totalLossPnL float64
	var totalHoldDays int
	var maxWinStreak, maxLoseStreak, curWinStreak, curLoseStreak int

	stratStats := make(map[string]*StrategyStats)
	regimeStats := make(map[string]*RegimeStats)

	for _, t := range s.trades {
		result.ExitReasons[t.ExitReason]++
		totalHoldDays += t.HoldDays

		if t.IsWin {
			result.WinningTrades++
			totalWinPnL += t.PnL
			curWinStreak++
			curLoseStreak = 0
			if curWinStreak > maxWinStreak {
				maxWinStreak = curWinStreak
			}
		} else {
			result.LosingTrades++
			totalLossPnL += math.Abs(t.PnL)
			curLoseStreak++
			curWinStreak = 0
			if curLoseStreak > maxLoseStreak {
				maxLoseStreak = curLoseStreak
			}
		}

		// Strategy breakdown
		ss, ok := stratStats[t.Strategy]
		if !ok {
			ss = &StrategyStats{Strategy: t.Strategy}
			stratStats[t.Strategy] = ss
		}
		ss.Trades++
		ss.TotalPnL += t.PnL
		if t.IsWin {
			ss.Wins++
			ss.WinPnL += t.PnL
		} else {
			ss.LossPnL += math.Abs(t.PnL)
		}

		// Regime breakdown
		rs, ok := regimeStats[t.Regime]
		if !ok {
			rs = &RegimeStats{Regime: t.Regime}
			regimeStats[t.Regime] = rs
		}
		rs.Trades++
		rs.TotalPnL += t.PnL
		if t.IsWin {
			rs.Wins++
			rs.WinPnL += t.PnL
		} else {
			rs.LossPnL += math.Abs(t.PnL)
		}
	}

	result.WinRate = float64(result.WinningTrades) / float64(result.TotalTrades) * 100
	result.TotalReturn = s.capital - s.config.InitialCapital
	result.TotalReturnPct = result.TotalReturn / s.config.InitialCapital * 100
	result.AvgHoldDays = float64(totalHoldDays) / float64(result.TotalTrades)
	result.MaxWinStreak = maxWinStreak
	result.MaxLoseStreak = maxLoseStreak

	// Averages
	if result.WinningTrades > 0 {
		result.AvgWin = totalWinPnL / float64(result.WinningTrades)
	}
	if result.LosingTrades > 0 {
		result.AvgLoss = totalLossPnL / float64(result.LosingTrades)
	}

	// Profit Factor
	if totalLossPnL > 0 {
		result.ProfitFactor = totalWinPnL / totalLossPnL
	}

	// Expectancy
	if result.TotalTrades > 0 {
		result.Expectancy = result.TotalReturn / float64(result.TotalTrades)
	}
	if result.AvgLoss > 0 {
		result.ExpectancyR = (result.WinRate/100*result.AvgWin - (1-result.WinRate/100)*result.AvgLoss) / result.AvgLoss
	}

	// Max Drawdown
	result.MaxDrawdown = s.calculateMaxDrawdown()

	// Sharpe Ratio (annualized, assuming 252 trading days)
	result.SharpeRatio = s.calculateSharpe()

	// Advanced metrics
	result.SortinoRatio = s.calculateSortino()
	result.MDDDuration = s.calculateMDDDuration()
	result.TailRatio = s.calculateTailRatio()
	if result.MaxDrawdown > 0 {
		result.CalmarRatio = (result.TotalReturnPct / 100 * 252 / float64(len(s.equity))) / (result.MaxDrawdown / 100)
		result.RecoveryFactor = math.Abs(result.TotalReturnPct) / result.MaxDrawdown
		if result.TotalReturnPct < 0 {
			result.RecoveryFactor = -result.RecoveryFactor
		}
	}

	// Strategy breakdown
	for _, ss := range stratStats {
		if ss.Trades > 0 {
			ss.WinRate = float64(ss.Wins) / float64(ss.Trades) * 100
		}
		if ss.LossPnL > 0 {
			ss.ProfitFactor = ss.WinPnL / ss.LossPnL
		}
		result.StrategyBreakdown = append(result.StrategyBreakdown, *ss)
	}
	sort.Slice(result.StrategyBreakdown, func(i, j int) bool {
		return result.StrategyBreakdown[i].Trades > result.StrategyBreakdown[j].Trades
	})

	// Regime breakdown
	for _, rs := range regimeStats {
		if rs.Trades > 0 {
			rs.WinRate = float64(rs.Wins) / float64(rs.Trades) * 100
		}
		if rs.LossPnL > 0 {
			rs.ProfitFactor = rs.WinPnL / rs.LossPnL
		}
		result.RegimeBreakdown = append(result.RegimeBreakdown, *rs)
	}

	return result
}

func (s *StockSimulator) calculateMaxDrawdown() float64 {
	if len(s.equity) == 0 {
		return 0
	}
	peak := s.equity[0]
	maxDD := 0.0
	for _, eq := range s.equity {
		if eq > peak {
			peak = eq
		}
		dd := (peak - eq) / peak * 100
		if dd > maxDD {
			maxDD = dd
		}
	}
	return maxDD
}

func (s *StockSimulator) calculateSharpe() float64 {
	if len(s.equity) < 2 {
		return 0
	}

	// Daily returns
	returns := make([]float64, len(s.equity)-1)
	for i := 1; i < len(s.equity); i++ {
		returns[i-1] = (s.equity[i] - s.equity[i-1]) / s.equity[i-1]
	}

	// Mean and stddev
	var sum float64
	for _, r := range returns {
		sum += r
	}
	mean := sum / float64(len(returns))

	var variance float64
	for _, r := range returns {
		variance += (r - mean) * (r - mean)
	}
	stddev := math.Sqrt(variance / float64(len(returns)))

	if stddev == 0 {
		return 0
	}

	// Annualized Sharpe (risk-free rate = 0 for simplicity)
	return (mean / stddev) * math.Sqrt(252)
}

func (s *StockSimulator) calculateSortino() float64 {
	if len(s.equity) < 2 {
		return 0
	}

	returns := make([]float64, len(s.equity)-1)
	for i := 1; i < len(s.equity); i++ {
		returns[i-1] = (s.equity[i] - s.equity[i-1]) / s.equity[i-1]
	}

	var sum float64
	for _, r := range returns {
		sum += r
	}
	mean := sum / float64(len(returns))

	// Downside deviation: only negative returns
	var downsideSum float64
	for _, r := range returns {
		if r < 0 {
			downsideSum += r * r
		}
	}
	downsideDev := math.Sqrt(downsideSum / float64(len(returns)))

	if downsideDev == 0 {
		return 0
	}
	return (mean / downsideDev) * math.Sqrt(252)
}

func (s *StockSimulator) calculateMDDDuration() int {
	if len(s.equity) < 2 {
		return 0
	}

	peak := s.equity[0]
	maxDuration := 0
	currentDuration := 0

	for _, eq := range s.equity {
		if eq >= peak {
			peak = eq
			currentDuration = 0
		} else {
			currentDuration++
			if currentDuration > maxDuration {
				maxDuration = currentDuration
			}
		}
	}
	return maxDuration
}

func (s *StockSimulator) calculateTailRatio() float64 {
	if len(s.equity) < 20 {
		return 0
	}

	returns := make([]float64, len(s.equity)-1)
	for i := 1; i < len(s.equity); i++ {
		returns[i-1] = (s.equity[i] - s.equity[i-1]) / s.equity[i-1]
	}

	sorted := make([]float64, len(returns))
	copy(sorted, returns)
	sort.Float64s(sorted)

	n := len(sorted)
	idx5 := int(float64(n) * 0.05)
	idx95 := int(float64(n) * 0.95)
	if idx95 >= n {
		idx95 = n - 1
	}

	p5 := sorted[idx5]
	p95 := sorted[idx95]

	if p5 == 0 {
		return 0
	}
	return p95 / math.Abs(p5)
}

// ──────────────────────────────────────────────
// Data Loading — Yahoo Finance → disk cache
// ──────────────────────────────────────────────

// FetchStockData downloads daily candles for all symbols using Yahoo Finance
// and returns a map suitable for BacktestProvider.
// Results are cached to disk (1-day validity).
func FetchStockData(ctx context.Context, yahoo provider.Provider, symbols []string, days int, dataDir string, noCache bool) (map[string][]model.Candle, error) {
	allCandles := make(map[string][]model.Candle)

	cacheDir := dataDir + "/backtest_cache"
	today := time.Now().Format("2006-01-02")

	total := len(symbols)
	fetched := 0

	for i, sym := range symbols {
		// Try cache first
		if !noCache {
			cached, err := loadCachedCandles(cacheDir, sym, today)
			if err == nil && len(cached) > 0 {
				allCandles[sym] = cached
				if (i+1)%50 == 0 {
					log.Printf("[DATA] Loading: %d/%d (cached)", i+1, total)
				}
				continue
			}
		}

		// Fetch from Yahoo
		candles, err := yahoo.GetDailyCandles(ctx, sym, days)
		if err != nil {
			log.Printf("[DATA] Failed to fetch %s: %v", sym, err)
			continue
		}

		if len(candles) < 20 {
			log.Printf("[DATA] Skipping %s: only %d candles", sym, len(candles))
			continue
		}

		allCandles[sym] = candles
		fetched++

		// Cache to disk
		saveCachedCandles(cacheDir, sym, today, candles)

		if (i+1)%10 == 0 || i == total-1 {
			log.Printf("[DATA] Fetching: %d/%d (fetched %d from API)", i+1, total, fetched)
		}
	}

	log.Printf("[DATA] Loaded %d symbols (%d from API, %d from cache)",
		len(allCandles), fetched, len(allCandles)-fetched)

	return allCandles, nil
}
