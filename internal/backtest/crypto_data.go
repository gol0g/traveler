package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"traveler/pkg/model"
)

const upbitBaseURL = "https://api.upbit.com/v1"

// CandleCache fetches and caches Upbit minute candles locally.
type CandleCache struct {
	CacheDir string
	client   *http.Client
}

// NewCandleCache creates a new cache under dataDir/backtest_cache/
func NewCandleCache(dataDir string) *CandleCache {
	cacheDir := filepath.Join(dataDir, "backtest_cache")
	os.MkdirAll(cacheDir, 0755)
	return &CandleCache{
		CacheDir: cacheDir,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

// cacheKey returns the file path for a symbol/date/interval combo
func (c *CandleCache) cacheKey(symbol string, date time.Time, interval int) string {
	return filepath.Join(c.CacheDir,
		fmt.Sprintf("%s_%dm_%s.json", symbol, interval, date.Format("2006-01-02")))
}

// FetchAllData fetches minute candles for all symbols over date range.
// Returns map[symbol]map[dateStr][]Candle sorted ascending.
func (c *CandleCache) FetchAllData(ctx context.Context, symbols []string, startDate, endDate time.Time, interval int, noCache bool) (map[string]map[string][]model.Candle, error) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	result := make(map[string]map[string][]model.Candle)

	// Count total work
	totalDays := 0
	for d := startDate; !d.After(endDate); d = d.AddDate(0, 0, 1) {
		totalDays++
	}
	totalWork := len(symbols) * totalDays
	done := 0

	for _, sym := range symbols {
		result[sym] = make(map[string][]model.Candle)

		for d := startDate; !d.After(endDate); d = d.AddDate(0, 0, 1) {
			done++
			dateKST := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
			dateStr := dateKST.Format("2006-01-02")

			// Check cache
			cachePath := c.cacheKey(sym, dateKST, interval)
			if !noCache {
				if candles, err := c.loadCache(cachePath); err == nil && len(candles) > 0 {
					result[sym][dateStr] = candles
					continue
				}
			}

			fmt.Printf("\r  Fetching %s %s... (%d/%d)", sym, dateStr, done, totalWork)

			candles, err := c.fetchDayCandles(ctx, sym, dateKST, interval)
			if err != nil {
				fmt.Printf(" error: %v\n", err)
				continue
			}

			if len(candles) > 0 {
				result[sym][dateStr] = candles
				c.saveCache(cachePath, candles)
			}
		}
	}
	fmt.Println()

	return result, nil
}

// fetchDayCandles fetches all minute candles for a single symbol/day via pagination.
func (c *CandleCache) fetchDayCandles(ctx context.Context, symbol string, date time.Time, interval int) ([]model.Candle, error) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	dayEnd := dayStart.Add(24*time.Hour - time.Second)
	toParam := dayEnd.UTC().Format("2006-01-02T15:04:05")

	var allCandles []model.Candle

	for page := 0; page < 3; page++ { // max 3 pages (3×200=600 candles, 288 needed for 5min)
		time.Sleep(150 * time.Millisecond) // rate limit

		url := fmt.Sprintf("%s/candles/minutes/%d?market=%s&count=200&to=%s",
			upbitBaseURL, interval, symbol, toParam)

		candles, err := c.doFetch(ctx, url, loc)
		if err != nil {
			return allCandles, err
		}
		if len(candles) == 0 {
			break
		}

		// Filter to target date only
		for _, cd := range candles {
			if cd.Time.Format("2006-01-02") == date.Format("2006-01-02") {
				allCandles = append(allCandles, cd)
			}
		}

		// Check if we've gone past the day start
		oldest := candles[len(candles)-1]
		if oldest.Time.Before(dayStart) {
			break
		}

		// Next page: use oldest candle time
		toParam = oldest.Time.UTC().Format("2006-01-02T15:04:05")

		if len(candles) < 200 {
			break
		}
	}

	// Sort ascending and deduplicate
	sort.Slice(allCandles, func(i, j int) bool {
		return allCandles[i].Time.Before(allCandles[j].Time)
	})
	if len(allCandles) > 1 {
		deduped := []model.Candle{allCandles[0]}
		for i := 1; i < len(allCandles); i++ {
			if !allCandles[i].Time.Equal(deduped[len(deduped)-1].Time) {
				deduped = append(deduped, allCandles[i])
			}
		}
		allCandles = deduped
	}

	return allCandles, nil
}

type upbitMinuteCandle struct {
	Market               string  `json:"market"`
	CandleDateTimeUTC    string  `json:"candle_date_time_utc"`
	CandleDateTimeKST    string  `json:"candle_date_time_kst"`
	OpeningPrice         float64 `json:"opening_price"`
	HighPrice            float64 `json:"high_price"`
	LowPrice             float64 `json:"low_price"`
	TradePrice           float64 `json:"trade_price"`
	CandleAccTradeVolume float64 `json:"candle_acc_trade_volume"`
	Unit                 int     `json:"unit"`
}

func (c *CandleCache) doFetch(ctx context.Context, url string, loc *time.Location) ([]model.Candle, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		time.Sleep(1 * time.Second)
		return nil, fmt.Errorf("rate limited")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var raw []upbitMinuteCandle
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	candles := make([]model.Candle, 0, len(raw))
	for _, r := range raw {
		t, err := time.ParseInLocation("2006-01-02T15:04:05", r.CandleDateTimeKST, loc)
		if err != nil {
			continue
		}
		candles = append(candles, model.Candle{
			Time:   t,
			Open:   r.OpeningPrice,
			High:   r.HighPrice,
			Low:    r.LowPrice,
			Close:  r.TradePrice,
			Volume: int64(r.CandleAccTradeVolume),
		})
	}
	return candles, nil
}

func (c *CandleCache) loadCache(path string) ([]model.Candle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var candles []model.Candle
	if err := json.Unmarshal(data, &candles); err != nil {
		return nil, err
	}
	return candles, nil
}

func (c *CandleCache) saveCache(path string, candles []model.Candle) {
	data, _ := json.Marshal(candles)
	os.WriteFile(path, data, 0644)
}
