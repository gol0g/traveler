package binance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"traveler/internal/broker"
)

const baseURL = "https://fapi.binance.com"
const spotBaseURL = "https://api.binance.com"

// SymbolInfo holds exchange filter rules for a futures symbol
type SymbolInfo struct {
	Symbol            string
	PricePrecision    int
	QuantityPrecision int
	MinQty            float64
	StepSize          float64
	MinNotional       float64
	TickSize          float64
}

// Client implements broker.Broker for Binance USDT-M Futures
type Client struct {
	apiKey    string
	secretKey string
	client    *http.Client

	mu          sync.Mutex
	lastRequest time.Time

	exchangeInfo map[string]*SymbolInfo
	leverage     int
}

// NewClient creates a Binance Futures client from environment variables
func NewClient(leverage int) *Client {
	if leverage <= 0 {
		leverage = 2
	}
	return &Client{
		apiKey:       os.Getenv("BINANCE_API_KEY"),
		secretKey:    os.Getenv("BINANCE_SECRET_KEY"),
		client:       &http.Client{Timeout: 30 * time.Second},
		exchangeInfo: make(map[string]*SymbolInfo),
		leverage:     leverage,
	}
}

func (c *Client) Name() string  { return "binance-futures" }
func (c *Client) IsReady() bool { return c.apiKey != "" && c.secretKey != "" }

// Init loads exchange info and sets leverage for given symbols.
// Must be called before trading.
func (c *Client) Init(ctx context.Context, symbols []string) error {
	if err := c.loadExchangeInfo(ctx); err != nil {
		return fmt.Errorf("load exchange info: %w", err)
	}
	for _, sym := range symbols {
		if err := c.setLeverage(ctx, sym, c.leverage); err != nil {
			log.Printf("[BINANCE] Warning: set leverage %s: %v", sym, err)
		}
	}
	// Set one-way position mode (ignore error if already set)
	c.setPositionMode(ctx, false)
	return nil
}

// --- Broker interface ---

