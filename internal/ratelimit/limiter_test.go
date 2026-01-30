package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestNewLimiter(t *testing.T) {
	limiter := NewLimiter("test", 60) // 60 per minute = 1 per second

	if limiter.Name() != "test" {
		t.Errorf("Expected name 'test', got '%s'", limiter.Name())
	}

	// First few requests should be allowed immediately (burst)
	for i := 0; i < 3; i++ {
		if !limiter.Allow() {
			t.Errorf("Request %d should have been allowed", i)
		}
	}
}

func TestLimiterWait(t *testing.T) {
	limiter := NewLimiter("test", 120) // 2 per second

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Should complete quickly
	start := time.Now()
	err := limiter.Wait(ctx)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if time.Since(start) > 1*time.Second {
		t.Error("Wait took too long")
	}
}

func TestLimiterBackoff(t *testing.T) {
	limiter := NewLimiter("test", 60)

	initial := limiter.GetBackoff()

	limiter.SignalRateLimited()
	after1 := limiter.GetBackoff()
	if after1 <= initial {
		t.Error("Backoff should increase after rate limit signal")
	}

	limiter.SignalRateLimited()
	after2 := limiter.GetBackoff()
	if after2 <= after1 {
		t.Error("Backoff should continue to increase")
	}

	limiter.ResetBackoff()
	afterReset := limiter.GetBackoff()
	if afterReset >= after2 {
		t.Error("Backoff should reset to initial value")
	}
}

func TestMultiLimiter(t *testing.T) {
	ml := NewMultiLimiter()

	ml.Add("api1", 60)
	ml.Add("api2", 30)

	// Check that both limiters exist
	if ml.Get("api1") == nil {
		t.Error("api1 limiter should exist")
	}
	if ml.Get("api2") == nil {
		t.Error("api2 limiter should exist")
	}
	if ml.Get("api3") != nil {
		t.Error("api3 limiter should not exist")
	}

	// Wait on existing limiter
	ctx := context.Background()
	err := ml.Wait(ctx, "api1")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	// Wait on non-existing limiter should succeed immediately
	err = ml.Wait(ctx, "nonexistent")
	if err != nil {
		t.Errorf("Wait on non-existing limiter should succeed: %v", err)
	}
}

func TestLimiterContextCancellation(t *testing.T) {
	limiter := NewLimiter("test", 1) // Very slow rate

	// Exhaust the burst
	for i := 0; i < 5; i++ {
		limiter.Allow()
	}

	// Create a context that will be cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := limiter.Wait(ctx)
	if err == nil {
		t.Error("Expected error from cancelled context")
	}
}
