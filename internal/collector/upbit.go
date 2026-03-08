package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

const upbitURL = "https://api.upbit.com/v1"

// UpbitCollector collects Upbit exchange data (candles, orderbook).
type UpbitCollector struct {
	db      *DB
	client  *http.Client
	symbols []string // e.g. ["KRW-BTC", "KRW-ETH", "KRW-SOL"]
}

// NewUpbitCollector creates a new Upbit collector.
func NewUpbitCollector(db *DB, symbols []string) *UpbitCollector {
	return &UpbitCollector{
		db:      db,
		client:  &http.Client{Timeout: 15 * time.Second},
		symbols: symbols,
	}
}

// CollectCandles fetches 1m candles for all symbols.
func (c *UpbitCollector) CollectCandles(ctx context.Context) error {
	var allCandles []Candle
	for _, sym := range c.symbols {
		candles, err := c.fetchMinuteCandles(ctx, sym, 2)
		if err != nil {
			log.Printf("[COLLECT-UP] candles %s: %v", sym, err)
			continue
		}
		allCandles = append(allCandles, candles...)
		time.Sleep(150 * time.Millisecond) // Upbit: 10 req/sec limit
	}
	if len(allCandles) > 0 {
		return c.db.InsertCandles(allCandles)
	}
	return nil
}

// CollectOrderbook fetches orderbook for all symbols (batch API).
func (c *UpbitCollector) CollectOrderbook(ctx context.Context) error {
	// Upbit supports batch orderbook: up to 10 markets per request
	var allSnaps []OrderbookSnapshot
	for i := 0; i < len(c.symbols); i += 10 {
		end := i + 10
		if end > len(c.symbols) {
			end = len(c.symbols)
		}
		batch := c.symbols[i:end]
		snaps, err := c.fetchOrderbook(ctx, batch)
		if err != nil {
			log.Printf("[COLLECT-UP] orderbook batch: %v", err)
			continue
		}
		allSnaps = append(allSnaps, snaps...)
		time.Sleep(150 * time.Millisecond)
	}
	if len(allSnaps) > 0 {
		return c.db.InsertOrderbook(allSnaps)
	}
	return nil
}

func (c *UpbitCollector) fetchMinuteCandles(ctx context.Context, symbol string, count int) ([]Candle, error) {
	u := fmt.Sprintf("%s/candles/minutes/1?market=%s&count=%d", upbitURL, symbol, count)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Timestamp            string  `json:"candle_date_time_utc"`
		OpeningPrice         float64 `json:"opening_price"`
		HighPrice            float64 `json:"high_price"`
		LowPrice             float64 `json:"low_price"`
		TradePrice           float64 `json:"trade_price"`
		CandleAccTradeVolume float64 `json:"candle_acc_trade_volume"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	var candles []Candle
	for _, r := range raw {
		t, _ := time.Parse("2006-01-02T15:04:05", r.Timestamp)
		candles = append(candles, Candle{
			Market: "upbit", Symbol: symbol, Interval: "1m",
			Time: t.Unix(),
			Open: r.OpeningPrice, High: r.HighPrice, Low: r.LowPrice,
			Close: r.TradePrice, Volume: r.CandleAccTradeVolume,
		})
	}
	return candles, nil
}

func (c *UpbitCollector) fetchOrderbook(ctx context.Context, symbols []string) ([]OrderbookSnapshot, error) {
	// Build markets param: KRW-BTC,KRW-ETH,...
	markets := ""
	for i, s := range symbols {
		if i > 0 {
			markets += ","
		}
		markets += s
	}

	u := fmt.Sprintf("%s/orderbook?markets=%s", upbitURL, markets)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Market    string `json:"market"`
		Timestamp int64  `json:"timestamp"`
		Units     []struct {
			AskPrice float64 `json:"ask_price"`
			BidPrice float64 `json:"bid_price"`
			AskSize  float64 `json:"ask_size"`
			BidSize  float64 `json:"bid_size"`
		} `json:"orderbook_units"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	var snaps []OrderbookSnapshot
	for _, ob := range raw {
		if len(ob.Units) == 0 {
			continue
		}

		snap := OrderbookSnapshot{
			Market:  "upbit",
			Symbol:  ob.Market,
			Time:    now,
			BestBid: ob.Units[0].BidPrice,
			BestAsk: ob.Units[0].AskPrice,
		}

		mid := (snap.BestBid + snap.BestAsk) / 2
		if mid > 0 {
			snap.SpreadPct = (snap.BestAsk - snap.BestBid) / mid * 100
		}

		// Calculate OBI at different depths
		var maxBidSize, maxAskSize float64
		obi := func(n int) (float64, float64, float64) {
			var bv, av float64
			for i := 0; i < n && i < len(ob.Units); i++ {
				bv += ob.Units[i].BidSize * ob.Units[i].BidPrice
				av += ob.Units[i].AskSize * ob.Units[i].AskPrice
			}
			total := bv + av
			if total == 0 {
				return 0, bv, av
			}
			return (bv - av) / total, bv, av
		}

		for _, u := range ob.Units {
			if u.BidSize > maxBidSize {
				maxBidSize = u.BidSize
			}
			if u.AskSize > maxAskSize {
				maxAskSize = u.AskSize
			}
		}

		snap.OBI5, _, _ = obi(5)
		snap.OBI10, _, _ = obi(10)
		snap.OBI20, snap.BidVol, snap.AskVol = obi(len(ob.Units))
		snap.BidWall = maxBidSize
		snap.AskWall = maxAskSize

		snaps = append(snaps, snap)
	}

	return snaps, nil
}

func (c *UpbitCollector) httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		errStr := string(body)
		if len(errStr) > 200 {
			errStr = errStr[:200]
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, errStr)
	}
	return body, nil
}

// FormatUpbitSymbol converts coin name to Upbit market format.
func FormatUpbitSymbol(coin string) string {
	return "KRW-" + coin
}

// upbitSymbolsToCoin extracts coin from KRW-XXX format.
func upbitSymbolsToCoin(s string) string {
	if len(s) > 4 && s[:4] == "KRW-" {
		return s[4:]
	}
	return s
}

// unused but keep for IDE
var _ = strconv.Itoa
var _ = upbitSymbolsToCoin
