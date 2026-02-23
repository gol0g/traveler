package trader

import (
	"context"
	"log"
	"sort"

	"traveler/internal/strategy"
	"traveler/internal/symbols"
	"traveler/pkg/model"
)

// AdaptiveConfig 적응형 스캔 설정
type AdaptiveConfig struct {
	// 품질 기준
	MinSignals   int     // 최소 시그널 수
	MinAvgProb   float64 // 최소 평균 승률
	MinAvgRR     float64 // 최소 평균 R/R

	// 확대 스캔 설정
	MaxExpansions int  // 최대 확대 횟수
	Verbose       bool // 상세 출력
}

// DefaultAdaptiveConfig 기본 설정
func DefaultAdaptiveConfig() AdaptiveConfig {
	return AdaptiveConfig{
		MinSignals:    3,
		MinAvgProb:    53.0, // 55 → 53: KR 불장 풀백 시그널 평균 54.6%
		MinAvgRR:      1.5,
		MaxExpansions: 2,
		Verbose:       false,
	}
}

// QualityScore 시그널 품질 점수
type QualityScore struct {
	SignalCount int
	AvgProb     float64
	AvgRR       float64
	MinProb     float64
	MaxProb     float64
}

// IsAcceptable 품질 기준 충족 여부
func (q QualityScore) IsAcceptable(cfg AdaptiveConfig) bool {
	return q.SignalCount >= cfg.MinSignals &&
		q.AvgProb >= cfg.MinAvgProb &&
		q.AvgRR >= cfg.MinAvgRR
}

// EvaluateSignals 시그널 품질 평가
func EvaluateSignals(signals []strategy.Signal) QualityScore {
	if len(signals) == 0 {
		return QualityScore{}
	}

	var totalProb, totalRR float64
	minProb := 100.0
	maxProb := 0.0

	for _, sig := range signals {
		totalProb += sig.Probability
		if sig.Guide != nil {
			totalRR += sig.Guide.RiskRewardRatio
		}
		if sig.Probability < minProb {
			minProb = sig.Probability
		}
		if sig.Probability > maxProb {
			maxProb = sig.Probability
		}
	}

	return QualityScore{
		SignalCount: len(signals),
		AvgProb:     totalProb / float64(len(signals)),
		AvgRR:       totalRR / float64(len(signals)),
		MinProb:     minProb,
		MaxProb:     maxProb,
	}
}

// UniverseTier 유니버스 티어 (스캔 순서)
type UniverseTier struct {
	Name     string
	Universe symbols.Universe
	Priority int // 낮을수록 먼저
}

// GetUniverseTiers 잔고 기반 유니버스 티어 결정
func GetUniverseTiers(balance float64) []UniverseTier {
	// 티어 1: 잔고에 맞는 1차 유니버스
	// 티어 2+: 확대 스캔용 추가 유니버스

	switch {
	case balance < 500:
		// 소액: russell(저가주) 우선, 이후 대형주
		return []UniverseTier{
			{Name: "russell", Universe: symbols.UniverseRussell, Priority: 1},
			{Name: "midcap", Universe: symbols.UniverseMidCap, Priority: 2},
			{Name: "sp500", Universe: symbols.UniverseSP500, Priority: 3},
			{Name: "nasdaq100", Universe: symbols.UniverseNasdaq100, Priority: 3},
		}
	case balance < 5000:
		// 중간: russell + midcap 우선
		return []UniverseTier{
			{Name: "russell", Universe: symbols.UniverseRussell, Priority: 1},
			{Name: "midcap", Universe: symbols.UniverseMidCap, Priority: 1},
			{Name: "sp500", Universe: symbols.UniverseSP500, Priority: 2},
			{Name: "nasdaq100", Universe: symbols.UniverseNasdaq100, Priority: 2},
		}
	case balance < 25000:
		// 중고액: 대형주 우선, 필요시 소형주로 확대
		return []UniverseTier{
			{Name: "nasdaq100", Universe: symbols.UniverseNasdaq100, Priority: 1},
			{Name: "sp500", Universe: symbols.UniverseSP500, Priority: 1},
			{Name: "midcap", Universe: symbols.UniverseMidCap, Priority: 2},
			{Name: "russell", Universe: symbols.UniverseRussell, Priority: 2},
		}
	default:
		// 고액: 전체 스캔
		return []UniverseTier{
			{Name: "nasdaq100", Universe: symbols.UniverseNasdaq100, Priority: 1},
			{Name: "sp500", Universe: symbols.UniverseSP500, Priority: 1},
			{Name: "russell", Universe: symbols.UniverseRussell, Priority: 1},
			{Name: "midcap", Universe: symbols.UniverseMidCap, Priority: 1},
		}
	}
}

