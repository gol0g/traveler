package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"traveler/internal/ratelimit"
	"traveler/pkg/model"
)

const upbitBaseURL = "https://api.upbit.com/v1"

// UpbitProvider implements the Provider interface for Upbit cryptocurrency exchange.
// Uses Upbit REST API for candle data and market info.
// No authentication needed for quotation API.
type UpbitProvider struct {
	client    *http.Client
	limiter   *ratelimit.Limiter
	rateLimit int
}

// NewUpbitProvider creates a new Upbit provider
func NewUpbitProvider() *UpbitProvider {
	return &UpbitProvider{
		client:    &http.Client{Timeout: 30 * time.Second},
		limiter:   ratelimit.NewLimiter("upbit", 600), // 10 req/sec = 600/min (official limit)
		rateLimit: 600,
	}
}

// Name returns the provider name
func (p *UpbitProvider) Name() string {
	return "upbit"
}

// IsAvailable always returns true (no API key needed for quotation)
func (p *UpbitProvider) IsAvailable() bool {
	return true
}

// RateLimit returns the rate limit per minute
func (p *UpbitProvider) RateLimit() int {
	return p.rateLimit
}

// upbitDailyCandle represents Upbit daily candle response
type upbitDailyCandle struct {
	Market               string  `json:"market"`
	CandleDateTimeUTC    string  `json:"candle_date_time_utc"`
	CandleDateTimeKST    string  `json:"candle_date_time_kst"`
	OpeningPrice         float64 `json:"opening_price"`
	HighPrice            float64 `json:"high_price"`
	LowPrice             float64 `json:"low_price"`
	TradePrice           float64 `json:"trade_price"`
	CandleAccTradeVolume float64 `json:"candle_acc_trade_volume"`
	CandleAccTradePrice  float64 `json:"candle_acc_trade_price"`
}

// upbitMinuteCandle represents Upbit minute candle response
type upbitMinuteCandle struct {
	Market               string  `json:"market"`
	CandleDateTimeUTC    string  `json:"candle_date_time_utc"`
	CandleDateTimeKST    string  `json:"candle_date_time_kst"`
	OpeningPrice         float64 `json:"opening_price"`
	HighPrice            float64 `json:"high_price"`
	LowPrice             float64 `json:"low_price"`
	TradePrice           float64 `json:"trade_price"`
	CandleAccTradeVolume float64 `json:"candle_acc_trade_volume"`
	CandleAccTradePrice  float64 `json:"candle_acc_trade_price"`
	Unit                 int     `json:"unit"`
}

// upbitMarketInfo represents Upbit market listing response
type upbitMarketInfo struct {
	Market      string `json:"market"`
	KoreanName  string `json:"korean_name"`
	EnglishName string `json:"english_name"`
}

// GetDailyCandles fetches daily OHLCV data from Upbit.
// Upbit returns max 200 candles per request; paginates if more needed.
func (p *UpbitProvider) GetDailyCandles(ctx context.Context, symbol string, days int) ([]model.Candle, error) {
	var allCandles []model.Candle
	remaining := days
	var toParam string // empty = latest

	for remaining > 0 {
		count := remaining
		if count > 200 {
			count = 200
		}

		if err := p.limiter.Wait(ctx); err != nil {
			return nil, err
		}

		url := fmt.Sprintf("%s/candles/days?market=%s&count=%d", upbitBaseURL, symbol, count)
		if toParam != "" {
			url += "&to=" + toParam
		}

		candles, err := p.fetchDailyCandles(ctx, url)
		if err != nil {
			return nil, err
		}

		if len(candles) == 0 {
			break
		}

		allCandles = append(allCandles, candles...)
		remaining -= len(candles)

		if len(candles) < count {
			break // no more data
		}

		// Set pagination: use the oldest candle's time as the next "to" parameter
		oldest := candles[len(candles)-1]
		toParam = oldest.Time.UTC().Format("2006-01-02T15:04:05")
	}

	// Sort ascending (oldest first) — Upbit returns newest first
	sort.Slice(allCandles, func(i, j int) bool {
		return allCandles[i].Time.Before(allCandles[j].Time)
	})

	// Deduplicate by time
	if len(allCandles) > 1 {
		deduped := []model.Candle{allCandles[0]}
		for i := 1; i < len(allCandles); i++ {
			if !allCandles[i].Time.Equal(deduped[len(deduped)-1].Time) {
				deduped = append(deduped, allCandles[i])
			}
		}
		allCandles = deduped
	}

	// Return only requested count
	if len(allCandles) > days {
		allCandles = allCandles[len(allCandles)-days:]
	}

	return allCandles, nil
}

