package provider

import (
	"context"
	"sync"
	"time"

	"traveler/pkg/model"
)

// CachingProvider wraps a Provider with an in-memory cache for GetDailyCandles.
// Designed for scan scenarios where multiple strategies analyze the same stock.
type CachingProvider struct {
	inner   Provider
	cache   map[string][]model.Candle
	mu      sync.Mutex
	maxDays int
}

// NewCachingProvider creates a caching wrapper. maxDays is the number of days
// to always fetch (use 250 to satisfy mean-reversion's MA200 requirement).
func NewCachingProvider(inner Provider, maxDays int) *CachingProvider {
	return &CachingProvider{
		inner:   inner,
		cache:   make(map[string][]model.Candle),
		maxDays: maxDays,
	}
}

func (p *CachingProvider) Name() string      { return p.inner.Name() }
func (p *CachingProvider) IsAvailable() bool  { return p.inner.IsAvailable() }
func (p *CachingProvider) RateLimit() int     { return p.inner.RateLimit() }

func (p *CachingProvider) GetIntradayData(ctx context.Context, symbol string, date time.Time, interval int) (*model.IntradayData, error) {
	return p.inner.GetIntradayData(ctx, symbol, date, interval)
}

func (p *CachingProvider) GetMultiDayIntraday(ctx context.Context, symbol string, days int, interval int) ([]model.IntradayData, error) {
	return p.inner.GetMultiDayIntraday(ctx, symbol, days, interval)
}

func (p *CachingProvider) GetSymbols(ctx context.Context, exchange string) ([]model.Stock, error) {
	return p.inner.GetSymbols(ctx, exchange)
}

func (p *CachingProvider) GetDailyCandles(ctx context.Context, symbol string, days int) ([]model.Candle, error) {
	p.mu.Lock()
	if cached, ok := p.cache[symbol]; ok {
		p.mu.Unlock()
		if len(cached) >= days {
			return cached[len(cached)-days:], nil
		}
		return cached, nil
	}
	p.mu.Unlock()

	// Fetch max days to satisfy all strategies in one call
	fetchDays := p.maxDays
	if days > fetchDays {
		fetchDays = days
	}

	candles, err := p.inner.GetDailyCandles(ctx, symbol, fetchDays)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.cache[symbol] = candles
	p.mu.Unlock()

	if len(candles) >= days {
		return candles[len(candles)-days:], nil
	}
	return candles, nil
}
