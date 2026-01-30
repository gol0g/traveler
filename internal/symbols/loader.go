package symbols

import (
	"context"
	"fmt"
	"strings"

	"traveler/internal/provider"
	"traveler/pkg/model"
)

// Loader handles loading stock symbols from various sources
type Loader struct {
	provider provider.Provider
}

// NewLoader creates a new symbol loader
func NewLoader(p provider.Provider) *Loader {
	return &Loader{provider: p}
}

// LoadUSStocks loads all US stocks (NYSE + NASDAQ)
func (l *Loader) LoadUSStocks(ctx context.Context) ([]model.Stock, error) {
	var allStocks []model.Stock

	// Load from each exchange
	exchanges := []string{"US"} // Finnhub uses "US" for combined NYSE/NASDAQ

	for _, exchange := range exchanges {
		stocks, err := l.provider.GetSymbols(ctx, exchange)
		if err != nil {
			// Try fallback for specific exchanges
			continue
		}
		allStocks = append(allStocks, stocks...)
	}

	if len(allStocks) == 0 {
		// Use hardcoded popular stocks as fallback
		return l.getDefaultUSStocks(), nil
	}

	// Filter out non-standard symbols (no dots, slashes, etc.)
	filtered := make([]model.Stock, 0, len(allStocks))
	for _, s := range allStocks {
		if isValidSymbol(s.Symbol) {
			filtered = append(filtered, s)
		}
	}

	return filtered, nil
}

// LoadSymbols loads specific symbols
func (l *Loader) LoadSymbols(ctx context.Context, symbols []string) ([]model.Stock, error) {
	stocks := make([]model.Stock, len(symbols))
	for i, sym := range symbols {
		stocks[i] = model.Stock{
			Symbol:   strings.ToUpper(strings.TrimSpace(sym)),
			Name:     sym,
			Exchange: "US",
		}
	}
	return stocks, nil
}

