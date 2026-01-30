package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"traveler/internal/ratelimit"
	"traveler/pkg/model"
)

const alphaVantageBaseURL = "https://www.alphavantage.co/query"

// AlphaVantageProvider implements the Provider interface for Alpha Vantage API
type AlphaVantageProvider struct {
	apiKey    string
	client    *http.Client
	limiter   *ratelimit.Limiter
	rateLimit int
}

// NewAlphaVantageProvider creates a new Alpha Vantage provider
func NewAlphaVantageProvider(apiKey string, rateLimitPerMin int) *AlphaVantageProvider {
	return &AlphaVantageProvider{
		apiKey:    apiKey,
		client:    &http.Client{Timeout: 30 * time.Second},
		limiter:   ratelimit.NewLimiter("alphavantage", rateLimitPerMin),
		rateLimit: rateLimitPerMin,
	}
}

// Name returns the provider name
func (p *AlphaVantageProvider) Name() string {
	return "alphavantage"
}

// IsAvailable checks if the provider has an API key
func (p *AlphaVantageProvider) IsAvailable() bool {
	return p.apiKey != ""
}

// RateLimit returns the rate limit per minute
func (p *AlphaVantageProvider) RateLimit() int {
	return p.rateLimit
}

// alphaVantageResponse represents the API response structure
type alphaVantageResponse struct {
	MetaData   map[string]string              `json:"Meta Data"`
	TimeSeries map[string]map[string]string   `json:"Time Series (1min)"`
	TimeSeries5 map[string]map[string]string  `json:"Time Series (5min)"`
	TimeSeries15 map[string]map[string]string `json:"Time Series (15min)"`
	TimeSeries30 map[string]map[string]string `json:"Time Series (30min)"`
	TimeSeries60 map[string]map[string]string `json:"Time Series (60min)"`
	Note       string                          `json:"Note"` // Rate limit message
	Error      string                          `json:"Error Message"`
}

// GetIntradayData fetches intraday candle data for a symbol
func (p *AlphaVantageProvider) GetIntradayData(ctx context.Context, symbol string, date time.Time, interval int) (*model.IntradayData, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	intervalStr := fmt.Sprintf("%dmin", interval)
	url := fmt.Sprintf("%s?function=TIME_SERIES_INTRADAY&symbol=%s&interval=%s&outputsize=full&apikey=%s",
		alphaVantageBaseURL, symbol, intervalStr, p.apiKey)

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

	var data alphaVantageResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if data.Note != "" {
		p.limiter.SignalRateLimited()
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("rate limited: %s", data.Note), Retryable: true}
	}

	if data.Error != "" {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("%s", data.Error), Retryable: false}
	}

	// Get the correct time series based on interval
	timeSeries := p.getTimeSeries(&data, interval)
	if len(timeSeries) == 0 {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("no data available"), Retryable: false}
	}

	candles := p.parseTimeSeries(timeSeries, date)
	if len(candles) == 0 {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("no data for date %s", date.Format("2006-01-02")), Retryable: false}
	}

	return &model.IntradayData{
		Symbol:  symbol,
		Date:    date,
		Candles: candles,
	}, nil
}

// getTimeSeries returns the appropriate time series map based on interval
func (p *AlphaVantageProvider) getTimeSeries(data *alphaVantageResponse, interval int) map[string]map[string]string {
	switch interval {
	case 1:
		return data.TimeSeries
	case 5:
		return data.TimeSeries5
	case 15:
		return data.TimeSeries15
	case 30:
		return data.TimeSeries30
	case 60:
		return data.TimeSeries60
	default:
		return data.TimeSeries5
	}
}

// parseTimeSeries converts the API response to candles
func (p *AlphaVantageProvider) parseTimeSeries(timeSeries map[string]map[string]string, filterDate time.Time) []model.Candle {
	loc, _ := time.LoadLocation("America/New_York")
	filterDateStr := filterDate.Format("2006-01-02")

	var candles []model.Candle
	for timeStr, values := range timeSeries {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", timeStr, loc)
		if err != nil {
			continue
		}

		// Filter to the requested date
		if t.Format("2006-01-02") != filterDateStr {
			continue
		}

		open, _ := strconv.ParseFloat(values["1. open"], 64)
		high, _ := strconv.ParseFloat(values["2. high"], 64)
		low, _ := strconv.ParseFloat(values["3. low"], 64)
		closePrice, _ := strconv.ParseFloat(values["4. close"], 64)
		volume, _ := strconv.ParseInt(values["5. volume"], 10, 64)

		candles = append(candles, model.Candle{
			Time:   t,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  closePrice,
			Volume: volume,
		})
	}

	// Sort by time
	sort.Slice(candles, func(i, j int) bool {
		return candles[i].Time.Before(candles[j].Time)
	})

	return candles
}

// GetMultiDayIntraday fetches intraday data for multiple days
func (p *AlphaVantageProvider) GetMultiDayIntraday(ctx context.Context, symbol string, days int, interval int) ([]model.IntradayData, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	intervalStr := fmt.Sprintf("%dmin", interval)
	// Use extended_hours=false to get regular hours only, outputsize=full for more data
	url := fmt.Sprintf("%s?function=TIME_SERIES_INTRADAY&symbol=%s&interval=%s&outputsize=full&apikey=%s",
		alphaVantageBaseURL, symbol, intervalStr, p.apiKey)

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

	var data alphaVantageResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if data.Note != "" {
		p.limiter.SignalRateLimited()
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("rate limited: %s", data.Note), Retryable: true}
	}

	if data.Error != "" {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("%s", data.Error), Retryable: false}
	}

	timeSeries := p.getTimeSeries(&data, interval)
	if len(timeSeries) == 0 {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("no data available"), Retryable: false}
	}

	// Group all candles by date
	loc, _ := time.LoadLocation("America/New_York")
	dayMap := make(map[string][]model.Candle)

	for timeStr, values := range timeSeries {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", timeStr, loc)
		if err != nil {
			continue
		}

		dateKey := t.Format("2006-01-02")
		open, _ := strconv.ParseFloat(values["1. open"], 64)
		high, _ := strconv.ParseFloat(values["2. high"], 64)
		low, _ := strconv.ParseFloat(values["3. low"], 64)
		closePrice, _ := strconv.ParseFloat(values["4. close"], 64)
		volume, _ := strconv.ParseInt(values["5. volume"], 10, 64)

		candle := model.Candle{
			Time:   t,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  closePrice,
			Volume: volume,
		}
		dayMap[dateKey] = append(dayMap[dateKey], candle)
	}

	// Convert to sorted list
	var result []model.IntradayData
	for dateKey, candles := range dayMap {
		date, _ := time.ParseInLocation("2006-01-02", dateKey, loc)
		sort.Slice(candles, func(i, j int) bool {
			return candles[i].Time.Before(candles[j].Time)
		})
		result = append(result, model.IntradayData{
			Symbol:  symbol,
			Date:    date,
			Candles: candles,
		})
	}

	// Sort by date descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date.After(result[j].Date)
	})

	if len(result) > days {
		result = result[:days]
	}

	return result, nil
}

// GetSymbols returns an empty list - Alpha Vantage doesn't have a good symbols endpoint for free tier
func (p *AlphaVantageProvider) GetSymbols(ctx context.Context, exchange string) ([]model.Stock, error) {
	return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("symbol listing not supported"), Retryable: false}
}
