package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"traveler/internal/ai"
	"database/sql"

	_ "modernc.org/sqlite"

	"traveler/internal/broker"
	"traveler/internal/provider"
	"traveler/internal/strategy"
	"traveler/internal/symbols"
	"traveler/internal/trader"
	"traveler/pkg/model"
)

// createMarketAwareStrategies creates a regime-aware meta strategy for stock markets (US/KR).
// Uses StockMetaStrategy with optimized regime-strategy mapping, matching the daemon.
// capital=0 means unspecified → uses "full" tier (backward compatible).
func createMarketAwareStrategies(p provider.Provider, market string, capital float64) []strategy.Strategy {
	metaCfg := strategy.DefaultStockMetaConfig(market, capital)
	meta := strategy.NewStockMetaStrategy(metaCfg, p)
	return []strategy.Strategy{meta}
}

// ScanRequest represents a scan request
type ScanRequest struct {
	Capital  float64  `json:"capital"`
	Universe string   `json:"universe"`
	Symbols  []string `json:"symbols,omitempty"`
}

// ScanResponse represents the scan response with chart data
type ScanResponse struct {
	Strategy      string           `json:"strategy"`
	TotalScanned  int              `json:"total_scanned"`
	SignalsFound  int              `json:"signals_found"`
	Signals       []SignalWithChart `json:"signals"`
	ScanTime      string           `json:"scan_time"`
	Capital       float64          `json:"capital"`
	TotalInvest   float64          `json:"total_invest"`
	TotalRisk     float64          `json:"total_risk"`
	UniversesUsed []string         `json:"universes_used,omitempty"`
	Decision      string           `json:"decision,omitempty"`
	Expansions    int              `json:"expansions,omitempty"`
	AvgProb              float64          `json:"avg_prob,omitempty"`
	FundamentalsFiltered int              `json:"fundamentals_filtered,omitempty"`

	// Market regime info
	Regime           string   `json:"regime,omitempty"`            // "bull", "sideways", "bear"
	ActiveStrategies []string `json:"active_strategies,omitempty"` // strategies active for this regime
	BenchmarkPrice   float64  `json:"benchmark_price,omitempty"`
	BenchmarkMA20    float64  `json:"benchmark_ma20,omitempty"`
	BenchmarkMA50    float64  `json:"benchmark_ma50,omitempty"`
	BenchmarkRSI     float64  `json:"benchmark_rsi,omitempty"`

	// Capital tier info
	CapitalTier      string   `json:"capital_tier,omitempty"`      // "etf", "hybrid", "full", "btc-only", "extended"

	// AI filter info
	AIFiltered       int             `json:"ai_filtered,omitempty"`       // signals rejected by AI
	AIRejections     []ai.AIRejection `json:"ai_rejections,omitempty"`    // rejected signals with reasons
}

// SignalWithChart extends Signal with chart data and fundamentals
type SignalWithChart struct {
	strategy.Signal
	Candles      []model.Candle           `json:"candles,omitempty"`
	Fundamentals *provider.FundamentalsData `json:"fundamentals,omitempty"`
}

// StockResponse represents a single stock with chart data
type StockResponse struct {
	Symbol  string           `json:"symbol"`
	Name    string           `json:"name"`
	Candles []model.Candle   `json:"candles"`
	Signal  *strategy.Signal `json:"signal,omitempty"`
}

// PortfolioRequest represents a portfolio recalculation request
type PortfolioRequest struct {
	Capital  float64           `json:"capital"`
	Signals  []strategy.Signal `json:"signals"`
	Excluded []string          `json:"excluded,omitempty"`
}

// UniverseResponse represents available universes
type UniverseResponse struct {
	Universes []UniverseInfo `json:"universes"`
}

// UniverseInfo contains universe details
type UniverseInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// getBrokerForMarket returns the broker for the given market
func (s *Server) getBrokerForMarket(market string) broker.Broker {
	switch market {
	case "kr":
		return s.brokerKR
	case "crypto":
		return s.brokerCrypto
	case "sim-us":
		return s.brokerSimUS
	case "sim-kr":
		return s.brokerSimKR
	default:
		return s.broker
	}
}

// getProviderForMarket returns the provider for the given market
func (s *Server) getProviderForMarket(market string) provider.Provider {
	switch market {
	case "kr", "sim-kr":
		return s.providerKR
	case "crypto":
		return s.providerCrypto
	default:
		return s.provider
	}
}

// webStockLoader implements trader.StockLoader for web scanning
type webStockLoader struct {
	korean bool // true이면 한국 유니버스에서 종목명 적용
	crypto bool // true이면 크립토 유니버스에서 종목명 적용
}

func (l *webStockLoader) LoadUniverse(ctx context.Context, u symbols.Universe) ([]model.Stock, error) {
	syms := symbols.GetUniverse(u)
	if syms == nil {
		return nil, fmt.Errorf("unknown universe: %s", u)
	}
	stocks := make([]model.Stock, len(syms))
	for i, sym := range syms {
		name := sym
		if l.korean {
			name = symbols.GetKRSymbolName(sym)
		} else if l.crypto {
			name = symbols.GetCryptoSymbolName(sym)
		}
		stocks[i] = model.Stock{Symbol: sym, Name: name}
	}
	return stocks, nil
}