func (p *UpbitProvider) fetchDailyCandles(ctx context.Context, url string) ([]model.Candle, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

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

	var raw []upbitDailyCandle
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	loc, _ := time.LoadLocation("Asia/Seoul")
	candles := make([]model.Candle, 0, len(raw))
	for _, c := range raw {
		t, err := time.ParseInLocation("2006-01-02T15:04:05", c.CandleDateTimeKST, loc)
		if err != nil {
			continue
		}

		candles = append(candles, model.Candle{
			Time:   t,
			Open:   c.OpeningPrice,
			High:   c.HighPrice,
			Low:    c.LowPrice,
			Close:  c.TradePrice,
			Volume: int64(c.CandleAccTradeVolume),
		})
	}

	return candles, nil
}

// GetIntradayData fetches intraday minute candle data for a symbol on a specific date.
// Interval is in minutes (valid: 1, 3, 5, 15, 30, 60, 240).
func (p *UpbitProvider) GetIntradayData(ctx context.Context, symbol string, date time.Time, interval int) (*model.IntradayData, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	unit := normalizeUpbitInterval(interval)
	loc, _ := time.LoadLocation("Asia/Seoul")

	// Fetch candles ending at end of the target date
	endOfDay := time.Date(date.Year(), date.Month(), date.Day(), 23, 59, 59, 0, loc)
	toParam := endOfDay.UTC().Format("2006-01-02T15:04:05")

	url := fmt.Sprintf("%s/candles/minutes/%d?market=%s&count=200&to=%s",
		upbitBaseURL, unit, symbol, toParam)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

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

	var raw []upbitMinuteCandle
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	dateStr := date.Format("2006-01-02")
	candles := make([]model.Candle, 0, len(raw))
	for _, c := range raw {
		t, err := time.ParseInLocation("2006-01-02T15:04:05", c.CandleDateTimeKST, loc)
		if err != nil {
			continue
		}
		// Only include candles from the target date
		if t.Format("2006-01-02") != dateStr {
			continue
		}

		candles = append(candles, model.Candle{
			Time:   t,
			Open:   c.OpeningPrice,
			High:   c.HighPrice,
			Low:    c.LowPrice,
			Close:  c.TradePrice,
			Volume: int64(c.CandleAccTradeVolume),
		})
	}

	// Sort ascending
	sort.Slice(candles, func(i, j int) bool {
		return candles[i].Time.Before(candles[j].Time)
	})

	return &model.IntradayData{
		Symbol:  symbol,
		Date:    date,
		Candles: candles,
	}, nil
}

// GetMultiDayIntraday fetches intraday data for multiple days
func (p *UpbitProvider) GetMultiDayIntraday(ctx context.Context, symbol string, days int, interval int) ([]model.IntradayData, error) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	now := time.Now().In(loc)

	var results []model.IntradayData
	for d := 0; d < days; d++ {
		date := now.AddDate(0, 0, -d)
		data, err := p.GetIntradayData(ctx, symbol, date, interval)
		if err != nil {
			continue
		}
		if len(data.Candles) > 0 {
			results = append(results, *data)
		}
	}

	// Sort by date descending (newest first)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Date.After(results[j].Date)
	})

	if len(results) > days {
		results = results[:days]
	}

	return results, nil
}

