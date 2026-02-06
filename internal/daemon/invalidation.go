package daemon

import (
	"log"

	"traveler/internal/strategy"
	"traveler/internal/trader"
)

// runInvalidationCheck checks all positions for strategy invalidation
// Called once daily at market open, uses previous day's daily candles
func (d *Daemon) runInvalidationCheck() {
	if d.autoTrader == nil {
		return
	}

	monitor := d.autoTrader.GetMonitor()
	positions := monitor.GetActivePositions()
	if len(positions) == 0 {
		return
	}

	planStore := d.autoTrader.GetPlanStore()
	if planStore == nil {
		return
	}

	log.Printf("[INVALIDATION] Checking %d positions for strategy invalidation...", len(positions))

	for _, pos := range positions {
		plan := planStore.Get(pos.Symbol)
		if plan == nil {
			continue
		}

		invalidated, reason := d.checkInvalidation(pos, plan)
		if invalidated {
			log.Printf("[INVALIDATION] %s (%s): %s - closing position",
				pos.Symbol, plan.Strategy, reason)
			monitor.ClosePosition(d.ctx, pos.Symbol, "invalidation: "+reason)
		}
	}

	log.Println("[INVALIDATION] Check complete.")
}

// checkInvalidation routes to strategy-specific invalidation logic
func (d *Daemon) checkInvalidation(pos *trader.ActivePosition, plan *trader.PositionPlan) (bool, string) {
	switch plan.Strategy {
	case "pullback":
		return d.checkPullbackInvalidation(pos, plan)
	case "breakout":
		return d.checkBreakoutInvalidation(pos, plan)
	case "mean-reversion":
		return d.checkMeanReversionInvalidation(pos, plan)
	default:
		return false, ""
	}
}

// checkPullbackInvalidation: close < MA20 for 2 consecutive days
func (d *Daemon) checkPullbackInvalidation(pos *trader.ActivePosition, plan *trader.PositionPlan) (bool, string) {
	candles, err := d.provider.GetDailyCandles(d.ctx, pos.Symbol, 30)
	if err != nil || len(candles) < 20 {
		return false, ""
	}

	ind := strategy.CalculateIndicators(candles)
	if ind.MA20 == 0 {
		return false, ""
	}

	lastClose := candles[len(candles)-1].Close

	if lastClose < ind.MA20 {
		newCount := plan.ConsecutiveDaysBelow + 1
		planStore := d.autoTrader.GetPlanStore()
		if planStore != nil {
			planStore.UpdateConsecutiveDaysBelow(pos.Symbol, newCount)
		}

		if newCount >= 2 {
			return true, "close below MA20 for 2 consecutive days"
		}

		log.Printf("[INVALIDATION] %s: close ($%.2f) < MA20 ($%.2f), day %d/2",
			pos.Symbol, lastClose, ind.MA20, newCount)
	} else {
		// Reset counter
		if plan.ConsecutiveDaysBelow > 0 {
			planStore := d.autoTrader.GetPlanStore()
			if planStore != nil {
				planStore.UpdateConsecutiveDaysBelow(pos.Symbol, 0)
			}
		}
	}

	return false, ""
}

// checkBreakoutInvalidation: close < breakout level (failed breakout)
func (d *Daemon) checkBreakoutInvalidation(pos *trader.ActivePosition, plan *trader.PositionPlan) (bool, string) {
	if plan.BreakoutLevel <= 0 {
		return false, ""
	}

	candles, err := d.provider.GetDailyCandles(d.ctx, pos.Symbol, 5)
	if err != nil || len(candles) == 0 {
		return false, ""
	}

	lastClose := candles[len(candles)-1].Close

	// Failed breakout: close back below the breakout level
	if lastClose < plan.BreakoutLevel {
		return true, "failed breakout - close below breakout level"
	}

	return false, ""
}

// checkMeanReversionInvalidation: RSI recovery failure + BB recovery failure
func (d *Daemon) checkMeanReversionInvalidation(pos *trader.ActivePosition, plan *trader.PositionPlan) (bool, string) {
	// Only check after at least 2 trading days (give it time to start recovering)
	tradingDays := trader.TradingDaysSince(plan.EntryTime)
	if tradingDays < 2 {
		return false, ""
	}

	candles, err := d.provider.GetDailyCandles(d.ctx, pos.Symbol, 30)
	if err != nil || len(candles) < 20 {
		return false, ""
	}

	ind := strategy.CalculateIndicators(candles)
	lastClose := candles[len(candles)-1].Close

	// Invalidation: RSI still deeply oversold AND price still below BB lower
	// After 2+ days, if neither RSI nor price has started recovering, thesis is failing
	rsiStillOversold := ind.RSI14 < 35
	belowBBLower := ind.BBLower > 0 && lastClose < ind.BBLower

	if rsiStillOversold && belowBBLower {
		return true, "RSI recovery failure + still below BB lower band"
	}

	return false, ""
}
