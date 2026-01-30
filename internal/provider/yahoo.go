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

const yahooBaseURL = "https://query1.finance.yahoo.com/v8/finance/chart"

// YahooProvider implements the Provider interface for Yahoo Finance (unofficial API)
type YahooProvider struct {
	client    *http.Client
	limiter   *ratelimit.Limiter
	rateLimit int
}

// NewYahooProvider creates a new Yahoo Finance provider
func NewYahooProvider() *YahooProvider {
	return &YahooProvider{
		client:    &http.Client{Timeout: 30 * time.Second},
		limiter:   ratelimit.NewLimiter("yahoo", 30), // Conservative rate limit
		rateLimit: 30,
	}
}

// Name returns the provider name
func (p *YahooProvider) Name() string {
	return "yahoo"
}

// IsAvailable always returns true (no API key needed)
func (p *YahooProvider) IsAvailable() bool {
	return true
}

// RateLimit returns the rate limit per minute
func (p *YahooProvider) RateLimit() int {
	return p.rateLimit
}

// yahooResponse represents the Yahoo Finance API response
type yahooResponse struct {
	Chart struct {
		Result []struct {
			Meta struct {
				Symbol string `json:"symbol"`
			} `json:"meta"`
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Open   []float64 `json:"open"`
					High   []float64 `json:"high"`
					Low    []float64 `json:"low"`
					Close  []float64 `json:"close"`
					Volume []int64   `json:"volume"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// GetIntradayData fetches intraday candle data for a symbol
func (p *YahooProvider) GetIntradayData(ctx context.Context, symbol string, date time.Time, interval int) (*model.IntradayData, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	loc, _ := time.LoadLocation("America/New_York")
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 9, 30, 0, 0, loc)
	endOfDay := time.Date(date.Year(), date.Month(), date.Day(), 16, 0, 0, 0, loc)

	intervalStr := fmt.Sprintf("%dm", interval)
	url := fmt.Sprintf("%s/%s?period1=%d&period2=%d&interval=%s&includePrePost=false",
		yahooBaseURL, symbol, startOfDay.Unix(), endOfDay.Unix(), intervalStr)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

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

	var data yahooResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if data.Chart.Error != nil {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("%s", data.Chart.Error.Description), Retryable: false}
	}

	if len(data.Chart.Result) == 0 || len(data.Chart.Result[0].Timestamp) == 0 {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("no data available"), Retryable: false}
	}

	result := data.Chart.Result[0]
	quotes := result.Indicators.Quote[0]

	candles := make([]model.Candle, 0, len(result.Timestamp))
	for i := range result.Timestamp {
		// Skip if any value is missing (nil or 0)
		if i >= len(quotes.Open) || i >= len(quotes.High) || i >= len(quotes.Low) || i >= len(quotes.Close) {
			continue
		}

		var volume int64
		if i < len(quotes.Volume) {
			volume = quotes.Volume[i]
		}

		candles = append(candles, model.Candle{
			Time:   time.Unix(result.Timestamp[i], 0),
			Open:   quotes.Open[i],
			High:   quotes.High[i],
			Low:    quotes.Low[i],
			Close:  quotes.Close[i],
			Volume: volume,
		})
	}

	return &model.IntradayData{
		Symbol:  symbol,
		Date:    date,
		Candles: candles,
	}, nil
}

// GetMultiDayIntraday fetches intraday data for multiple days
func (p *YahooProvider) GetMultiDayIntraday(ctx context.Context, symbol string, days int, interval int) ([]model.IntradayData, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	loc, _ := time.LoadLocation("America/New_York")
	now := time.Now().In(loc)

	// Yahoo allows up to 7 days of 1m data, or 60 days of 5m+ data
	rangeDays := days * 2 // Buffer for weekends
	if rangeDays > 60 {
		rangeDays = 60
	}

	endTime := now
	startTime := now.AddDate(0, 0, -rangeDays)

	intervalStr := fmt.Sprintf("%dm", interval)
	url := fmt.Sprintf("%s/%s?period1=%d&period2=%d&interval=%s&includePrePost=false",
		yahooBaseURL, symbol, startTime.Unix(), endTime.Unix(), intervalStr)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

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

	var data yahooResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if data.Chart.Error != nil {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("%s", data.Chart.Error.Description), Retryable: false}
	}

	if len(data.Chart.Result) == 0 || len(data.Chart.Result[0].Timestamp) == 0 {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("no data available"), Retryable: false}
	}

	result := data.Chart.Result[0]
	quotes := result.Indicators.Quote[0]

	// Group by date
	dayMap := make(map[string][]model.Candle)
	for i := range result.Timestamp {
		if i >= len(quotes.Open) || i >= len(quotes.High) || i >= len(quotes.Low) || i >= len(quotes.Close) {
			continue
		}

		t := time.Unix(result.Timestamp[i], 0).In(loc)
		dateKey := t.Format("2006-01-02")

		var volume int64
		if i < len(quotes.Volume) {
			volume = quotes.Volume[i]
		}

		candle := model.Candle{
			Time:   t,
			Open:   quotes.Open[i],
			High:   quotes.High[i],
			Low:    quotes.Low[i],
			Close:  quotes.Close[i],
			Volume: volume,
		}
		dayMap[dateKey] = append(dayMap[dateKey], candle)
	}

	// Convert to sorted list
	var results []model.IntradayData
	for dateKey, candles := range dayMap {
		date, _ := time.ParseInLocation("2006-01-02", dateKey, loc)
		sort.Slice(candles, func(i, j int) bool {
			return candles[i].Time.Before(candles[j].Time)
		})
		results = append(results, model.IntradayData{
			Symbol:  symbol,
			Date:    date,
			Candles: candles,
		})
	}

	// Sort by date descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Date.After(results[j].Date)
	})

	if len(results) > days {
		results = results[:days]
	}

	return results, nil
}

// GetSymbols is not supported by Yahoo Finance unofficial API
func (p *YahooProvider) GetSymbols(ctx context.Context, exchange string) ([]model.Stock, error) {
	return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("symbol listing not supported"), Retryable: false}
}
