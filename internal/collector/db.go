package collector

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps SQLite connection with schema management.
type DB struct {
	db *sql.DB
	mu sync.Mutex // serialize writes (SQLite single-writer)
}

// OpenDB opens or creates the SQLite database at the given path.
func OpenDB(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "traveler.db")
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL", dbPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite single-writer
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	d := &DB{db: db}
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	log.Printf("[DB] Opened %s", dbPath)
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) migrate() error {
	stmts := []string{
		// 캔들 (전 시장 통합)
		`CREATE TABLE IF NOT EXISTS candles (
			market TEXT NOT NULL,
			symbol TEXT NOT NULL,
			interval TEXT NOT NULL,
			time INTEGER NOT NULL,
			open REAL, high REAL, low REAL, close REAL, volume REAL,
			PRIMARY KEY (market, symbol, interval, time)
		) WITHOUT ROWID`,

		// 호가 스냅샷
		`CREATE TABLE IF NOT EXISTS orderbook (
			market TEXT NOT NULL,
			symbol TEXT NOT NULL,
			time INTEGER NOT NULL,
			best_bid REAL, best_ask REAL,
			bid_vol REAL, ask_vol REAL,
			spread_pct REAL,
			obi_5 REAL, obi_10 REAL, obi_20 REAL,
			bid_wall REAL, ask_wall REAL,
			PRIMARY KEY (market, symbol, time)
		) WITHOUT ROWID`,

		// 크립토 시그널 (펀딩비, OI, LS비율, Taker비율)
		`CREATE TABLE IF NOT EXISTS crypto_signals (
			symbol TEXT NOT NULL,
			time INTEGER NOT NULL,
			funding_rate REAL,
			open_interest REAL,
			long_short_ratio REAL,
			taker_buy_ratio REAL,
			PRIMARY KEY (symbol, time)
		) WITHOUT ROWID`,

		// KR 투자자별 매매동향
		`CREATE TABLE IF NOT EXISTS kr_investor (
			symbol TEXT NOT NULL,
			time INTEGER NOT NULL,
			foreign_net REAL,
			institution_net REAL,
			individual_net REAL,
			PRIMARY KEY (symbol, time)
		) WITHOUT ROWID`,

		// 쿼리 성능용 인덱스
		`CREATE INDEX IF NOT EXISTS idx_candles_time ON candles(time)`,
		`CREATE INDEX IF NOT EXISTS idx_orderbook_time ON orderbook(time)`,
		`CREATE INDEX IF NOT EXISTS idx_crypto_signals_time ON crypto_signals(time)`,
	}

	for _, stmt := range stmts {
		if _, err := d.db.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:60], err)
		}
	}
	return nil
}

// Candle represents a single OHLCV candle.
type Candle struct {
	Market   string
	Symbol   string
	Interval string
	Time     int64
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
}

// OrderbookSnapshot represents a point-in-time orderbook snapshot.
type OrderbookSnapshot struct {
	Market   string
	Symbol   string
	Time     int64
	BestBid  float64
	BestAsk  float64
	BidVol   float64
	AskVol   float64
	SpreadPct float64
	OBI5     float64
	OBI10    float64
	OBI20    float64
	BidWall  float64
	AskWall  float64
}

// CryptoSignal represents crypto-specific market data.
type CryptoSignal struct {
	Symbol         string
	Time           int64
	FundingRate    float64
	OpenInterest   float64
	LongShortRatio float64
	TakerBuyRatio  float64
}

