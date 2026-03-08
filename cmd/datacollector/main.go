package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	futuresURL = "https://fapi.binance.com"
	// Collection intervals
	tickInterval   = 1 * time.Minute  // price, OBI, taker volume
	statInterval   = 5 * time.Minute  // OI, funding rate, long/short ratio
	flushInterval  = 1 * time.Minute  // flush CSV to disk (every tick cycle)
)

// Snapshot holds one row of collected data
type Snapshot struct {
	Time            time.Time
	Price           float64
	PriceChange1m   float64 // % change from 1 min ago
	PriceChange5m   float64 // % change from 5 min ago
	PriceChange15m  float64 // % change from 15 min ago

	// Order Book Imbalance
	OBI5            float64 // top 5 levels
	OBI10           float64 // top 10 levels
	OBI20           float64 // top 20 levels
	BidWall         float64 // largest single bid in top 20
	AskWall         float64 // largest single ask in top 20
	Spread          float64 // bid-ask spread %

	// Taker Buy/Sell
	TakerBuyRatio   float64 // taker buy vol / total vol (last 5m)

	// Open Interest
	OpenInterest    float64
	OIChange5m      float64 // % change in OI over 5 min

	// Funding Rate
	FundingRate     float64
	NextFundingTime time.Time

	// Long/Short Ratio (top traders)
	LongShortRatio  float64

	// Volume
	Volume5m        float64 // 5-min volume in USDT

	// Volatility
	ATR15m          float64 // ATR on 15-min candles
}

type Collector struct {
	client *http.Client
	symbol string
	dataDir string

	mu         sync.Mutex
	snapshots  []Snapshot
	priceHist  []pricePoint // rolling price history for change calc

	// Cached stat values (updated every 5 min)
	lastOI          float64
	lastFundingRate float64
	lastFundingTime time.Time
	lastLSRatio     float64
}

type pricePoint struct {
	time  time.Time
	price float64
}

func main() {
	symbol := "BTCUSDT"
	dataDir := "."
	if len(os.Args) > 1 {
		dataDir = os.Args[1]
	}

	log.Printf("[COLLECTOR] Starting data collection for %s", symbol)
	log.Printf("[COLLECTOR] Data dir: %s", dataDir)

	c := &Collector{
		client:  &http.Client{Timeout: 15 * time.Second},
		symbol:  symbol,
		dataDir: dataDir,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[COLLECTOR] Shutting down...")
		cancel()
	}()

	c.run(ctx)
}

func (c *Collector) run(ctx context.Context) {
	tickTicker := time.NewTicker(tickInterval)
	statTicker := time.NewTicker(statInterval)
	flushTicker := time.NewTicker(flushInterval)
	defer tickTicker.Stop()
	defer statTicker.Stop()
	defer flushTicker.Stop()

	// Initial collection
	c.collectStats(ctx)
	c.collectTick(ctx)

	for {
		select {
		case <-ctx.Done():
			c.flush()
			return
		case <-tickTicker.C:
			c.collectTick(ctx)
		case <-statTicker.C:
			c.collectStats(ctx)
		case <-flushTicker.C:
			c.flush()
		}
	}
}

