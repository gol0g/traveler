package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"
)

const binanceURL = "https://fapi.binance.com"

// BinanceCollector collects Binance Futures data (candles, orderbook, signals).
type BinanceCollector struct {
	db      *DB
	client  *http.Client
	symbols []string // e.g. ["BTCUSDT", "ETHUSDT", "SOLUSDT"]
}

// NewBinanceCollector creates a new Binance Futures collector.
func NewBinanceCollector(db *DB, symbols []string) *BinanceCollector {
	return &BinanceCollector{
		db:      db,
		client:  &http.Client{Timeout: 15 * time.Second},
		symbols: symbols,
	}
}

// CollectCandles fetches 1m candles for all symbols and stores them.
func (c *BinanceCollector) CollectCandles(ctx context.Context) error {
	var allCandles []Candle
	for _, sym := range c.symbols {
		candles, err := c.fetchKlines(ctx, sym, "1m", 2) // last 2 candles (current + previous complete)
		if err != nil {
			log.Printf("[COLLECT-BN] candles %s: %v", sym, err)
			continue
		}
		allCandles = append(allCandles, candles...)
		time.Sleep(100 * time.Millisecond)
	}
	if len(allCandles) > 0 {
		return c.db.InsertCandles(allCandles)
	}
	return nil
}

// CollectOrderbook fetches orderbook snapshots for all symbols.
func (c *BinanceCollector) CollectOrderbook(ctx context.Context) error {
	var allSnaps []OrderbookSnapshot
	for _, sym := range c.symbols {
		snap, err := c.fetchDepth(ctx, sym, 20)
		if err != nil {
			log.Printf("[COLLECT-BN] depth %s: %v", sym, err)
			continue
		}
		allSnaps = append(allSnaps, *snap)
		time.Sleep(100 * time.Millisecond)
	}
	if len(allSnaps) > 0 {
		return c.db.InsertOrderbook(allSnaps)
	}
	return nil
}

// CollectSignals fetches crypto-specific signals (funding, OI, LS ratio, taker ratio).
func (c *BinanceCollector) CollectSignals(ctx context.Context) error {
	var allSigs []CryptoSignal
	now := time.Now().Unix()

	for _, sym := range c.symbols {
		sig := CryptoSignal{Symbol: sym, Time: now}

		if fr, err := c.fetchFundingRate(ctx, sym); err == nil {
			sig.FundingRate = fr
		}
		time.Sleep(100 * time.Millisecond)

		if oi, err := c.fetchOpenInterest(ctx, sym); err == nil {
			sig.OpenInterest = oi
		}
		time.Sleep(100 * time.Millisecond)

		if lr, err := c.fetchLongShortRatio(ctx, sym); err == nil {
			sig.LongShortRatio = lr
		}
		time.Sleep(100 * time.Millisecond)

		if tr, err := c.fetchTakerRatio(ctx, sym); err == nil {
			sig.TakerBuyRatio = tr
		}
		time.Sleep(100 * time.Millisecond)

		allSigs = append(allSigs, sig)
	}

	if len(allSigs) > 0 {
		return c.db.InsertCryptoSignals(allSigs)
	}
	return nil
}

func (c *BinanceCollector) fetchKlines(ctx context.Context, symbol, interval string, limit int) ([]Candle, error) {
	u := fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=%s&limit=%d", binanceURL, symbol, interval, limit)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return nil, err
	}

	var raw [][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	var candles []Candle
	for _, k := range raw {
		if len(k) < 6 {
			continue
		}
		var openTimeMs int64
		json.Unmarshal(k[0], &openTimeMs)

		var oStr, hStr, lStr, cStr, vStr string
		json.Unmarshal(k[1], &oStr)
		json.Unmarshal(k[2], &hStr)
		json.Unmarshal(k[3], &lStr)
		json.Unmarshal(k[4], &cStr)
		json.Unmarshal(k[5], &vStr)

		o, _ := strconv.ParseFloat(oStr, 64)
		h, _ := strconv.ParseFloat(hStr, 64)
		l, _ := strconv.ParseFloat(lStr, 64)
		cl, _ := strconv.ParseFloat(cStr, 64)
		v, _ := strconv.ParseFloat(vStr, 64)

		candles = append(candles, Candle{
			Market: "binance_futures", Symbol: symbol, Interval: "1m",
			Time: openTimeMs / 1000, Open: o, High: h, Low: l, Close: cl, Volume: v,
		})
	}
	return candles, nil
}

