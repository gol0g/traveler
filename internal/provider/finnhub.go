package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"traveler/internal/ratelimit"
	"traveler/pkg/model"
)

const finnhubBaseURL = "https://finnhub.io/api/v1"

// FinnhubProvider implements the Provider interface for Finnhub API
type FinnhubProvider struct {
	apiKey    string
	client    *http.Client
	limiter   *ratelimit.Limiter
	rateLimit int
}

// NewFinnhubProvider creates a new Finnhub provider
func NewFinnhubProvider(apiKey string, rateLimitPerMin int) *FinnhubProvider {
	return &FinnhubProvider{
		apiKey:    apiKey,
		client:    &http.Client{Timeout: 30 * time.Second},
		limiter:   ratelimit.NewLimiter("finnhub", rateLimitPerMin),
		rateLimit: rateLimitPerMin,
	}
}

// Name returns the provider name
func (p *FinnhubProvider) Name() string {
	return "finnhub"
}

// IsAvailable checks if the provider has an API key
func (p *FinnhubProvider) IsAvailable() bool {
	return p.apiKey != ""
}

// RateLimit returns the rate limit per minute
func (p *FinnhubProvider) RateLimit() int {
	return p.rateLimit
}

// finnhubCandle represents the Finnhub candle response
type finnhubCandle struct {
	C []float64 `json:"c"` // Close prices
	H []float64 `json:"h"` // High prices
	L []float64 `json:"l"` // Low prices
	O []float64 `json:"o"` // Open prices
	S string    `json:"s"` // Status
	T []int64   `json:"t"` // Timestamps
	V []int64   `json:"v"` // Volumes
}

// finnhubSymbol represents a stock symbol from Finnhub
type finnhubSymbol struct {
	Symbol      string `json:"symbol"`
	Description string `json:"description"`
	Type        string `json:"type"`
}

// GetIntradayData fetches intraday candle data for a symbol
func (p *FinnhubProvider) GetIntradayData(ctx context.Context, symbol string, date time.Time, interval int) (*model.IntradayData, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	// Calculate start and end timestamps for the trading day
	// NYSE/NASDAQ: 9:30 AM - 4:00 PM ET
	loc, _ := time.LoadLocation("America/New_York")
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 9, 30, 0, 0, loc)
	endOfDay := time.Date(date.Year(), date.Month(), date.Day(), 16, 0, 0, 0, loc)

	url := fmt.Sprintf("%s/stock/candle?symbol=%s&resolution=%d&from=%d&to=%d&token=%s",
		finnhubBaseURL, symbol, interval, startOfDay.Unix(), endOfDay.Unix(), p.apiKey)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, &ProviderError{Provider: p.Name(), Err: err, Retryable: true}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		p.limiter.SignalRateLimited()
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("rate limited"), Retryable: true}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("status %d", resp.StatusCode), Retryable: false}
	}

	p.limiter.ResetBackoff()

	var data finnhubCandle
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if data.S != "ok" || len(data.T) == 0 {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("no data available"), Retryable: false}
	}

	candles := make([]model.Candle, len(data.T))
	for i := range data.T {
		candles[i] = model.Candle{
			Time:   time.Unix(data.T[i], 0),
			Open:   data.O[i],
			High:   data.H[i],
			Low:    data.L[i],
			Close:  data.C[i],
			Volume: data.V[i],
		}
	}

	return &model.IntradayData{
		Symbol:  symbol,
		Date:    date,
		Candles: candles,
	}, nil
}

