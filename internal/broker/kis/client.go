package kis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"traveler/internal/broker"
	"traveler/internal/ratelimit"
)

// Client KIS API 클라이언트
type Client struct {
	tokenMgr   *TokenManager
	creds      Credentials
	httpClient *http.Client
	limiter    *ratelimit.Limiter
}

// NewClient KIS 클라이언트 생성
func NewClient(creds Credentials) *Client {
	return &Client{
		tokenMgr:   NewTokenManager(creds),
		creds:      creds,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		limiter:    ratelimit.NewLimiter("kis", 300), // 초당 5회 = 분당 300
	}
}

// Name 브로커 이름
func (c *Client) Name() string {
	return "kis"
}

// IsReady 연결 상태 확인
func (c *Client) IsReady() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := c.tokenMgr.GetToken(ctx)
	return err == nil
}

// doRequest 공통 HTTP 요청 메서드
func (c *Client) doRequest(ctx context.Context, method, path string, trID string, body interface{}) ([]byte, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit: %w", err)
	}

	token, err := c.tokenMgr.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	url := BaseURL + path

	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// KIS 필수 헤더
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("appkey", c.creds.AppKey)
	req.Header.Set("appsecret", c.creds.AppSecret)
	req.Header.Set("tr_id", trID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// getAccountParts 계좌번호를 앞 8자리와 뒤 2자리로 분리
func (c *Client) getAccountParts() (string, string, error) {
	parts := strings.Split(c.creds.AccountNo, "-")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid account number format: %s (expected XXXXXXXX-XX)", c.creds.AccountNo)
	}
	return parts[0], parts[1], nil
}

// detectExchange 종목 코드로 거래소 판단
func (c *Client) detectExchange(symbol string) string {
	// 나스닥 주요 종목들
	nasdaqSymbols := map[string]bool{
		"AAPL": true, "MSFT": true, "GOOGL": true, "GOOG": true, "AMZN": true,
		"NVDA": true, "META": true, "TSLA": true, "AVGO": true, "COST": true,
		"NFLX": true, "AMD": true, "QCOM": true, "INTC": true, "CSCO": true,
		"ADBE": true, "PEP": true, "CMCSA": true, "TMUS": true, "TXN": true,
	}

	if nasdaqSymbols[symbol] {
		return ExchangeNASDAQ
	}

	// NYSE 주요 종목들
	nyseSymbols := map[string]bool{
		"JPM": true, "V": true, "JNJ": true, "WMT": true, "PG": true,
		"MA": true, "UNH": true, "HD": true, "DIS": true, "BAC": true,
		"XOM": true, "CVX": true, "KO": true, "MRK": true, "PFE": true,
	}

	if nyseSymbols[symbol] {
		return ExchangeNYSE
	}

	// 기본값: 나스닥
	return ExchangeNASDAQ
}

// PlaceOrder 주문 실행
func (c *Client) PlaceOrder(ctx context.Context, order broker.Order) (*broker.OrderResult, error) {
	cano, acnt, err := c.getAccountParts()
	if err != nil {
		return nil, err
	}

	// 거래 ID 결정
	var trID string
	if order.Side == broker.OrderSideBuy {
		trID = TrIDBuyReal
	} else {
		trID = TrIDSellReal
	}

	// 주문 구분
	ordDvsn := "00" // 지정가
	price := fmt.Sprintf("%.2f", order.LimitPrice)
	if order.Type == broker.OrderTypeMarket {
		ordDvsn = "01" // 시장가
		price = "0"
	}

	exchange := c.detectExchange(order.Symbol)

	req := orderRequest{
		CANO:            cano,
		ACNT:            acnt,
		OVRS_EXCG_CD:    exchange,
		PDNO:            order.Symbol,
		ORD_QTY:         fmt.Sprintf("%d", order.Quantity),
		OVRS_ORD_UNPR:   price,
		ORD_SVR_DVSN_CD: "0",
		ORD_DVSN:        ordDvsn,
	}

	respBody, err := c.doRequest(ctx, "POST", "/uapi/overseas-stock/v1/trading/order", trID, req)
	if err != nil {
		return nil, err
	}

	var resp orderResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.RtCd != "0" {
		return nil, fmt.Errorf("order failed: [%s] %s", resp.MsgCd, resp.Msg1)
	}

	return &broker.OrderResult{
		OrderID:     resp.Output.ODNO,
		Symbol:      order.Symbol,
		Side:        order.Side,
		Type:        order.Type,
		Quantity:    order.Quantity,
		Status:      "submitted",
		Message:     resp.Msg1,
		SubmittedAt: time.Now(),
	}, nil
}