// InsertCandles batch-inserts candles.
func (d *DB) InsertCandles(candles []Candle) error {
	if len(candles) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO candles (market,symbol,interval,time,open,high,low,close,volume)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, c := range candles {
		_, err := stmt.Exec(c.Market, c.Symbol, c.Interval, c.Time, c.Open, c.High, c.Low, c.Close, c.Volume)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// InsertOrderbook batch-inserts orderbook snapshots.
func (d *DB) InsertOrderbook(snaps []OrderbookSnapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO orderbook
		(market,symbol,time,best_bid,best_ask,bid_vol,ask_vol,spread_pct,obi_5,obi_10,obi_20,bid_wall,ask_wall)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, s := range snaps {
		_, err := stmt.Exec(s.Market, s.Symbol, s.Time, s.BestBid, s.BestAsk, s.BidVol, s.AskVol,
			s.SpreadPct, s.OBI5, s.OBI10, s.OBI20, s.BidWall, s.AskWall)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// InsertCryptoSignals batch-inserts crypto signals.
func (d *DB) InsertCryptoSignals(sigs []CryptoSignal) error {
	if len(sigs) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO crypto_signals
		(symbol,time,funding_rate,open_interest,long_short_ratio,taker_buy_ratio)
		VALUES (?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, s := range sigs {
		_, err := stmt.Exec(s.Symbol, s.Time, s.FundingRate, s.OpenInterest, s.LongShortRatio, s.TakerBuyRatio)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// QueryCandles returns candles for the given market/symbol/interval within a time range.
func (d *DB) QueryCandles(market, symbol, interval string, from, to int64) ([]Candle, error) {
	rows, err := d.db.Query(`SELECT time,open,high,low,close,volume FROM candles
		WHERE market=? AND symbol=? AND interval=? AND time>=? AND time<=?
		ORDER BY time`, market, symbol, interval, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Candle
	for rows.Next() {
		c := Candle{Market: market, Symbol: symbol, Interval: interval}
		if err := rows.Scan(&c.Time, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, nil
}

// QueryOrderbook returns orderbook snapshots for the given market/symbol within a time range.
func (d *DB) QueryOrderbook(market, symbol string, from, to int64) ([]OrderbookSnapshot, error) {
	rows, err := d.db.Query(`SELECT time,best_bid,best_ask,bid_vol,ask_vol,spread_pct,obi_5,obi_10,obi_20,bid_wall,ask_wall
		FROM orderbook WHERE market=? AND symbol=? AND time>=? AND time<=?
		ORDER BY time`, market, symbol, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []OrderbookSnapshot
	for rows.Next() {
		s := OrderbookSnapshot{Market: market, Symbol: symbol}
		if err := rows.Scan(&s.Time, &s.BestBid, &s.BestAsk, &s.BidVol, &s.AskVol,
			&s.SpreadPct, &s.OBI5, &s.OBI10, &s.OBI20, &s.BidWall, &s.AskWall); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, nil
}

// QueryCryptoSignals returns crypto signals for the given symbol within a time range.
func (d *DB) QueryCryptoSignals(symbol string, from, to int64) ([]CryptoSignal, error) {
	rows, err := d.db.Query(`SELECT time,funding_rate,open_interest,long_short_ratio,taker_buy_ratio
		FROM crypto_signals WHERE symbol=? AND time>=? AND time<=?
		ORDER BY time`, symbol, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CryptoSignal
	for rows.Next() {
		s := CryptoSignal{Symbol: symbol}
		if err := rows.Scan(&s.Time, &s.FundingRate, &s.OpenInterest, &s.LongShortRatio, &s.TakerBuyRatio); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, nil
}

// PurgeOlderThan removes data older than the given duration.
func (d *DB) PurgeOlderThan(age time.Duration) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := time.Now().Add(-age).Unix()
	var total int64

	for _, table := range []string{"candles", "orderbook", "crypto_signals", "kr_investor"} {
		res, err := d.db.Exec(fmt.Sprintf("DELETE FROM %s WHERE time < ?", table), cutoff)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}

	// VACUUM after large deletes
	if total > 10000 {
		d.db.Exec("PRAGMA incremental_vacuum")
	}

	return total, nil
}

// Stats returns row counts per table.
func (d *DB) Stats() map[string]int64 {
	result := map[string]int64{}
	for _, table := range []string{"candles", "orderbook", "crypto_signals", "kr_investor"} {
		var count int64
		d.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count)
		result[table] = count
	}
	return result
}