// isValidSymbol checks if a symbol is a standard ticker
func isValidSymbol(symbol string) bool {
	if len(symbol) == 0 || len(symbol) > 5 {
		return false
	}
	for _, c := range symbol {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	return true
}

// getDefaultUSStocks returns a list of popular US stocks for fallback
func (l *Loader) getDefaultUSStocks() []model.Stock {
	popularSymbols := []struct {
		symbol   string
		name     string
		exchange string
	}{
		// Tech Giants
		{"AAPL", "Apple Inc.", "NASDAQ"},
		{"MSFT", "Microsoft Corporation", "NASDAQ"},
		{"GOOGL", "Alphabet Inc.", "NASDAQ"},
		{"AMZN", "Amazon.com Inc.", "NASDAQ"},
		{"META", "Meta Platforms Inc.", "NASDAQ"},
		{"NVDA", "NVIDIA Corporation", "NASDAQ"},
		{"TSLA", "Tesla Inc.", "NASDAQ"},
		{"AMD", "Advanced Micro Devices", "NASDAQ"},
		{"INTC", "Intel Corporation", "NASDAQ"},
		{"CRM", "Salesforce Inc.", "NYSE"},
		{"ORCL", "Oracle Corporation", "NYSE"},
		{"ADBE", "Adobe Inc.", "NASDAQ"},
		{"CSCO", "Cisco Systems Inc.", "NASDAQ"},
		{"AVGO", "Broadcom Inc.", "NASDAQ"},
		{"QCOM", "Qualcomm Inc.", "NASDAQ"},

		// Finance
		{"JPM", "JPMorgan Chase & Co.", "NYSE"},
		{"BAC", "Bank of America Corp", "NYSE"},
		{"WFC", "Wells Fargo & Company", "NYSE"},
		{"GS", "Goldman Sachs Group", "NYSE"},
		{"MS", "Morgan Stanley", "NYSE"},
		{"C", "Citigroup Inc.", "NYSE"},
		{"BLK", "BlackRock Inc.", "NYSE"},
		{"SCHW", "Charles Schwab Corp", "NYSE"},
		{"AXP", "American Express Co.", "NYSE"},
		{"V", "Visa Inc.", "NYSE"},
		{"MA", "Mastercard Inc.", "NYSE"},
		{"PYPL", "PayPal Holdings Inc.", "NASDAQ"},

		// Healthcare
		{"JNJ", "Johnson & Johnson", "NYSE"},
		{"UNH", "UnitedHealth Group", "NYSE"},
		{"PFE", "Pfizer Inc.", "NYSE"},
		{"ABBV", "AbbVie Inc.", "NYSE"},
		{"MRK", "Merck & Co. Inc.", "NYSE"},
		{"LLY", "Eli Lilly and Company", "NYSE"},
		{"TMO", "Thermo Fisher Scientific", "NYSE"},
		{"ABT", "Abbott Laboratories", "NYSE"},
		{"BMY", "Bristol-Myers Squibb", "NYSE"},
		{"AMGN", "Amgen Inc.", "NASDAQ"},

		// Consumer
		{"WMT", "Walmart Inc.", "NYSE"},
		{"HD", "Home Depot Inc.", "NYSE"},
		{"PG", "Procter & Gamble Co.", "NYSE"},
		{"KO", "Coca-Cola Company", "NYSE"},
		{"PEP", "PepsiCo Inc.", "NASDAQ"},
		{"COST", "Costco Wholesale Corp", "NASDAQ"},
		{"NKE", "Nike Inc.", "NYSE"},
		{"MCD", "McDonald's Corporation", "NYSE"},
		{"SBUX", "Starbucks Corporation", "NASDAQ"},
		{"TGT", "Target Corporation", "NYSE"},

		// Industrial
		{"CAT", "Caterpillar Inc.", "NYSE"},
		{"BA", "Boeing Company", "NYSE"},
		{"HON", "Honeywell International", "NASDAQ"},
		{"UPS", "United Parcel Service", "NYSE"},
		{"GE", "General Electric Co.", "NYSE"},
		{"MMM", "3M Company", "NYSE"},
		{"LMT", "Lockheed Martin Corp", "NYSE"},
		{"RTX", "Raytheon Technologies", "NYSE"},

		// Energy
		{"XOM", "Exxon Mobil Corporation", "NYSE"},
		{"CVX", "Chevron Corporation", "NYSE"},
		{"COP", "ConocoPhillips", "NYSE"},
		{"SLB", "Schlumberger Limited", "NYSE"},
		{"EOG", "EOG Resources Inc.", "NYSE"},

		// Communication
		{"DIS", "Walt Disney Company", "NYSE"},
		{"NFLX", "Netflix Inc.", "NASDAQ"},
		{"CMCSA", "Comcast Corporation", "NASDAQ"},
		{"VZ", "Verizon Communications", "NYSE"},
		{"T", "AT&T Inc.", "NYSE"},
		{"TMUS", "T-Mobile US Inc.", "NASDAQ"},

		// Real Estate & Utilities
		{"AMT", "American Tower Corp", "NYSE"},
		{"PLD", "Prologis Inc.", "NYSE"},
		{"NEE", "NextEra Energy Inc.", "NYSE"},
		{"DUK", "Duke Energy Corp", "NYSE"},
		{"SO", "Southern Company", "NYSE"},
	}

	stocks := make([]model.Stock, len(popularSymbols))
	for i, s := range popularSymbols {
		stocks[i] = model.Stock{
			Symbol:   s.symbol,
			Name:     s.name,
			Exchange: s.exchange,
		}
	}
	return stocks
}

// GetExchangeSymbols returns symbols for a specific exchange
func (l *Loader) GetExchangeSymbols(ctx context.Context, exchange string) ([]model.Stock, error) {
	exchange = strings.ToUpper(exchange)
	switch exchange {
	case "US", "NYSE", "NASDAQ":
		return l.provider.GetSymbols(ctx, exchange)
	default:
		return nil, fmt.Errorf("unsupported exchange: %s", exchange)
	}
}