func (c *Collector) collectTick(ctx context.Context) {
	snap := Snapshot{Time: time.Now().UTC()}

	// 1. Order book depth (top 20)
	depth, err := c.getDepth(ctx, 20)
	if err != nil {
		log.Printf("[COLLECTOR] depth error: %v", err)
		return
	}

	snap.Price = depth.midPrice
	snap.OBI5 = depth.obi(5)
	snap.OBI10 = depth.obi(10)
	snap.OBI20 = depth.obi(20)
	snap.BidWall = depth.maxBidQty
	snap.AskWall = depth.maxAskQty
	snap.Spread = depth.spreadPct

	// 2. Price changes from history
	c.mu.Lock()
	now := time.Now()
	c.priceHist = append(c.priceHist, pricePoint{time: now, price: snap.Price})
	// Keep last 20 minutes of history
	cutoff := now.Add(-20 * time.Minute)
	newHist := c.priceHist[:0]
	for _, pp := range c.priceHist {
		if pp.time.After(cutoff) {
			newHist = append(newHist, pp)
		}
	}
	c.priceHist = newHist

	snap.PriceChange1m = c.priceChangeAt(1*time.Minute, snap.Price)
	snap.PriceChange5m = c.priceChangeAt(5*time.Minute, snap.Price)
	snap.PriceChange15m = c.priceChangeAt(15*time.Minute, snap.Price)

	// Copy cached stats
	snap.OpenInterest = c.lastOI
	snap.FundingRate = c.lastFundingRate
	snap.NextFundingTime = c.lastFundingTime
	snap.LongShortRatio = c.lastLSRatio
	c.mu.Unlock()

	// 3. Taker buy/sell ratio (last 5 min candle)
	takerRatio, vol5m, err := c.getTakerBuySellRatio(ctx)
	if err != nil {
		log.Printf("[COLLECTOR] taker ratio error: %v", err)
	} else {
		snap.TakerBuyRatio = takerRatio
		snap.Volume5m = vol5m
	}

	// 4. ATR from 15-min candles
	atr, err := c.getATR15m(ctx)
	if err != nil {
		log.Printf("[COLLECTOR] ATR error: %v", err)
	} else {
		snap.ATR15m = atr
	}

	c.mu.Lock()
	c.snapshots = append(c.snapshots, snap)
	c.mu.Unlock()

	log.Printf("[TICK] price=%.1f OBI5=%.3f OBI20=%.3f taker=%.3f spread=%.4f%% chg1m=%.3f%%",
		snap.Price, snap.OBI5, snap.OBI20, snap.TakerBuyRatio, snap.Spread, snap.PriceChange1m)
}

func (c *Collector) collectStats(ctx context.Context) {
	// Open Interest
	oi, err := c.getOpenInterest(ctx)
	if err != nil {
		log.Printf("[COLLECTOR] OI error: %v", err)
	}

	// Funding Rate
	rate, nextTime, err := c.getFundingRate(ctx)
	if err != nil {
		log.Printf("[COLLECTOR] funding error: %v", err)
	}

	// Long/Short Ratio
	lsRatio, err := c.getLongShortRatio(ctx)
	if err != nil {
		log.Printf("[COLLECTOR] LS ratio error: %v", err)
	}

	c.mu.Lock()
	prevOI := c.lastOI
	c.lastOI = oi
	c.lastFundingRate = rate
	c.lastFundingTime = nextTime
	c.lastLSRatio = lsRatio
	c.mu.Unlock()

	oiChange := 0.0
	if prevOI > 0 {
		oiChange = (oi - prevOI) / prevOI * 100
	}

	log.Printf("[STAT] OI=%.0f (%.2f%%) funding=%.4f%% LS=%.3f",
		oi, oiChange, rate*100, lsRatio)
}

func (c *Collector) priceChangeAt(ago time.Duration, currentPrice float64) float64 {
	target := time.Now().Add(-ago)
	var closest pricePoint
	minDiff := time.Duration(math.MaxInt64)
	for _, pp := range c.priceHist {
		diff := pp.time.Sub(target)
		if diff < 0 {
			diff = -diff
		}
		if diff < minDiff {
			minDiff = diff
			closest = pp
		}
	}
	if closest.price == 0 || minDiff > ago/2 {
		return 0
	}
	return (currentPrice - closest.price) / closest.price * 100
}

