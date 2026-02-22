package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"traveler/internal/ai"
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