// CancelOrder 주문 취소
func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	cano, acnt, err := c.getAccountParts()
	if err != nil {
		return err
	}

	req := cancelRequest{
		CANO:              cano,
		ACNT:              acnt,
		OVRS_EXCG_CD:      ExchangeNASDAQ, // TODO: 원주문의 거래소 정보 필요
		PDNO:              "",              // TODO: 원주문의 종목코드 필요
		ORGN_ODNO:         orderID,
		RVSE_CNCL_DVSN_CD: "02", // 취소
		ORD_QTY:           "0",  // 전량취소
		OVRS_ORD_UNPR:     "0",
		ORD_SVR_DVSN_CD:   "0",
	}

	respBody, err := c.doRequest(ctx, "POST", "/uapi/overseas-stock/v1/trading/order-rvsecncl", TrIDCancelReal, req)
	if err != nil {
		return err
	}

	var resp orderResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.RtCd != "0" {
		return fmt.Errorf("cancel failed: [%s] %s", resp.MsgCd, resp.Msg1)
	}

	return nil
}

// GetOrder 주문 조회 (미구현 - 필요시 추가)
func (c *Client) GetOrder(ctx context.Context, orderID string) (*broker.OrderResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// GetBalance 계좌 잔고 조회
func (c *Client) GetBalance(ctx context.Context) (*broker.AccountBalance, error) {
	cano, acnt, err := c.getAccountParts()
	if err != nil {
		return nil, err
	}

	// Query parameters - 해외주식 체결기준현재잔고
	params := fmt.Sprintf("?CANO=%s&ACNT_PRDT_CD=%s&OVRS_EXCG_CD=NASD&TR_CRCY_CD=USD&CTX_AREA_FK200=&CTX_AREA_NK200=",
		cano, acnt)

	respBody, err := c.doRequest(ctx, "GET", "/uapi/overseas-stock/v1/trading/inquire-present-balance"+params, TrIDBalanceReal, nil)
	if err != nil {
		return nil, err
	}

	// 디버그: raw 응답 출력 (필요시 주석 해제)
	// fmt.Printf("[DEBUG] Balance API Response: %s\n", string(respBody))

	var resp balanceResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.RtCd != "0" {
		return nil, fmt.Errorf("balance query failed: [%s] %s", resp.MsgCd, resp.Msg1)
	}

	balance := &broker.AccountBalance{
		Currency:  "USD",
		Positions: make([]broker.Position, 0, len(resp.Output1)),
	}

	for _, p := range resp.Output1 {
		qty := parseFloat(p.CBLC_QTY13)
		if qty <= 0 {
			continue
		}

		avgCost := parseFloat(p.PCHS_AVG_PRIC)
		marketValue := parseFloat(p.OVRS_STCK_EVLU_AMT)
		unrealizedPnL := parseFloat(p.EVLU_PFLS_AMT)
		currentPrice := parseFloat(p.NOW_PRIC2)

		var unrealizedPct float64
		if avgCost > 0 && qty > 0 {
			unrealizedPct = unrealizedPnL / (avgCost * qty) * 100
		}

		pos := broker.Position{
			Symbol:        p.OVRS_PDNO,
			Quantity:      int(qty),
			AvgCost:       avgCost,
			CurrentPrice:  currentPrice,
			MarketValue:   marketValue,
			UnrealizedPnL: unrealizedPnL,
			UnrealizedPct: unrealizedPct,
		}

		balance.Positions = append(balance.Positions, pos)
	}

	// 총 평가금액 (보유 주식)
	balance.TotalEquity = parseFloat(resp.Output2.FRCR_EVLU_AMT2)

	// 매수가능금액(외화예수금) 조회
	buyingPower, err := c.getBuyingPower(ctx)
	if err == nil && buyingPower > 0 {
		balance.BuyingPower = buyingPower
		balance.CashBalance = buyingPower
		// 총 자산 = 보유 주식 평가금액 + 현금
		if balance.TotalEquity == 0 {
			balance.TotalEquity = buyingPower
		} else {
			balance.TotalEquity += buyingPower
		}
	}

	return balance, nil
}

// getBuyingPower 매수가능금액(외화예수금) 조회
func (c *Client) getBuyingPower(ctx context.Context) (float64, error) {
	cano, acnt, err := c.getAccountParts()
	if err != nil {
		return 0, err
	}

	// 매수가능금액 조회 - AAPL 기준으로 조회 (종목 지정 필요)
	params := fmt.Sprintf("?CANO=%s&ACNT_PRDT_CD=%s&OVRS_EXCG_CD=NASD&OVRS_ORD_UNPR=0&ITEM_CD=AAPL",
		cano, acnt)

	respBody, err := c.doRequest(ctx, "GET", "/uapi/overseas-stock/v1/trading/inquire-psamount"+params, TrIDBuyingPower, nil)
	if err != nil {
		return 0, err
	}

	// 디버그: raw 응답 출력 (필요시 주석 해제)
	// fmt.Printf("[DEBUG] BuyingPower API Response: %s\n", string(respBody))

	var resp buyingPowerResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return 0, err
	}

	if resp.RtCd != "0" {
		return 0, fmt.Errorf("buying power query failed: [%s] %s", resp.MsgCd, resp.Msg1)
	}

	return parseFloat(resp.Output.ORD_PSBL_FRCR_AMT), nil
}