// TierFunc 유니버스 티어 결정 함수
type TierFunc func(balance float64) []UniverseTier

// FilterFunc 시그널 필터 함수 (펀더멘탈 등). 통과한 시그널만 반환.
type FilterFunc func(ctx context.Context, signals []strategy.Signal) []strategy.Signal

// AdaptiveScanner 적응형 스캐너
type AdaptiveScanner struct {
	config      AdaptiveConfig
	sizerConfig SizerConfig
	scanFunc    ScanFunc
	tierFunc    TierFunc   // nil이면 기본 GetUniverseTiers 사용
	filterFunc  FilterFunc // nil이면 필터 없음 (품질 평가 전에 적용)
}

// ScanFunc 스캔 함수 타입
type ScanFunc func(ctx context.Context, stocks []model.Stock) ([]strategy.Signal, error)

// NewAdaptiveScanner 생성자
func NewAdaptiveScanner(cfg AdaptiveConfig, sizerCfg SizerConfig, scanFunc ScanFunc) *AdaptiveScanner {
	return &AdaptiveScanner{
		config:      cfg,
		sizerConfig: sizerCfg,
		scanFunc:    scanFunc,
	}
}

// SetTierFunc 유니버스 티어 결정 함수 커스터마이즈 (한국 시장용)
func (s *AdaptiveScanner) SetTierFunc(fn TierFunc) {
	s.tierFunc = fn
}

// SetFilterFunc 품질 평가 전 시그널 필터 설정 (펀더멘탈 등)
func (s *AdaptiveScanner) SetFilterFunc(fn FilterFunc) {
	s.filterFunc = fn
}

// ScanResult 스캔 결과
type AdaptiveScanResult struct {
	Signals       []strategy.Signal
	Quality       QualityScore
	ScannedCount  int
	UniversesUsed []string
	Expansions    int
	Decision      string // "trade", "skip", "expanded"
}