// flush writes snapshots to CSV
func (c *Collector) flush() {
	c.mu.Lock()
	if len(c.snapshots) == 0 {
		c.mu.Unlock()
		return
	}
	toWrite := make([]Snapshot, len(c.snapshots))
	copy(toWrite, c.snapshots)
	c.snapshots = c.snapshots[:0]
	c.mu.Unlock()

	// One file per day
	date := toWrite[0].Time.Format("2006-01-02")
	filename := filepath.Join(c.dataDir, fmt.Sprintf("btc_signals_%s.csv", date))

	isNew := false
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		isNew = true
	}

	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[COLLECTOR] flush error: %v", err)
		return
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if isNew {
		w.Write([]string{
			"time", "price",
			"price_chg_1m", "price_chg_5m", "price_chg_15m",
			"obi_5", "obi_10", "obi_20",
			"bid_wall", "ask_wall", "spread_pct",
			"taker_buy_ratio", "volume_5m",
			"open_interest", "oi_chg_5m",
			"funding_rate", "long_short_ratio",
			"atr_15m",
		})
	}

	for _, s := range toWrite {
		oiChg := 0.0
		if s.OpenInterest > 0 && c.lastOI > 0 {
			// Approximate — real OI change computed in collectStats
		}
		_ = oiChg

		w.Write([]string{
			s.Time.Format("2006-01-02T15:04:05Z"),
			fmt.Sprintf("%.2f", s.Price),
			fmt.Sprintf("%.4f", s.PriceChange1m),
			fmt.Sprintf("%.4f", s.PriceChange5m),
			fmt.Sprintf("%.4f", s.PriceChange15m),
			fmt.Sprintf("%.4f", s.OBI5),
			fmt.Sprintf("%.4f", s.OBI10),
			fmt.Sprintf("%.4f", s.OBI20),
			fmt.Sprintf("%.2f", s.BidWall),
			fmt.Sprintf("%.2f", s.AskWall),
			fmt.Sprintf("%.6f", s.Spread),
			fmt.Sprintf("%.4f", s.TakerBuyRatio),
			fmt.Sprintf("%.2f", s.Volume5m),
			fmt.Sprintf("%.2f", s.OpenInterest),
			fmt.Sprintf("%.4f", s.OIChange5m),
			fmt.Sprintf("%.6f", s.FundingRate),
			fmt.Sprintf("%.4f", s.LongShortRatio),
			fmt.Sprintf("%.2f", s.ATR15m),
		})
	}

	log.Printf("[FLUSH] Wrote %d rows to %s", len(toWrite), filepath.Base(filename))
}

// --- Binance API calls ---

type depthResult struct {
	midPrice   float64
	bids       []levelQty // price, qty
	asks       []levelQty
	maxBidQty  float64
	maxAskQty  float64
	spreadPct  float64
}

type levelQty struct {
	price float64
	qty   float64
}

func (d *depthResult) obi(levels int) float64 {
	bidVol := 0.0
	askVol := 0.0
	n := levels
	if n > len(d.bids) {
		n = len(d.bids)
	}
	for i := 0; i < n && i < len(d.bids); i++ {
		bidVol += d.bids[i].qty * d.bids[i].price // weight by notional
	}
	for i := 0; i < n && i < len(d.asks); i++ {
		askVol += d.asks[i].qty * d.asks[i].price
	}
	total := bidVol + askVol
	if total == 0 {
		return 0
	}
	return (bidVol - askVol) / total
}

func (c *Collector) getDepth(ctx context.Context, limit int) (*depthResult, error) {
	u := fmt.Sprintf("%s/fapi/v1/depth?symbol=%s&limit=%d", futuresURL, c.symbol, limit)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return nil, err
	}

	var data struct {
		Bids [][]string `json:"bids"` // [[price, qty], ...]
		Asks [][]string `json:"asks"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	result := &depthResult{}
	for _, b := range data.Bids {
		if len(b) < 2 {
			continue
		}
		p, _ := strconv.ParseFloat(b[0], 64)
		q, _ := strconv.ParseFloat(b[1], 64)
		result.bids = append(result.bids, levelQty{p, q})
		if q > result.maxBidQty {
			result.maxBidQty = q
		}
	}
	for _, a := range data.Asks {
		if len(a) < 2 {
			continue
		}
		p, _ := strconv.ParseFloat(a[0], 64)
		q, _ := strconv.ParseFloat(a[1], 64)
		result.asks = append(result.asks, levelQty{p, q})
		if q > result.maxAskQty {
			result.maxAskQty = q
		}
	}

	if len(result.bids) > 0 && len(result.asks) > 0 {
		bestBid := result.bids[0].price
		bestAsk := result.asks[0].price
		result.midPrice = (bestBid + bestAsk) / 2
		if result.midPrice > 0 {
			result.spreadPct = (bestAsk - bestBid) / result.midPrice * 100
		}
	}

	return result, nil
}

func (c *Collector) getOpenInterest(ctx context.Context) (float64, error) {
	u := fmt.Sprintf("%s/fapi/v1/openInterest?symbol=%s", futuresURL, c.symbol)
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
	oi, _ := strconv.ParseFloat(data.OpenInterest, 64)
	return oi, nil
}

func (c *Collector) getFundingRate(ctx context.Context) (float64, time.Time, error) {
	u := fmt.Sprintf("%s/fapi/v1/premiumIndex?symbol=%s", futuresURL, c.symbol)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return 0, time.Time{}, err
	}

	var data struct {
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, time.Time{}, err
	}

	rate, _ := strconv.ParseFloat(data.LastFundingRate, 64)
	return rate, time.UnixMilli(data.NextFundingTime), nil
}

func (c *Collector) getLongShortRatio(ctx context.Context) (float64, error) {
	u := fmt.Sprintf("%s/futures/data/topLongShortAccountRatio?symbol=%s&period=5m&limit=1",
		futuresURL, c.symbol)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return 0, err
	}

	var data []struct {
		LongShortRatio string `json:"longShortRatio"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, fmt.Errorf("no LS ratio data")
	}
	ratio, _ := strconv.ParseFloat(data[0].LongShortRatio, 64)
	return ratio, nil
}