func (c *Client) PlaceOrder(ctx context.Context, order broker.Order) (*broker.OrderResult, error) {
	c.rateLimit()

	params := url.Values{}
	params.Set("symbol", order.Symbol)
	params.Set("type", "MARKET")

	if order.Side == broker.OrderSideBuy {
		params.Set("side", "BUY")
	} else {
		params.Set("side", "SELL")
	}

	if order.ReduceOnly {
		params.Set("reduceOnly", "true")
	}

	// Quantity: if Amount is set (USDT), convert to base qty using current price
	qty := order.Quantity
	if qty <= 0 && order.Amount > 0 {
		price, err := c.GetQuote(ctx, order.Symbol)
		if err != nil {
			return nil, fmt.Errorf("get quote for qty calc: %w", err)
		}
		qty = order.Amount * float64(c.leverage) / price
	}

	qty = c.roundQuantity(order.Symbol, qty)
	if qty <= 0 {
		return nil, fmt.Errorf("quantity too small after rounding")
	}
	params.Set("quantity", c.formatQuantity(order.Symbol, qty))

	resp, err := c.signedRequest(ctx, "POST", "/fapi/v1/order", params)
	if err != nil {
		return nil, err
	}

	var data struct {
		OrderID     int64  `json:"orderId"`
		Symbol      string `json:"symbol"`
		Status      string `json:"status"`
		ExecutedQty string `json:"executedQty"`
		AvgPrice    string `json:"avgPrice"`
		Side        string `json:"side"`
		Type        string `json:"type"`
		UpdateTime  int64  `json:"updateTime"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("parse order response: %w", err)
	}

	filledQty, _ := strconv.ParseFloat(data.ExecutedQty, 64)
	avgPrice, _ := strconv.ParseFloat(data.AvgPrice, 64)

	result := &broker.OrderResult{
		OrderID:     strconv.FormatInt(data.OrderID, 10),
		Symbol:      data.Symbol,
		Side:        order.Side,
		Type:        order.Type,
		Quantity:    qty,
		FilledQty:   filledQty,
		AvgPrice:    avgPrice,
		Status:      strings.ToLower(data.Status),
		SubmittedAt: time.Now(),
	}
	if data.Status == "FILLED" {
		result.FilledAt = time.Now()
	}
	return result, nil
}

func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	c.rateLimit()
	// We need symbol for cancel; not available from orderID alone.
	// Market orders fill instantly so cancel is rarely needed.
	return fmt.Errorf("cancel not implemented for market orders")
}

func (c *Client) GetOrder(ctx context.Context, orderID string) (*broker.OrderResult, error) {
	// Not needed for market-order-only flow
	return nil, fmt.Errorf("GetOrder not implemented")
}

func (c *Client) GetBalance(ctx context.Context) (*broker.AccountBalance, error) {
	c.rateLimit()

	resp, err := c.signedRequest(ctx, "GET", "/fapi/v2/balance", nil)
	if err != nil {
		return nil, err
	}

	var balances []struct {
		Asset            string `json:"asset"`
		Balance          string `json:"balance"`
		AvailableBalance string `json:"availableBalance"`
		CrossUnPnl       string `json:"crossUnPnl"`
	}
	if err := json.Unmarshal(resp, &balances); err != nil {
		return nil, fmt.Errorf("parse balance: %w", err)
	}

	for _, b := range balances {
		if b.Asset == "USDT" {
			bal, _ := strconv.ParseFloat(b.Balance, 64)
			avail, _ := strconv.ParseFloat(b.AvailableBalance, 64)
			unrealized, _ := strconv.ParseFloat(b.CrossUnPnl, 64)

			positions, _ := c.GetPositions(ctx)

			return &broker.AccountBalance{
				Currency:    "USDT",
				CashBalance: avail,
				BuyingPower: avail * float64(c.leverage),
				TotalEquity: bal + unrealized,
				Positions:   positions,
			}, nil
		}
	}
	return nil, fmt.Errorf("USDT balance not found")
}

func (c *Client) GetPositions(ctx context.Context) ([]broker.Position, error) {
	c.rateLimit()

	resp, err := c.signedRequest(ctx, "GET", "/fapi/v2/positionRisk", nil)
	if err != nil {
		return nil, err
	}

	var positions []struct {
		Symbol           string `json:"symbol"`
		PositionAmt      string `json:"positionAmt"`
		EntryPrice       string `json:"entryPrice"`
		MarkPrice        string `json:"markPrice"`
		UnRealizedProfit string `json:"unRealizedProfit"`
		Leverage         string `json:"leverage"`
		Notional         string `json:"notional"`
	}
	if err := json.Unmarshal(resp, &positions); err != nil {
		return nil, fmt.Errorf("parse positions: %w", err)
	}

	var result []broker.Position
	for _, p := range positions {
		posAmt, _ := strconv.ParseFloat(p.PositionAmt, 64)
		if posAmt == 0 {
			continue
		}

		entryPrice, _ := strconv.ParseFloat(p.EntryPrice, 64)
		markPrice, _ := strconv.ParseFloat(p.MarkPrice, 64)
		unrealizedPnL, _ := strconv.ParseFloat(p.UnRealizedProfit, 64)

		qty := math.Abs(posAmt)
		marketValue := qty * markPrice
		unrealizedPct := 0.0
		if entryPrice > 0 {
			if posAmt < 0 {
				// Short position: profit when price drops
				unrealizedPct = (entryPrice - markPrice) / entryPrice * 100
			} else {
				unrealizedPct = (markPrice - entryPrice) / entryPrice * 100
			}
		}

		result = append(result, broker.Position{
			Symbol:        p.Symbol,
			Quantity:      qty,
			AvgCost:       entryPrice,
			CurrentPrice:  markPrice,
			MarketValue:   marketValue,
			UnrealizedPnL: unrealizedPnL,
			UnrealizedPct: unrealizedPct,
		})
	}
	return result, nil
}

func (c *Client) GetPendingOrders(ctx context.Context) ([]broker.PendingOrder, error) {
	// Market orders only — no pending orders expected
	return nil, nil
}

func (c *Client) GetQuote(ctx context.Context, symbol string) (float64, error) {
	c.rateLimit()

	u := fmt.Sprintf("%s/fapi/v1/ticker/price?symbol=%s", baseURL, symbol)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("ticker %s: HTTP %d: %s", symbol, resp.StatusCode, string(body))
	}

	var data struct {
		Price string `json:"price"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(data.Price, 64)
}

// --- Binance-specific methods ---

// GetFundingRate returns the current funding rate for a symbol.
// Positive = shorts receive, negative = shorts pay.
func (c *Client) GetFundingRate(ctx context.Context, symbol string) (float64, time.Time, error) {
	c.rateLimit()

	u := fmt.Sprintf("%s/fapi/v1/premiumIndex?symbol=%s", baseURL, symbol)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, time.Time{}, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, time.Time{}, fmt.Errorf("funding %s: HTTP %d: %s", symbol, resp.StatusCode, string(body))
	}

	var data struct {
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, time.Time{}, err
	}

	rate, _ := strconv.ParseFloat(data.LastFundingRate, 64)
	nextTime := time.UnixMilli(data.NextFundingTime)
	return rate, nextTime, nil
}

// GetExchangeInfo returns cached symbol info
func (c *Client) GetExchangeInfo(symbol string) *SymbolInfo {
	return c.exchangeInfo[symbol]
}

