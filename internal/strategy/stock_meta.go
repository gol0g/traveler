package strategy

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// StockMetaConfig defines the regime-to-strategy mapping for stock markets.
// Each regime (bull/sideways/bear) maps to a list of strategy names.
type StockMetaConfig struct {
	Name            string            // config name (for optimization reports)
	Market          string            // "us" or "kr"
	BenchmarkSym    string            // "SPY" or "069500"
	Bull            []string          // strategy names active in bull regime
	Sideways        []string          // strategy names active in sideways regime
	Bear            []string          // strategy names active in bear regime
	MaxHoldOverride map[string]int    // strategy name → override max hold days
}

// DefaultStockMetaConfig returns the optimized config for a market and capital level.
// Capital tiers:
//   - ETF tier (US < $500, KR < ₩500K): ETF momentum only (GEM/TQQQ/KODEX timing)
//   - Hybrid tier: ETF + individual stocks
//   - Full tier: Current individual stock strategies
func DefaultStockMetaConfig(market string, capital ...float64) StockMetaConfig {
	// Capital tier 결정
	cap := 0.0
	if len(capital) > 0 {
		cap = capital[0]
	}
	tier := GetCapitalTier(market, cap)

	// ETF tier: ETF 전략만 사용
	if tier == "etf" {
		if market == "kr" {
			return StockMetaConfig{
				Name:         "kr-etf-timing",
				Market:       "kr",
				BenchmarkSym: "069500",
				Bull:         []string{"etf-momentum"},
				Sideways:     []string{"etf-momentum"},
				Bear:         []string{"etf-momentum"},
			}
		}
		return StockMetaConfig{
			Name:         "us-etf-momentum",
			Market:       "us",
			BenchmarkSym: "SPY",
			Bull:         []string{"etf-momentum", "etf-tqqq-sma"},
			Sideways:     []string{"etf-momentum", "etf-tqqq-sma"},
			Bear:         []string{"etf-momentum"}, // Bear에서는 TQQQ 제외 (SMA 하회 시 자동 skip)
		}
	}

	// Full / Hybrid tier: 개별종목 + ETF 모멘텀 병행
	// ETF는 항상 포함: 개별종목 시그널이 0일 때도 ETF 타이밍이 커버
	// (강한 상승장에서 pullback/breakout 시그널 없는 문제 해결)
	if market == "kr" {
		return StockMetaConfig{
			Name:         "extended-hold",
			Market:       "kr",
			BenchmarkSym: "069500",
			Bull:         []string{"etf-momentum", "breakout"},
			Sideways:     []string{"etf-momentum", "mean-reversion", "oversold"},
			Bear:         []string{"etf-momentum", "oversold"},
			MaxHoldOverride: map[string]int{
				"breakout": 20,
			},
		}
	}
	return StockMetaConfig{
		Name:         "breakout-bull",
		Market:       "us",
		BenchmarkSym: "SPY",
		Bull:         []string{"etf-momentum", "etf-tqqq-sma", "breakout"},
		Sideways:     []string{"etf-momentum", "oversold"},
		Bear:         []string{"etf-momentum", "oversold"},
	}
}

// StockMetaStrategy is a regime-aware meta strategy for stock markets (US/KR).
// It detects market regime and delegates to the appropriate sub-strategies,
// following the same pattern as CryptoMetaStrategy.
type StockMetaStrategy struct {
	config   StockMetaConfig
	regime   *RegimeDetector
	provider provider.Provider
	bull     []Strategy
	sideways []Strategy
	bear     []Strategy
}

// NewStockMetaStrategy creates a new stock meta strategy from config
func NewStockMetaStrategy(cfg StockMetaConfig, p provider.Provider) *StockMetaStrategy {
	s := &StockMetaStrategy{
		config:   cfg,
		regime:   NewRegimeDetectorForSymbol(p, cfg.BenchmarkSym),
		provider: p,
	}
	s.bull = s.buildStrategies(cfg.Bull, RegimeBull)
	s.sideways = s.buildStrategies(cfg.Sideways, RegimeSideways)
	s.bear = s.buildStrategies(cfg.Bear, RegimeBear)
	return s
}

// buildStrategies creates Strategy instances from a list of strategy names.
// regime parameter allows creating regime-specific configs (e.g., relaxed conditions for sideways).
func (s *StockMetaStrategy) buildStrategies(names []string, regime Regime) []Strategy {
	var strats []Strategy
	for _, name := range names {
		strat := s.createStrategy(name, regime)
		if strat != nil {
			strats = append(strats, strat)
		}
	}
	return strats
}

