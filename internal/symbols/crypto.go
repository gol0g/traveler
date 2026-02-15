package symbols

import "strings"

// Crypto universes
const (
	UniverseCryptoTop10 Universe = "crypto-top10"
	UniverseCryptoTop30 Universe = "crypto-top30"
)

// CryptoTop10Symbols — Top 10 KRW market coins by volume on Upbit
var CryptoTop10Symbols = []string{
	"KRW-BTC",   // 비트코인
	"KRW-ETH",   // 이더리움
	"KRW-XRP",   // 리플
	"KRW-SOL",   // 솔라나
	"KRW-DOGE",  // 도지코인
	"KRW-ADA",   // 에이다
	"KRW-AVAX",  // 아발란체
	"KRW-DOT",   // 폴카닷
	"KRW-MATIC", // 폴리곤
	"KRW-LINK",  // 체인링크
}

// CryptoTop30Symbols — Top 30 including mid-caps
var CryptoTop30Symbols = []string{
	// Top 10
	"KRW-BTC",   // 비트코인
	"KRW-ETH",   // 이더리움
	"KRW-XRP",   // 리플
	"KRW-SOL",   // 솔라나
	"KRW-DOGE",  // 도지코인
	"KRW-ADA",   // 에이다
	"KRW-AVAX",  // 아발란체
	"KRW-DOT",   // 폴카닷
	"KRW-MATIC", // 폴리곤
	"KRW-LINK",  // 체인링크
	// Mid-caps
	"KRW-NEAR", // 니어프로토콜
	"KRW-ATOM", // 코스모스
	"KRW-EOS",  // 이오스
	"KRW-TRX",  // 트론
	"KRW-SAND", // 샌드박스
	"KRW-MANA", // 디센트럴랜드
	"KRW-SUI",  // 수이
	"KRW-APT",  // 앱토스
	"KRW-ARB",  // 아비트럼
	"KRW-OP",   // 옵티미즘
	"KRW-HBAR", // 헤데라
	"KRW-ALGO", // 알고랜드
	"KRW-XLM",  // 스텔라루멘
	"KRW-ETC",  // 이더리움클래식
	"KRW-BCH",  // 비트코인캐시
	"KRW-AAVE", // 에이브
	"KRW-UNI",  // 유니스왑
	"KRW-SHIB", // 시바이누
	"KRW-IMX",  // 이뮤터블엑스
	"KRW-SEI",  // 세이
}

// CryptoSymbolNames — Korean names for crypto symbols
var CryptoSymbolNames = map[string]string{
	"KRW-BTC":   "비트코인",
	"KRW-ETH":   "이더리움",
	"KRW-XRP":   "리플",
	"KRW-SOL":   "솔라나",
	"KRW-DOGE":  "도지코인",
	"KRW-ADA":   "에이다",
	"KRW-AVAX":  "아발란체",
	"KRW-DOT":   "폴카닷",
	"KRW-MATIC": "폴리곤",
	"KRW-LINK":  "체인링크",
	"KRW-NEAR":  "니어프로토콜",
	"KRW-ATOM":  "코스모스",
	"KRW-EOS":   "이오스",
	"KRW-TRX":   "트론",
	"KRW-SAND":  "샌드박스",
	"KRW-MANA":  "디센트럴랜드",
	"KRW-SUI":   "수이",
	"KRW-APT":   "앱토스",
	"KRW-ARB":   "아비트럼",
	"KRW-OP":    "옵티미즘",
	"KRW-HBAR":  "헤데라",
	"KRW-ALGO":  "알고랜드",
	"KRW-XLM":   "스텔라루멘",
	"KRW-ETC":   "이더리움클래식",
	"KRW-BCH":   "비트코인캐시",
	"KRW-AAVE":  "에이브",
	"KRW-UNI":   "유니스왑",
	"KRW-SHIB":  "시바이누",
	"KRW-IMX":   "이뮤터블엑스",
	"KRW-SEI":   "세이",
}

// IsCryptoSymbol checks if symbol is a crypto market symbol (KRW-, BTC-, USDT- prefix)
func IsCryptoSymbol(sym string) bool {
	return strings.HasPrefix(sym, "KRW-") ||
		strings.HasPrefix(sym, "BTC-") ||
		strings.HasPrefix(sym, "USDT-")
}

// GetCryptoSymbolName returns the Korean name for a crypto symbol
func GetCryptoSymbolName(sym string) string {
	if name, ok := CryptoSymbolNames[sym]; ok {
		return name
	}
	// Fallback: return coin part (e.g., "KRW-BTC" -> "BTC")
	if idx := strings.Index(sym, "-"); idx >= 0 && idx+1 < len(sym) {
		return sym[idx+1:]
	}
	return sym
}

// IsCryptoUniverse returns true if the universe is a crypto universe
func IsCryptoUniverse(u Universe) bool {
	switch u {
	case UniverseCryptoTop10, UniverseCryptoTop30:
		return true
	}
	return false
}
