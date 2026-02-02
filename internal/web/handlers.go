package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"traveler/internal/strategy"
	"traveler/internal/symbols"
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
	Strategy     string          `json:"strategy"`
	TotalScanned int             `json:"total_scanned"`
	SignalsFound int             `json:"signals_found"`
	Signals      []SignalWithChart `json:"signals"`
	ScanTime     string          `json:"scan_time"`
	Capital      float64         `json:"capital"`
	TotalInvest  float64         `json:"total_invest"`
	TotalRisk    float64         `json:"total_risk"`
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

// handleScan runs a scan and returns signals with chart data
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	// Parse parameters
	capital := s.capital
	universe := s.universe

	if r.Method == http.MethodGet {
		if c := r.URL.Query().Get("capital"); c != "" {
			if v, err := strconv.ParseFloat(c, 64); err == nil {
				capital = v
			}
		}
		if u := r.URL.Query().Get("universe"); u != "" {
			universe = u
		}
	} else {
		var req ScanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			if req.Capital > 0 {
				capital = req.Capital
			}
			if req.Universe != "" {
				universe = req.Universe
			}
		}
	}

	// Get symbols
	var syms []string
	if universe != "" {
		syms = symbols.GetUniverse(symbols.Universe(universe))
	}
	if len(syms) == 0 {
		syms = symbols.TestSymbols
	}

	// Run scan
	pullbackCfg := strategy.DefaultPullbackConfig()
	strat := strategy.NewPullbackStrategy(pullbackCfg, s.provider)

	startTime := time.Now()
	var signals []SignalWithChart

	for _, sym := range syms {
		select {
		case <-ctx.Done():
			break
		default:
		}

		stock := model.Stock{Symbol: sym, Name: sym}
		signal, err := strat.Analyze(ctx, stock)
		if err == nil && signal != nil {
			// Get candle data for chart
			candles, _ := s.provider.GetDailyCandles(ctx, sym, 100)
			signals = append(signals, SignalWithChart{
				Signal:  *signal,
				Candles: candles,
			})
		}
	}

	// Sort by probability
	sort.Slice(signals, func(i, j int) bool {
		return signals[i].Probability > signals[j].Probability
	})

	// Limit to top 5
	if len(signals) > 5 {
		signals = signals[:5]
	}

	// Calculate position sizing
	var totalInvest, totalRisk float64
	if len(signals) > 0 {
		allocationPerPosition := capital / float64(len(signals))
		riskPerPosition := capital * 0.01 / float64(len(signals))

		for i := range signals {
			if signals[i].Guide != nil {
				g := signals[i].Guide
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
					g.RiskPct = g.RiskAmount / capital * 100
					g.AllocationPct = g.InvestAmount / capital * 100

					totalInvest += g.InvestAmount
					totalRisk += g.RiskAmount
				}
			}
		}
	}

	scanTime := time.Since(startTime)

	resp := ScanResponse{
		Strategy:     "pullback",
		TotalScanned: len(syms),
		SignalsFound: len(signals),
		Signals:      signals,
		ScanTime:     scanTime.Round(time.Second).String(),
		Capital:      capital,
		TotalInvest:  totalInvest,
		TotalRisk:    totalRisk,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
	symbol := strings.ToUpper(strings.TrimSpace(path))
	if symbol == "" {
		http.Error(w, "Symbol required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Get candle data
	candles, err := s.provider.GetDailyCandles(ctx, symbol, 100)
	if err != nil {
		http.Error(w, "Failed to get stock data: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Try to get signal
	stock := model.Stock{Symbol: symbol, Name: symbol}
	pullbackCfg := strategy.DefaultPullbackConfig()
	strat := strategy.NewPullbackStrategy(pullbackCfg, s.provider)
	signal, _ := strat.Analyze(ctx, stock)

	resp := StockResponse{
		Symbol:  symbol,
		Name:    symbol,
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
