package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	yahooUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
)

// FundamentalsData holds fundamental metrics for a stock
type FundamentalsData struct {
	Symbol          string   `json:"symbol"`
	MarketCap       float64  `json:"marketCap"`
	TrailingPE      float64  `json:"trailingPE"`
	ForwardPE       float64  `json:"forwardPE"`
	ProfitMargins   float64  `json:"profitMargins"`
	DebtToEquity    float64  `json:"debtToEquity"`
	FiftyTwoWeekChg float64  `json:"fiftyTwoWeekChg"`
	RevenueGrowth   float64  `json:"revenueGrowth"`
	ReturnOnEquity  float64  `json:"returnOnEquity"`
	PassFilter      bool     `json:"passFilter"`
	RejectReasons   []string `json:"rejectReasons,omitempty"`
	FetchedAt       string   `json:"fetchedAt"`
}

// FundamentalsChecker fetches and filters stocks by Yahoo Finance fundamentals
type FundamentalsChecker struct {
	client     *http.Client
	crumb      string
	cacheDir   string
	kosdaqSyms map[string]bool // KOSDAQ symbols for .KQ suffix
	cache      map[string]FundamentalsData
	mu         sync.Mutex
}

// Yahoo Finance API response types (same as PoC)
type quoteSummaryResponse struct {
	QuoteSummary struct {
		Result []struct {
			FinancialData   *yfFinancialData   `json:"financialData"`
			DefaultKeyStats *yfDefaultKeyStats `json:"defaultKeyStatistics"`
			SummaryDetail   *yfSummaryDetail   `json:"summaryDetail"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"quoteSummary"`
}

type yfValue struct {
	Raw float64 `json:"raw"`
	Fmt string  `json:"fmt"`
}

type yfFinancialData struct {
	ProfitMargins  yfValue `json:"profitMargins"`
	DebtToEquity   yfValue `json:"debtToEquity"`
	RevenueGrowth  yfValue `json:"revenueGrowth"`
	ReturnOnEquity yfValue `json:"returnOnEquity"`
}

type yfDefaultKeyStats struct {
	FiftyTwoWeekChange yfValue `json:"52WeekChange"`
}

type yfSummaryDetail struct {
	MarketCap  yfValue `json:"marketCap"`
	TrailingPE yfValue `json:"trailingPE"`
	ForwardPE  yfValue `json:"forwardPE"`
}

// NewFundamentalsChecker creates a new checker with daily file cache.
// kosdaqSyms: set of KOSDAQ symbol codes (for .KQ suffix). Pass nil for US-only.
func NewFundamentalsChecker(cacheDir string, kosdaqSyms map[string]bool) *FundamentalsChecker {
	jar, _ := cookiejar.New(nil)
	f := &FundamentalsChecker{
		client: &http.Client{
			Timeout: 15 * time.Second,
			Jar:     jar,
		},
		cacheDir:   cacheDir,
		kosdaqSyms: kosdaqSyms,
		cache:      make(map[string]FundamentalsData),
	}
	f.loadDayCache()
	return f
}

// Init acquires Yahoo Finance crumb (must be called before Check/FilterSignals)
func (f *FundamentalsChecker) Init(ctx context.Context) error {
	// Step 1: Visit fc.yahoo.com for cookies
	req, err := http.NewRequestWithContext(ctx, "GET", "https://fc.yahoo.com", nil)
	if err != nil {
		return fmt.Errorf("creating cookie request: %w", err)
	}
	req.Header.Set("User-Agent", yahooUserAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetching cookies: %w", err)
	}
	resp.Body.Close()

	// Step 2: Get crumb
	req, err = http.NewRequestWithContext(ctx, "GET", "https://query2.finance.yahoo.com/v1/test/getcrumb", nil)
	if err != nil {
		return fmt.Errorf("creating crumb request: %w", err)
	}
	req.Header.Set("User-Agent", yahooUserAgent)
	resp, err = f.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetching crumb: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	crumb := strings.TrimSpace(string(body))
	if crumb == "" || strings.Contains(crumb, "error") || strings.Contains(crumb, "Unauthorized") {
		return fmt.Errorf("invalid crumb: %s", crumb)
	}

	f.crumb = crumb
	log.Printf("[FUNDAMENTALS] Crumb acquired successfully")
	return nil
}

// Check fetches fundamentals for a single symbol (cache-first)
func (f *FundamentalsChecker) Check(ctx context.Context, symbol string) (*FundamentalsData, error) {
	f.mu.Lock()
	if cached, ok := f.cache[symbol]; ok {
		f.mu.Unlock()
		return &cached, nil
	}
	f.mu.Unlock()

	// Convert to Yahoo Finance symbol
	yahooSym := f.toYahooSymbol(symbol)
	isKR := isKoreanSymbol(symbol)

	// Fetch from API
	data, err := f.fetchFromAPI(ctx, yahooSym)
	if err != nil {
		return nil, err
	}

	// Apply filter
	result := f.applyFilter(symbol, data, isKR)

	// Cache result
	f.mu.Lock()
	f.cache[symbol] = *result
	f.mu.Unlock()
	f.saveDayCache()

	return result, nil
}

// FilterSymbols checks fundamentals for given symbols, returns rejected symbols with reasons
func (f *FundamentalsChecker) FilterSymbols(ctx context.Context, syms []string) map[string][]string {
	rejected := make(map[string][]string)
	for _, sym := range syms {
		data, err := f.Check(ctx, sym)
		if err != nil {
			log.Printf("[FUNDAMENTALS] %s: check failed (%v), passing through", sym, err)
			continue
		}
		if data.PassFilter {
			log.Printf("[FUNDAMENTALS] %s: PASS", sym)
		} else {
			rejected[sym] = data.RejectReasons
			log.Printf("[FUNDAMENTALS] %s: REJECT — %s", sym, strings.Join(data.RejectReasons, "; "))
		}
		// Brief delay between API calls
		time.Sleep(500 * time.Millisecond)
	}
	return rejected
}

// fetchFromAPI calls Yahoo Finance quoteSummary API
func (f *FundamentalsChecker) fetchFromAPI(ctx context.Context, yahooSymbol string) (*quoteSummaryResponse, error) {
	modules := "financialData,defaultKeyStatistics,summaryDetail"
	url := fmt.Sprintf("https://query2.finance.yahoo.com/v10/finance/quoteSummary/%s?modules=%s&crumb=%s",
		yahooSymbol, modules, f.crumb)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", yahooUserAgent)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var data quoteSummaryResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	if data.QuoteSummary.Error != nil {
		return nil, fmt.Errorf("API error: %s", data.QuoteSummary.Error.Description)
	}

	if len(data.QuoteSummary.Result) == 0 {
		return nil, fmt.Errorf("no data for %s", yahooSymbol)
	}

	return &data, nil
}

// applyFilter evaluates fundamental data against filter criteria
func (f *FundamentalsChecker) applyFilter(symbol string, data *quoteSummaryResponse, isKR bool) *FundamentalsData {
	r := data.QuoteSummary.Result[0]
	result := &FundamentalsData{
		Symbol:    symbol,
		FetchedAt: time.Now().Format("2006-01-02 15:04"),
	}

	pass := true
	var reasons []string

	if r.SummaryDetail != nil {
		result.MarketCap = r.SummaryDetail.MarketCap.Raw
		result.TrailingPE = r.SummaryDetail.TrailingPE.Raw
		result.ForwardPE = r.SummaryDetail.ForwardPE.Raw

		// Market cap filter: $200M for US, ₩200B for KR
		minCap := 200_000_000.0 // $200M
		if isKR {
			minCap = 200_000_000_000.0 // ₩200B
		}
		if result.MarketCap > 0 && result.MarketCap < minCap {
			pass = false
			if isKR {
				reasons = append(reasons, fmt.Sprintf("MarketCap ₩%.0f (< ₩200B)", result.MarketCap))
			} else {
				reasons = append(reasons, fmt.Sprintf("MarketCap $%.0f (< $200M)", result.MarketCap))
			}
		}
	}

	if r.FinancialData != nil {
		result.ProfitMargins = r.FinancialData.ProfitMargins.Raw
		result.DebtToEquity = r.FinancialData.DebtToEquity.Raw
		result.RevenueGrowth = r.FinancialData.RevenueGrowth.Raw
		result.ReturnOnEquity = r.FinancialData.ReturnOnEquity.Raw

		if result.DebtToEquity > 200 {
			pass = false
			reasons = append(reasons, fmt.Sprintf("D/E %.0f (> 200)", result.DebtToEquity))
		}
		if result.ProfitMargins < -0.1 {
			pass = false
			reasons = append(reasons, fmt.Sprintf("ProfitMargin %.1f%% (< -10%%)", result.ProfitMargins*100))
		}
	}

	if r.DefaultKeyStats != nil {
		result.FiftyTwoWeekChg = r.DefaultKeyStats.FiftyTwoWeekChange.Raw

		if result.FiftyTwoWeekChg < -0.3 {
			pass = false
			reasons = append(reasons, fmt.Sprintf("52W %.0f%% (< -30%%)", result.FiftyTwoWeekChg*100))
		}
	}

	result.PassFilter = pass
	result.RejectReasons = reasons
	return result
}

// toYahooSymbol converts internal symbol to Yahoo Finance format
func (f *FundamentalsChecker) toYahooSymbol(symbol string) string {
	if !isKoreanSymbol(symbol) {
		return symbol // US symbols as-is
	}
	if f.kosdaqSyms != nil && f.kosdaqSyms[symbol] {
		return symbol + ".KQ"
	}
	return symbol + ".KS"
}

// isKoreanSymbol checks if symbol is a 6-digit numeric KR code
func isKoreanSymbol(sym string) bool {
	if len(sym) != 6 {
		return false
	}
	for _, c := range sym {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// loadDayCache loads today's cache from disk
func (f *FundamentalsChecker) loadDayCache() {
	path := f.cachePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return // No cache for today
	}

	var cache map[string]FundamentalsData
	if err := json.Unmarshal(data, &cache); err != nil {
		return
	}

	f.mu.Lock()
	f.cache = cache
	f.mu.Unlock()
	log.Printf("[FUNDAMENTALS] Loaded %d cached entries from %s", len(cache), filepath.Base(path))

	// Clean old cache files
	f.cleanOldCache()
}

// saveDayCache saves today's cache to disk
func (f *FundamentalsChecker) saveDayCache() {
	f.mu.Lock()
	data, err := json.MarshalIndent(f.cache, "", "  ")
	f.mu.Unlock()
	if err != nil {
		return
	}

	path := f.cachePath()
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, data, 0644)
}

// cachePath returns today's cache file path
func (f *FundamentalsChecker) cachePath() string {
	today := time.Now().Format("2006-01-02")
	return filepath.Join(f.cacheDir, fmt.Sprintf("fundamentals_%s.json", today))
}

// cleanOldCache removes cache files older than 3 days
func (f *FundamentalsChecker) cleanOldCache() {
	pattern := filepath.Join(f.cacheDir, "fundamentals_*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -3)
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(path)
			log.Printf("[FUNDAMENTALS] Cleaned old cache: %s", filepath.Base(path))
		}
	}
}