// handleScan starts an async scan (POST) — browser polls /api/scan/status
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed — use POST", http.StatusMethodNotAllowed)
		return
	}

	// Parse market (us or kr)
	market := r.URL.Query().Get("market")
	if market == "" {
		market = "us"
	}

	// sim 마켓은 스캔 불가 (데몬이 자동 실행, 웹은 결과만 표시)
	if market == "sim-us" || market == "sim-kr" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": "Scan not available for simulation markets"})
		return
	}

	// Check if scan already running for this market
	s.scanMu.RLock()
	running := false
	switch market {
	case "kr":
		running = s.scanKR.Status == "running"
	case "crypto":
		running = s.scanCrypto.Status == "running"
	default:
		running = s.scan.Status == "running"
	}
	s.scanMu.RUnlock()
	if running {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "already_running"})
		return
	}

	// Parse capital — query param > broker balance > default
	capital := s.capital
	if c := r.URL.Query().Get("capital"); c != "" {
		if v, err := strconv.ParseFloat(c, 64); err == nil {
			capital = v
		}
	} else {
		// 실제 브로커 잔고 조회
		var b broker.Broker
		switch market {
		case "kr":
			b = s.brokerKR
		case "crypto":
			b = s.brokerCrypto
		default:
			b = s.broker
		}
		if b != nil {
			if bal, err := b.GetBalance(context.Background()); err == nil && bal.TotalEquity > 0 {
				capital = bal.TotalEquity
				log.Printf("[WEB] Using actual broker balance for %s: %.2f", market, capital)
			}
		}
	}

	// Init scan state per market
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	s.scanMu.Lock()
	switch market {
	case "kr":
		s.scanKRCancel = cancel
		s.scanKR = scanState{
			Status:    "running",
			Message:   "Starting KR adaptive scan...",
			StartedAt: time.Now(),
		}
	case "crypto":
		s.scanCryptoCancel = cancel
		s.scanCrypto = scanState{
			Status:    "running",
			Message:   "Starting crypto scan...",
			StartedAt: time.Now(),
		}
	default:
		s.scanCancel = cancel
		s.scan = scanState{
			Status:    "running",
			Message:   "Starting adaptive multi-strategy scan...",
			StartedAt: time.Now(),
		}
	}
	s.scanMu.Unlock()

	switch market {
	case "kr":
		log.Printf("[WEB] KR scan starting (capital=₩%.0f)", capital)
		go s.runKRScanAsync(ctx, cancel, capital)
	case "crypto":
		log.Printf("[WEB] Crypto scan starting (capital=₩%.0f)", capital)
		go s.runCryptoScanAsync(ctx, cancel, capital)
	default:
		log.Printf("[WEB] Adaptive scan starting (capital=$%.2f)", capital)
		go s.runScanAsync(ctx, cancel, capital)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// runScanAsync runs the scan in background, updating scanState as it goes
func (s *Server) runScanAsync(ctx context.Context, cancel context.CancelFunc, capital float64) {
	defer cancel()
	startTime := time.Now()

	// Caching provider: each stock fetched once, shared across strategies
	cachedProvider := provider.NewCachingProvider(s.provider, 250)

	capitalTier := strategy.GetCapitalTier("us", capital)
	strategies := createMarketAwareStrategies(cachedProvider, "us", capital)
	meta := strategies[0].(*strategy.StockMetaStrategy)
	regimeInfo := meta.GetRegimeInfo(ctx)
	activeStrats := meta.GetActiveStrategyNames(ctx)
	log.Printf("[WEB] US regime: %s (benchmark=%s, price=%.2f, MA20=%.2f, RSI=%.1f), strategies=%v",
		regimeInfo.Regime, regimeInfo.Symbol, regimeInfo.Price, regimeInfo.MA20, regimeInfo.RSI14, activeStrats)
	totalScanned := 0
	totalFound := 0

	scanFunc := func(ctx context.Context, stocks []model.Stock) ([]strategy.Signal, error) {
		var signals []strategy.Signal
		for i, stock := range stocks {
			select {
			case <-ctx.Done():
				return signals, ctx.Err()
			default:
			}

			stockCtx, stockCancel := context.WithTimeout(ctx, 15*time.Second)

			var best *strategy.Signal
			for _, strat := range strategies {
				sig, err := strat.Analyze(stockCtx, stock)
				if err == nil && sig != nil {
					if best == nil || sig.Strength > best.Strength {
						best = sig
					}
				}
			}
			stockCancel()

			if best != nil {
				signals = append(signals, *best)
				totalFound++
			}

			totalScanned++
			s.updateScanProgress(
				fmt.Sprintf("Scanning %d/%d stocks...", i+1, len(stocks)),
				totalScanned, totalFound,
			)
		}
		return signals, nil
	}

	// Adaptive scanner
	sizerCfg := trader.AdjustConfigForBalance(capital)
	adaptiveCfg := trader.DefaultAdaptiveConfig()
	adaptiveCfg.Verbose = true
	scanner := trader.NewAdaptiveScanner(adaptiveCfg, sizerCfg, scanFunc)

	// ETF tier: route to ETF universe
	if capitalTier == "etf" {
		scanner.SetTierFunc(trader.GetUSETFTiers)
	}

	result, err := scanner.Scan(ctx, &webStockLoader{})
	if err != nil {
		log.Printf("[WEB] Scan error: %v", err)
		s.scanMu.Lock()
		s.scan.Status = "error"
		s.scan.Error = err.Error()
		s.scanMu.Unlock()
		return
	}

	// Fundamentals filter (use fresh context since scan may have consumed most of the timeout)
	var fundamentalsFiltered int
	var fundChecker *provider.FundamentalsChecker
	if len(result.Signals) > 0 && s.dataDir != "" {
		s.updateScanProgress("Checking fundamentals...", totalScanned, totalFound)
		fundCtx, fundCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		fundChecker = provider.NewFundamentalsChecker(s.dataDir, nil) // US: no KOSDAQ
		if err := fundChecker.Init(fundCtx); err != nil {
			log.Printf("[WEB] Fundamentals init failed: %v", err)
			fundChecker = nil
		} else {
			syms := make([]string, 0, len(result.Signals))
			for _, sig := range result.Signals {
				syms = append(syms, sig.Stock.Symbol)
			}
			rejected := fundChecker.FilterSymbols(fundCtx, syms)
			if len(rejected) > 0 {
				var filtered []strategy.Signal
				for _, sig := range result.Signals {
					if _, rej := rejected[sig.Stock.Symbol]; !rej {
						filtered = append(filtered, sig)
					}
				}
				fundamentalsFiltered = len(result.Signals) - len(filtered)
				result.Signals = filtered
				log.Printf("[WEB] Fundamentals filter: %d → %d signals", fundamentalsFiltered+len(filtered), len(filtered))
			}
		}
		fundCancel()
	}

	// AI signal filter + SL/TP optimization
	var aiFilteredCount int
	var aiRejections []ai.AIRejection
	if s.aiClient != nil && len(result.Signals) > 0 {
		s.updateScanProgress("AI analyzing signals...", totalScanned, totalFound)
		regime := string(regimeInfo.Regime)
		before := len(result.Signals)
		result.Signals, aiRejections = s.aiClient.FilterSignals(ctx, result.Signals, regime, "us")
		aiFilteredCount = before - len(result.Signals)
		if len(result.Signals) > 0 {
			result.Signals = s.aiClient.OptimizeGuides(ctx, result.Signals, regime, "us")
		}
	}

	s.updateScanProgress("Applying position sizing...", totalScanned, totalFound)

	sizer := trader.NewPositionSizer(sizerCfg)
	sized := sizer.ApplyToSignals(result.Signals)
	if len(sized) > 10 {
		sized = sized[:10]
	}

	s.updateScanProgress("Loading chart data...", totalScanned, totalFound)

	var signals []SignalWithChart
	var totalInvest, totalRisk float64
	for _, sig := range sized {
		candles, _ := cachedProvider.GetDailyCandles(ctx, sig.Stock.Symbol, 100)
		swc := SignalWithChart{Signal: sig, Candles: candles}
		if fundChecker != nil {
			if fd, err := fundChecker.Check(context.Background(), sig.Stock.Symbol); err == nil {
				swc.Fundamentals = fd
			}
		}
		signals = append(signals, swc)
		if sig.Guide != nil {
			totalInvest += sig.Guide.InvestAmount
			totalRisk += sig.Guide.RiskAmount
		}
	}

	scanTime := time.Since(startTime)
	log.Printf("[WEB] Scan complete: %d signals from %v in %s (decision: %s)",
		len(signals), result.UniversesUsed, scanTime.Round(time.Second), result.Decision)

	resp := ScanResponse{
		Strategy:             "multi",
		TotalScanned:         result.ScannedCount,
		SignalsFound:         len(signals),
		Signals:              signals,
		ScanTime:             scanTime.Round(time.Second).String(),
		Capital:              capital,
		TotalInvest:          totalInvest,
		TotalRisk:            totalRisk,
		UniversesUsed:        result.UniversesUsed,
		Decision:             result.Decision,
		Expansions:           result.Expansions,
		AvgProb:              result.Quality.AvgProb,
		FundamentalsFiltered: fundamentalsFiltered,
		Regime:           string(regimeInfo.Regime),
		ActiveStrategies: activeStrats,
		BenchmarkPrice:   regimeInfo.Price,
		BenchmarkMA20:    regimeInfo.MA20,
		BenchmarkMA50:    regimeInfo.MA50,
		BenchmarkRSI:     regimeInfo.RSI14,
		CapitalTier:      capitalTier,
		AIFiltered:       aiFilteredCount,
		AIRejections:     aiRejections,
	}

	respJSON, _ := json.Marshal(resp)

	s.scanMu.Lock()
	s.scan.Status = "done"
	s.scan.Message = fmt.Sprintf("Complete: %d signals in %s", len(signals), scanTime.Round(time.Second))
	s.scan.Result = respJSON
	s.scanMu.Unlock()

	s.saveScanResultToDisk(respJSON, "us")
}

// runKRScanAsync runs Korean market scan in background
func (s *Server) runKRScanAsync(ctx context.Context, cancel context.CancelFunc, capital float64) {
	defer cancel()
	startTime := time.Now()

	if s.providerKR == nil {
		s.scanMu.Lock()
		s.scanKR.Status = "error"
		s.scanKR.Error = "Korean market provider not configured"
		s.scanMu.Unlock()
		return
	}

	cachedProvider := provider.NewCachingProvider(s.providerKR, 250)
	capitalTierKR := strategy.GetCapitalTier("kr", capital)
	strategies := createMarketAwareStrategies(cachedProvider, "kr", capital)
	metaKR := strategies[0].(*strategy.StockMetaStrategy)
	regimeInfoKR := metaKR.GetRegimeInfo(ctx)
	activeStratsKR := metaKR.GetActiveStrategyNames(ctx)
	log.Printf("[WEB] KR regime: %s (benchmark=%s, price=%.0f, MA20=%.0f, RSI=%.1f), strategies=%v",
		regimeInfoKR.Regime, regimeInfoKR.Symbol, regimeInfoKR.Price, regimeInfoKR.MA20, regimeInfoKR.RSI14, activeStratsKR)
	totalScanned := 0
	totalFound := 0

	scanFunc := func(ctx context.Context, stocks []model.Stock) ([]strategy.Signal, error) {
		var signals []strategy.Signal
		for i, stock := range stocks {
			select {
			case <-ctx.Done():
				return signals, ctx.Err()
			default:
			}

			stockCtx, stockCancel := context.WithTimeout(ctx, 15*time.Second)
			var best *strategy.Signal
			for _, strat := range strategies {
				sig, err := strat.Analyze(stockCtx, stock)
				if err == nil && sig != nil {
					if best == nil || sig.Strength > best.Strength {
						best = sig
					}
				}
			}
			stockCancel()

			if best != nil {
				signals = append(signals, *best)
				totalFound++
			}
			totalScanned++
			s.updateScanKRProgress(
				fmt.Sprintf("Scanning KR %d/%d stocks...", i+1, len(stocks)),
				totalScanned, totalFound,
			)
		}
		return signals, nil
	}

	sizerCfg := trader.AdjustConfigForKRBalance(capital)
	adaptiveCfg := trader.DefaultAdaptiveConfig()
	adaptiveCfg.Verbose = true

	// Override GetUniverseTiers for KR
	scanner := trader.NewAdaptiveScanner(adaptiveCfg, sizerCfg, scanFunc)
	if capitalTierKR == "etf" {
		scanner.SetTierFunc(trader.GetKRETFTiers)
	} else {
		scanner.SetTierFunc(func(balance float64) []trader.UniverseTier {
			return trader.GetKRUniverseTiers(balance)
		})
	}

	result, err := scanner.Scan(ctx, &webStockLoader{korean: true})
	if err != nil {
		log.Printf("[WEB] KR Scan error: %v", err)
		s.scanMu.Lock()
		s.scanKR.Status = "error"
		s.scanKR.Error = err.Error()
		s.scanMu.Unlock()
		return
	}

	// Fundamentals filter (KR — use fresh context)
	var fundamentalsFiltered int
	var fundChecker *provider.FundamentalsChecker
	if len(result.Signals) > 0 && s.dataDir != "" {
		s.updateScanKRProgress("Checking fundamentals...", totalScanned, totalFound)
		fundCtx, fundCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		kosdaqSet := make(map[string]bool)
		for _, sym := range symbols.Kosdaq30Symbols {
			kosdaqSet[sym] = true
		}
		fundChecker = provider.NewFundamentalsChecker(s.dataDir, kosdaqSet)
		if err := fundChecker.Init(fundCtx); err != nil {
			log.Printf("[WEB] KR Fundamentals init failed: %v", err)
			fundChecker = nil
		} else {
			syms := make([]string, 0, len(result.Signals))
			for _, sig := range result.Signals {
				syms = append(syms, sig.Stock.Symbol)
			}
			rejected := fundChecker.FilterSymbols(fundCtx, syms)
			if len(rejected) > 0 {
				var filtered []strategy.Signal
				for _, sig := range result.Signals {
					if _, rej := rejected[sig.Stock.Symbol]; !rej {
						filtered = append(filtered, sig)
					}
				}
				fundamentalsFiltered = len(result.Signals) - len(filtered)
				result.Signals = filtered
				log.Printf("[WEB] KR Fundamentals filter: %d → %d signals", fundamentalsFiltered+len(filtered), len(filtered))
			}
		}
		fundCancel()
	}

	// AI signal filter + SL/TP optimization (KR)
	var aiFilteredKR int
	var aiRejectionsKR []ai.AIRejection
	if s.aiClient != nil && len(result.Signals) > 0 {
		s.updateScanKRProgress("AI analyzing signals...", totalScanned, totalFound)
		regime := string(regimeInfoKR.Regime)
		before := len(result.Signals)
		result.Signals, aiRejectionsKR = s.aiClient.FilterSignals(ctx, result.Signals, regime, "kr")
		aiFilteredKR = before - len(result.Signals)
		if len(result.Signals) > 0 {
			result.Signals = s.aiClient.OptimizeGuides(ctx, result.Signals, regime, "kr")
		}
	}

	s.updateScanKRProgress("Applying position sizing...", totalScanned, totalFound)

	sizer := trader.NewPositionSizer(sizerCfg)
	sized := sizer.ApplyToSignals(result.Signals)
	if len(sized) > 10 {
		sized = sized[:10]
	}

	s.updateScanKRProgress("Loading chart data...", totalScanned, totalFound)

	var signals []SignalWithChart
	var totalInvest, totalRisk float64
	for _, sig := range sized {
		candles, _ := cachedProvider.GetDailyCandles(ctx, sig.Stock.Symbol, 100)
		swc := SignalWithChart{Signal: sig, Candles: candles}
		if fundChecker != nil {
			if fd, err := fundChecker.Check(context.Background(), sig.Stock.Symbol); err == nil {
				swc.Fundamentals = fd
			}
		}
		signals = append(signals, swc)
		if sig.Guide != nil {
			totalInvest += sig.Guide.InvestAmount
			totalRisk += sig.Guide.RiskAmount
		}
	}

	scanTime := time.Since(startTime)
	log.Printf("[WEB] KR Scan complete: %d signals from %v in %s",
		len(signals), result.UniversesUsed, scanTime.Round(time.Second))

	resp := ScanResponse{
		Strategy:             "multi-kr",
		TotalScanned:         result.ScannedCount,
		SignalsFound:         len(signals),
		Signals:              signals,
		ScanTime:             scanTime.Round(time.Second).String(),
		Capital:              capital,
		TotalInvest:          totalInvest,
		TotalRisk:            totalRisk,
		UniversesUsed:        result.UniversesUsed,
		Decision:             result.Decision,
		Expansions:           result.Expansions,
		AvgProb:              result.Quality.AvgProb,
		FundamentalsFiltered: fundamentalsFiltered,
		Regime:           string(regimeInfoKR.Regime),
		ActiveStrategies: activeStratsKR,
		BenchmarkPrice:   regimeInfoKR.Price,
		BenchmarkMA20:    regimeInfoKR.MA20,
		BenchmarkMA50:    regimeInfoKR.MA50,
		BenchmarkRSI:     regimeInfoKR.RSI14,
		CapitalTier:      capitalTierKR,
		AIFiltered:       aiFilteredKR,
		AIRejections:     aiRejectionsKR,
	}

	respJSON, _ := json.Marshal(resp)

	s.scanMu.Lock()
	s.scanKR.Status = "done"
	s.scanKR.Message = fmt.Sprintf("KR Complete: %d signals in %s", len(signals), scanTime.Round(time.Second))
	s.scanKR.Result = respJSON
	s.scanMu.Unlock()

	s.saveScanResultToDisk(respJSON, "kr")
}

// updateScanCryptoProgress thread-safely updates crypto scan progress
func (s *Server) updateScanCryptoProgress(message string, scanned, found int) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.scanCrypto.Message = message
	s.scanCrypto.Scanned = scanned
	s.scanCrypto.Found = found
}