func (c *Collector) getTakerBuySellRatio(ctx context.Context) (buyRatio float64, volume float64, err error) {
	u := fmt.Sprintf("%s/futures/data/takerlongshortRatio?symbol=%s&period=5m&limit=1",
		futuresURL, c.symbol)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return 0, 0, err
	}

	var data []struct {
		BuySellRatio string `json:"buySellRatio"`
		BuyVol       string `json:"buyVol"`
		SellVol      string `json:"sellVol"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, 0, err
	}
	if len(data) == 0 {
		return 0, 0, fmt.Errorf("no taker data")
	}

	ratio, _ := strconv.ParseFloat(data[0].BuySellRatio, 64)
	buyV, _ := strconv.ParseFloat(data[0].BuyVol, 64)
	sellV, _ := strconv.ParseFloat(data[0].SellVol, 64)
	// Convert ratio to 0-1 range: buySellRatio = buyVol/sellVol
	// buyRatio = buy/(buy+sell) = ratio/(ratio+1)
	buyRatio = ratio / (ratio + 1)
	volume = buyV + sellV

	return buyRatio, volume, nil
}

func (c *Collector) getATR15m(ctx context.Context) (float64, error) {
	u := fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=15m&limit=15", futuresURL, c.symbol)
	body, err := c.httpGet(ctx, u)
	if err != nil {
		return 0, err
	}

	var raw [][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, err
	}

	type candle struct {
		high, low, close float64
	}
	var candles []candle
	for _, k := range raw {
		if len(k) < 5 {
			continue
		}
		var hStr, lStr, cStr string
		json.Unmarshal(k[2], &hStr)
		json.Unmarshal(k[3], &lStr)
		json.Unmarshal(k[4], &cStr)
		h, _ := strconv.ParseFloat(hStr, 64)
		l, _ := strconv.ParseFloat(lStr, 64)
		cl, _ := strconv.ParseFloat(cStr, 64)
		candles = append(candles, candle{h, l, cl})
	}

	if len(candles) < 2 {
		return 0, nil
	}

	// ATR(14) on 15-min candles
	period := 14
	if len(candles)-1 < period {
		period = len(candles) - 1
	}

	var sum float64
	for i := len(candles) - period; i < len(candles); i++ {
		tr := candles[i].high - candles[i].low
		if i > 0 {
			hpc := math.Abs(candles[i].high - candles[i-1].close)
			lpc := math.Abs(candles[i].low - candles[i-1].close)
			if hpc > tr {
				tr = hpc
			}
			if lpc > tr {
				tr = lpc
			}
		}
		sum += tr
	}
	return sum / float64(period), nil
}

func (c *Collector) httpGet(ctx context.Context, url string) ([]byte, error) {
	// Basic rate limiting
	time.Sleep(100 * time.Millisecond)

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
		// Truncate error body
		errMsg := string(body)
		if len(errMsg) > 200 {
			errMsg = errMsg[:200]
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, errMsg)
	}
	return body, nil
}

// Suppress unused import warning
var _ = strings.TrimSpace
