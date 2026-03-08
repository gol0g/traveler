package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// KIS API for domestic intraday candles
// TR_ID: FHKST03010200 (주식당일분봉조회)
// Endpoint: /uapi/domestic-stock/v1/quotations/inquire-time-itemchartprice

const kisBaseURL = "https://openapi.koreainvestment.com:9443"

// KISKRCollector collects KR stock intraday data via KIS API.
type KISKRCollector struct {
	db      *DB
	client  *http.Client
	symbols []string // e.g. ["005930", "000660", ...]

	appKey    string
	appSecret string
	token     string
	tokenExp  time.Time
}

// NewKISKRCollector creates a new KIS KR stock collector.
func NewKISKRCollector(db *DB, symbols []string, appKey, appSecret string) *KISKRCollector {
	return &KISKRCollector{
		db:        db,
		client:    &http.Client{Timeout: 15 * time.Second},
		symbols:   symbols,
		appKey:    appKey,
		appSecret: appSecret,
	}
}

// NewKISKRCollectorFromEnv creates a collector using environment variables.
func NewKISKRCollectorFromEnv(db *DB, symbols []string) (*KISKRCollector, error) {
	appKey := os.Getenv("KIS_KR_APP_KEY")
	appSecret := os.Getenv("KIS_KR_APP_SECRET")
	if appKey == "" || appSecret == "" {
		return nil, fmt.Errorf("KIS_KR_APP_KEY and KIS_KR_APP_SECRET required")
	}
	return NewKISKRCollector(db, symbols, appKey, appSecret), nil
}

// CollectCandles fetches 1m candles for all symbols.
// KIS rate limit: 300/min → with sleep 250ms between calls, ~240/min (safe margin).
func (c *KISKRCollector) CollectCandles(ctx context.Context) error {
	if err := c.ensureToken(ctx); err != nil {
		return fmt.Errorf("token: %w", err)
	}

	var allCandles []Candle
	for _, sym := range c.symbols {
		candles, err := c.fetchMinuteCandles(ctx, sym)
		if err != nil {
			log.Printf("[COLLECT-KR] candles %s: %v", sym, err)
			continue
		}
		allCandles = append(allCandles, candles...)
		time.Sleep(250 * time.Millisecond) // KIS rate limit safety
	}
	if len(allCandles) > 0 {
		return c.db.InsertCandles(allCandles)
	}
	return nil
}

func (c *KISKRCollector) fetchMinuteCandles(ctx context.Context, symbol string) ([]Candle, error) {
	// FHKST03010200: 주식당일분봉조회
	now := time.Now().In(time.FixedZone("KST", 9*60*60))
	timeStr := now.Format("150405") // HHMMSS

	params := fmt.Sprintf("?FID_ETC_CLS_CODE=&FID_COND_MRKT_DIV_CODE=J"+
		"&FID_INPUT_ISCD=%s&FID_INPUT_HOUR_1=%s&FID_PW_DATA_INCU_YN=N",
		symbol, timeStr)

	body, err := c.kisRequest(ctx, "GET",
		"/uapi/domestic-stock/v1/quotations/inquire-time-itemchartprice"+params,
		"FHKST03010200")
	if err != nil {
		return nil, err
	}

	var resp struct {
		RtCd   string `json:"rt_cd"`
		MsgCd  string `json:"msg_cd"`
		Msg1   string `json:"msg1"`
		Output2 []struct {
			StckBsopDate string `json:"stck_bsop_date"` // YYYYMMDD
			StckCntgHour string `json:"stck_cntg_hour"` // HHMMSS
			StckOprc     string `json:"stck_oprc"`       // 시가
			StckHgpr     string `json:"stck_hgpr"`       // 고가
			StckLwpr     string `json:"stck_lwpr"`       // 저가
			StckPrpr     string `json:"stck_prpr"`       // 현재가(종가)
			CntgVol      string `json:"cntg_vol"`        // 거래량
		} `json:"output2"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.RtCd != "0" {
		return nil, fmt.Errorf("KIS error: [%s] %s", resp.MsgCd, resp.Msg1)
	}

	kst := time.FixedZone("KST", 9*60*60)
	var candles []Candle
	// Take only last 2 candles (most recent)
	limit := 2
	if len(resp.Output2) < limit {
		limit = len(resp.Output2)
	}
	for i := 0; i < limit; i++ {
		r := resp.Output2[i]
		dateStr := r.StckBsopDate + r.StckCntgHour // YYYYMMDDHHMMSS
		t, err := time.ParseInLocation("20060102150405", dateStr, kst)
		if err != nil {
			continue
		}

		open, _ := strconv.ParseFloat(r.StckOprc, 64)
		high, _ := strconv.ParseFloat(r.StckHgpr, 64)
		low, _ := strconv.ParseFloat(r.StckLwpr, 64)
		close_, _ := strconv.ParseFloat(r.StckPrpr, 64)
		vol, _ := strconv.ParseFloat(r.CntgVol, 64)

		candles = append(candles, Candle{
			Market: "kis_kr", Symbol: symbol, Interval: "1m",
			Time: t.Unix(), Open: open, High: high, Low: low, Close: close_, Volume: vol,
		})
	}
	return candles, nil
}

func (c *KISKRCollector) ensureToken(ctx context.Context) error {
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return nil
	}

	payload := map[string]string{
		"grant_type": "client_credentials",
		"appkey":     c.appKey,
		"appsecret":  c.appSecret,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", kisBaseURL+"/oauth2/tokenP",
		strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("token HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return err
	}

	c.token = tokenResp.AccessToken
	c.tokenExp = time.Now().Add(time.Duration(tokenResp.ExpiresIn-600) * time.Second) // 10min buffer
	log.Printf("[COLLECT-KR] Token refreshed, expires in %ds", tokenResp.ExpiresIn)
	return nil
}

func (c *KISKRCollector) kisRequest(ctx context.Context, method, path, trID string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, kisBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("authorization", "Bearer "+c.token)
	req.Header.Set("appkey", c.appKey)
	req.Header.Set("appsecret", c.appSecret)
	req.Header.Set("tr_id", trID)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("KIS HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}
