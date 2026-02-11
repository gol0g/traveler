package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"time"
)

// Yahoo Finance fundamentals PoC
// 1. Get crumb + cookies from Yahoo Finance
// 2. Use crumb to access quoteSummary API
// 3. Extract key fundamental metrics

type QuoteSummaryResponse struct {
	QuoteSummary struct {
		Result []struct {
			FinancialData       *FinancialData       `json:"financialData"`
			DefaultKeyStats     *DefaultKeyStats     `json:"defaultKeyStatistics"`
			SummaryDetail       *SummaryDetail       `json:"summaryDetail"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"quoteSummary"`
}

type YahooValue struct {
	Raw float64 `json:"raw"`
	Fmt string  `json:"fmt"`
}

type FinancialData struct {
	CurrentPrice     YahooValue `json:"currentPrice"`
	TotalRevenue     YahooValue `json:"totalRevenue"`
	RevenueGrowth    YahooValue `json:"revenueGrowth"`
	GrossProfits     YahooValue `json:"grossProfits"`
	GrossMargins     YahooValue `json:"grossMargins"`
	OperatingMargins YahooValue `json:"operatingMargins"`
	ProfitMargins    YahooValue `json:"profitMargins"`
	TotalDebt        YahooValue `json:"totalDebt"`
	TotalCash        YahooValue `json:"totalCash"`
	DebtToEquity     YahooValue `json:"debtToEquity"`
	ReturnOnEquity   YahooValue `json:"returnOnEquity"`
	EarningsGrowth   YahooValue `json:"earningsGrowth"`
	RevenuePerShare  YahooValue `json:"revenuePerShare"`
}

type DefaultKeyStats struct {
	EnterpriseValue    YahooValue `json:"enterpriseValue"`
	TrailingEps        YahooValue `json:"trailingEps"`
	ForwardEps         YahooValue `json:"forwardEps"`
	PriceToBook        YahooValue `json:"priceToBook"`
	EnterpriseToEbitda YahooValue `json:"enterpriseToEbitda"`
	Beta               YahooValue `json:"beta"`
	SharesOutstanding  YahooValue `json:"sharesOutstanding"`
	FloatShares        YahooValue `json:"floatShares"`
	ShortRatio         YahooValue `json:"shortRatio"`
	PegRatio           YahooValue `json:"pegRatio"`
	FiftyTwoWeekChange YahooValue `json:"52WeekChange"`
}

type SummaryDetail struct {
	MarketCap      YahooValue `json:"marketCap"`
	TrailingPE     YahooValue `json:"trailingPE"`
	ForwardPE      YahooValue `json:"forwardPE"`
	DividendYield  YahooValue `json:"dividendYield"`
	PayoutRatio    YahooValue `json:"payoutRatio"`
	FiftyDayAvg    YahooValue `json:"fiftyDayAverage"`
	TwoHundredAvg  YahooValue `json:"twoHundredDayAverage"`
}

func main() {
	symbols := []string{"BLMN", "DVAX", "AAPL", "MSFT"}
	if len(os.Args) > 1 {
		symbols = os.Args[1:]
	}

	// Create HTTP client with cookie jar
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
	}

	// Step 1: Get crumb
	fmt.Println("=== Getting Yahoo Finance crumb ===")
	crumb, err := getCrumb(client)
	if err != nil {
		fmt.Printf("Failed to get crumb: %v\n", err)
		// Try alternative method
		crumb, err = getCrumbAlt(client)
		if err != nil {
			fmt.Printf("Alternative crumb method also failed: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Printf("Crumb: %s\n\n", crumb)

	// Step 2: Fetch fundamentals for each symbol
	for _, sym := range symbols {
		fmt.Printf("=== %s ===\n", sym)
		data, err := getFundamentals(client, sym, crumb)
		if err != nil {
			fmt.Printf("  ERROR: %v\n\n", err)
			continue
		}
		printFundamentals(sym, data)
		fmt.Println()
	}
}

func getCrumb(client *http.Client) (string, error) {
	// First, visit Yahoo Finance to get cookies
	req, _ := http.NewRequest("GET", "https://fc.yahoo.com", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()

	// Then get the crumb
	req, _ = http.NewRequest("GET", "https://query2.finance.yahoo.com/v1/test/getcrumb", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp, err = client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	crumb := strings.TrimSpace(string(body))
	if crumb == "" || strings.Contains(crumb, "error") || strings.Contains(crumb, "Unauthorized") {
		return "", fmt.Errorf("invalid crumb response: %s", crumb)
	}
	return crumb, nil
}

func getCrumbAlt(client *http.Client) (string, error) {
	// Alternative: use consent flow
	req, _ := http.NewRequest("GET", "https://finance.yahoo.com/quote/AAPL", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	// Extract crumb from page
	idx := strings.Index(html, `"crumb":"`)
	if idx == -1 {
		return "", fmt.Errorf("crumb not found in page")
	}
	start := idx + len(`"crumb":"`)
	end := strings.Index(html[start:], `"`)
	if end == -1 {
		return "", fmt.Errorf("crumb end not found")
	}
	return html[start : start+end], nil
}

func getFundamentals(client *http.Client, symbol, crumb string) (*QuoteSummaryResponse, error) {
	modules := "financialData,defaultKeyStatistics,summaryDetail"
	url := fmt.Sprintf("https://query2.finance.yahoo.com/v10/finance/quoteSummary/%s?modules=%s&crumb=%s",
		symbol, modules, crumb)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var data QuoteSummaryResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	if data.QuoteSummary.Error != nil {
		return nil, fmt.Errorf("API error: %s", data.QuoteSummary.Error.Description)
	}

	if len(data.QuoteSummary.Result) == 0 {
		return nil, fmt.Errorf("no data returned")
	}

	return &data, nil
}

func printFundamentals(symbol string, data *QuoteSummaryResponse) {
	r := data.QuoteSummary.Result[0]

	if r.SummaryDetail != nil {
		sd := r.SummaryDetail
		fmt.Printf("  Market Cap:      %s\n", sd.MarketCap.Fmt)
		fmt.Printf("  P/E (trailing):  %s\n", sd.TrailingPE.Fmt)
		fmt.Printf("  P/E (forward):   %s\n", sd.ForwardPE.Fmt)
		fmt.Printf("  Dividend Yield:  %s\n", sd.DividendYield.Fmt)
		fmt.Printf("  50-day Avg:      %s\n", sd.FiftyDayAvg.Fmt)
		fmt.Printf("  200-day Avg:     %s\n", sd.TwoHundredAvg.Fmt)
	}

	if r.FinancialData != nil {
		fd := r.FinancialData
		fmt.Printf("  Revenue:         %s\n", fd.TotalRevenue.Fmt)
		fmt.Printf("  Revenue Growth:  %s\n", fd.RevenueGrowth.Fmt)
		fmt.Printf("  Profit Margin:   %s\n", fd.ProfitMargins.Fmt)
		fmt.Printf("  Gross Margin:    %s\n", fd.GrossMargins.Fmt)
		fmt.Printf("  Debt/Equity:     %s\n", fd.DebtToEquity.Fmt)
		fmt.Printf("  Total Debt:      %s\n", fd.TotalDebt.Fmt)
		fmt.Printf("  Total Cash:      %s\n", fd.TotalCash.Fmt)
		fmt.Printf("  ROE:             %s\n", fd.ReturnOnEquity.Fmt)
		fmt.Printf("  Earnings Growth: %s\n", fd.EarningsGrowth.Fmt)
	}

	if r.DefaultKeyStats != nil {
		ks := r.DefaultKeyStats
		fmt.Printf("  EPS (trailing):  %s\n", ks.TrailingEps.Fmt)
		fmt.Printf("  EPS (forward):   %s\n", ks.ForwardEps.Fmt)
		fmt.Printf("  EV/EBITDA:       %s\n", ks.EnterpriseToEbitda.Fmt)
		fmt.Printf("  Beta:            %s\n", ks.Beta.Fmt)
		fmt.Printf("  52W Change:      %s\n", ks.FiftyTwoWeekChange.Fmt)
		fmt.Printf("  Short Ratio:     %s\n", ks.ShortRatio.Fmt)
		fmt.Printf("  P/B:             %s\n", ks.PriceToBook.Fmt)
	}

	// Filter verdict
	fmt.Printf("\n  --- FILTER VERDICT ---\n")
	passFilter := true
	reasons := []string{}

	if r.DefaultKeyStats != nil {
		if r.DefaultKeyStats.FiftyTwoWeekChange.Raw < -0.3 {
			passFilter = false
			reasons = append(reasons, fmt.Sprintf("52W decline %.0f%% (< -30%%)", r.DefaultKeyStats.FiftyTwoWeekChange.Raw*100))
		}
	}
	if r.FinancialData != nil {
		if r.FinancialData.DebtToEquity.Raw > 200 {
			passFilter = false
			reasons = append(reasons, fmt.Sprintf("D/E ratio %.0f (> 200)", r.FinancialData.DebtToEquity.Raw))
		}
		if r.FinancialData.ProfitMargins.Raw < -0.1 {
			passFilter = false
			reasons = append(reasons, fmt.Sprintf("Profit margin %.1f%% (< -10%%)", r.FinancialData.ProfitMargins.Raw*100))
		}
	}
	if r.SummaryDetail != nil {
		if r.SummaryDetail.MarketCap.Raw > 0 && r.SummaryDetail.MarketCap.Raw < 200_000_000 {
			passFilter = false
			reasons = append(reasons, fmt.Sprintf("Market cap %s (< $200M)", r.SummaryDetail.MarketCap.Fmt))
		}
	}

	if passFilter {
		fmt.Printf("  PASS - Fundamentals OK\n")
	} else {
		fmt.Printf("  REJECT - %s\n", strings.Join(reasons, "; "))
	}
}
