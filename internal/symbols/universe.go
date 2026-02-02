package symbols

// Universe represents a predefined stock universe
type Universe string

const (
	UniverseTest      Universe = "test"      // 10 stocks for testing
	UniverseDow30     Universe = "dow30"     // Dow Jones 30 blue chips
	UniverseNasdaq100 Universe = "nasdaq100" // NASDAQ 100 tech giants
	UniverseSP500     Universe = "sp500"     // S&P 500 top 100
	UniverseMidCap    Universe = "midcap"    // S&P MidCap 400 top 100
	UniverseRussell   Universe = "russell"   // Russell 2000 top 200
)

// UniverseInfo contains metadata about a universe
type UniverseInfo struct {
	ID          Universe
	Name        string
	Description string
	Count       int
}

// AvailableUniverses returns all available universes with metadata
func AvailableUniverses() []UniverseInfo {
	return []UniverseInfo{
		{UniverseTest, "Test", "10 large-cap stocks for quick testing", len(TestSymbols)},
		{UniverseDow30, "Dow 30", "Dow Jones 30 blue-chip stocks", len(Dow30Symbols)},
		{UniverseNasdaq100, "NASDAQ 100", "Top 100 NASDAQ tech stocks", len(Nasdaq100Symbols)},
		{UniverseSP500, "S&P 500", "Top 100 S&P 500 by market cap", len(SP500Symbols)},
		{UniverseMidCap, "MidCap 400", "Top 100 S&P MidCap 400", len(MidCap100Symbols)},
		{UniverseRussell, "Russell 2000", "Top 200 Russell 2000 small-caps", len(Russell200Symbols)},
	}
}

// GetUniverse returns the list of symbols for a given universe
func GetUniverse(u Universe) []string {
	switch u {
	case UniverseTest:
		return TestSymbols
	case UniverseDow30:
		return Dow30Symbols
	case UniverseNasdaq100:
		return Nasdaq100Symbols
	case UniverseSP500:
		return SP500Symbols
	case UniverseMidCap:
		return MidCap100Symbols
	case UniverseRussell:
		return Russell200Symbols
	default:
		return nil
	}
}

// TestSymbols is a small set for quick testing
var TestSymbols = []string{
	"AAPL", "MSFT", "GOOGL", "AMZN", "NVDA",
	"META", "TSLA", "AMD", "NFLX", "JPM",
}

// Dow30Symbols is the Dow Jones Industrial Average 30 components
var Dow30Symbols = []string{
	"AAPL", "AMGN", "AXP", "BA", "CAT", "CRM", "CSCO", "CVX", "DIS", "DOW",
	"GS", "HD", "HON", "IBM", "INTC", "JNJ", "JPM", "KO", "MCD", "MMM",
	"MRK", "MSFT", "NKE", "PG", "TRV", "UNH", "V", "VZ", "WBA", "WMT",
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

// MidCap100Symbols is top 100 from S&P MidCap 400 (mid-sized companies $3B-$15B)
var MidCap100Symbols = []string{
	// Technology & Software
	"FFIV", "JKHY", "MANH", "MTSI", "NOVT", "PEGA", "PLUS", "SAIC", "SMAR", "TENB",
	// Healthcare & Biotech
	"ALGN", "BIO", "CHE", "CRL", "DXCM", "EXAS", "HOLX", "ICLR", "INCY", "JAZZ",
	"LGND", "MEDP", "NTRA", "NVST", "RARE", "RVMD", "SRPT", "UTHR", "VCYT", "XRAY",
	// Financials
	"AIZ", "AMP", "CBSH", "CFR", "COOP", "EWBC", "FHN", "GBCI", "HWC", "IBKR",
	"KMPR", "LPLA", "MTG", "NAVI", "ORI", "PRI", "RGA", "SEIC", "SNV", "UMBF",
	// Consumer & Retail
	"AAP", "AEO", "BBWI", "BOOT", "BURL", "CASY", "DKS", "FIVE", "FL", "GPS",
	"HBI", "LKQ", "OLLI", "SBH", "SIG", "SKX", "TXRH", "ULTA", "WING", "WSM",
	// Industrials
	"ALK", "AXON", "B", "EME", "EXP", "FIX", "GGG", "GNRC", "ITT", "JBL",
	"KEX", "LECO", "MIDD", "RBC", "RRX", "SCI", "SITE", "TTC", "UFPI", "WMS",
}

// Russell200Symbols is top 200 from Russell 2000 (small-cap $300M-$2B)
// Selected for liquidity and trading volume
var Russell200Symbols = []string{
	// Technology
	"AMBA", "ASAN", "BIGC", "BILL", "BLKB", "BOX", "BRZE", "CFLT", "CLVT", "CWAN",
	"DLO", "DOCN", "DT", "ESTC", "EVBG", "FRSH", "GENI", "GTLB", "HUBS", "JAMF",
	"KARO", "KNBE", "LITE", "LSPD", "MGNI", "MQ", "NCNO", "NEWR", "OOMA", "OSIS",
	"PDFS", "PING", "PLTK", "PRGS", "PTC", "QLYS", "RPD", "SAIL", "SCWX", "SLAB",
	// Healthcare & Biotech
	"ACAD", "ADMA", "AGIO", "AIMD", "AKRO", "ALKS", "ALLO", "AMPH", "ANIK", "ARQT",
	"ARWR", "ATEC", "AVNS", "AXNX", "AXSM", "BCPC", "BEAM", "BHVN", "BLFS", "BMRN",
	"BPMC", "CARA", "CERS", "CORT", "CPRX", "CRNX", "CRSP", "CYTK", "DCPH", "DVAX",
	"EDIT", "ELVN", "ENSG", "FATE", "FOLD", "GERN", "GOSS", "HALO", "HRMY", "ICUI",
	// Financials
	"AFRM", "AGL", "APAM", "ARES", "BANF", "BHLB", "BKU", "BY", "CASH", "CATY",
	"CCBG", "CFFN", "CVBF", "DCOM", "ECPG", "ESNT", "ESGR", "EVO", "FCFS", "FCNC",
	"FBP", "FG", "FIBK", "FNB", "FULT", "HBAN", "HOMB", "HTLF", "INDB", "LKFN",
	"MCY", "NMIH", "NWBI", "OFG", "ONB", "OUT", "PACW", "PEBO", "PFS", "PIPR",
	// Consumer & Retail
	"ARKO", "BIG", "BJRI", "BLMN", "BROS", "CAKE", "CHUY", "CLAR", "CNXC", "COOK",
	"CROX", "DENN", "DIN", "EAT", "EYE", "FWRG", "GCO", "GIII", "GOLF", "GPRO",
	"HIBB", "HELE", "HGV", "IPAR", "JACK", "KRUS", "LE", "LESL", "LZB", "MOD",
	"NATH", "NGVT", "ODP", "PLAY", "PLBY", "PRPL", "RGS", "RUTH", "SBH", "SHAK",
	// Industrials & Energy
	"AAON", "AGCO", "ARCB", "ASPN", "ATKR", "AZZ", "BANR", "BCC", "BE", "BLDR",
	"CALX", "CECO", "CENX", "CMCO", "CNX", "CRS", "DNOW", "DY", "ESE", "EVTC",
	"FELE", "FLR", "GBX", "GEO", "GVA", "HNI", "HP", "HSII", "HY", "ICFI",
	"IEA", "JBT", "KAI", "KALU", "KAMN", "KNTK", "KWR", "LAUR", "LNN", "MASI",
}