// Scan 적응형 스캔 실행
func (s *AdaptiveScanner) Scan(ctx context.Context, loader StockLoader) (*AdaptiveScanResult, error) {
	result := &AdaptiveScanResult{
		UniversesUsed: make([]string, 0),
	}

	balance := s.sizerConfig.TotalCapital
	maxPrice := balance * s.sizerConfig.MaxPositionPct
	log.Printf("[ADAPTIVE] Balance=$%.2f, maxPrice=$%.2f, maxExpansions=%d", balance, maxPrice, s.config.MaxExpansions)

	var tiers []UniverseTier
	if s.tierFunc != nil {
		tiers = s.tierFunc(balance)
	} else {
		tiers = GetUniverseTiers(balance)
	}
	currentPriority := 1
	var allSignals []strategy.Signal
	scannedSymbols := make(map[string]bool)

	for expansion := 0; expansion <= s.config.MaxExpansions; expansion++ {
		// 현재 priority의 유니버스들 수집
		var tierUniverses []UniverseTier
		for _, t := range tiers {
			if t.Priority == currentPriority {
				tierUniverses = append(tierUniverses, t)
			}
		}

		if len(tierUniverses) == 0 {
			currentPriority++
			continue
		}

		// 유니버스 로드 및 스캔
		for _, tier := range tierUniverses {
			stocks, err := loader.LoadUniverse(ctx, tier.Universe)
			if err != nil {
				log.Printf("[ADAPTIVE] Failed to load %s: %v", tier.Name, err)
				continue
			}
			log.Printf("[ADAPTIVE] Scanning %s universe (%d stocks)...", tier.Name, len(stocks))

			// 이미 스캔한 종목 제외 + 가격 필터
			var newStocks []model.Stock
			for _, stock := range stocks {
				if !scannedSymbols[stock.Symbol] {
					scannedSymbols[stock.Symbol] = true
					newStocks = append(newStocks, stock)
				}
			}

			if len(newStocks) == 0 {
				continue
			}

			result.UniversesUsed = append(result.UniversesUsed, tier.Name)
			result.ScannedCount += len(newStocks)

			// 스캔 실행
			signals, err := s.scanFunc(ctx, newStocks)
			if err != nil {
				continue
			}

			// 가격 필터링 (매수 가능한 것만)
			var filtered int
			for _, sig := range signals {
				if sig.Guide != nil && sig.Guide.EntryPrice <= maxPrice {
					allSignals = append(allSignals, sig)
				} else {
					price := 0.0
					if sig.Guide != nil {
						price = sig.Guide.EntryPrice
					}
					log.Printf("[ADAPTIVE] Price filter: %s rejected (price=%.0f > max=%.0f)", sig.Stock.Symbol, price, maxPrice)
					filtered++
				}
			}
			log.Printf("[ADAPTIVE] %s: %d raw signals, %d passed price filter (max $%.2f), %d filtered",
				tier.Name, len(signals), len(signals)-filtered, maxPrice, filtered)
		}

		// 펀더멘탈 등 필터 적용 (품질 평가 전)
		if s.filterFunc != nil {
			before := len(allSignals)
			allSignals = s.filterFunc(ctx, allSignals)
			if filtered := before - len(allSignals); filtered > 0 {
				log.Printf("[ADAPTIVE] Filter applied: %d → %d signals", before, len(allSignals))
			}
		}

		// 품질 평가
		quality := EvaluateSignals(allSignals)
		result.Quality = quality
		result.Signals = allSignals

		log.Printf("[ADAPTIVE] Tier %d complete: %d signals (total), avg prob %.1f%%, avg R/R %.2f",
			currentPriority, quality.SignalCount, quality.AvgProb, quality.AvgRR)

		// 품질 충족시 종료
		if quality.IsAcceptable(s.config) {
			log.Printf("[ADAPTIVE] Quality acceptable. Stopping expansion.")
			result.Decision = "trade"
			break
		}

		// 다음 tier로 확대
		currentPriority++
		result.Expansions++

		if expansion < s.config.MaxExpansions {
			result.Decision = "expanded"
			log.Printf("[ADAPTIVE] Quality not met (need %d signals, %.1f%% prob, %.1f R/R). Expanding to tier %d...",
				s.config.MinSignals, s.config.MinAvgProb, s.config.MinAvgRR, currentPriority)
		} else {
			log.Printf("[ADAPTIVE] Max expansions (%d) reached. Using %d signals as-is.", s.config.MaxExpansions, quality.SignalCount)
		}
	}

	// 최종 판단
	if len(result.Signals) == 0 {
		result.Decision = "skip"
	} else if result.Decision == "" {
		// 확대 다 했는데도 품질 미달
		if result.Quality.SignalCount > 0 {
			result.Decision = "trade_low_quality"
		} else {
			result.Decision = "skip"
		}
	}

	// 승률순 정렬
	sort.Slice(result.Signals, func(i, j int) bool {
		return result.Signals[i].Probability > result.Signals[j].Probability
	})

	return result, nil
}

