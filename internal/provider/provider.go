package provider

import (
	"context"
	"time"

	"traveler/pkg/model"
)

// Provider defines the interface for data providers
type Provider interface {
	// Name returns the provider name
	Name() string

	// GetIntradayData fetches intraday candle data for a symbol on a specific date
	// interval is in minutes (e.g., 1, 5, 15)
	GetIntradayData(ctx context.Context, symbol string, date time.Time, interval int) (*model.IntradayData, error)

	// GetMultiDayIntraday fetches intraday data for multiple days
	GetMultiDayIntraday(ctx context.Context, symbol string, days int, interval int) ([]model.IntradayData, error)

	// GetDailyCandles fetches daily OHLCV data for the specified number of days
	GetDailyCandles(ctx context.Context, symbol string, days int) ([]model.Candle, error)

	// GetSymbols returns the list of symbols for the given exchange
	GetSymbols(ctx context.Context, exchange string) ([]model.Stock, error)

	// IsAvailable checks if the provider is available (has valid API key)
	IsAvailable() bool

	// RateLimit returns the rate limit per minute
	RateLimit() int
}

// ProviderError represents a provider-specific error
type ProviderError struct {
	Provider string
	Err      error
	Retryable bool
}

func (e *ProviderError) Error() string {
	return e.Provider + ": " + e.Err.Error()
}

func (e *ProviderError) Unwrap() error {
	return e.Err
}

// FallbackProvider tries multiple providers in order
type FallbackProvider struct {
	providers []Provider
}

// NewFallbackProvider creates a new fallback provider
func NewFallbackProvider(providers ...Provider) *FallbackProvider {
	// Filter to only available providers
	available := make([]Provider, 0, len(providers))
	for _, p := range providers {
		if p.IsAvailable() {
			available = append(available, p)
		}
	}
	return &FallbackProvider{providers: available}
}

// Name returns the combined provider name
func (f *FallbackProvider) Name() string {
	return "fallback"
}

// GetIntradayData tries each provider in order until one succeeds
func (f *FallbackProvider) GetIntradayData(ctx context.Context, symbol string, date time.Time, interval int) (*model.IntradayData, error) {
	var lastErr error
	for _, p := range f.providers {
		data, err := p.GetIntradayData(ctx, symbol, date, interval)
		if err == nil {
			return data, nil
		}
		lastErr = err
		// Check if error is retryable
		if pe, ok := err.(*ProviderError); ok && !pe.Retryable {
			continue
		}
	}
	return nil, lastErr
}

// GetMultiDayIntraday tries each provider in order
func (f *FallbackProvider) GetMultiDayIntraday(ctx context.Context, symbol string, days int, interval int) ([]model.IntradayData, error) {
	var lastErr error
	for _, p := range f.providers {
		data, err := p.GetMultiDayIntraday(ctx, symbol, days, interval)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// GetDailyCandles tries each provider in order
func (f *FallbackProvider) GetDailyCandles(ctx context.Context, symbol string, days int) ([]model.Candle, error) {
	var lastErr error
	for _, p := range f.providers {
		data, err := p.GetDailyCandles(ctx, symbol, days)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// GetSymbols returns symbols from the first available provider
func (f *FallbackProvider) GetSymbols(ctx context.Context, exchange string) ([]model.Stock, error) {
	var lastErr error
	for _, p := range f.providers {
		symbols, err := p.GetSymbols(ctx, exchange)
		if err == nil {
			return symbols, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// IsAvailable returns true if any provider is available
func (f *FallbackProvider) IsAvailable() bool {
	return len(f.providers) > 0
}

// RateLimit returns the highest rate limit among providers
func (f *FallbackProvider) RateLimit() int {
	maxRate := 0
	for _, p := range f.providers {
		if p.RateLimit() > maxRate {
			maxRate = p.RateLimit()
		}
	}
	return maxRate
}

// Providers returns the list of underlying providers
func (f *FallbackProvider) Providers() []Provider {
	return f.providers
}
