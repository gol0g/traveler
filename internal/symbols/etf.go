package symbols

const (
	// ETF 유니버스
	UniverseUSETF Universe = "us-etf" // US ETF (GEM + TQQQ/SMA)
	UniverseKRETF Universe = "kr-etf" // KR ETF (KODEX 타이밍)
)

// USETFSymbols US ETF 유니버스 (GEM + TQQQ/SMA)
var USETFSymbols = []string{
	"SPY",  // S&P 500 ETF
	"VXUS", // Vanguard Total International Stock ETF
	"SHY",  // iShares 1-3 Year Treasury Bond ETF (T-Bill proxy)
	"QQQ",  // NASDAQ-100 ETF (TQQQ 벤치마크)
	"TQQQ", // ProShares UltraPro QQQ (3x leverage)
}

// KRETFSymbols 한국 ETF 유니버스
var KRETFSymbols = []string{
	"069500", // KODEX 200 (KOSPI 200 추종)
	"122630", // KODEX 레버리지 (KOSPI 200 2x)
	"114800", // KODEX 인버스 (KOSPI 200 -1x)
}

// KRETFNames 한국 ETF 종목명
var KRETFNames = map[string]string{
	"069500": "KODEX 200",
	"122630": "KODEX 레버리지",
	"114800": "KODEX 인버스",
}

// USETFNames US ETF 종목명
var USETFNames = map[string]string{
	"SPY":  "SPDR S&P 500 ETF",
	"VXUS": "Vanguard Total Intl Stock ETF",
	"SHY":  "iShares 1-3Y Treasury Bond ETF",
	"QQQ":  "Invesco QQQ Trust",
	"TQQQ": "ProShares UltraPro QQQ",
}

func init() {
	// KR ETF 이름을 글로벌 KRSymbolNames에 등록
	for sym, name := range KRETFNames {
		KRSymbolNames[sym] = name
	}
}