// GetKRUniverseTiers 한국 시장 유니버스 티어 (KRW 기준)
func GetKRUniverseTiers(balance float64) []UniverseTier {
	switch {
	case balance < 5000000: // 500만원 미만: KOSDAQ(저가주) 우선 + KOSPI200 확대
		return []UniverseTier{
			{Name: "kosdaq30", Universe: symbols.UniverseKosdaq30, Priority: 1},
			{Name: "kospi30", Universe: symbols.UniverseKospi30, Priority: 1},
			{Name: "kospi200", Universe: symbols.UniverseKospi200, Priority: 2},
		}
	case balance < 50000000: // 5000만원 미만: KOSPI+KOSDAQ
		return []UniverseTier{
			{Name: "kospi30", Universe: symbols.UniverseKospi30, Priority: 1},
			{Name: "kosdaq30", Universe: symbols.UniverseKosdaq30, Priority: 1},
			{Name: "kospi200", Universe: symbols.UniverseKospi200, Priority: 2},
		}
	default: // 고액: 전체
		return []UniverseTier{
			{Name: "kospi30", Universe: symbols.UniverseKospi30, Priority: 1},
			{Name: "kosdaq30", Universe: symbols.UniverseKosdaq30, Priority: 1},
			{Name: "kospi200", Universe: symbols.UniverseKospi200, Priority: 1},
		}
	}
}

// AdjustConfigForKRBalance KRW 잔고 기반 Sizer 설정
func AdjustConfigForKRBalance(balance float64) SizerConfig {
	cfg := SizerConfig{
		TotalCapital:   balance,
		RiskPerTrade:   0.01,
		MaxPositionPct: 0.20,
		MaxPositions:   5,
		MinRiskReward:  1.5,
		CommissionRate: 0.005, // 국내 수수료 0.25% x 2 = 0.5%
	}

	switch {
	case balance < 500000: // 50만원 미만: ETF tier — 집중 투자
		cfg.RiskPerTrade = 0.05
		cfg.MaxPositionPct = 1.0     // ETF 1주 매수를 위해 전액 투자 허용
		cfg.MaxPositions = 2
		cfg.MinRiskReward = 1.0
		cfg.MinExpectedReturn = 0.005
	case balance < 5000000: // 500만원 미만: 적극적
		cfg.RiskPerTrade = 0.02
		cfg.MaxPositionPct = 0.25
		cfg.MaxPositions = 5
		cfg.MinRiskReward = 1.5
	case balance < 50000000: // 5000만원 미만
		cfg.RiskPerTrade = 0.015
		cfg.MaxPositionPct = 0.25
		cfg.MaxPositions = 5
		cfg.MinRiskReward = 1.5
	default:
		cfg.RiskPerTrade = 0.01
		cfg.MaxPositionPct = 0.20
		cfg.MaxPositions = 5
		cfg.MinRiskReward = 2.0
	}

	return cfg
}

// GetCryptoUniverseTiers returns adaptive scanner tiers for crypto
func GetCryptoUniverseTiers(balance float64) []UniverseTier {
	// Crypto uses same tiers regardless of balance (small universe)
	return []UniverseTier{
		{Name: "crypto-top10", Universe: symbols.UniverseCryptoTop10, Priority: 1},
		{Name: "crypto-top30", Universe: symbols.UniverseCryptoTop30, Priority: 2},
	}
}

// GetUSETFTiers returns ETF-only universe tiers for US
func GetUSETFTiers(balance float64) []UniverseTier {
	return []UniverseTier{
		{Name: "us-etf", Universe: symbols.UniverseUSETF, Priority: 1},
	}
}

// GetKRETFTiers returns ETF-only universe tiers for KR
func GetKRETFTiers(balance float64) []UniverseTier {
	return []UniverseTier{
		{Name: "kr-etf", Universe: symbols.UniverseKRETF, Priority: 1},
	}
}

// StockLoader 종목 로더 인터페이스
type StockLoader interface {
	LoadUniverse(ctx context.Context, universe symbols.Universe) ([]model.Stock, error)
}