// runCryptoScanAsync runs crypto market scan in background
func (s *Server) runCryptoScanAsync(ctx context.Context, cancel context.CancelFunc, capital float64) {
	defer cancel()
	startTime := time.Now()

	if s.providerCrypto == nil {
		s.scanMu.Lock()
		s.scanCrypto.Status = "error"
		s.scanCrypto.Error = "Crypto provider not configured"
		s.scanMu.Unlock()
		return
	}

	cachedProvider := provider.NewCachingProvider(s.providerCrypto, 50)
	totalScanned := 0
	totalFound := 0

	// Crypto: regime-aware meta strategy (Bull→VolatilityBreakout, Sideways→RangeTrading, Bear→skip)
	capitalTierCrypto := strategy.GetCapitalTier("crypto", capital)
	cryptoMeta := strategy.NewCryptoMetaStrategy(cachedProvider, capital)
	cryptoRegimeInfo := cryptoMeta.GetRegimeInfo(ctx)
	log.Printf("[WEB] Crypto regime: %s (benchmark=%s, price=%.0f, MA20=%.0f, RSI=%.1f)",
		cryptoRegimeInfo.Regime, cryptoRegimeInfo.Symbol, cryptoRegimeInfo.Price, cryptoRegimeInfo.MA20, cryptoRegimeInfo.RSI14)
	strategies := []strategy.Strategy{cryptoMeta}

	scanFunc := func(ctx context.Context, stocks []model.Stock) ([]strategy.Signal, error) {
		var signals []strategy.Signal
		for i, stock := range stocks {
			select {
			case <-ctx.Done():
				return signals, ctx.Err()
			default:
			}

			stockCtx, stockCancel := context.WithTimeout(ctx, 15*time.Second)
			var best *strategy.Signal
			for _, strat := range strategies {
				sig, err := strat.Analyze(stockCtx, stock)
				if err == nil && sig != nil {
					if best == nil || sig.Strength > best.Strength {
						best = sig
					}
				}
			}
			stockCancel()

			if best != nil {
				signals = append(signals, *best)
				totalFound++
			}
			totalScanned++
			s.updateScanCryptoProgress(
				fmt.Sprintf("Scanning Crypto %d/%d symbols...", i+1, len(stocks)),
				totalScanned, totalFound,
			)
		}
		return signals, nil
	}

	sizerCfg := trader.AdjustConfigForCryptoBalance(capital)
	adaptiveCfg := trader.DefaultAdaptiveConfig()
	adaptiveCfg.Verbose = true

	scanner := trader.NewAdaptiveScanner(adaptiveCfg, sizerCfg, scanFunc)
	scanner.SetTierFunc(func(balance float64) []trader.UniverseTier {
		return trader.GetCryptoUniverseTiers(balance)
	})

	result, err := scanner.Scan(ctx, &webStockLoader{crypto: true})
	if err != nil {
		log.Printf("[WEB] Crypto Scan error: %v", err)
		s.scanMu.Lock()
		s.scanCrypto.Status = "error"
		s.scanCrypto.Error = err.Error()
		s.scanMu.Unlock()
		return
	}

	// AI signal filter + SL/TP optimization (Crypto)
	var aiFilteredCrypto int
	var aiRejectionsCrypto []ai.AIRejection
	if s.aiClient != nil && len(result.Signals) > 0 {
		s.updateScanCryptoProgress("AI analyzing signals...", totalScanned, totalFound)
		regime := string(cryptoRegimeInfo.Regime)
		before := len(result.Signals)
		result.Signals, aiRejectionsCrypto = s.aiClient.FilterSignals(ctx, result.Signals, regime, "crypto")
		aiFilteredCrypto = before - len(result.Signals)
		if len(result.Signals) > 0 {
			result.Signals = s.aiClient.OptimizeGuides(ctx, result.Signals, regime, "crypto")
		}
	}

	s.updateScanCryptoProgress("Applying position sizing...", totalScanned, totalFound)

	sizer := trader.NewPositionSizer(sizerCfg)
	sized := sizer.ApplyToSignals(result.Signals)
	if len(sized) > 10 {
		sized = sized[:10]
	}

	s.updateScanCryptoProgress("Loading chart data...", totalScanned, totalFound)

	var signals []SignalWithChart
	var totalInvest, totalRisk float64
	for _, sig := range sized {
		candles, _ := cachedProvider.GetDailyCandles(ctx, sig.Stock.Symbol, 100)
		swc := SignalWithChart{Signal: sig, Candles: candles}
		signals = append(signals, swc)
		if sig.Guide != nil {
			totalInvest += sig.Guide.InvestAmount
			totalRisk += sig.Guide.RiskAmount
		}
	}

	scanTime := time.Since(startTime)
	log.Printf("[WEB] Crypto Scan complete: %d signals from %v in %s",
		len(signals), result.UniversesUsed, scanTime.Round(time.Second))

	var cryptoActiveStrats []string
	switch cryptoRegimeInfo.Regime {
	case strategy.RegimeBull:
		cryptoActiveStrats = []string{"volatility-breakout"}
	case strategy.RegimeSideways:
		cryptoActiveStrats = []string{"range-trading"}
	case strategy.RegimeBear:
		cryptoActiveStrats = []string{"(none — bear skip)"}
	}
	resp := ScanResponse{
		Strategy:         "multi-crypto",
		TotalScanned:     result.ScannedCount,
		SignalsFound:     len(signals),
		Signals:          signals,
		ScanTime:         scanTime.Round(time.Second).String(),
		Capital:          capital,
		TotalInvest:      totalInvest,
		TotalRisk:        totalRisk,
		UniversesUsed:    result.UniversesUsed,
		Decision:         result.Decision,
		Expansions:       result.Expansions,
		AvgProb:          result.Quality.AvgProb,
		Regime:           string(cryptoRegimeInfo.Regime),
		ActiveStrategies: cryptoActiveStrats,
		BenchmarkPrice:   cryptoRegimeInfo.Price,
		BenchmarkMA20:    cryptoRegimeInfo.MA20,
		BenchmarkMA50:    cryptoRegimeInfo.MA50,
		BenchmarkRSI:     cryptoRegimeInfo.RSI14,
		CapitalTier:      capitalTierCrypto,
		AIFiltered:       aiFilteredCrypto,
		AIRejections:     aiRejectionsCrypto,
	}

	respJSON, _ := json.Marshal(resp)

	s.scanMu.Lock()
	s.scanCrypto.Status = "done"
	s.scanCrypto.Message = fmt.Sprintf("Crypto Complete: %d signals in %s", len(signals), scanTime.Round(time.Second))
	s.scanCrypto.Result = respJSON
	s.scanMu.Unlock()

	s.saveScanResultToDisk(respJSON, "crypto")
}