// createStrategy creates a single strategy by name with market/regime-appropriate config.
// MarketRegimeSymbol is left empty so sub-strategies skip their own regime check —
// the meta strategy's regime detection is authoritative.
// For sideways regime, strategy conditions are relaxed to produce more signals.
func (s *StockMetaStrategy) createStrategy(name string, regime Regime) Strategy {
	isKR := s.config.Market == "kr"
	isSideways := regime == RegimeSideways

	switch name {
	case "pullback":
		cfg := DefaultPullbackConfig()
		if isSideways {
			cfg.RequireUptrend = false // sideways: MA50 상승추세 불요
			cfg.MaxRSI = 60            // sideways: RSI 60까지 허용 (기본 50)
		}
		if isKR {
			cfg.MinPrice = 1000
			cfg.MinDailyDollarVol = 500000000
			// KR bull: 풀백 조건 적절히 완화
			if regime == RegimeBull {
				cfg.MA20TouchTolerance = 0.04    // 2% → 4% (적당히 완화)
				cfg.MaxRSI = 60                  // 50 → 60
				cfg.RequireVolumePattern = false  // 거래량 패턴 선택사항
				cfg.RequireBouncing = true        // 바운싱 필수 (반등 확인)
			}
		}
		return NewPullbackStrategy(cfg, s.provider)

	case "breakout":
		cfg := DefaultBreakoutConfig()
		if isKR {
			cfg.MinPrice = 1000
			cfg.MinDailyDollarVol = 500000000
			// KR bull: 수렴 필수 해제 + 필터 완화
			if regime == RegimeBull {
				cfg.RequireConsolidation = false // 수렴 없어도 시그널 (probability 감소)
				cfg.KRVolumeMultiple = 1.3       // 2.0 → 1.3 (KR 거래량 패턴 고려)
				cfg.KRMinBreakoutPct = 0.3       // 1.5% → 0.3% (소폭 돌파도 허용)
			}
		}
		return NewBreakoutStrategy(cfg, s.provider)

	case "mean-reversion":
		cfg := DefaultMeanReversionConfig()
		// Strict conditions only (no sideways relaxation).
		// 2026-02-27 교훈: RSI 35, BB 2%, MA200 면제 → 3W/8L 대참사.
		// 기본값 유지: RSI < 30, BB 1%, MA200 필수.
		//
		// 추가 안전장치: SPY/KODEX200 > MA20 필터 (매크로 폭락일 진입 차단)
		if isKR {
			cfg.MarketRegimeSymbol = "069500"
			cfg.MinPrice = 1000
			cfg.MinDailyDollarVol = 500000000
		} else {
			cfg.MarketRegimeSymbol = "SPY"
		}
		return NewMeanReversionStrategy(cfg, s.provider)

	case "oversold":
		cfg := DefaultOversoldConfig()
		if isKR {
			cfg.MinPrice = 1000
			cfg.MinDailyDollarVol = 500000000
		}
		return NewOversoldStrategy(cfg, s.provider)

	case "etf-momentum":
		if isKR {
			return NewETFMomentumStrategy(ETFMomentumConfig{Mode: ETFModeKRTiming, Market: "kr"}, s.provider)
		}
		return NewETFMomentumStrategy(ETFMomentumConfig{Mode: ETFModeGEM, Market: "us"}, s.provider)

	case "etf-tqqq-sma":
		return NewETFMomentumStrategy(ETFMomentumConfig{Mode: ETFModeTQQQSMA, Market: "us"}, s.provider)

	default:
		log.Printf("[STOCK-META] Unknown strategy: %s", name)
		return nil
	}
}

// Name returns the strategy name
func (s *StockMetaStrategy) Name() string {
	return "stock-meta"
}

// Description returns the strategy description
func (s *StockMetaStrategy) Description() string {
	return fmt.Sprintf("Stock Meta Strategy - regime-aware (%s)", s.config.Market)
}

