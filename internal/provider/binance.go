package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"traveler/pkg/model"
)

const binanceFuturesURL = "https://fapi.binance.com"

// BinanceProvider fetches candle data from Binance USDT-M Futures
type BinanceProvider struct {
	client *http.Client

	mu          sync.Mutex
	lastRequest time.Time
}

// NewBinanceProvider creates a new Binance Futures data provider
func NewBinanceProvider() *BinanceProvider {
	return &BinanceProvider{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetRecentMinuteCandles fetches recent minute candles from Binance Futures.
// Implements strategy.ScalpProvider interface.
func (p *BinanceProvider) GetRecentMinuteCandles(ctx context.Context, symbol string, interval int, count int) ([]model.Candle, error) {
	p.rateLimit()

	// Map interval to Binance format
	intervalStr := fmt.Sprintf("%dm", interval)
	if count > 1500 {
		count = 1500
	}

	u := fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=%s&limit=%d",
		binanceFuturesURL, symbol, intervalStr, count)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("binance klines %s: HTTP %d: %s", symbol, resp.StatusCode, string(body))
	}

	// Response is array of arrays:
	// [openTime, open, high, low, close, volume, closeTime, quoteVol, trades, takerBuyBase, takerBuyQuote, ignore]
	var raw [][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse klines: %w", err)
	}

	candles := make([]model.Candle, 0, len(raw))
	for _, k := range raw {
		if len(k) < 6 {
			continue
		}

		var openTimeMs int64
		json.Unmarshal(k[0], &openTimeMs)

		var openStr, highStr, lowStr, closeStr, volStr string
		json.Unmarshal(k[1], &openStr)
		json.Unmarshal(k[2], &highStr)
		json.Unmarshal(k[3], &lowStr)
		json.Unmarshal(k[4], &closeStr)
		json.Unmarshal(k[5], &volStr)

		open, _ := strconv.ParseFloat(openStr, 64)
		high, _ := strconv.ParseFloat(highStr, 64)
		low, _ := strconv.ParseFloat(lowStr, 64)
		close_, _ := strconv.ParseFloat(closeStr, 64)
		vol, _ := strconv.ParseFloat(volStr, 64)

		candles = append(candles, model.Candle{
			Time:   time.UnixMilli(openTimeMs),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close_,
			Volume: int64(vol * 1e6), // Scale for int64 storage
		})
	}

	return candles, nil
}

// GetDailyCandles fetches daily candles from Binance Futures
func (p *BinanceProvider) GetDailyCandles(ctx context.Context, symbol string, count int) ([]model.Candle, error) {
	p.rateLimit()

	if count > 1500 {
		count = 1500
	}

	u := fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=1d&limit=%d",
		binanceFuturesURL, symbol, count)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("binance daily %s: HTTP %d: %s", symbol, resp.StatusCode, string(body))
	}

	var raw [][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	candles := make([]model.Candle, 0, len(raw))
	for _, k := range raw {
		if len(k) < 6 {
			continue
		}

		var openTimeMs int64
		json.Unmarshal(k[0], &openTimeMs)

		var openStr, highStr, lowStr, closeStr, volStr string
		json.Unmarshal(k[1], &openStr)
		json.Unmarshal(k[2], &highStr)
		json.Unmarshal(k[3], &lowStr)
		json.Unmarshal(k[4], &closeStr)
		json.Unmarshal(k[5], &volStr)

		open, _ := strconv.ParseFloat(openStr, 64)
		high, _ := strconv.ParseFloat(highStr, 64)
		low, _ := strconv.ParseFloat(lowStr, 64)
		close_, _ := strconv.ParseFloat(closeStr, 64)
		vol, _ := strconv.ParseFloat(volStr, 64)

		candles = append(candles, model.Candle{
			Time:   time.UnixMilli(openTimeMs),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close_,
			Volume: int64(vol * 1e6),
		})
	}

	return candles, nil
}

// GetOpenInterest fetches current open interest for a symbol.
// Implements strategy.OpenInterestProvider.
func (p *BinanceProvider) GetOpenInterest(ctx context.Context, symbol string) (float64, error) {
	p.rateLimit()

	u := fmt.Sprintf("%s/fapi/v1/openInterest?symbol=%s", binanceFuturesURL, symbol)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("binance OI %s: HTTP %d: %s", symbol, resp.StatusCode, string(body))
	}

	var data struct {
		OpenInterest string `json:"openInterest"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, err
	}

	v, _ := strconv.ParseFloat(data.OpenInterest, 64)
	return v, nil
}

func (p *BinanceProvider) rateLimit() {
	p.mu.Lock()
	defer p.mu.Unlock()

	minInterval := 100 * time.Millisecond
	elapsed := time.Since(p.lastRequest)
	if elapsed < minInterval {
		time.Sleep(minInterval - elapsed)
	}
	p.lastRequest = time.Now()
}
