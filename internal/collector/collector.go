package collector

import (
	"context"
	"log"
	"time"
)

// Config holds collector configuration.
type Config struct {
	DataDir string

	// Binance Futures symbols
	BinanceSymbols []string // e.g. ["BTCUSDT", "ETHUSDT", "SOLUSDT", "XRPUSDT"]

	// Upbit symbols (KRW market)
	UpbitSymbols []string // e.g. ["KRW-BTC", "KRW-ETH", "KRW-SOL", ...]

	// KR stock symbols (장중만 수집)
	KRSymbols []string // e.g. ["005930", "000660", ...]

	// Data retention
	RetentionDays int // default 90
}

// DefaultConfig returns a sensible default config.
func DefaultConfig() Config {
	return Config{
		BinanceSymbols: []string{
			"BTCUSDT", "ETHUSDT", "SOLUSDT", "XRPUSDT",
		},
		UpbitSymbols: []string{
			"KRW-BTC", "KRW-ETH", "KRW-SOL", "KRW-XRP",
			"KRW-AVAX", "KRW-LINK", "KRW-ADA", "KRW-DOGE",
		},
		KRSymbols: []string{
			"005930", // 삼성전자
			"000660", // SK하이닉스
			"373220", // LG에너지솔루션
			"005380", // 현대차
			"035420", // NAVER
			"000270", // 기아
			"068270", // 셀트리온
			"035720", // 카카오
			"051910", // LG화학
			"006400", // 삼성SDI
		},
		RetentionDays: 90,
	}
}

// Collector orchestrates data collection across all markets.
type Collector struct {
	db     *DB
	config Config

	binance *BinanceCollector
	upbit   *UpbitCollector
	kisKR   *KISKRCollector
}

// New creates a new Collector.
func New(cfg Config) (*Collector, error) {
	db, err := OpenDB(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	c := &Collector{
		db:     db,
		config: cfg,
	}

	// Binance Futures (24/7)
	if len(cfg.BinanceSymbols) > 0 {
		c.binance = NewBinanceCollector(db, cfg.BinanceSymbols)
		log.Printf("[COLLECTOR] Binance: %d symbols", len(cfg.BinanceSymbols))
	}

	// Upbit (24/7)
	if len(cfg.UpbitSymbols) > 0 {
		c.upbit = NewUpbitCollector(db, cfg.UpbitSymbols)
		log.Printf("[COLLECTOR] Upbit: %d symbols", len(cfg.UpbitSymbols))
	}

	// KIS KR stocks (market hours only)
	if len(cfg.KRSymbols) > 0 {
		kisKR, err := NewKISKRCollectorFromEnv(db, cfg.KRSymbols)
		if err != nil {
			log.Printf("[COLLECTOR] KIS KR disabled: %v", err)
		} else {
			c.kisKR = kisKR
			log.Printf("[COLLECTOR] KIS KR: %d symbols", len(cfg.KRSymbols))
		}
	}

	return c, nil
}

// Run starts the collection loop. Blocks until context is cancelled.
func (c *Collector) Run(ctx context.Context) error {
	defer c.db.Close()

	log.Printf("[COLLECTOR] Starting data collection...")

	// Print initial DB stats
	stats := c.db.Stats()
	for table, count := range stats {
		if count > 0 {
			log.Printf("[COLLECTOR] DB %s: %d rows", table, count)
		}
	}

	// Timers
	candleTicker := time.NewTicker(1 * time.Minute)
	signalTicker := time.NewTicker(5 * time.Minute)
	purgeTicker := time.NewTicker(24 * time.Hour)
	defer candleTicker.Stop()
	defer signalTicker.Stop()
	defer purgeTicker.Stop()

	// Initial collection
	c.collectCandles(ctx)
	c.collectSignals(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[COLLECTOR] Shutting down...")
			c.logStats()
			return nil

		case <-candleTicker.C:
			c.collectCandles(ctx)

		case <-signalTicker.C:
			c.collectSignals(ctx)

		case <-purgeTicker.C:
			c.purgeOldData()
		}
	}
}

// collectCandles collects candles + orderbook for all active markets.
func (c *Collector) collectCandles(ctx context.Context) {
	// Binance Futures (24/7)
	if c.binance != nil {
		if err := c.binance.CollectCandles(ctx); err != nil {
			log.Printf("[COLLECTOR] Binance candles error: %v", err)
		}
		if err := c.binance.CollectOrderbook(ctx); err != nil {
			log.Printf("[COLLECTOR] Binance orderbook error: %v", err)
		}
	}

	// Upbit (24/7)
	if c.upbit != nil {
		if err := c.upbit.CollectCandles(ctx); err != nil {
			log.Printf("[COLLECTOR] Upbit candles error: %v", err)
		}
		if err := c.upbit.CollectOrderbook(ctx); err != nil {
			log.Printf("[COLLECTOR] Upbit orderbook error: %v", err)
		}
	}

	// KIS KR (market hours: 09:00-15:30 KST)
	if c.kisKR != nil && isKRMarketHours() {
		if err := c.kisKR.CollectCandles(ctx); err != nil {
			log.Printf("[COLLECTOR] KIS KR candles error: %v", err)
		}
	}
}

// collectSignals collects crypto-specific signals (every 5 min).
func (c *Collector) collectSignals(ctx context.Context) {
	if c.binance != nil {
		if err := c.binance.CollectSignals(ctx); err != nil {
			log.Printf("[COLLECTOR] Binance signals error: %v", err)
		}
	}
}

func (c *Collector) purgeOldData() {
	days := c.config.RetentionDays
	if days <= 0 {
		days = 90
	}
	deleted, err := c.db.PurgeOlderThan(time.Duration(days) * 24 * time.Hour)
	if err != nil {
		log.Printf("[COLLECTOR] Purge error: %v", err)
	} else if deleted > 0 {
		log.Printf("[COLLECTOR] Purged %d rows older than %d days", deleted, days)
	}
}

func (c *Collector) logStats() {
	stats := c.db.Stats()
	for table, count := range stats {
		log.Printf("[COLLECTOR] DB %s: %d rows", table, count)
	}
}

// isKRMarketHours returns true during KR market hours (09:00-15:30 KST).
func isKRMarketHours() bool {
	kst := time.FixedZone("KST", 9*60*60)
	now := time.Now().In(kst)

	// Skip weekends
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}

	hour := now.Hour()
	min := now.Minute()
	minutes := hour*60 + min

	// 09:00 ~ 15:30 KST
	return minutes >= 540 && minutes <= 930
}

// GetDB returns the database for external use (e.g., web API).
func (c *Collector) GetDB() *DB {
	return c.db
}