// GetSymbols returns all KRW market symbols from Upbit
func (p *UpbitProvider) GetSymbols(ctx context.Context, exchange string) ([]model.Stock, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/market/all?is_details=false", upbitBaseURL)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, &ProviderError{Provider: p.Name(), Err: err, Retryable: true}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("status %d", resp.StatusCode), Retryable: false}
	}

	p.limiter.ResetBackoff()

	var markets []upbitMarketInfo
	if err := json.NewDecoder(resp.Body).Decode(&markets); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	// Filter to KRW market only (unless specific exchange requested)
	prefix := "KRW-"
	if exchange != "" {
		prefix = strings.ToUpper(exchange) + "-"
	}

	var stocks []model.Stock
	for _, m := range markets {
		if strings.HasPrefix(m.Market, prefix) {
			stocks = append(stocks, model.Stock{
				Symbol:   m.Market,
				Name:     m.KoreanName,
				Exchange: "UPBIT",
			})
		}
	}

	return stocks, nil
}

// GetRecentMinuteCandles fetches the most recent N minute candles (no date filter).
// This is useful for real-time scalping where we need a rolling window.
func (p *UpbitProvider) GetRecentMinuteCandles(ctx context.Context, symbol string, interval int, count int) ([]model.Candle, error) {
	var allCandles []model.Candle
	remaining := count
	var toParam string

	unit := normalizeUpbitInterval(interval)

	for remaining > 0 {
		batch := remaining
		if batch > 200 {
			batch = 200
		}

		if err := p.limiter.Wait(ctx); err != nil {
			return nil, err
		}

		reqURL := fmt.Sprintf("%s/candles/minutes/%d?market=%s&count=%d",
			upbitBaseURL, unit, symbol, batch)
		if toParam != "" {
			reqURL += "&to=" + toParam
		}

		req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := p.client.Do(req)
		if err != nil {
			return nil, &ProviderError{Provider: p.Name(), Err: err, Retryable: true}
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			p.limiter.SignalRateLimited()
			return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("rate limited"), Retryable: true}
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, &ProviderError{Provider: p.Name(), Err: fmt.Errorf("status %d", resp.StatusCode), Retryable: false}
		}

		p.limiter.ResetBackoff()

		var raw []upbitMinuteCandle
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decoding response: %w", err)
		}
		resp.Body.Close()

		if len(raw) == 0 {
			break
		}

		loc, _ := time.LoadLocation("Asia/Seoul")
		for _, c := range raw {
			t, err := time.ParseInLocation("2006-01-02T15:04:05", c.CandleDateTimeKST, loc)
			if err != nil {
				continue
			}
			allCandles = append(allCandles, model.Candle{
				Time:   t,
				Open:   c.OpeningPrice,
				High:   c.HighPrice,
				Low:    c.LowPrice,
				Close:  c.TradePrice,
				Volume: int64(c.CandleAccTradeVolume),
			})
		}

		remaining -= len(raw)
		if len(raw) < batch {
			break
		}

		// Paginate using oldest candle time
		oldest := raw[len(raw)-1]
		t, _ := time.Parse("2006-01-02T15:04:05", oldest.CandleDateTimeUTC)
		toParam = t.Format("2006-01-02T15:04:05")

		// Rate limit between pagination batches
		time.Sleep(200 * time.Millisecond)
	}

	// Sort ascending (Upbit returns newest first)
	sort.Slice(allCandles, func(i, j int) bool {
		return allCandles[i].Time.Before(allCandles[j].Time)
	})

	// Deduplicate
	if len(allCandles) > 1 {
		deduped := []model.Candle{allCandles[0]}
		for i := 1; i < len(allCandles); i++ {
			if !allCandles[i].Time.Equal(deduped[len(deduped)-1].Time) {
				deduped = append(deduped, allCandles[i])
			}
		}
		allCandles = deduped
	}

	if len(allCandles) > count {
		allCandles = allCandles[len(allCandles)-count:]
	}

	return allCandles, nil
}

// normalizeUpbitInterval converts minute interval to valid Upbit unit.
// Valid units: 1, 3, 5, 15, 30, 60, 240
func normalizeUpbitInterval(interval int) int {
	valid := []int{1, 3, 5, 15, 30, 60, 240}
	for _, v := range valid {
		if interval <= v {
			return v
		}
	}
	return 240
}