// GetPositions 보유 포지션 조회
func (c *Client) GetPositions(ctx context.Context) ([]broker.Position, error) {
	balance, err := c.GetBalance(ctx)
	if err != nil {
		return nil, err
	}
	return balance.Positions, nil
}

// GetPendingOrders 미체결 주문 조회
func (c *Client) GetPendingOrders(ctx context.Context) ([]broker.PendingOrder, error) {
	cano, acnt, err := c.getAccountParts()
	if err != nil {
		return nil, err
	}

	params := fmt.Sprintf("?CANO=%s&ACNT_PRDT_CD=%s&OVRS_EXCG_CD=NASD&SORT_SQN=DS&CTX_AREA_FK200=&CTX_AREA_NK200=",
		cano, acnt)

	respBody, err := c.doRequest(ctx, "GET", "/uapi/overseas-stock/v1/trading/inquire-nccs"+params, TrIDPendingReal, nil)
	if err != nil {
		return nil, err
	}

	var resp pendingResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.RtCd != "0" {
		return nil, fmt.Errorf("pending query failed: [%s] %s", resp.MsgCd, resp.Msg1)
	}

	orders := make([]broker.PendingOrder, 0, len(resp.Output))
	for _, o := range resp.Output {
		side := broker.OrderSideBuy
		if o.SLL_BUY_DVSN_CD == "01" {
			side = broker.OrderSideSell
		}

		orders = append(orders, broker.PendingOrder{
			OrderID:   o.ODNO,
			Symbol:    o.OVRS_PDNO,
			Side:      side,
			Type:      broker.OrderTypeLimit,
			Quantity:  int(parseFloat(o.ORD_QTY)),
			FilledQty: int(parseFloat(o.ORD_QTY) - parseFloat(o.NCCS_QTY)),
			Price:     parseFloat(o.FT_ORD_UNPR3),
			Status:    "pending",
		})
	}

	return orders, nil
}

// GetQuote 현재가 조회
func (c *Client) GetQuote(ctx context.Context, symbol string) (float64, error) {
	exchange := c.detectExchange(symbol)

	params := fmt.Sprintf("?AUTH=&EXCD=%s&SYMB=%s", exchange, symbol)

	respBody, err := c.doRequest(ctx, "GET", "/uapi/overseas-price/v1/quotations/price"+params, TrIDPriceReal, nil)
	if err != nil {
		return 0, err
	}

	var resp priceResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return 0, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.RtCd != "0" {
		return 0, fmt.Errorf("quote query failed: [%s] %s", resp.MsgCd, resp.Msg1)
	}

	return parseFloat(resp.Output.LAST), nil
}

// parseFloat 문자열을 float64로 변환
func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