// GetMultiDayIntraday fetches intraday data for multiple days
func (p *FinnhubProvider) GetMultiDayIntraday(ctx context.Context, symbol string, days int, interval int) ([]model.IntradayData, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	loc, _ := time.LoadLocation("America/New_York")
	now := time.Now().In(loc)

	// Calculate date range
	endDate := now
	startDate := now.AddDate(0, 0, -days*2) // Extra buffer for weekends/holidays

	url := fmt.Sprintf("%s/stock/candle?symbol=%s&resolution=%d&from=%d&to=%d&token=%s",
		finnhubBaseURL, symbol, interval, startDate.Unix(), endDate.Unix(), p.apiKey)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, &ProviderError{Provider: p.Name(), Err: err, Retryable: true}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		p.limiter.SignalRateLimited()
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("rate limited"), Retryable: true}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("status %d", resp.StatusCode), Retryable: false}
	}

	p.limiter.ResetBackoff()

	var data finnhubCandle
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if data.S != "ok" || len(data.T) == 0 {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("no data available"), Retryable: false}
	}

	// Group candles by date
	dayMap := make(map[string][]model.Candle)
	for i := range data.T {
		t := time.Unix(data.T[i], 0).In(loc)
		dateKey := t.Format("2006-01-02")

		candle := model.Candle{
			Time:   t,
			Open:   data.O[i],
			High:   data.H[i],
			Low:    data.L[i],
			Close:  data.C[i],
			Volume: data.V[i],
		}
		dayMap[dateKey] = append(dayMap[dateKey], candle)
	}

	// Convert to sorted list of IntradayData
	var result []model.IntradayData
	for dateKey, candles := range dayMap {
		date, _ := time.ParseInLocation("2006-01-02", dateKey, loc)
		// Sort candles by time
		sort.Slice(candles, func(i, j int) bool {
			return candles[i].Time.Before(candles[j].Time)
		})
		result = append(result, model.IntradayData{
			Symbol:  symbol,
			Date:    date,
			Candles: candles,
		})
	}

	// Sort by date descending (most recent first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date.After(result[j].Date)
	})

	// Limit to requested days
	if len(result) > days {
		result = result[:days]
	}

	return result, nil
}

// GetSymbols returns the list of symbols for the given exchange
func (p *FinnhubProvider) GetSymbols(ctx context.Context, exchange string) ([]model.Stock, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/stock/symbol?exchange=%s&token=%s", finnhubBaseURL, exchange, p.apiKey)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, &ProviderError{Provider: p.Name(), Err: err, Retryable: true}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		p.limiter.SignalRateLimited()
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("rate limited"), Retryable: true}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("status %d", resp.StatusCode), Retryable: false}
	}

	p.limiter.ResetBackoff()

	var symbols []finnhubSymbol
	if err := json.NewDecoder(resp.Body).Decode(&symbols); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	result := make([]model.Stock, 0, len(symbols))
	for _, s := range symbols {
		// Filter to common stocks only
		if s.Type == "Common Stock" || s.Type == "" {
			result = append(result, model.Stock{
				Symbol:   s.Symbol,
				Name:     s.Description,
				Exchange: exchange,
			})
		}
	}

	return result, nil
}

// GetDailyCandles fetches daily OHLCV data
func (p *FinnhubProvider) GetDailyCandles(ctx context.Context, symbol string, days int) ([]model.Candle, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	now := time.Now()
	from := now.AddDate(0, 0, -days*2) // Buffer for weekends

	url := fmt.Sprintf("%s/stock/candle?symbol=%s&resolution=D&from=%d&to=%d&token=%s",
		finnhubBaseURL, symbol, from.Unix(), now.Unix(), p.apiKey)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, &ProviderError{Provider: p.Name(), Err: err, Retryable: true}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		p.limiter.SignalRateLimited()
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("rate limited"), Retryable: true}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("status %d", resp.StatusCode), Retryable: false}
	}

	p.limiter.ResetBackoff()

	var data finnhubCandle
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if data.S == "no_data" || len(data.T) == 0 {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("no data available"), Retryable: false}
	}

	loc, _ := time.LoadLocation("America/New_York")
	candles := make([]model.Candle, 0, len(data.T))
	for i := range data.T {
		if i >= len(data.O) || i >= len(data.H) || i >= len(data.L) || i >= len(data.C) {
			continue
		}

		var volume int64
		if i < len(data.V) {
			volume = data.V[i]
		}

		candles = append(candles, model.Candle{
			Time:   time.Unix(data.T[i], 0).In(loc),
			Open:   data.O[i],
			High:   data.H[i],
			Low:    data.L[i],
			Close:  data.C[i],
			Volume: volume,
		})
	}

	// Sort by date ascending
	sort.Slice(candles, func(i, j int) bool {
		return candles[i].Time.Before(candles[j].Time)
	})

	if len(candles) > days {
		candles = candles[len(candles)-days:]
	}

	return candles, nil
}