func (c *BinanceCollector) fetchDepth(ctx context.Context, symbol string, limit int) (*OrderbookSnapshot, error) {
	u := fmt.Sprintf("%s/fapi/v1/depth?symbol=%s&limit=%d", binanceURL, symbol, limit)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return nil, err
	}

	var data struct {
		Bids [][]string `json:"bids"`
		Asks [][]string `json:"asks"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	type level struct{ price, qty float64 }
	var bids, asks []level
	var maxBidQty, maxAskQty float64

	for _, b := range data.Bids {
		if len(b) < 2 {
			continue
		}
		p, _ := strconv.ParseFloat(b[0], 64)
		q, _ := strconv.ParseFloat(b[1], 64)
		bids = append(bids, level{p, q})
		if q > maxBidQty {
			maxBidQty = q
		}
	}
	for _, a := range data.Asks {
		if len(a) < 2 {
			continue
		}
		p, _ := strconv.ParseFloat(a[0], 64)
		q, _ := strconv.ParseFloat(a[1], 64)
		asks = append(asks, level{p, q})
		if q > maxAskQty {
			maxAskQty = q
		}
	}

	if len(bids) == 0 || len(asks) == 0 {
		return nil, fmt.Errorf("empty orderbook")
	}

	snap := &OrderbookSnapshot{
		Market:  "binance_futures",
		Symbol:  symbol,
		Time:    time.Now().Unix(),
		BestBid: bids[0].price,
		BestAsk: asks[0].price,
	}

	mid := (snap.BestBid + snap.BestAsk) / 2
	if mid > 0 {
		snap.SpreadPct = (snap.BestAsk - snap.BestBid) / mid * 100
	}

	// OBI at different depths
	obi := func(n int) (float64, float64, float64) {
		var bv, av float64
		for i := 0; i < n && i < len(bids); i++ {
			bv += bids[i].qty * bids[i].price
		}
		for i := 0; i < n && i < len(asks); i++ {
			av += asks[i].qty * asks[i].price
		}
		total := bv + av
		if total == 0 {
			return 0, bv, av
		}
		return (bv - av) / total, bv, av
	}

	snap.OBI5, _, _ = obi(5)
	snap.OBI10, _, _ = obi(10)
	snap.OBI20, snap.BidVol, snap.AskVol = obi(20)
	snap.BidWall = maxBidQty
	snap.AskWall = maxAskQty

	return snap, nil
}

func (c *BinanceCollector) fetchFundingRate(ctx context.Context, symbol string) (float64, error) {
	u := fmt.Sprintf("%s/fapi/v1/premiumIndex?symbol=%s", binanceURL, symbol)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return 0, err
	}
	var data struct {
		LastFundingRate string `json:"lastFundingRate"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, err
	}
	v, _ := strconv.ParseFloat(data.LastFundingRate, 64)
	return v, nil
}

func (c *BinanceCollector) fetchOpenInterest(ctx context.Context, symbol string) (float64, error) {
	u := fmt.Sprintf("%s/fapi/v1/openInterest?symbol=%s", binanceURL, symbol)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return 0, err
	}
	var data struct {
		OpenInterest string `json:"openInterest"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, err
	}
	v, _ := strconv.ParseFloat(data.OpenInterest, 64)
	return v, nil
}

func (c *BinanceCollector) fetchLongShortRatio(ctx context.Context, symbol string) (float64, error) {
	u := fmt.Sprintf("%s/futures/data/globalLongShortAccountRatio?symbol=%s&period=5m&limit=1", binanceURL, symbol)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return 0, err
	}
	var data []struct {
		LongShortRatio string `json:"longShortRatio"`
	}
	if err := json.Unmarshal(body, &data); err != nil || len(data) == 0 {
		return 0, fmt.Errorf("no data")
	}
	v, _ := strconv.ParseFloat(data[0].LongShortRatio, 64)
	return v, nil
}

func (c *BinanceCollector) fetchTakerRatio(ctx context.Context, symbol string) (float64, error) {
	u := fmt.Sprintf("%s/futures/data/takerlongshortRatio?symbol=%s&period=5m&limit=1", binanceURL, symbol)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return 0, err
	}
	var data []struct {
		BuySellRatio string `json:"buySellRatio"`
	}
	if err := json.Unmarshal(body, &data); err != nil || len(data) == 0 {
		return 0, fmt.Errorf("no data")
	}
	v, _ := strconv.ParseFloat(data[0].BuySellRatio, 64)
	return v, nil
}

func (c *BinanceCollector) httpGet(ctx context.Context, url string) ([]byte, error) {
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
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Ensure math import is used
var _ = math.Abs
