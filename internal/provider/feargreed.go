package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	fearGreedAPI      = "https://api.alternative.me/fng/"
	fearGreedCacheTTL = 1 * time.Hour
)

// FearGreedData represents the Fear & Greed index
type FearGreedData struct {
	Value          int    `json:"value"`          // 0-100
	Classification string `json:"classification"` // "Extreme Fear", "Fear", "Neutral", "Greed", "Extreme Greed"
	Timestamp      int64  `json:"timestamp"`
}

// FearGreedClient fetches the Crypto Fear & Greed Index from alternative.me
type FearGreedClient struct {
	httpClient *http.Client
	cache      *FearGreedData
	cacheTime  time.Time
	mu         sync.RWMutex
}

// NewFearGreedClient creates a new F&G client
func NewFearGreedClient() *FearGreedClient {
	return &FearGreedClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// GetIndex fetches the current Fear & Greed index (cached for 1 hour)
func (c *FearGreedClient) GetIndex(ctx context.Context) (*FearGreedData, error) {
	c.mu.RLock()
	if c.cache != nil && time.Since(c.cacheTime) < fearGreedCacheTTL {
		cached := *c.cache
		c.mu.RUnlock()
		return &cached, nil
	}
	c.mu.RUnlock()

	data, err := c.fetch(ctx, 1)
	if err != nil {
		// Fallback to cached value if available
		c.mu.RLock()
		if c.cache != nil {
			cached := *c.cache
			c.mu.RUnlock()
			return &cached, nil
		}
		c.mu.RUnlock()
		// Last resort: return neutral
		return &FearGreedData{Value: 50, Classification: "Neutral"}, err
	}

	if len(data) == 0 {
		return &FearGreedData{Value: 50, Classification: "Neutral"}, nil
	}

	c.mu.Lock()
	c.cache = &data[0]
	c.cacheTime = time.Now()
	c.mu.Unlock()

	return &data[0], nil
}

// GetHistorical fetches historical F&G data for the last N days
func (c *FearGreedClient) GetHistorical(ctx context.Context, days int) ([]FearGreedData, error) {
	return c.fetch(ctx, days)
}

type fngResponse struct {
	Name string `json:"name"`
	Data []struct {
		Value                string `json:"value"`
		ValueClassification  string `json:"value_classification"`
		Timestamp            string `json:"timestamp"`
	} `json:"data"`
}

func (c *FearGreedClient) fetch(ctx context.Context, limit int) ([]FearGreedData, error) {
	url := fmt.Sprintf("%s?limit=%d", fearGreedAPI, limit)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fear&greed API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fear&greed API status %d", resp.StatusCode)
	}

	var fng fngResponse
	if err := json.NewDecoder(resp.Body).Decode(&fng); err != nil {
		return nil, fmt.Errorf("fear&greed decode: %w", err)
	}

	results := make([]FearGreedData, 0, len(fng.Data))
	for _, d := range fng.Data {
		val, _ := strconv.Atoi(d.Value)
		ts, _ := strconv.ParseInt(d.Timestamp, 10, 64)
		results = append(results, FearGreedData{
			Value:          val,
			Classification: d.ValueClassification,
			Timestamp:      ts,
		})
	}

	return results, nil
}
