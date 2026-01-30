package ratelimit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter wraps rate.Limiter with additional functionality
type Limiter struct {
	limiter  *rate.Limiter
	name     string
	mu       sync.Mutex
	backoff  time.Duration
	maxWait  time.Duration
}

// NewLimiter creates a new rate limiter
// perMinute specifies the number of requests allowed per minute
func NewLimiter(name string, perMinute int) *Limiter {
	// Convert per-minute rate to per-second
	rps := float64(perMinute) / 60.0
	// Allow burst of up to 5 requests or 1/10th of per-minute limit
	burst := perMinute / 10
	if burst < 1 {
		burst = 1
	}
	if burst > 5 {
		burst = 5
	}

	return &Limiter{
		limiter: rate.NewLimiter(rate.Limit(rps), burst),
		name:    name,
		backoff: 100 * time.Millisecond,
		maxWait: 2 * time.Minute,
	}
}

// Wait blocks until a token is available or context is cancelled
func (l *Limiter) Wait(ctx context.Context) error {
	return l.limiter.Wait(ctx)
}

// Allow reports whether an event may happen now
func (l *Limiter) Allow() bool {
	return l.limiter.Allow()
}

// SignalRateLimited should be called when a 429 response is received
// It applies exponential backoff
func (l *Limiter) SignalRateLimited() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.backoff *= 2
	if l.backoff > l.maxWait {
		l.backoff = l.maxWait
	}
}

// ResetBackoff resets the backoff duration after successful request
func (l *Limiter) ResetBackoff() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.backoff = 100 * time.Millisecond
}

// GetBackoff returns the current backoff duration
func (l *Limiter) GetBackoff() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.backoff
}

// Name returns the limiter name
func (l *Limiter) Name() string {
	return l.name
}

// MultiLimiter manages multiple rate limiters for different APIs
type MultiLimiter struct {
	limiters map[string]*Limiter
	mu       sync.RWMutex
}

// NewMultiLimiter creates a new multi-limiter
func NewMultiLimiter() *MultiLimiter {
	return &MultiLimiter{
		limiters: make(map[string]*Limiter),
	}
}

// Add adds a new limiter
func (m *MultiLimiter) Add(name string, perMinute int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.limiters[name] = NewLimiter(name, perMinute)
}

// Get returns a limiter by name
func (m *MultiLimiter) Get(name string) *Limiter {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.limiters[name]
}

// Wait waits on the specified limiter
func (m *MultiLimiter) Wait(ctx context.Context, name string) error {
	limiter := m.Get(name)
	if limiter == nil {
		return nil // No limiter, proceed immediately
	}
	return limiter.Wait(ctx)
}
