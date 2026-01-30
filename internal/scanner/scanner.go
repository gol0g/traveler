package scanner

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"traveler/internal/analyzer"
	"traveler/internal/provider"
	"traveler/pkg/model"
)

// ProgressCallback is called with progress updates
type ProgressCallback func(scanned, total int)

// Scanner performs parallel stock scanning
type Scanner struct {
	provider     provider.Provider
	config       analyzer.PatternConfig
	workers      int
	timeout      time.Duration
	progressFunc ProgressCallback
}

// NewScanner creates a new scanner
func NewScanner(p provider.Provider, cfg analyzer.PatternConfig, workers int, timeout time.Duration) *Scanner {
	return &Scanner{
		provider: p,
		config:   cfg,
		workers:  workers,
		timeout:  timeout,
	}
}

// SetProgressCallback sets the progress callback function
func (s *Scanner) SetProgressCallback(fn ProgressCallback) {
	s.progressFunc = fn
}

// Scan scans all provided stocks for the pattern
func (s *Scanner) Scan(ctx context.Context, stocks []model.Stock) (*model.ScanResult, error) {
	startTime := time.Now()

	if len(stocks) == 0 {
		return &model.ScanResult{
			TotalScanned:  0,
			MatchingCount: 0,
			Results:       []model.PatternResult{},
			ScanTime:      time.Since(startTime),
		}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// Channels
	jobChan := make(chan model.Stock, len(stocks))
	resultChan := make(chan *model.PatternResult, len(stocks))

	// Send all jobs
	for _, stock := range stocks {
		jobChan <- stock
	}
	close(jobChan)

	// Progress counter
	var scannedCount int64

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < s.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			patternAnalyzer := analyzer.NewPatternAnalyzer(s.config, s.provider)

			for stock := range jobChan {
				select {
				case <-ctx.Done():
					return
				default:
					result, err := patternAnalyzer.AnalyzeStock(ctx, stock)
					if err == nil && result != nil {
						resultChan <- result
					}

					// Update progress
					count := atomic.AddInt64(&scannedCount, 1)
					if s.progressFunc != nil {
						s.progressFunc(int(count), len(stocks))
					}
				}
			}
		}()
	}

	// Close result channel when all workers are done
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	var results []model.PatternResult
	for result := range resultChan {
		results = append(results, *result)
	}

	return &model.ScanResult{
		TotalScanned:  len(stocks),
		MatchingCount: len(results),
		Results:       results,
		ScanTime:      time.Since(startTime),
	}, nil
}

// ScanSymbols scans specific symbols
func (s *Scanner) ScanSymbols(ctx context.Context, symbols []string) (*model.ScanResult, error) {
	stocks := make([]model.Stock, len(symbols))
	for i, sym := range symbols {
		stocks[i] = model.Stock{
			Symbol:   sym,
			Name:     sym,
			Exchange: "US",
		}
	}
	return s.Scan(ctx, stocks)
}

// QuickScan performs a faster scan with fewer days of data
func (s *Scanner) QuickScan(ctx context.Context, stocks []model.Stock) (*model.ScanResult, error) {
	// Use fewer workers and shorter timeout for quick scan
	originalWorkers := s.workers
	originalTimeout := s.timeout

	s.workers = s.workers / 2
	if s.workers < 1 {
		s.workers = 1
	}
	s.timeout = s.timeout / 2

	result, err := s.Scan(ctx, stocks)

	s.workers = originalWorkers
	s.timeout = originalTimeout

	return result, err
}
