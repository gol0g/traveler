package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"traveler/internal/ai"
	"traveler/internal/broker"
	"traveler/internal/config"
	"traveler/internal/provider"
	"traveler/internal/trader"
)

//go:embed static
var staticFiles embed.FS

// scanState tracks background scan progress
type scanState struct {
	Status    string          `json:"status"` // idle, running, done, error
	Message   string          `json:"message"`
	Scanned   int             `json:"scanned"`
	Found     int             `json:"found"`
	StartedAt time.Time       `json:"started_at,omitempty"`
	Error     string          `json:"error,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
}

// Server represents the web server
type Server struct {
	config    *config.Config
	provider  provider.Provider
	capital   float64
	universe  string
	broker    broker.Broker
	planStore *trader.PlanStore
	history   *trader.TradeHistory
	srv       *http.Server
	dataDir   string

	// 국내 시장 지원
	brokerKR   broker.Broker
	providerKR provider.Provider

	// 크립토 시장 지원
	brokerCrypto   broker.Broker
	providerCrypto provider.Provider

	// 모의투자 시장 지원
	brokerSimUS    broker.Broker
	brokerSimKR    broker.Broker
	historySimUS   *trader.TradeHistory
	historySimKR   *trader.TradeHistory
	planStoreSimUS *trader.PlanStore
	planStoreSimKR *trader.PlanStore

	// AI signal filter
	aiClient *ai.GeminiClient

	scan             scanState
	scanKR           scanState
	scanCrypto       scanState
	scanSimUS        scanState
	scanSimKR        scanState
	scanMu           sync.RWMutex
	scanCancel       context.CancelFunc
	scanKRCancel     context.CancelFunc
	scanCryptoCancel context.CancelFunc
}

// SetKoreanMarket 국내 시장 브로커/Provider 설정
func (s *Server) SetKoreanMarket(b broker.Broker, p provider.Provider) {
	s.brokerKR = b
	s.providerKR = p
}

// SetCryptoMarket 크립토 시장 브로커/Provider 설정
func (s *Server) SetCryptoMarket(b broker.Broker, p provider.Provider) {
	s.brokerCrypto = b
	s.providerCrypto = p
}

// SetAIClient sets the Gemini AI client for signal filtering
func (s *Server) SetAIClient(c *ai.GeminiClient) {
	s.aiClient = c
}

// SetSimMarkets 모의투자 브로커/히스토리/planStore 설정
func (s *Server) SetSimMarkets(bUS, bKR broker.Broker, hUS, hKR *trader.TradeHistory) {
	s.brokerSimUS = bUS
	s.brokerSimKR = bKR
	s.historySimUS = hUS
	s.historySimKR = hKR

	// sim PlanStore 초기화 (sim 데이터 디렉토리에서 plans.json 읽기)
	if s.dataDir != "" {
		if bUS != nil {
			if ps, err := trader.NewPlanStore(filepath.Join(s.dataDir, "sim_us")); err == nil {
				s.planStoreSimUS = ps
			}
		}
		if bKR != nil {
			if ps, err := trader.NewPlanStore(filepath.Join(s.dataDir, "sim_kr")); err == nil {
				s.planStoreSimKR = ps
			}
		}
	}
}

// NewServer creates a new web server
func NewServer(cfg *config.Config, p provider.Provider, capital float64, universe string, b broker.Broker, dataDir string) *Server {
	s := &Server{
		config:   cfg,
		provider: p,
		capital:  capital,
		universe: universe,
		broker:   b,
		dataDir:  dataDir,
		scan:     scanState{Status: "idle"},
	}

	if b != nil && dataDir != "" {
		ps, err := trader.NewPlanStore(dataDir)
		if err == nil {
			s.planStore = ps
		} else {
			log.Printf("[WEB] Warning: could not load PlanStore: %v", err)
		}
	}

	if dataDir != "" {
		h, err := trader.NewTradeHistory(dataDir)
		if err == nil {
			s.history = h
		} else {
			log.Printf("[WEB] Warning: could not load TradeHistory: %v", err)
		}
	}

	// Load last scan result from disk
	s.loadScanResultFromDisk()

	return s
}

// Start starts the web server on the specified port
func (s *Server) Start(port int) error {
	mux := http.NewServeMux()

	// Scan routes (async polling)
	mux.HandleFunc("/api/scan", s.handleScan)
	mux.HandleFunc("/api/scan/status", s.handleScanStatus)
	mux.HandleFunc("/api/scan/result", s.handleScanResult)

	// Other API routes
	mux.HandleFunc("/api/signals", s.handleSignals)
	mux.HandleFunc("/api/stock/", s.handleStock)
	mux.HandleFunc("/api/portfolio", s.handlePortfolio)
	mux.HandleFunc("/api/universes", s.handleUniverses)
	mux.HandleFunc("/api/positions", s.handlePositions)
	mux.HandleFunc("/api/balance", s.handleBalance)
	mux.HandleFunc("/api/orders", s.handleOrders)
	mux.HandleFunc("/api/trade-history", s.handleTradeHistory)
	mux.HandleFunc("/api/dca/status", s.handleDCAStatus)
	mux.HandleFunc("/api/dca/feargreed", s.handleDCAFearGreed)
	mux.HandleFunc("/api/scalp/status", s.handleScalpStatus)

	// Static files (no-cache to prevent stale JS)
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("failed to create static file system: %w", err)
	}
	fileServer := http.FileServer(http.FS(staticFS))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		fileServer.ServeHTTP(w, r)
	}))

	s.srv = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      corsMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("Starting Traveler Web UI at http://localhost:%d", port)
	log.Printf("Press Ctrl+C to stop")

	return s.srv.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv != nil {
		return s.srv.Shutdown(ctx)
	}
	return nil
}

// updateScanProgress thread-safely updates scan progress
func (s *Server) updateScanProgress(message string, scanned, found int) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.scan.Message = message
	s.scan.Scanned = scanned
	s.scan.Found = found
}

// updateScanKRProgress thread-safely updates KR scan progress
func (s *Server) updateScanKRProgress(message string, scanned, found int) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.scanKR.Message = message
	s.scanKR.Scanned = scanned
	s.scanKR.Found = found
}

// getScanState returns the appropriate scan state for the market
func (s *Server) getScanState(market string) scanState {
	s.scanMu.RLock()
	defer s.scanMu.RUnlock()
	switch market {
	case "kr":
		return s.scanKR
	case "crypto":
		return s.scanCrypto
	case "sim-us":
		return s.scanSimUS
	case "sim-kr":
		return s.scanSimKR
	default:
		return s.scan
	}
}

// handleScanStatus returns current scan state (for polling)
func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	market := r.URL.Query().Get("market")
	state := s.getScanState(market)

	// idle 상태이면 디스크에서 로드 시도
	if state.Status == "idle" || (state.Status == "" && state.Result == nil) {
		if data := s.tryLoadFromDisk(market); data != nil {
			state = s.getScanState(market) // 다시 읽기
		}
	}

	resp := struct {
		Status    string `json:"status"`
		Message   string `json:"message"`
		Scanned   int    `json:"scanned"`
		Found     int    `json:"found"`
		Error     string `json:"error,omitempty"`
		ElapsedMs int64  `json:"elapsed_ms,omitempty"`
	}{
		Status:  state.Status,
		Message: state.Message,
		Scanned: state.Scanned,
		Found:   state.Found,
		Error:   state.Error,
	}
	if !state.StartedAt.IsZero() {
		resp.ElapsedMs = time.Since(state.StartedAt).Milliseconds()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleScanResult returns the completed scan result
func (s *Server) handleScanResult(w http.ResponseWriter, r *http.Request) {
	market := r.URL.Query().Get("market")
	state := s.getScanState(market)

	w.Header().Set("Content-Type", "application/json")

	// 메모리에 결과가 있으면 바로 반환
	if state.Status == "done" && state.Result != nil {
		w.Write(state.Result)
		return
	}

	// 메모리에 없으면 디스크에서 직접 로드 (데몬이 별도 프로세스로 결과를 썼을 수 있음)
	data := s.tryLoadFromDisk(market)
	if data != nil {
		w.Write(data)
		return
	}

	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{"error": "no scan result available"})
}

// tryLoadFromDisk 디스크에서 스캔 결과를 읽고 메모리에 캐시
func (s *Server) tryLoadFromDisk(market string) json.RawMessage {
	path := s.scanResultPath(market)
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if time.Since(info.ModTime()) > 24*time.Hour {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	// 메모리에 캐시
	msg := fmt.Sprintf("Loaded from disk (%s)", info.ModTime().Format("15:04"))
	s.scanMu.Lock()
	switch market {
	case "kr":
		s.scanKR.Status = "done"
		s.scanKR.Result = data
		s.scanKR.Message = msg
	case "crypto":
		s.scanCrypto.Status = "done"
		s.scanCrypto.Result = data
		s.scanCrypto.Message = msg
	case "sim-us":
		s.scanSimUS.Status = "done"
		s.scanSimUS.Result = data
		s.scanSimUS.Message = msg
	case "sim-kr":
		s.scanSimKR.Status = "done"
		s.scanSimKR.Result = data
		s.scanSimKR.Message = msg
	default:
		s.scan.Status = "done"
		s.scan.Result = data
		s.scan.Message = msg
	}
	s.scanMu.Unlock()
	log.Printf("[WEB] Loaded %s scan result from disk (%s)", market, path)
	return data
}

func (s *Server) scanResultPath(market string) string {
	if s.dataDir == "" {
		return ""
	}
	switch market {
	case "kr":
		return filepath.Join(s.dataDir, "last_scan_kr.json")
	case "crypto":
		return filepath.Join(s.dataDir, "last_scan_crypto.json")
	case "sim-us":
		return filepath.Join(s.dataDir, "sim_us", "last_scan_us.json")
	case "sim-kr":
		return filepath.Join(s.dataDir, "sim_kr", "last_scan_kr.json")
	default:
		return filepath.Join(s.dataDir, "last_scan_us.json")
	}
}

func (s *Server) saveScanResultToDisk(data json.RawMessage, market string) {
	path := s.scanResultPath(market)
	if path == "" {
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("[WEB] Failed to save scan result: %v", err)
	}
}

func (s *Server) loadScanResultFromDisk() {
	// Load US, KR, crypto, and sim results — store them separately
	for _, market := range []string{"us", "kr", "crypto", "sim-us", "sim-kr"} {
		path := s.scanResultPath(market)
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > 24*time.Hour {
			continue
		}
		loadMsg := fmt.Sprintf("Loaded from disk (%s)", info.ModTime().Format("15:04"))
		switch market {
		case "kr":
			s.scanKR.Status = "done"
			s.scanKR.Result = data
			s.scanKR.Message = loadMsg
		case "crypto":
			s.scanCrypto.Status = "done"
			s.scanCrypto.Result = data
			s.scanCrypto.Message = loadMsg
		case "sim-us":
			s.scanSimUS.Status = "done"
			s.scanSimUS.Result = data
			s.scanSimUS.Message = loadMsg
		case "sim-kr":
			s.scanSimKR.Status = "done"
			s.scanSimKR.Result = data
			s.scanSimKR.Message = loadMsg
		default:
			s.scan.Status = "done"
			s.scan.Result = data
			s.scan.Message = loadMsg
		}
		log.Printf("[WEB] Loaded %s scan result from %s", market, path)
	}

	// Migrate old single file if exists
	oldPath := filepath.Join(s.dataDir, "last_scan.json")
	if _, err := os.Stat(oldPath); err == nil {
		data, _ := os.ReadFile(oldPath)
		if len(data) > 0 {
			// Check if it's KR or US
			if bytes.Contains(data, []byte(`"multi-kr"`)) {
				if s.scanKR.Result == nil {
					s.scanKR.Status = "done"
					s.scanKR.Result = data
					s.scanKR.Message = "Loaded from disk (migrated)"
				}
			} else {
				if s.scan.Result == nil {
					s.scan.Status = "done"
					s.scan.Result = data
					s.scan.Message = "Loaded from disk (migrated)"
				}
			}
		}
	}
}

// corsMiddleware adds CORS headers for local development
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
