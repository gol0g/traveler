package symbols

// Universe represents a predefined stock universe
type Universe string

const (
	UniverseSP500    Universe = "sp500"
	UniverseNasdaq100 Universe = "nasdaq100"
	UniverseTest     Universe = "test" // Small set for testing
)

// GetUniverse returns the list of symbols for a given universe
func GetUniverse(u Universe) []string {
	switch u {
	case UniverseSP500:
		return SP500Symbols
	case UniverseNasdaq100:
		return Nasdaq100Symbols
	case UniverseTest:
		return TestSymbols
	default:
		return nil
	}
}

// TestSymbols is a small set for quick testing
var TestSymbols = []string{
	"AAPL", "MSFT", "GOOGL", "AMZN", "NVDA",
	"META", "TSLA", "AMD", "NFLX", "JPM",
}

// Nasdaq100Symbols is the NASDAQ-100 components (as of 2024)
var Nasdaq100Symbols = []string{
	"AAPL", "ABNB", "ADBE", "ADI", "ADP", "ADSK", "AEP", "AMAT", "AMD", "AMGN",
	"AMZN", "ANSS", "ARM", "ASML", "AVGO", "AZN", "BIIB", "BKNG", "BKR", "CCEP",
	"CDNS", "CDW", "CEG", "CHTR", "CMCSA", "COST", "CPRT", "CRWD", "CSCO", "CSGP",
	"CSX", "CTAS", "CTSH", "DDOG", "DLTR", "DXCM", "EA", "EXC", "FANG", "FAST",
	"FTNT", "GEHC", "GFS", "GILD", "GOOG", "GOOGL", "HON", "IDXX", "ILMN", "INTC",
	"INTU", "ISRG", "KDP", "KHC", "KLAC", "LIN", "LRCX", "LULU", "MAR", "MCHP",
	"MDB", "MDLZ", "MELI", "META", "MNST", "MRNA", "MRVL", "MSFT", "MU", "NFLX",
	"NVDA", "NXPI", "ODFL", "ON", "ORLY", "PANW", "PAYX", "PCAR", "PDD", "PEP",
	"PYPL", "QCOM", "REGN", "ROP", "ROST", "SBUX", "SMCI", "SNPS", "TEAM", "TMUS",
	"TSLA", "TTD", "TTWO", "TXN", "VRSK", "VRTX", "WBD", "WDAY", "XEL", "ZS",
}

// SP500Symbols is a representative subset of S&P 500 (top 100 by market cap)
// Full S&P 500 would be too slow for free API tier
var SP500Symbols = []string{
	// Technology
	"AAPL", "MSFT", "GOOGL", "GOOG", "AMZN", "NVDA", "META", "TSLA", "AVGO", "ORCL",
	"CRM", "ADBE", "AMD", "ACN", "CSCO", "INTC", "IBM", "TXN", "QCOM", "AMAT",
	// Financials
	"BRK.B", "JPM", "V", "MA", "BAC", "WFC", "GS", "MS", "BLK", "SPGI",
	"AXP", "C", "SCHW", "CB", "MMC", "PGR", "AON", "ICE", "CME", "MCO",
	// Healthcare
	"UNH", "JNJ", "LLY", "PFE", "ABBV", "MRK", "TMO", "ABT", "DHR", "BMY",
	"AMGN", "MDT", "ISRG", "GILD", "CVS", "ELV", "SYK", "REGN", "VRTX", "ZTS",
	// Consumer
	"WMT", "PG", "KO", "PEP", "COST", "MCD", "NKE", "SBUX", "TGT", "LOW",
	"HD", "TJX", "BKNG", "MAR", "ORLY", "AZO", "ROST", "DG", "DLTR", "CMG",
	// Industrials
	"CAT", "DE", "UNP", "HON", "UPS", "BA", "RTX", "LMT", "GE", "MMM",
	// Energy
	"XOM", "CVX", "COP", "SLB", "EOG", "MPC", "PSX", "VLO", "OXY", "KMI",
	// Communications
	"NFLX", "DIS", "CMCSA", "T", "VZ", "TMUS", "CHTR", "EA", "TTWO", "WBD",
	// Real Estate & Utilities
	"AMT", "PLD", "CCI", "EQIX", "PSA", "NEE", "DUK", "SO", "D", "AEP",
}