// SetLeverage sets leverage for a specific symbol (public wrapper).
func (c *Client) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	return c.setLeverage(ctx, symbol, leverage)
}

// --- Spot API methods ---

// SpotBuy places a Spot MARKET BUY order spending a given USDT amount.
// Returns filled quantity and average price.
func (c *Client) SpotBuy(ctx context.Context, symbol string, quoteAmount float64) (filledQty, avgPrice float64, err error) {
	c.rateLimit()
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("side", "BUY")
	params.Set("type", "MARKET")
	params.Set("quoteOrderQty", strconv.FormatFloat(quoteAmount, 'f', 2, 64))

	resp, err := c.spotSignedRequest(ctx, "POST", "/api/v3/order", params)
	if err != nil {
		return 0, 0, err
	}

	var data struct {
		ExecutedQty         string `json:"executedQty"`
		CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
		Status              string `json:"status"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return 0, 0, fmt.Errorf("parse spot buy response: %w", err)
	}

	filledQty, _ = strconv.ParseFloat(data.ExecutedQty, 64)
	quoteQty, _ := strconv.ParseFloat(data.CummulativeQuoteQty, 64)
	if filledQty > 0 {
		avgPrice = quoteQty / filledQty
	}
	return filledQty, avgPrice, nil
}

// SpotSell places a Spot MARKET SELL order for a given quantity.
func (c *Client) SpotSell(ctx context.Context, symbol string, quantity float64) (avgPrice float64, err error) {
	c.rateLimit()
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("side", "SELL")
	params.Set("type", "MARKET")
	params.Set("quantity", c.formatQuantity(symbol, quantity))

	resp, err := c.spotSignedRequest(ctx, "POST", "/api/v3/order", params)
	if err != nil {
		return 0, err
	}

	var data struct {
		ExecutedQty         string `json:"executedQty"`
		CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return 0, fmt.Errorf("parse spot sell response: %w", err)
	}

	filledQty, _ := strconv.ParseFloat(data.ExecutedQty, 64)
	quoteQty, _ := strconv.ParseFloat(data.CummulativeQuoteQty, 64)
	if filledQty > 0 {
		avgPrice = quoteQty / filledQty
	}
	return avgPrice, nil
}

// SpotGetBalance returns the free balance for a given asset on Spot.
func (c *Client) SpotGetBalance(ctx context.Context, asset string) (free float64, err error) {
	c.rateLimit()
	params := url.Values{}
	params.Set("omitZeroBalances", "true")

	resp, err := c.spotSignedRequest(ctx, "GET", "/api/v3/account", params)
	if err != nil {
		return 0, err
	}

	var acct struct {
		Balances []struct {
			Asset string `json:"asset"`
			Free  string `json:"free"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(resp, &acct); err != nil {
		return 0, fmt.Errorf("parse spot account: %w", err)
	}

	for _, b := range acct.Balances {
		if b.Asset == asset {
			free, _ = strconv.ParseFloat(b.Free, 64)
			return free, nil
		}
	}
	return 0, nil
}

// Transfer moves an asset between Spot and Futures wallets.
// transferType: "MAIN_UMFUTURE" (Spot → Futures), "UMFUTURE_MAIN" (Futures → Spot)
func (c *Client) Transfer(ctx context.Context, asset string, amount float64, transferType string) error {
	c.rateLimit()
	params := url.Values{}
	params.Set("asset", asset)
	params.Set("amount", strconv.FormatFloat(amount, 'f', 8, 64))
	params.Set("type", transferType)

	_, err := c.spotSignedRequest(ctx, "POST", "/sapi/v1/asset/transfer", params)
	return err
}

// GetFundingIncome returns total funding fee income for a symbol since a start time.
func (c *Client) GetFundingIncome(ctx context.Context, symbol string, startTime time.Time) (float64, error) {
	c.rateLimit()
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("incomeType", "FUNDING_FEE")
	params.Set("startTime", strconv.FormatInt(startTime.UnixMilli(), 10))
	params.Set("limit", "1000")

	resp, err := c.signedRequest(ctx, "GET", "/fapi/v1/income", params)
	if err != nil {
		return 0, err
	}

	var incomes []struct {
		Income string `json:"income"`
	}
	if err := json.Unmarshal(resp, &incomes); err != nil {
		return 0, fmt.Errorf("parse funding income: %w", err)
	}

	var total float64
	for _, inc := range incomes {
		v, _ := strconv.ParseFloat(inc.Income, 64)
		total += v
	}
	return total, nil
}

// spotSignedRequest sends a signed request to the Spot API (api.binance.com).
func (c *Client) spotSignedRequest(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	signed := c.sign(params)

	var u string
	var req *http.Request
	var err error

	if method == "GET" {
		u = fmt.Sprintf("%s%s?%s", spotBaseURL, path, signed)
		req, err = http.NewRequestWithContext(ctx, method, u, nil)
	} else {
		u = fmt.Sprintf("%s%s", spotBaseURL, path)
		req, err = http.NewRequestWithContext(ctx, method, u, strings.NewReader(signed))
		if req != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("binance-spot %s %s: HTTP %d: %s", method, path, resp.StatusCode, string(body))
	}
	return body, nil
}

// --- Internal helpers ---

func (c *Client) sign(params url.Values) string {
	if params == nil {
		params = url.Values{}
	}
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	query := params.Encode()
	mac := hmac.New(sha256.New, []byte(c.secretKey))
	mac.Write([]byte(query))
	sig := hex.EncodeToString(mac.Sum(nil))
	return query + "&signature=" + sig
}

func (c *Client) signedRequest(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	signed := c.sign(params)

	var u string
	var req *http.Request
	var err error

	if method == "GET" {
		u = fmt.Sprintf("%s%s?%s", baseURL, path, signed)
		req, err = http.NewRequestWithContext(ctx, method, u, nil)
	} else {
		u = fmt.Sprintf("%s%s", baseURL, path)
		req, err = http.NewRequestWithContext(ctx, method, u, strings.NewReader(signed))
		if req != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("binance %s %s: HTTP %d: %s", method, path, resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *Client) rateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()

	minInterval := 100 * time.Millisecond // 10 req/sec
	elapsed := time.Since(c.lastRequest)
	if elapsed < minInterval {
		time.Sleep(minInterval - elapsed)
	}
	c.lastRequest = time.Now()
}

func (c *Client) loadExchangeInfo(ctx context.Context) error {
	u := baseURL + "/fapi/v1/exchangeInfo"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("exchangeInfo: HTTP %d", resp.StatusCode)
	}

	var info struct {
		Symbols []struct {
			Symbol            string `json:"symbol"`
			PricePrecision    int    `json:"pricePrecision"`
			QuantityPrecision int    `json:"quantityPrecision"`
			Filters           []struct {
				FilterType string `json:"filterType"`
				MinQty     string `json:"minQty,omitempty"`
				StepSize   string `json:"stepSize,omitempty"`
				TickSize   string `json:"tickSize,omitempty"`
				Notional   string `json:"notional,omitempty"`
			} `json:"filters"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return err
	}

	for _, s := range info.Symbols {
		si := &SymbolInfo{
			Symbol:            s.Symbol,
			PricePrecision:    s.PricePrecision,
			QuantityPrecision: s.QuantityPrecision,
		}
		for _, f := range s.Filters {
			switch f.FilterType {
			case "LOT_SIZE":
				si.MinQty, _ = strconv.ParseFloat(f.MinQty, 64)
				si.StepSize, _ = strconv.ParseFloat(f.StepSize, 64)
			case "PRICE_FILTER":
				si.TickSize, _ = strconv.ParseFloat(f.TickSize, 64)
			case "MIN_NOTIONAL":
				si.MinNotional, _ = strconv.ParseFloat(f.Notional, 64)
			}
		}
		c.exchangeInfo[s.Symbol] = si
	}

	log.Printf("[BINANCE] Loaded exchange info: %d symbols", len(c.exchangeInfo))
	return nil
}

func (c *Client) setLeverage(ctx context.Context, symbol string, leverage int) error {
	c.rateLimit()
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("leverage", strconv.Itoa(leverage))

	_, err := c.signedRequest(ctx, "POST", "/fapi/v1/leverage", params)
	if err != nil {
		return err
	}
	log.Printf("[BINANCE] Set leverage %s = %dx", symbol, leverage)
	return nil
}

func (c *Client) setPositionMode(ctx context.Context, dualSide bool) {
	c.rateLimit()
	params := url.Values{}
	params.Set("dualSidePosition", strconv.FormatBool(dualSide))
	// Ignore error — already in correct mode
	c.signedRequest(ctx, "POST", "/fapi/v1/positionSide/dual", params)
}

func (c *Client) roundQuantity(symbol string, qty float64) float64 {
	si, ok := c.exchangeInfo[symbol]
	if !ok || si.StepSize <= 0 {
		return math.Floor(qty*1e8) / 1e8
	}
	rounded := math.Floor(qty/si.StepSize) * si.StepSize
	if rounded < si.MinQty {
		return 0
	}
	return rounded
}

func (c *Client) formatQuantity(symbol string, qty float64) string {
	si, ok := c.exchangeInfo[symbol]
	if !ok {
		return strconv.FormatFloat(qty, 'f', 8, 64)
	}
	return strconv.FormatFloat(qty, 'f', si.QuantityPrecision, 64)
}