// Analyze detects market regime and tries all strategies for that regime.
// Returns the best signal (highest probability x strength) or nil.
func (s *StockMetaStrategy) Analyze(ctx context.Context, stock model.Stock) (*Signal, error) {
	regime := s.regime.Detect(ctx)

	var strategies []Strategy
	switch regime {
	case RegimeBull:
		strategies = s.bull
	case RegimeSideways:
		strategies = s.sideways
	case RegimeBear:
		strategies = s.bear
	default:
		strategies = s.sideways
	}

	if len(strategies) == 0 {
		return nil, nil
	}

	// Try all strategies, collect best signal by score
	var bestSignal *Signal
	var bestScore float64

	for _, strat := range strategies {
		sig, err := strat.Analyze(ctx, stock)
		if err != nil || sig == nil {
			continue
		}

		// Score = probability x strength (both 0-100), matching CryptoMetaStrategy
		score := sig.Probability * sig.Strength / 100.0
		if score > bestScore {
			bestScore = score
			bestSignal = sig
		}
	}

	if bestSignal == nil {
		return nil, nil
	}

	// RR ratio 최소 1.5 강제 (개별종목만 — ETF는 시그널 역전 기반 청산이라 RR 무의미)
	isETF := strings.Contains(bestSignal.Strategy, "etf-momentum")
	if !isETF && bestSignal.Guide != nil && bestSignal.Guide.RiskRewardRatio > 0 && bestSignal.Guide.RiskRewardRatio < 1.45 {
		log.Printf("[STOCK-META] %s %s rejected: RR %.2f < 1.45", stock.Symbol, bestSignal.Strategy, bestSignal.Guide.RiskRewardRatio)
		return nil, nil
	}

	// Enrich signal with regime info (for sizer risk reduction)
	if bestSignal.Details == nil {
		bestSignal.Details = make(map[string]float64)
	}
	switch regime {
	case RegimeBull:
		bestSignal.Details["regime"] = 1
	case RegimeSideways:
		bestSignal.Details["regime"] = 0
	case RegimeBear:
		bestSignal.Details["regime"] = -1
	}

	// MaxHoldOverride: inject into Details for backtester to pick up
	baseName := bestSignal.Strategy
	if override, ok := s.config.MaxHoldOverride[baseName]; ok {
		bestSignal.Details["max_hold_override"] = float64(override)
	}

	// Add regime prefix to reason
	bestSignal.Reason = fmt.Sprintf("[%s] %s", regimeLabel(regime), bestSignal.Reason)

	// Override strategy name to include regime info (e.g., "breakout(bull)")
	bestSignal.Strategy = fmt.Sprintf("%s(%s)", bestSignal.Strategy, regime)

	return bestSignal, nil
}

// ResetRegimeCache resets regime caches for all sub-strategies and the detector itself.
// Must be called between simulation days in backtesting.
func (s *StockMetaStrategy) ResetRegimeCache() {
	// Reset the regime detector's cache so it recalculates for the new date
	s.regime.mu.Lock()
	s.regime.updatedAt = time.Time{} // force recalculation
	s.regime.mu.Unlock()

	// Reset all sub-strategies' regime caches
	allStrats := make([]Strategy, 0, len(s.bull)+len(s.sideways)+len(s.bear))
	allStrats = append(allStrats, s.bull...)
	allStrats = append(allStrats, s.sideways...)
	allStrats = append(allStrats, s.bear...)

	seen := make(map[string]bool)
	for _, strat := range allStrats {
		key := fmt.Sprintf("%p", strat) // deduplicate by pointer
		if seen[key] {
			continue
		}
		seen[key] = true
		if rr, ok := strat.(interface{ ResetRegimeCache() }); ok {
			rr.ResetRegimeCache()
		}
	}
}

// GetCurrentRegime returns the current cached regime (for external display)
func (s *StockMetaStrategy) GetCurrentRegime(ctx context.Context) Regime {
	return s.regime.Detect(ctx)
}

// GetRegimeInfo returns detailed regime info with benchmark indicators
func (s *StockMetaStrategy) GetRegimeInfo(ctx context.Context) RegimeInfo {
	return s.regime.DetectWithInfo(ctx)
}

// GetActiveStrategyNames returns the strategy names active for the current regime
func (s *StockMetaStrategy) GetActiveStrategyNames(ctx context.Context) []string {
	regime := s.regime.Detect(ctx)
	switch regime {
	case RegimeBull:
		return s.config.Bull
	case RegimeSideways:
		return s.config.Sideways
	case RegimeBear:
		return s.config.Bear
	default:
		return s.config.Sideways
	}
}
