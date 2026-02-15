package upbit

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"traveler/internal/broker"
)

const baseURL = "https://api.upbit.com/v1"

// Client Upbit REST API client implementing broker.Broker interface
type Client struct {
	accessKey  string
	secretKey  string
	httpClient *http.Client

	mu          sync.Mutex
	lastRequest time.Time
}

// NewClient creates a new Upbit client using environment variables
func NewClient() *Client {
	return &Client{
		accessKey:  os.Getenv("UPBIT_ACCESS_KEY"),
		secretKey:  os.Getenv("UPBIT_SECRET_KEY"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// NewClientWithKeys creates a new Upbit client with explicit keys
func NewClientWithKeys(accessKey, secretKey string) *Client {
	return &Client{
		accessKey:  accessKey,
		secretKey:  secretKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Name returns broker name
func (c *Client) Name() string {
	return "upbit"
}

// IsReady checks if API keys are configured
func (c *Client) IsReady() bool {
	return c.accessKey != "" && c.secretKey != ""
}

// generateToken creates a JWT token without query params
func (c *Client) generateToken() (string, error) {
	claims := jwt.MapClaims{
		"access_key": c.accessKey,
		"nonce":      uuid.New().String(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	return token.SignedString([]byte(c.secretKey))
}

// generateTokenWithQuery creates a JWT token with query hash
func (c *Client) generateTokenWithQuery(queryString string) (string, error) {
	hash := sha512.Sum512([]byte(queryString))
	queryHash := hex.EncodeToString(hash[:])

	claims := jwt.MapClaims{
		"access_key":     c.accessKey,
		"nonce":          uuid.New().String(),
		"query_hash":     queryHash,
		"query_hash_alg": "SHA512",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	return token.SignedString([]byte(c.secretKey))
}

// rateLimit waits to respect Upbit's 8 req/sec exchange API limit
func (c *Client) rateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()

	minInterval := 200 * time.Millisecond // 5 req/sec (conservative)
	elapsed := time.Since(c.lastRequest)
	if elapsed < minInterval {
		time.Sleep(minInterval - elapsed)
	}
	c.lastRequest = time.Now()
}

// doGet performs an authenticated GET request
func (c *Client) doGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
	c.rateLimit()

	fullURL := baseURL + path
	var tokenStr string
	var err error

	if len(params) > 0 {
		queryString := params.Encode()
		fullURL += "?" + queryString
		tokenStr, err = c.generateTokenWithQuery(queryString)
	} else {
		tokenStr, err = c.generateToken()
	}
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return body, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// doPost performs an authenticated POST request with form body
func (c *Client) doPost(ctx context.Context, path string, params url.Values) ([]byte, error) {
	c.rateLimit()

	queryString := params.Encode()
	tokenStr, err := c.generateTokenWithQuery(queryString)
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+path, strings.NewReader(queryString))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return body, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// doDelete performs an authenticated DELETE request
func (c *Client) doDelete(ctx context.Context, path string, params url.Values) ([]byte, error) {
	c.rateLimit()

	queryString := params.Encode()
	fullURL := baseURL + path + "?" + queryString
	tokenStr, err := c.generateTokenWithQuery(queryString)
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// ========== Upbit API response types ==========

type accountEntry struct {
	Currency    string `json:"currency"`
	Balance     string `json:"balance"`
	Locked      string `json:"locked"`
	AvgBuyPrice string `json:"avg_buy_price"`
	UnitCurrency string `json:"unit_currency"`
}

type tickerEntry struct {
	Market     string  `json:"market"`
	TradePrice float64 `json:"trade_price"`
	Change     string  `json:"change"`
	ChangeRate float64 `json:"change_rate"`
}

type orderEntry struct {
	UUID            string  `json:"uuid"`
	Side            string  `json:"side"`      // "bid" or "ask"
	OrdType         string  `json:"ord_type"`  // "limit", "price", "market"
	Price           string  `json:"price"`
	State           string  `json:"state"`     // "wait", "watch", "done", "cancel"
	Market          string  `json:"market"`
	Volume          string  `json:"volume"`
	RemainingVolume string  `json:"remaining_volume"`
	ExecutedVolume  string  `json:"executed_volume"`
	TradesCount     int     `json:"trades_count"`
	CreatedAt       string  `json:"created_at"`
	AvgPrice        string  `json:"avg_price"`
}

// ========== Broker interface implementation ==========

// GetQuotes returns current prices for multiple markets in a single API call
func (c *Client) GetQuotes(ctx context.Context, symbols []string) (map[string]float64, error) {
	if len(symbols) == 0 {
		return map[string]float64{}, nil
	}

	params := url.Values{}
	params.Set("markets", strings.Join(symbols, ","))

	body, err := c.doGet(ctx, "/ticker", params)
	if err != nil {
		return nil, fmt.Errorf("get tickers: %w", err)
	}

	var tickers []tickerEntry
	if err := json.Unmarshal(body, &tickers); err != nil {
		return nil, fmt.Errorf("unmarshal tickers: %w", err)
	}

	result := make(map[string]float64, len(tickers))
	for _, t := range tickers {
		result[t.Market] = t.TradePrice
	}
	return result, nil
}

// GetBalance returns account balance
func (c *Client) GetBalance(ctx context.Context) (*broker.AccountBalance, error) {
	body, err := c.doGet(ctx, "/accounts", nil)
	if err != nil {
		return nil, fmt.Errorf("get accounts: %w", err)
	}

	var accounts []accountEntry
	if err := json.Unmarshal(body, &accounts); err != nil {
		return nil, fmt.Errorf("unmarshal accounts: %w", err)
	}

	balance := &broker.AccountBalance{
		Currency:  "KRW",
		Positions: make([]broker.Position, 0),
	}

	// Collect crypto markets for batch price query
	type cryptoAcc struct {
		market  string
		balance float64
		avgCost float64
		name    string
	}
	var cryptoAccounts []cryptoAcc
	var markets []string

	for _, acc := range accounts {
		bal := parseFloat(acc.Balance) + parseFloat(acc.Locked)

		if acc.Currency == "KRW" {
			balance.CashBalance = parseFloat(acc.Balance)
			balance.BuyingPower = parseFloat(acc.Balance)
			continue
		}

		if bal <= 0 {
			continue
		}

		market := "KRW-" + acc.Currency
		cryptoAccounts = append(cryptoAccounts, cryptoAcc{
			market:  market,
			balance: bal,
			avgCost: parseFloat(acc.AvgBuyPrice),
			name:    acc.Currency,
		})
		markets = append(markets, market)
	}

	// Batch price query (single API call instead of N calls)
	prices := make(map[string]float64)
	if len(markets) > 0 {
		var err error
		prices, err = c.GetQuotes(ctx, markets)
		if err != nil {
			log.Printf("[UPBIT] Batch GetQuotes failed: %v", err)
		}
	}

	for _, ca := range cryptoAccounts {
		currentPrice := ca.avgCost // fallback
		if p, ok := prices[ca.market]; ok {
			currentPrice = p
		}

		marketValue := ca.balance * currentPrice
		investedValue := ca.balance * ca.avgCost
		unrealizedPnL := marketValue - investedValue
		unrealizedPct := 0.0
		if investedValue > 0 {
			unrealizedPct = unrealizedPnL / investedValue * 100
		}

		pos := broker.Position{
			Symbol:        ca.market,
			Name:          ca.name,
			Quantity:      ca.balance,
			AvgCost:       ca.avgCost,
			CurrentPrice:  currentPrice,
			MarketValue:   marketValue,
			UnrealizedPnL: unrealizedPnL,
			UnrealizedPct: unrealizedPct,
		}
		balance.Positions = append(balance.Positions, pos)
		balance.TotalEquity += marketValue
	}

	balance.TotalEquity += balance.CashBalance

	return balance, nil
}

// GetQuote returns current trade price for a market (e.g. "KRW-BTC")
func (c *Client) GetQuote(ctx context.Context, symbol string) (float64, error) {
	params := url.Values{}
	params.Set("markets", symbol)

	body, err := c.doGet(ctx, "/ticker", params)
	if err != nil {
		return 0, fmt.Errorf("get ticker: %w", err)
	}

	var tickers []tickerEntry
	if err := json.Unmarshal(body, &tickers); err != nil {
		return 0, fmt.Errorf("unmarshal ticker: %w", err)
	}

	if len(tickers) == 0 {
		return 0, fmt.Errorf("no ticker data for %s", symbol)
	}

	return tickers[0].TradePrice, nil
}

// GetPositions returns non-KRW positions
func (c *Client) GetPositions(ctx context.Context) ([]broker.Position, error) {
	bal, err := c.GetBalance(ctx)
	if err != nil {
		return nil, err
	}
	return bal.Positions, nil
}

// PlaceOrder places an order on Upbit
func (c *Client) PlaceOrder(ctx context.Context, order broker.Order) (*broker.OrderResult, error) {
	params := url.Values{}
	params.Set("market", order.Symbol)

	// Map side
	if order.Side == broker.OrderSideBuy {
		params.Set("side", "bid")
	} else {
		params.Set("side", "ask")
	}

	// Determine order type and parameters
	if order.Type == broker.OrderTypeMarket {
		if order.Side == broker.OrderSideBuy {
			// Market buy: use "price" ord_type with KRW amount
			params.Set("ord_type", "price")
			amount := order.Amount
			if amount <= 0 {
				// Fallback: calculate from quantity * current price
				price, err := c.GetQuote(ctx, order.Symbol)
				if err != nil {
					return nil, fmt.Errorf("get quote for market buy: %w", err)
				}
				amount = order.Quantity * price
			}
			params.Set("price", fmt.Sprintf("%.0f", amount))
		} else {
			// Market sell: use "market" ord_type with volume
			params.Set("ord_type", "market")
			params.Set("volume", formatVolume(order.Quantity))
		}
	} else {
		// Limit order
		params.Set("ord_type", "limit")
		params.Set("price", fmt.Sprintf("%.0f", order.LimitPrice))
		params.Set("volume", formatVolume(order.Quantity))
	}

	body, err := c.doPost(ctx, "/orders", params)
	if err != nil {
		return nil, fmt.Errorf("place order: %w", err)
	}

	var resp orderEntry
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal order response: %w", err)
	}

	return &broker.OrderResult{
		OrderID:     resp.UUID,
		Symbol:      order.Symbol,
		Side:        order.Side,
		Type:        order.Type,
		Quantity:    order.Quantity,
		Status:      mapOrderState(resp.State),
		Message:     fmt.Sprintf("ord_type=%s", resp.OrdType),
		SubmittedAt: time.Now(),
	}, nil
}

// CancelOrder cancels an order by UUID
func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	params := url.Values{}
	params.Set("uuid", orderID)

	_, err := c.doDelete(ctx, "/order", params)
	return err
}

// GetOrder retrieves order status by UUID
func (c *Client) GetOrder(ctx context.Context, orderID string) (*broker.OrderResult, error) {
	params := url.Values{}
	params.Set("uuid", orderID)

	body, err := c.doGet(ctx, "/order", params)
	if err != nil {
		return nil, fmt.Errorf("get order: %w", err)
	}

	var resp orderEntry
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal order: %w", err)
	}

	side := broker.OrderSideBuy
	if resp.Side == "ask" {
		side = broker.OrderSideSell
	}

	return &broker.OrderResult{
		OrderID:   resp.UUID,
		Symbol:    resp.Market,
		Side:      side,
		Quantity:  parseFloat(resp.Volume),
		FilledQty: parseFloat(resp.ExecutedVolume),
		AvgPrice:  parseFloat(resp.AvgPrice),
		Status:    mapOrderState(resp.State),
	}, nil
}

// GetPendingOrders returns orders in "wait" state
func (c *Client) GetPendingOrders(ctx context.Context) ([]broker.PendingOrder, error) {
	params := url.Values{}
	params.Set("state", "wait")

	body, err := c.doGet(ctx, "/orders", params)
	if err != nil {
		return nil, fmt.Errorf("get pending orders: %w", err)
	}

	var orders []orderEntry
	if err := json.Unmarshal(body, &orders); err != nil {
		return nil, fmt.Errorf("unmarshal orders: %w", err)
	}

	result := make([]broker.PendingOrder, 0, len(orders))
	for _, o := range orders {
		side := broker.OrderSideBuy
		if o.Side == "ask" {
			side = broker.OrderSideSell
		}

		totalVol := parseFloat(o.Volume)
		execVol := parseFloat(o.ExecutedVolume)

		createdAt, _ := time.Parse(time.RFC3339, o.CreatedAt)

		result = append(result, broker.PendingOrder{
			OrderID:   o.UUID,
			Symbol:    o.Market,
			Side:      side,
			Type:      broker.OrderTypeLimit,
			Quantity:  totalVol,
			FilledQty: execVol,
			Price:     parseFloat(o.Price),
			Status:    "pending",
			CreatedAt: createdAt,
		})
	}

	return result, nil
}

// ========== Helpers ==========

func mapOrderState(state string) string {
	switch state {
	case "wait", "watch":
		return "submitted"
	case "done":
		return "filled"
	case "cancel":
		return "cancelled"
	default:
		return state
	}
}

func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func formatVolume(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
