package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"traveler/internal/provider"
	"traveler/internal/strategy"
	"traveler/internal/symbols"
	"traveler/internal/trader"
	"traveler/pkg/model"
)

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
	AvgProb       float64          `json:"avg_prob,omitempty"`
}

// SignalWithChart extends Signal with chart data
type SignalWithChart struct {
	strategy.Signal
	Candles []model.Candle `json:"candles,omitempty"`
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

// webStockLoader implements trader.StockLoader for web scanning
type webStockLoader struct {
	korean bool // true이면 한국 유니버스에서 종목명 적용
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

	// Check if scan already running for this market
	s.scanMu.RLock()
	running := false
	if market == "kr" {
		running = s.scanKR.Status == "running"
	} else {
		running = s.scan.Status == "running"
	}
	s.scanMu.RUnlock()
	if running {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "already_running"})
		return
	}

	// Parse capital
	capital := s.capital
	if c := r.URL.Query().Get("capital"); c != "" {
		if v, err := strconv.ParseFloat(c, 64); err == nil {
			capital = v
		}
	}

	// Init scan state per market
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	s.scanMu.Lock()
	if market == "kr" {
		s.scanKRCancel = cancel
		s.scanKR = scanState{
			Status:    "running",
			Message:   "Starting KR adaptive scan...",
			StartedAt: time.Now(),
		}
	} else {
		s.scanCancel = cancel
		s.scan = scanState{
			Status:    "running",
			Message:   "Starting adaptive multi-strategy scan...",
			StartedAt: time.Now(),
		}
	}
	s.scanMu.Unlock()

	if market == "kr" {
		log.Printf("[WEB] KR scan starting (capital=₩%.0f)", capital)
		go s.runKRScanAsync(ctx, cancel, capital)
	} else {
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

	strategies := strategy.GetAll(cachedProvider)
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

	result, err := scanner.Scan(ctx, &webStockLoader{})
	if err != nil {
		log.Printf("[WEB] Scan error: %v", err)
		s.scanMu.Lock()
		s.scan.Status = "error"
		s.scan.Error = err.Error()
		s.scanMu.Unlock()
		return
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
		signals = append(signals, SignalWithChart{
			Signal:  sig,
			Candles: candles,
		})
		if sig.Guide != nil {
			totalInvest += sig.Guide.InvestAmount
			totalRisk += sig.Guide.RiskAmount
		}
	}

	scanTime := time.Since(startTime)
	log.Printf("[WEB] Scan complete: %d signals from %v in %s (decision: %s)",
		len(signals), result.UniversesUsed, scanTime.Round(time.Second), result.Decision)

	resp := ScanResponse{
		Strategy:      "multi",
		TotalScanned:  result.ScannedCount,
		SignalsFound:  len(signals),
		Signals:       signals,
		ScanTime:      scanTime.Round(time.Second).String(),
		Capital:       capital,
		TotalInvest:   totalInvest,
		TotalRisk:     totalRisk,
		UniversesUsed: result.UniversesUsed,
		Decision:      result.Decision,
		Expansions:    result.Expansions,
		AvgProb:       result.Quality.AvgProb,
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
	strategies := strategy.GetAll(cachedProvider)
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
	scanner.SetTierFunc(func(balance float64) []trader.UniverseTier {
		return trader.GetKRUniverseTiers(balance)
	})

	result, err := scanner.Scan(ctx, &webStockLoader{korean: true})
	if err != nil {
		log.Printf("[WEB] KR Scan error: %v", err)
		s.scanMu.Lock()
		s.scanKR.Status = "error"
		s.scanKR.Error = err.Error()
		s.scanMu.Unlock()
		return
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
		signals = append(signals, SignalWithChart{
			Signal:  sig,
			Candles: candles,
		})
		if sig.Guide != nil {
			totalInvest += sig.Guide.InvestAmount
			totalRisk += sig.Guide.RiskAmount
		}
	}

	scanTime := time.Since(startTime)
	log.Printf("[WEB] KR Scan complete: %d signals from %v in %s",
		len(signals), result.UniversesUsed, scanTime.Round(time.Second))

	resp := ScanResponse{
		Strategy:      "multi-kr",
		TotalScanned:  result.ScannedCount,
		SignalsFound:  len(signals),
		Signals:       signals,
		ScanTime:      scanTime.Round(time.Second).String(),
		Capital:       capital,
		TotalInvest:   totalInvest,
		TotalRisk:     totalRisk,
		UniversesUsed: result.UniversesUsed,
		Decision:      result.Decision,
		Expansions:    result.Expansions,
		AvgProb:       result.Quality.AvgProb,
	}

	respJSON, _ := json.Marshal(resp)

	s.scanMu.Lock()
	s.scanKR.Status = "done"
	s.scanKR.Message = fmt.Sprintf("KR Complete: %d signals in %s", len(signals), scanTime.Round(time.Second))
	s.scanKR.Result = respJSON
	s.scanMu.Unlock()

	s.saveScanResultToDisk(respJSON, "kr")
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

	// Use KR provider for Korean symbols
	prov := s.provider
	if symbols.IsKoreanSymbol(symbol) && s.providerKR != nil {
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
	if symbols.IsKoreanSymbol(symbol) {
		stockName = symbols.GetKRSymbolName(symbol)
	}
	stock := model.Stock{Symbol: symbol, Name: stockName}
	pullbackCfg := strategy.DefaultPullbackConfig()
	strat := strategy.NewPullbackStrategy(pullbackCfg, prov)
	signal, _ := strat.Analyze(ctx, stock)

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
					sharesByRisk := int(riskPerPosition / riskPerShare)
					sharesByAllocation := int(allocationPerPosition / g.EntryPrice)
					g.PositionSize = sharesByRisk
					if sharesByAllocation < sharesByRisk {
						g.PositionSize = sharesByAllocation
					}
					if g.PositionSize < 1 {
						g.PositionSize = 1
					}
					g.InvestAmount = float64(g.PositionSize) * g.EntryPrice
					g.RiskAmount = float64(g.PositionSize) * riskPerShare
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
	Quantity      int     `json:"quantity"`
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
	Quantity  int     `json:"quantity"`
	FilledQty int     `json:"filled_qty"`
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
	b := s.broker
	if market == "kr" {
		b = s.brokerKR
	}

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

	// Reload PlanStore from disk for freshness
	var plans map[string]*trader.PositionPlan
	if s.planStore != nil {
		s.planStore.Reload()
		plans = s.planStore.All()
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
	b := s.broker
	if market == "kr" {
		b = s.brokerKR
	}

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
	b := s.broker
	if market == "kr" {
		b = s.brokerKR
	}

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