// handleSignals returns current cached signals (used for file-based reports)
func (s *Server) handleSignals(w http.ResponseWriter, r *http.Request) {
	// This endpoint is for returning cached/stored signals
	// For now, return empty - client will load from JSON file
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"signals": []interface{}{},
		"message": "Load a JSON report file to view signals",
	})
}

// handleStock returns a single stock with chart data
func (s *Server) handleStock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract symbol from path: /api/stock/AAPL
	path := strings.TrimPrefix(r.URL.Path, "/api/stock/")
	symbol := strings.TrimSpace(path)
	if !symbols.IsKoreanSymbol(symbol) {
		symbol = strings.ToUpper(symbol)
	}
	if symbol == "" {
		http.Error(w, "Symbol required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Use appropriate provider for symbol type
	prov := s.provider
	if symbols.IsCryptoSymbol(symbol) && s.providerCrypto != nil {
		prov = s.providerCrypto
	} else if symbols.IsKoreanSymbol(symbol) && s.providerKR != nil {
		prov = s.providerKR
	}

	// Get candle data
	candles, err := prov.GetDailyCandles(ctx, symbol, 100)
	if err != nil {
		http.Error(w, "Failed to get stock data: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Try to get signal
	stockName := symbol
	if symbols.IsCryptoSymbol(symbol) {
		stockName = symbols.GetCryptoSymbolName(symbol)
	} else if symbols.IsKoreanSymbol(symbol) {
		stockName = symbols.GetKRSymbolName(symbol)
	}
	stock := model.Stock{Symbol: symbol, Name: stockName}

	var signal *strategy.Signal
	if symbols.IsCryptoSymbol(symbol) {
		// Crypto: use regime-aware meta strategy
		strat := strategy.NewCryptoMetaStrategy(prov)
		signal, _ = strat.Analyze(ctx, stock)
	} else {
		// Stock: use regime-aware meta strategy (matching daemon)
		market := "us"
		if symbols.IsKoreanSymbol(symbol) {
			market = "kr"
		}
		metaCfg := strategy.DefaultStockMetaConfig(market)
		strat := strategy.NewStockMetaStrategy(metaCfg, prov)
		signal, _ = strat.Analyze(ctx, stock)
	}

	resp := StockResponse{
		Symbol:  symbol,
		Name:    stockName,
		Candles: candles,
		Signal:  signal,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handlePortfolio recalculates portfolio allocation
func (s *Server) handlePortfolio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PortfolioRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Capital <= 0 {
		http.Error(w, "Capital must be positive", http.StatusBadRequest)
		return
	}

	// Filter out excluded symbols
	excludedMap := make(map[string]bool)
	for _, sym := range req.Excluded {
		excludedMap[sym] = true
	}

	var activeSignals []strategy.Signal
	for _, sig := range req.Signals {
		if !excludedMap[sig.Stock.Symbol] {
			activeSignals = append(activeSignals, sig)
		}
	}

	// Recalculate position sizing
	var totalInvest, totalRisk float64
	if len(activeSignals) > 0 {
		allocationPerPosition := req.Capital / float64(len(activeSignals))
		riskPerPosition := req.Capital * 0.01 / float64(len(activeSignals))

		for i := range activeSignals {
			if activeSignals[i].Guide != nil {
				g := activeSignals[i].Guide
				riskPerShare := g.EntryPrice - g.StopLoss
				if riskPerShare > 0 {
					sharesByRisk := math.Floor(riskPerPosition / riskPerShare)
					sharesByAllocation := math.Floor(allocationPerPosition / g.EntryPrice)
					g.PositionSize = sharesByRisk
					if sharesByAllocation < sharesByRisk {
						g.PositionSize = sharesByAllocation
					}
					if g.PositionSize < 1 {
						g.PositionSize = 1
					}
					g.InvestAmount = g.PositionSize * g.EntryPrice
					g.RiskAmount = g.PositionSize * riskPerShare
					g.RiskPct = g.RiskAmount / req.Capital * 100
					g.AllocationPct = g.InvestAmount / req.Capital * 100

					totalInvest += g.InvestAmount
					totalRisk += g.RiskAmount
				}
			}
		}
	}

	resp := map[string]interface{}{
		"signals":      activeSignals,
		"capital":      req.Capital,
		"total_invest": totalInvest,
		"total_risk":   totalRisk,
		"cash":         req.Capital - totalInvest,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleUniverses returns available stock universes
func (s *Server) handleUniverses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	available := symbols.AvailableUniverses()
	universes := make([]UniverseInfo, len(available))
	for i, u := range available {
		universes[i] = UniverseInfo{
			ID:    string(u.ID),
			Name:  u.Name + " (" + u.Description + ")",
			Count: u.Count,
		}
	}

	resp := UniverseResponse{Universes: universes}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// PositionResponse represents a position with plan data merged
type PositionResponse struct {
	Symbol        string  `json:"symbol"`
	Name          string  `json:"name,omitempty"`
	Quantity      float64 `json:"quantity"`
	AvgCost       float64 `json:"avg_cost"`
	CurrentPrice  float64 `json:"current_price"`
	MarketValue   float64 `json:"market_value"`
	UnrealizedPnL float64 `json:"unrealized_pnl"`
	UnrealizedPct float64 `json:"unrealized_pct"`

	// Plan data (from PlanStore)
	HasPlan              bool    `json:"has_plan"`
	Strategy             string  `json:"strategy,omitempty"`
	StopLoss             float64 `json:"stop_loss,omitempty"`
	Target1              float64 `json:"target1,omitempty"`
	Target2              float64 `json:"target2,omitempty"`
	Target1Hit           bool    `json:"target1_hit,omitempty"`
	EntryTime            string  `json:"entry_time,omitempty"`
	MaxHoldDays          int     `json:"max_hold_days,omitempty"`
	DaysHeld             int     `json:"days_held,omitempty"`
	DaysRemaining        int     `json:"days_remaining,omitempty"`
	BreakoutLevel        float64 `json:"breakout_level,omitempty"`
	ConsecutiveDaysBelow int     `json:"consecutive_days_below,omitempty"`
}

// BalanceResponse represents the account balance
type BalanceResponse struct {
	TotalEquity float64 `json:"total_equity"`
	CashBalance float64 `json:"cash_balance"`
	BuyingPower float64 `json:"buying_power"`
	Currency    string  `json:"currency"`
}

// OrderResponse represents a pending order
type OrderResponse struct {
	OrderID   string  `json:"order_id"`
	Symbol    string  `json:"symbol"`
	Side      string  `json:"side"`
	Type      string  `json:"type"`
	Quantity  float64 `json:"quantity"`
	FilledQty float64 `json:"filled_qty"`
	Price     float64 `json:"price"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
}

// handlePositions returns positions merged with plan data
func (s *Server) handlePositions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	market := r.URL.Query().Get("market")
	b := s.getBrokerForMarket(market)

	if b == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"positions": []interface{}{},
			"error":     "broker not configured",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	positions, err := b.GetPositions(ctx)
	if err != nil {
		log.Printf("[WEB] GetPositions error: %v", err)
		http.Error(w, "Failed to get positions: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Reload PlanStore from disk for freshness (sim 마켓은 별도 planStore)
	var plans map[string]*trader.PositionPlan
	ps := s.planStore
	switch market {
	case "sim-us":
		ps = s.planStoreSimUS
	case "sim-kr":
		ps = s.planStoreSimKR
	}
	if ps != nil {
		ps.Reload()
		plans = ps.All()
	}

	// Merge positions with plan data
	result := make([]PositionResponse, 0, len(positions))
	for _, pos := range positions {
		pr := PositionResponse{
			Symbol:        pos.Symbol,
			Name:          pos.Name,
			Quantity:      pos.Quantity,
			AvgCost:       pos.AvgCost,
			CurrentPrice:  pos.CurrentPrice,
			MarketValue:   pos.MarketValue,
			UnrealizedPnL: pos.UnrealizedPnL,
			UnrealizedPct: pos.UnrealizedPct,
		}

		if plan, ok := plans[pos.Symbol]; ok {
			pr.HasPlan = true
			pr.Strategy = plan.Strategy
			pr.StopLoss = plan.StopLoss
			pr.Target1 = plan.Target1
			pr.Target2 = plan.Target2
			pr.Target1Hit = plan.Target1Hit
			pr.EntryTime = plan.EntryTime.Format(time.RFC3339)
			pr.MaxHoldDays = plan.MaxHoldDays
			pr.DaysHeld = trader.TradingDaysSince(plan.EntryTime)
			pr.DaysRemaining = plan.MaxHoldDays - pr.DaysHeld
			if pr.DaysRemaining < 0 {
				pr.DaysRemaining = 0
			}
			pr.BreakoutLevel = plan.BreakoutLevel
			pr.ConsecutiveDaysBelow = plan.ConsecutiveDaysBelow
		}

		result = append(result, pr)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"positions": result,
	})
}

// handleBalance returns account balance
func (s *Server) handleBalance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	market := r.URL.Query().Get("market")
	b := s.getBrokerForMarket(market)

	if b == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(BalanceResponse{})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	balance, err := b.GetBalance(ctx)
	if err != nil {
		log.Printf("[WEB] GetBalance error: %v", err)
		http.Error(w, "Failed to get balance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := BalanceResponse{
		TotalEquity: balance.TotalEquity,
		CashBalance: balance.CashBalance,
		BuyingPower: balance.BuyingPower,
		Currency:    balance.Currency,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleOrders returns pending orders
func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	market := r.URL.Query().Get("market")
	b := s.getBrokerForMarket(market)

	if b == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"orders": []interface{}{},
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	orders, err := b.GetPendingOrders(ctx)
	if err != nil {
		log.Printf("[WEB] GetPendingOrders error: %v", err)
		http.Error(w, "Failed to get orders: "+err.Error(), http.StatusInternalServerError)
		return
	}

	result := make([]OrderResponse, 0, len(orders))
	for _, o := range orders {
		result = append(result, OrderResponse{
			OrderID:   o.OrderID,
			Symbol:    o.Symbol,
			Side:      string(o.Side),
			Type:      string(o.Type),
			Quantity:  o.Quantity,
			FilledQty: o.FilledQty,
			Price:     o.Price,
			Status:    o.Status,
			CreatedAt: o.CreatedAt.Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"orders": result,
	})
}

// handleTradeHistory 누적 매매 기록 + 요약 반환
func (s *Server) handleTradeHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.history == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"records": []interface{}{},
			"summary": trader.TradeSummary{
				ByStrategy: map[string]trader.StrategySummary{},
				ByMarket:   map[string]trader.MarketSummary{},
			},
		})
		return
	}

	market := r.URL.Query().Get("market")
	if market == "" {
		market = "us"
	}

	// sim 마켓은 별도 history 인스턴스 사용
	hist := s.history
	filterMarket := market
	switch market {
	case "sim-us":
		hist = s.historySimUS
		filterMarket = "us"
	case "sim-kr":
		hist = s.historySimKR
		filterMarket = "kr"
	}
	if hist == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"records": []interface{}{},
			"summary": trader.TradeSummary{
				ByStrategy: map[string]trader.StrategySummary{},
				ByMarket:   map[string]trader.MarketSummary{},
			},
		})
		return
	}

	// 디스크에서 최신 데이터 리로드
	hist.Reload()

	records := hist.GetAll(filterMarket)
	// 종목명 보강
	for i := range records {
		if records[i].Name == "" {
			if symbols.IsCryptoSymbol(records[i].Symbol) {
				records[i].Name = symbols.GetCryptoSymbolName(records[i].Symbol)
			} else if n := symbols.GetKRSymbolName(records[i].Symbol); n != "" {
				records[i].Name = n
			}
		}
	}
	summary := hist.Summary(filterMarket)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"records": records,
		"summary": summary,
	})
}

// handleScalpStatus returns the current scalping status (read from scalp_status.json)
func (s *Server) handleScalpStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	fp := filepath.Join(s.dataDir, "scalp_status.json")
	data, err := os.ReadFile(fp)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"active": false,
			"error":  "Scalp daemon not running",
		})
		return
	}

	info, _ := os.Stat(fp)
	active := info != nil && time.Since(info.ModTime()) < 1*time.Hour

	w.Write([]byte(`{"active":`))
	if active {
		w.Write([]byte("true"))
	} else {
		w.Write([]byte("false"))
	}
	w.Write([]byte(`,"data":`))
	w.Write(data)
	w.Write([]byte("}"))
}

// handleBinanceScalpStatus returns the current Binance short scalping status (read from binance_status.json)
func (s *Server) handleBinanceScalpStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	fp := filepath.Join(s.dataDir, "binance_status.json")
	data, err := os.ReadFile(fp)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"active": false,
			"error":  "Binance scalp daemon not running",
		})
		return
	}

	info, _ := os.Stat(fp)
	active := info != nil && time.Since(info.ModTime()) < 1*time.Hour

	w.Write([]byte(`{"active":`))
	if active {
		w.Write([]byte("true"))
	} else {
		w.Write([]byte("false"))
	}
	w.Write([]byte(`,"data":`))
	w.Write(data)
	w.Write([]byte("}"))
}

// handleBinanceArbStatus returns the current Binance funding arb status (read from arb_status.json)
func (s *Server) handleBinanceArbStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	fp := filepath.Join(s.dataDir, "arb_status.json")
	data, err := os.ReadFile(fp)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"active": false,
			"error":  "Binance arb daemon not running",
		})
		return
	}

	info, _ := os.Stat(fp)
	active := info != nil && time.Since(info.ModTime()) < 1*time.Hour

	w.Write([]byte(`{"active":`))
	if active {
		w.Write([]byte("true"))
	} else {
		w.Write([]byte("false"))
	}
	w.Write([]byte(`,"data":`))
	w.Write(data)
	w.Write([]byte("}"))
}

// handleKRDCAStatus returns the current KR stock DCA status (read from kr_dca_status.json)
func (s *Server) handleKRDCAStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	fp := filepath.Join(s.dataDir, "kr_dca_status.json")
	data, err := os.ReadFile(fp)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"active": false,
			"error":  "KR DCA not running",
		})
		return
	}

	info, _ := os.Stat(fp)
	active := info != nil && time.Since(info.ModTime()) < 8*24*time.Hour // weekly → 8 day freshness

	w.Write([]byte(`{"active":`))
	if active {
		w.Write([]byte("true"))
	} else {
		w.Write([]byte("false"))
	}
	w.Write([]byte(`,"data":`))
	w.Write(data)
	w.Write([]byte("}"))
}

// handleDCAStatus returns the current DCA status (read from dca_status.json)
func (s *Server) handleDCAStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	fp := filepath.Join(s.dataDir, "dca_status.json")
	data, err := os.ReadFile(fp)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"active": false,
			"error":  "DCA not running",
		})
		return
	}

	// Check freshness (if file is older than 48h, consider DCA inactive)
	info, _ := os.Stat(fp)
	active := info != nil && time.Since(info.ModTime()) < 48*time.Hour

	// Write combined response
	w.Write([]byte(`{"active":`))
	if active {
		w.Write([]byte("true"))
	} else {
		w.Write([]byte("false"))
	}
	w.Write([]byte(`,"data":`))
	w.Write(data)
	w.Write([]byte("}"))
}

// ==================== Portfolio Overview ====================

// PortfolioOverviewResponse aggregates all strategies into a single view
type PortfolioOverviewResponse struct {
	UpdatedAt  time.Time              `json:"updated_at"`
	TotalValue float64                `json:"total_value"` // 전체 현재 가치 (KRW)
	TotalCost  float64                `json:"total_cost"`  // 전체 투입 원금 (KRW)
	TotalPnL   float64                `json:"total_pnl"`   // 미실현 손익
	TotalPct   float64                `json:"total_pct"`   // 미실현 수익률 %
	Strategies []StrategyOverview     `json:"strategies"`
	FIRE       FIREProjection         `json:"fire"`
	Projection []GrowthPoint          `json:"projection"` // 24개월 예측
}

// StrategyOverview represents one strategy's summary
type StrategyOverview struct {
	Name      string  `json:"name"`
	Type      string  `json:"type"`      // "dca", "scalp", "kr-dca", "us-stock", "kr-stock"
	Active    bool    `json:"active"`
	Invested  float64 `json:"invested"`  // 투입 원금 (KRW)
	Value     float64 `json:"value"`     // 현재 가치 (KRW)
	PnL       float64 `json:"pnl"`       // 미실현 손익
	PnLPct    float64 `json:"pnl_pct"`   // 수익률 %
	Currency  string  `json:"currency"`  // "KRW" or "USD"
	ExtraInfo string  `json:"extra_info,omitempty"` // 추가 정보 (F&G, RSI 등)
}

// FIREScenario contains projection for a single growth rate scenario
type FIREScenario struct {
	Label        string  `json:"label"`          // 시나리오 이름
	AnnualReturn float64 `json:"annual_return"`  // 연 수익률 %
	YearsTo4Pct  float64 `json:"years_to_4pct"`  // 4% 룰 도달 연수
	YearsTo6Pct  float64 `json:"years_to_6pct"`  // 6% 룰 도달 연수
	FireYear4Pct int     `json:"fire_year_4pct"` // 4% 룰 도달 연도
	FireYear6Pct int     `json:"fire_year_6pct"` // 6% 룰 도달 연도
}

// FIREProjection contains retirement projection data
type FIREProjection struct {
	CurrentAssets     float64        `json:"current_assets"`      // 현재 총 자산 (KRW)
	MonthlyInvestment float64        `json:"monthly_investment"`  // 월 투입 (KRW)
	TargetMonthly     float64        `json:"target_monthly"`      // FIRE 목표 월소득 (KRW)
	TargetAssets4Pct  float64        `json:"target_assets_4pct"`  // 4% 룰 목표 자산
	TargetAssets6Pct  float64        `json:"target_assets_6pct"`  // 6% 룰 목표 자산
	Scenarios         []FIREScenario `json:"scenarios"`           // 보수/중립/낙관 시나리오
}

// GrowthPoint represents a point in the growth projection
type GrowthPoint struct {
	Month        int     `json:"month"`
	TotalAssets  float64 `json:"total_assets"`
	Invested     float64 `json:"invested"`
	Growth       float64 `json:"growth"`
}

// handlePortfolioOverview aggregates all strategies into a portfolio view
func (s *Server) handlePortfolioOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	resp := PortfolioOverviewResponse{
		UpdatedAt: time.Now(),
	}

	var totalValue, totalCost float64

	// Load realized PnL from trade history
	realizedPnL := s.getRealizedPnLByMarket()

	// 1. Crypto DCA
	if dcaData := s.readStatusFile("dca_status.json", 48*time.Hour); dcaData != nil {
		var dca struct {
			TotalInvested float64 `json:"total_invested"`
			CurrentValue  float64 `json:"current_value"`
			UnrealizedPnL float64 `json:"unrealized_pnl"`
			UnrealizedPct float64 `json:"unrealized_pct"`
			FearGreed     int     `json:"fear_greed"`
			FGLabel       string  `json:"fg_label"`
		}
		if json.Unmarshal(dcaData, &dca) == nil && dca.TotalInvested > 0 {
			so := StrategyOverview{
				Name:     "Crypto DCA",
				Type:     "dca",
				Active:   true,
				Invested: dca.TotalInvested,
				Value:    dca.CurrentValue,
				PnL:      dca.UnrealizedPnL,
				PnLPct:   dca.UnrealizedPct,
				Currency: "KRW",
			}
			if dca.FearGreed > 0 {
				so.ExtraInfo = fmt.Sprintf("F&G: %d (%s)", dca.FearGreed, dca.FGLabel)
			}
			resp.Strategies = append(resp.Strategies, so)
			totalValue += dca.CurrentValue
			totalCost += dca.TotalInvested
		}
	}

	// 2. Scalp
	if scalpData := s.readStatusFile("scalp_status.json", 1*time.Hour); scalpData != nil {
		var scalp struct {
			ActivePositions map[string]json.RawMessage `json:"active_positions"`
			Daily struct {
				NetPnL float64 `json:"net_pnl"`
			} `json:"daily"`
			Total struct {
				NetPnL  float64 `json:"net_pnl"`
				WinRate float64 `json:"win_rate"`
				Trades  int     `json:"trades"`
			} `json:"total"`
			OrderAmount float64 `json:"order_amount"`
		}
		if json.Unmarshal(scalpData, &scalp) == nil {
			posCount := len(scalp.ActivePositions)
			posValue := float64(posCount) * scalp.OrderAmount
			so := StrategyOverview{
				Name:     "Crypto Scalp",
				Type:     "scalp",
				Active:   true,
				Invested: posValue,
				Value:    posValue + scalp.Total.NetPnL,
				PnL:      scalp.Total.NetPnL,
				Currency: "KRW",
			}
			if posValue > 0 {
				so.PnLPct = scalp.Total.NetPnL / posValue * 100
			}
			if scalp.Total.Trades > 0 {
				so.ExtraInfo = fmt.Sprintf("WR: %.0f%% (%d trades)", scalp.Total.WinRate, scalp.Total.Trades)
			}
			resp.Strategies = append(resp.Strategies, so)
			totalValue += posValue + scalp.Total.NetPnL
			totalCost += posValue
		}
	}

	// 3. KR DCA
	if krDcaData := s.readStatusFile("kr_dca_status.json", 8*24*time.Hour); krDcaData != nil {
		var krDca struct {
			TotalInvested float64 `json:"total_invested"`
			CurrentValue  float64 `json:"current_value"`
			UnrealizedPnL float64 `json:"unrealized_pnl"`
			UnrealizedPct float64 `json:"unrealized_pct"`
			RSI           float64 `json:"rsi"`
			RSILabel      string  `json:"rsi_label"`
			TotalShares   float64 `json:"total_shares"`
		}
		if json.Unmarshal(krDcaData, &krDca) == nil {
			so := StrategyOverview{
				Name:     "KR DCA (KODEX 200)",
				Type:     "kr-dca",
				Active:   true,
				Invested: krDca.TotalInvested,
				Value:    krDca.CurrentValue,
				PnL:      krDca.UnrealizedPnL,
				PnLPct:   krDca.UnrealizedPct,
				Currency: "KRW",
			}
			if krDca.RSI > 0 {
				so.ExtraInfo = fmt.Sprintf("RSI: %.1f (%s), %d shares", krDca.RSI, krDca.RSILabel, int(krDca.TotalShares))
			}
			resp.Strategies = append(resp.Strategies, so)
			totalValue += krDca.CurrentValue
			totalCost += krDca.TotalInvested
		}
	}

	// 4. US Stock (broker balance + positions)
	if s.broker != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if bal, err := s.broker.GetBalance(ctx); err == nil && bal.TotalEquity > 0 {
			usdToKrw := 1450.0 // 환율 근사치
			valueKRW := bal.TotalEquity * usdToKrw

			// unrealized = current positions value - cost
			var unrealizedPnL float64
			if positions, err2 := s.broker.GetPositions(ctx); err2 == nil {
				for _, pos := range positions {
					unrealizedPnL += pos.UnrealizedPnL
				}
			}
			// total PnL = realized (closed trades) + unrealized (open positions)
			usRealizedPnL := realizedPnL[""]  // US market has empty market field
			totalPnLUSD := usRealizedPnL + unrealizedPnL
			// invested = current equity - total PnL (what we originally put in)
			investedUSD := bal.TotalEquity - totalPnLUSD
			investedKRW := investedUSD * usdToKrw
			so := StrategyOverview{
				Name:     "US Stock",
				Type:     "us-stock",
				Active:   true,
				Invested: investedKRW,
				Value:    valueKRW,
				PnL:      totalPnLUSD * usdToKrw,
				Currency: "USD",
			}
			if investedUSD > 0 {
				so.PnLPct = totalPnLUSD / investedUSD * 100
			}
			so.ExtraInfo = fmt.Sprintf("$%.2f (₩%.0f)", bal.TotalEquity, valueKRW)
			resp.Strategies = append(resp.Strategies, so)
			totalValue += valueKRW
			totalCost += investedKRW
		}
		cancel()
	}

	// 5. KR Stock (broker balance + realized PnL from trade history)
	if s.brokerKR != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if bal, err := s.brokerKR.GetBalance(ctx); err == nil && bal.TotalEquity > 0 {
			var unrealizedPnL float64
			if positions, err := s.brokerKR.GetPositions(ctx); err == nil {
				for _, pos := range positions {
					unrealizedPnL += pos.UnrealizedPnL
				}
			}
			krRealizedPnL := realizedPnL["kr"]
			totalPnL := krRealizedPnL + unrealizedPnL
			investedKRW := bal.TotalEquity - totalPnL
			so := StrategyOverview{
				Name:     "KR Stock",
				Type:     "kr-stock",
				Active:   true,
				Invested: investedKRW,
				Value:    bal.TotalEquity,
				PnL:      totalPnL,
				Currency: "KRW",
			}
			if investedKRW > 0 {
				so.PnLPct = totalPnL / investedKRW * 100
			}
			resp.Strategies = append(resp.Strategies, so)
			totalValue += bal.TotalEquity
			totalCost += investedKRW
		}
		cancel()
	}

	// 6. Crypto (short-term trading balance)
	if s.brokerCrypto != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if bal, err := s.brokerCrypto.GetBalance(ctx); err == nil && bal.CashBalance > 0 {
			var posInvested, posValue float64
			if positions, err := s.brokerCrypto.GetPositions(ctx); err == nil {
				for _, pos := range positions {
					posInvested += pos.AvgCost * pos.Quantity
					posValue += pos.MarketValue
				}
			}
			// DCA+Scalp에 이미 반영된 금액을 빼야 하지만,
			// crypto broker는 전체 Upbit 잔고이므로 별도 추가하지 않음 (이미 DCA/Scalp에서 집계)
			// 여기서는 DCA/Scalp가 없는 순수 crypto 잔고만 — skip duplicate
			_ = posInvested
			_ = posValue
		}
		cancel()
	}

	// 7. Binance Futures (short scalp + BTC futures)
	if binanceData := s.readStatusFile("binance_status.json", 1*time.Hour); binanceData != nil {
		var binance struct {
			BalanceUSDT float64                        `json:"balance_usdt"`
			Total       struct {
				NetPnL  float64 `json:"net_pnl"`
				WinRate float64 `json:"win_rate"`
				Trades  int     `json:"trades"`
			} `json:"total"`
			FundingEarned float64 `json:"funding_earned"`
		}
		if json.Unmarshal(binanceData, &binance) == nil && binance.BalanceUSDT > 0 {
			usdToKrw := 1450.0
			balKRW := binance.BalanceUSDT * usdToKrw
			netPnLKRW := binance.Total.NetPnL * usdToKrw
			investedKRW := balKRW - netPnLKRW
			so := StrategyOverview{
				Name:     "Binance Futures",
				Type:     "binance-futures",
				Active:   true,
				Invested: investedKRW,
				Value:    balKRW,
				PnL:      netPnLKRW,
				Currency: "USD",
			}
			if investedKRW > 0 {
				so.PnLPct = netPnLKRW / investedKRW * 100
			}
			if binance.Total.Trades > 0 {
				so.ExtraInfo = fmt.Sprintf("$%.2f, WR: %.0f%% (%d trades)", binance.BalanceUSDT, binance.Total.WinRate, binance.Total.Trades)
			} else {
				so.ExtraInfo = fmt.Sprintf("$%.2f", binance.BalanceUSDT)
			}
			resp.Strategies = append(resp.Strategies, so)
			totalValue += balKRW
			totalCost += investedKRW
		}
	}

	resp.TotalValue = totalValue
	resp.TotalCost = totalCost
	resp.TotalPnL = totalValue - totalCost
	if totalCost > 0 {
		resp.TotalPct = (totalValue - totalCost) / totalCost * 100
	}

	// FIRE Projection — 실제 수익률 기반
	monthlyInvestment := 2000000.0 // ₩200만/월
	targetMonthly := 3000000.0     // ₩300만/월 (물가 보정 전)
	inflationRate := 0.03          // 3%/년

	targetAssets4Pct := (targetMonthly * 12) / 0.04 // 4% 룰: 9억
	targetAssets6Pct := (targetMonthly * 12) / 0.06 // 6% 룰: 6억

	// 실제 연환산 수익률 계산
	actualAnnualReturn := 0.0
	operatingDays := 0.0
	if s.history != nil {
		records := s.history.GetAll("")
		if len(records) > 0 {
			// 첫 거래일 ~ 현재 기간
			firstTrade := records[0].Timestamp
			operatingDays = time.Since(firstTrade).Hours() / 24
			if operatingDays < 30 {
				operatingDays = 30 // 최소 1개월
			}
		}
	}
	if operatingDays == 0 {
		operatingDays = 30
	}
	operatingYears := operatingDays / 365.0
	if totalCost > 0 && operatingYears > 0 {
		// 총 수익률 → 연환산: (1 + totalReturn)^(1/years) - 1
		totalReturn := (totalValue - totalCost) / totalCost
		if totalReturn > -1 {
			actualAnnualReturn = math.Pow(1+totalReturn, 1.0/operatingYears) - 1
		}
	}

	// 시나리오: 현재 실적, S&P500 평균, 인덱스+적극투자
	type scenario struct {
		label        string
		annualReturn float64
	}
	actualLabel := fmt.Sprintf("현재 실적 (연 %.1f%%)", actualAnnualReturn*100)
	scenarios := []scenario{
		{actualLabel, actualAnnualReturn},
		{"S&P500 평균 (연 10%)", 0.10},
		{"적극 투자 (연 15%)", 0.15},
	}

	currentYear := time.Now().Year()
	var fireScenarios []FIREScenario

	// 현재 실적 기반으로 projection 차트 생성
	projRate := actualAnnualReturn
	if projRate <= 0 {
		projRate = 0.001 // 음수/0이면 projection용 최소값
	}
	projMonthlyGrowth := math.Pow(1+projRate, 1.0/12.0) - 1

	var projection []GrowthPoint
	assets := totalValue
	invested := totalCost
	for m := 1; m <= 24; m++ {
		assets = assets*(1+projMonthlyGrowth) + monthlyInvestment
		invested += monthlyInvestment
		projection = append(projection, GrowthPoint{
			Month:       m,
			TotalAssets: assets,
			Invested:    invested,
			Growth:      assets - invested,
		})
	}

	for _, sc := range scenarios {
		annualRet := sc.annualReturn
		a := totalValue
		var years4, years6 float64
		found4, found6 := false, false

		if annualRet <= 0 {
			// 수익률 0 이하면 목표 도달 불가
			years4 = 50.0
			years6 = 50.0
		} else {
			monthlyGrowth := math.Pow(1+annualRet, 1.0/12.0) - 1
			for m := 1; m <= 600; m++ { // 50년
				a = a*(1+monthlyGrowth) + monthlyInvestment
				yearsElapsed := float64(m) / 12.0
				adj4 := targetAssets4Pct * math.Pow(1+inflationRate, yearsElapsed)
				adj6 := targetAssets6Pct * math.Pow(1+inflationRate, yearsElapsed)

				if !found4 && a >= adj4 {
					years4 = yearsElapsed
					found4 = true
				}
				if !found6 && a >= adj6 {
					years6 = yearsElapsed
					found6 = true
				}
				if found4 && found6 {
					break
				}
			}
			if !found4 {
				years4 = 50.0
			}
			if !found6 {
				years6 = 50.0
			}
		}

		fireScenarios = append(fireScenarios, FIREScenario{
			Label:        sc.label,
			AnnualReturn: annualRet * 100,
			YearsTo4Pct:  math.Round(years4*10) / 10,
			YearsTo6Pct:  math.Round(years6*10) / 10,
			FireYear4Pct: currentYear + int(math.Ceil(years4)),
			FireYear6Pct: currentYear + int(math.Ceil(years6)),
		})
	}

	resp.FIRE = FIREProjection{
		CurrentAssets:     totalValue,
		MonthlyInvestment: monthlyInvestment,
		TargetMonthly:     targetMonthly,
		TargetAssets4Pct:  targetAssets4Pct,
		TargetAssets6Pct:  targetAssets6Pct,
		Scenarios:         fireScenarios,
	}
	resp.Projection = projection

	json.NewEncoder(w).Encode(resp)
}

// readStatusFile reads a daemon status JSON file if it's fresh enough
func (s *Server) readStatusFile(filename string, maxAge time.Duration) json.RawMessage {
	if s.dataDir == "" {
		return nil
	}
	fp := filepath.Join(s.dataDir, filename)
	info, err := os.Stat(fp)
	if err != nil {
		return nil
	}
	if time.Since(info.ModTime()) > maxAge {
		return nil
	}
	data, err := os.ReadFile(fp)
	if err != nil || len(data) == 0 {
		return nil
	}
	return data
}

// handleBTCFuturesStatus returns the current BTC funding rate long strategy status
func (s *Server) handleBTCFuturesStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	fp := filepath.Join(s.dataDir, "btc_futures_status.json")
	data, err := os.ReadFile(fp)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"active": false,
			"error":  "BTC Futures daemon not running",
		})
		return
	}

	info, _ := os.Stat(fp)
	active := info != nil && time.Since(info.ModTime()) < 1*time.Hour

	w.Write([]byte(`{"active":`))
	if active {
		w.Write([]byte("true"))
	} else {
		w.Write([]byte("false"))
	}
	w.Write([]byte(`,"data":`))
	w.Write(data)
	w.Write([]byte("}"))
}

// handleBTCFuturesChartData returns parsed CSV data for BTC Futures charts
func (s *Server) handleBTCFuturesChartData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	signalsDir := filepath.Join(s.dataDir, "btc_signals")

	// days parameter (default 1)
	days := 1
	if d := r.URL.Query().Get("days"); d != "" {
		if v, err := strconv.Atoi(d); err == nil && v > 0 && v <= 7 {
			days = v
		}
	}

	type ScanPoint struct {
		Time          int64   `json:"time"` // unix seconds
		Price         float64 `json:"price"`
		Funding       float64 `json:"funding"`
		RSI           float64 `json:"rsi"`
		ATR           float64 `json:"atr"`
		EMA50         float64 `json:"ema50"`
		Volume        float64 `json:"volume"`
		AvgVol        float64 `json:"avg_volume"`
		OI            float64 `json:"oi"`
		OIChange      float64 `json:"oi_change"`
		OIDivergence  string  `json:"oi_divergence"`
		Signal        string  `json:"signal"`
	}
	type SignalPoint struct {
		Time        int64   `json:"time"`
		Price       float64 `json:"price"`
		OBI5        float64 `json:"obi5"`
		OBI10       float64 `json:"obi10"`
		OBI20       float64 `json:"obi20"`
		BidWall     float64 `json:"bid_wall"`
		AskWall     float64 `json:"ask_wall"`
		Spread      float64 `json:"spread"`
		TakerBuy    float64 `json:"taker_buy"`
		Volume5m    float64 `json:"volume_5m"`
		OI          float64 `json:"oi"`
		FundingRate float64 `json:"funding_rate"`
		LSRatio     float64 `json:"ls_ratio"`
	}
	type TradePoint struct {
		EntryTime    int64   `json:"entry_time"`
		ExitTime     int64   `json:"exit_time"`
		EntryPrice   float64 `json:"entry_price"`
		ExitPrice    float64 `json:"exit_price"`
		NetPnL       float64 `json:"net_pnl"`
		PnLPct       float64 `json:"pnl_pct"`
		CumPnL       float64 `json:"cum_pnl"`
		EntryFunding float64 `json:"entry_funding"`
		EntryRSI     float64 `json:"entry_rsi"`
		ExitReason   string  `json:"exit_reason"`
	}

	resp := struct {
		Scans   []ScanPoint   `json:"scans"`
		Signals []SignalPoint `json:"signals"`
		Trades  []TradePoint  `json:"trades"`
	}{}

	// Parse scan CSVs
	for d := days - 1; d >= 0; d-- {
		date := time.Now().AddDate(0, 0, -d).Format("2006-01-02")
		fp := filepath.Join(signalsDir, "scan_"+date+".csv")
		if rows, err := readCSVFile(fp); err == nil {
			for _, row := range rows {
				if len(row) < 10 {
					continue
				}
				t, _ := time.Parse(time.RFC3339, row[0])
				price, _ := strconv.ParseFloat(row[2], 64)
				funding, _ := strconv.ParseFloat(row[3], 64)
				rsi, _ := strconv.ParseFloat(row[4], 64)
				atr, _ := strconv.ParseFloat(row[5], 64)
				ema50, _ := strconv.ParseFloat(row[6], 64)
				vol, _ := strconv.ParseFloat(row[7], 64)
				avgVol, _ := strconv.ParseFloat(row[8], 64)
				sp := ScanPoint{
					Time: t.Unix(), Price: price, Funding: funding,
					RSI: rsi, ATR: atr, EMA50: ema50,
					Volume: vol, AvgVol: avgVol,
				}
				if len(row) >= 14 {
					// New format with OI columns
					sp.OI, _ = strconv.ParseFloat(row[9], 64)
					sp.OIChange, _ = strconv.ParseFloat(row[10], 64)
					sp.OIDivergence = row[11]
					sp.Signal = row[12]
				} else {
					// Old format without OI
					sp.Signal = row[9]
				}
				resp.Scans = append(resp.Scans, sp)
			}
		}
	}

	// Parse btc_signals CSVs (1-min data, downsample to 5-min for OBI)
	for d := days - 1; d >= 0; d-- {
		date := time.Now().AddDate(0, 0, -d).Format("2006-01-02")
		fp := filepath.Join(signalsDir, "btc_signals_"+date+".csv")
		if rows, err := readCSVFile(fp); err == nil {
			count := 0
			for _, row := range rows {
				if len(row) < 17 {
					continue
				}
				count++
				// Downsample: every 5th row (~5 min)
				if count%5 != 0 {
					continue
				}
				t, _ := time.Parse(time.RFC3339, row[0])
				price, _ := strconv.ParseFloat(row[1], 64)
				obi5, _ := strconv.ParseFloat(row[5], 64)
				obi10, _ := strconv.ParseFloat(row[6], 64)
				obi20, _ := strconv.ParseFloat(row[7], 64)
				bidWall, _ := strconv.ParseFloat(row[8], 64)
				askWall, _ := strconv.ParseFloat(row[9], 64)
				spread, _ := strconv.ParseFloat(row[10], 64)
				takerBuy, _ := strconv.ParseFloat(row[11], 64)
				vol5m, _ := strconv.ParseFloat(row[12], 64)
				oi, _ := strconv.ParseFloat(row[13], 64)
				funding, _ := strconv.ParseFloat(row[15], 64)
				lsRatio, _ := strconv.ParseFloat(row[16], 64)
				resp.Signals = append(resp.Signals, SignalPoint{
					Time: t.Unix(), Price: price,
					OBI5: obi5, OBI10: obi10, OBI20: obi20,
					BidWall: bidWall, AskWall: askWall,
					Spread: spread, TakerBuy: takerBuy,
					Volume5m: vol5m, OI: oi,
					FundingRate: funding, LSRatio: lsRatio,
				})
			}
		}
	}

	// Parse trades from status JSON (recent_trades)
	statusFP := filepath.Join(s.dataDir, "btc_futures_status.json")
	if data, err := os.ReadFile(statusFP); err == nil {
		var status struct {
			RecentTrades []struct {
				EntryTime    time.Time `json:"entry_time"`
				ExitTime     time.Time `json:"exit_time"`
				EntryPrice   float64   `json:"entry_price"`
				ExitPrice    float64   `json:"exit_price"`
				NetPnL       float64   `json:"net_pnl"`
				PnLPct       float64   `json:"pnl_pct"`
				EntryFunding float64   `json:"entry_funding"`
				EntryRSI     float64   `json:"entry_rsi"`
				ExitReason   string    `json:"exit_reason"`
			} `json:"recent_trades"`
		}
		if json.Unmarshal(data, &status) == nil && len(status.RecentTrades) > 0 {
			// Sort by exit time
			sort.Slice(status.RecentTrades, func(i, j int) bool {
				return status.RecentTrades[i].ExitTime.Before(status.RecentTrades[j].ExitTime)
			})
			cumPnL := 0.0
			for _, t := range status.RecentTrades {
				cumPnL += t.NetPnL
				resp.Trades = append(resp.Trades, TradePoint{
					EntryTime:    t.EntryTime.Unix(),
					ExitTime:     t.ExitTime.Unix(),
					EntryPrice:   t.EntryPrice,
					ExitPrice:    t.ExitPrice,
					NetPnL:       t.NetPnL,
					PnLPct:       t.PnLPct,
					CumPnL:       cumPnL,
					EntryFunding: t.EntryFunding,
					EntryRSI:     t.EntryRSI,
					ExitReason:   t.ExitReason,
				})
			}
		}
	}

	json.NewEncoder(w).Encode(resp)
}

// readCSVFile reads a CSV file and returns rows (skipping header).
func readCSVFile(fp string) ([][]string, error) {
	f, err := os.Open(fp)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	var rows [][]string
	first := true
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		if first {
			first = false
			continue // skip header
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// handleDCAFearGreed returns current and historical Fear & Greed data
func (s *Server) handleDCAFearGreed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	fgClient := provider.NewFearGreedClient()

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	current, err := fgClient.GetIndex(ctx)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Get 30-day history
	history, _ := fgClient.GetHistorical(ctx, 30)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"current": current,
		"history": history,
	})
}

// getRealizedPnLByMarket returns net realized PnL per market from trade history.
// Net = sum(sell pnl) - sum(all commissions) per market.
func (s *Server) getRealizedPnLByMarket() map[string]float64 {
	result := make(map[string]float64)
	if s.history == nil {
		return result
	}

	records := s.history.GetAll("")
	// Accumulate gross PnL (sell pnl) and total commissions per market
	grossPnL := make(map[string]float64)
	totalComm := make(map[string]float64)

	for _, r := range records {
		mkt := r.Market // "" for US, "kr" for KR
		totalComm[mkt] += r.Commission
		if r.Side == "sell" {
			grossPnL[mkt] += r.PnL
		}
	}

	for mkt := range grossPnL {
		result[mkt] = grossPnL[mkt] - totalComm[mkt]
	}
	// Include markets with only buys (all commission, no realized PnL)
	for mkt := range totalComm {
		if _, ok := result[mkt]; !ok {
			result[mkt] = -totalComm[mkt]
		}
	}

	return result
}

// handleCollectorStatus returns data collection statistics from the SQLite DB.
func (s *Server) handleCollectorStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	dbPath := filepath.Join(s.dataDir, "traveler.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		json.NewEncoder(w).Encode(map[string]interface{}{"active": false})
		return
	}

	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&mode=ro", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"active": false, "error": err.Error()})
		return
	}
	defer db.Close()

	type TableStat struct {
		Market string `json:"market"`
		Count  int64  `json:"count"`
		Latest int64  `json:"latest"`
	}

	// Candle stats by market
	var candleStats []TableStat
	rows, err := db.Query("SELECT market, COUNT(*), COALESCE(MAX(time),0) FROM candles GROUP BY market")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ts TableStat
			rows.Scan(&ts.Market, &ts.Count, &ts.Latest)
			candleStats = append(candleStats, ts)
		}
	}

	// Orderbook stats by market
	var orderbookStats []TableStat
	rows2, err := db.Query("SELECT market, COUNT(*), COALESCE(MAX(time),0) FROM orderbook GROUP BY market")
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var ts TableStat
			rows2.Scan(&ts.Market, &ts.Count, &ts.Latest)
			orderbookStats = append(orderbookStats, ts)
		}
	}

	// Crypto signals stats
	var signalCount int64
	var signalLatest int64
	db.QueryRow("SELECT COUNT(*), COALESCE(MAX(time),0) FROM crypto_signals").Scan(&signalCount, &signalLatest)

	// DB file size
	var dbSize int64
	if info, err := os.Stat(dbPath); err == nil {
		dbSize = info.Size()
	}

	// Today's candle counts by market+symbol
	todayStart := time.Now().UTC().Truncate(24 * time.Hour).Unix()
	type SymbolStat struct {
		Market string `json:"market"`
		Symbol string `json:"symbol"`
		Count  int64  `json:"count"`
	}
	var todayStats []SymbolStat
	rows3, err := db.Query("SELECT market, symbol, COUNT(*) FROM candles WHERE time >= ? GROUP BY market, symbol ORDER BY market, symbol", todayStart)
	if err == nil {
		defer rows3.Close()
		for rows3.Next() {
			var ss SymbolStat
			rows3.Scan(&ss.Market, &ss.Symbol, &ss.Count)
			todayStats = append(todayStats, ss)
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"active":    true,
		"candles":   candleStats,
		"orderbook": orderbookStats,
		"signals": map[string]interface{}{
			"count":  signalCount,
			"latest": signalLatest,
		},
		"today":   todayStats,
		"db_size": dbSize,
	})
}
