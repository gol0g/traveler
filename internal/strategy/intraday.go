package strategy

import (
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"traveler/pkg/model"
)

// IntradayConfig 장중 매매 설정
type IntradayConfig struct {
	// ORB 설정
	ORBEnabled    bool
	ORBCollectMin int     // OR 수집 시간 (분, default 30)
	ORBMinRange   float64 // 최소 레인지 % (default 0.3%)
	ORBMaxRange   float64 // 최대 레인지 % (너무 변동성 크면 스킵, default 5%)

	// DipBuy 설정
	DipBuyEnabled  bool
	DipMinDrop     float64 // 최소 낙폭 % (default -3.0)
	DipLookbackMin int     // 낙폭 감지 기간 (분, default 30)

	// 공통
	ScanInterval  time.Duration // 스캔 간격 (default 5 min)
	StopLossPct   float64       // 손절 % (default 1.5)
	TargetPct     float64       // 목표 % (default 2.0)
	MaxPositions  int           // 최대 동시 장중 포지션 (default 3)
	MaxDailyLoss  float64       // 일일 최대 손실 % of capital (default 3.0)
	ForceCloseMin int           // 장 마감 N분 전 강제 청산 (default 30)
}

// DefaultIntradayConfig 기본 설정
func DefaultIntradayConfig() IntradayConfig {
	return IntradayConfig{
		ORBEnabled:     true,
		ORBCollectMin:  30,
		ORBMinRange:    0.3,
		ORBMaxRange:    5.0,
		DipBuyEnabled:  true,
		DipMinDrop:     -3.0,
		DipLookbackMin: 30,
		ScanInterval:   5 * time.Minute,
		StopLossPct:    1.5,
		TargetPct:      2.0,
		MaxPositions:   3,
		MaxDailyLoss:   3.0,
		ForceCloseMin:  30,
	}
}

// PriceSnapshot 시세 스냅샷
type PriceSnapshot struct {
	Time  time.Time
	Price float64
}

// SymbolState 종목별 장중 상태
type SymbolState struct {
	// ORB (Opening Range)
	ORHigh      float64
	ORLow       float64
	ORCollected bool

	// 가격 이력 (DipBuy용)
	Prices []PriceSnapshot

	// 전일 종가 (일중 변동률 계산)
	PrevClose float64

	// 시그널 발생 여부 (중복 방지)
	ORBTriggered    bool
	DipBuyTriggered bool
}

// IntradayScanner 장중 스캐너
type IntradayScanner struct {
	config IntradayConfig

	mu     sync.RWMutex
	states map[string]*SymbolState // symbol → state

	// 일일 손익 추적
	dailyPnL     float64
	capital      float64
	lossLimitHit bool

	// 활성 장중 포지션 수
	activeCount int
}

// NewIntradayScanner 생성자
func NewIntradayScanner(cfg IntradayConfig, capital float64) *IntradayScanner {
	return &IntradayScanner{
		config:  cfg,
		states:  make(map[string]*SymbolState),
		capital: capital,
	}
}

// InitSymbol 종목 초기화 (전일 종가 설정)
func (s *IntradayScanner) InitSymbol(symbol string, prevClose float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.states[symbol] = &SymbolState{
		PrevClose: prevClose,
		ORHigh:    0,
		ORLow:     math.MaxFloat64,
		Prices:    make([]PriceSnapshot, 0, 64),
	}
}

// RecordPrice 가격 기록 (OR 수집 + DipBuy 이력)
func (s *IntradayScanner) RecordPrice(symbol string, price float64, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[symbol]
	if !ok {
		return
	}

	// OR 수집 중이면 고/저 업데이트
	if !state.ORCollected {
		if price > state.ORHigh {
			state.ORHigh = price
		}
		if price < state.ORLow {
			state.ORLow = price
		}
	}

	// 가격 이력 추가
	state.Prices = append(state.Prices, PriceSnapshot{Time: t, Price: price})

	// 이력이 너무 길면 오래된 것 제거 (최근 2시간)
	cutoff := t.Add(-2 * time.Hour)
	startIdx := 0
	for i, p := range state.Prices {
		if p.Time.After(cutoff) {
			startIdx = i
			break
		}
	}
	if startIdx > 0 {
		state.Prices = state.Prices[startIdx:]
	}
}

// FinalizeOR OR 수집 완료 표시
func (s *IntradayScanner) FinalizeOR() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for symbol, state := range s.states {
		if state.ORHigh > 0 && state.ORLow < math.MaxFloat64 {
			state.ORCollected = true
			rangePct := (state.ORHigh - state.ORLow) / state.ORLow * 100
			log.Printf("[INTRADAY] %s OR: %.2f ~ %.2f (range=%.1f%%)",
				symbol, state.ORLow, state.ORHigh, rangePct)
		}
	}
}

// Scan 장중 시그널 스캔 (OR 수집 완료 후 호출)
func (s *IntradayScanner) Scan() []Signal {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.lossLimitHit {
		return nil
	}

	var signals []Signal

	for symbol, state := range s.states {
		if len(state.Prices) < 2 {
			continue
		}

		currentPrice := state.Prices[len(state.Prices)-1].Price

		// ORB 스캔
		if s.config.ORBEnabled && !state.ORBTriggered {
			if sig := s.checkORB(symbol, state, currentPrice); sig != nil {
				signals = append(signals, *sig)
			}
		}

		// DipBuy 스캔
		if s.config.DipBuyEnabled && !state.DipBuyTriggered {
			if sig := s.checkDipBuy(symbol, state, currentPrice); sig != nil {
				signals = append(signals, *sig)
			}
		}
	}

	// 최대 포지션 수 제한
	available := s.config.MaxPositions - s.activeCount
	if available <= 0 {
		return nil
	}
	if len(signals) > available {
		signals = signals[:available]
	}

	return signals
}

// checkORB Opening Range Breakout 체크
func (s *IntradayScanner) checkORB(symbol string, state *SymbolState, currentPrice float64) *Signal {
	if !state.ORCollected || state.ORHigh <= 0 {
		return nil
	}

	rangePct := (state.ORHigh - state.ORLow) / state.ORLow * 100

	// 레인지 필터
	if rangePct < s.config.ORBMinRange || rangePct > s.config.ORBMaxRange {
		return nil
	}

	// 돌파 확인: 현재가 > OR 고가
	if currentPrice <= state.ORHigh {
		return nil
	}

	// 돌파 강도: OR 고가 대비 초과 비율
	breakoutPct := (currentPrice - state.ORHigh) / state.ORHigh * 100

	// 너무 많이 올라간 건 스킵 (이미 진입 타이밍 놓침)
	if breakoutPct > 2.0 {
		return nil
	}

	entry := currentPrice
	rangeWidth := state.ORHigh - state.ORLow
	stop := state.ORLow + rangeWidth*0.5 // OR 중간
	target1 := entry + rangeWidth         // 레인지 폭만큼
	target2 := entry + rangeWidth*1.5

	// 최소 R/R 체크
	risk := entry - stop
	reward := target1 - entry
	if risk <= 0 || reward/risk < 1.0 {
		return nil
	}

	stopPct := (entry - stop) / entry * 100
	targetPct := (target1 - entry) / entry * 100

	return &Signal{
		Stock:       model.Stock{Symbol: symbol},
		Type:        SignalBuy,
		Strategy:    "intraday_orb",
		Strength:    60 + breakoutPct*10,
		Probability: 55,
		Reason: fmt.Sprintf("ORB breakout: OR=%.2f~%.2f (%.1f%%), break=+%.1f%%",
			state.ORLow, state.ORHigh, rangePct, breakoutPct),
		Details: map[string]float64{
			"or_high":      state.ORHigh,
			"or_low":       state.ORLow,
			"or_range_pct": rangePct,
			"breakout_pct": breakoutPct,
		},
		Guide: &TradeGuide{
			EntryPrice:      entry,
			EntryType:       "market",
			StopLoss:        stop,
			StopLossPct:     stopPct,
			Target1:         target1,
			Target1Pct:      targetPct,
			Target2:         target2,
			Target2Pct:      (target2 - entry) / entry * 100,
			RiskRewardRatio: reward / risk,
		},
	}
}

// checkDipBuy 장중 급락 반등 체크
func (s *IntradayScanner) checkDipBuy(symbol string, state *SymbolState, currentPrice float64) *Signal {
	if len(state.Prices) < 6 {
		return nil // 최소 30분 데이터 필요
	}

	now := state.Prices[len(state.Prices)-1].Time

	// 최근 30분 이내 고점 찾기
	lookback := time.Duration(s.config.DipLookbackMin) * time.Minute
	cutoff := now.Add(-lookback)

	recentHigh := 0.0
	for _, p := range state.Prices {
		if p.Time.Before(cutoff) {
			continue
		}
		if p.Price > recentHigh {
			recentHigh = p.Price
		}
	}

	if recentHigh <= 0 {
		return nil
	}

	// 낙폭 계산
	dropPct := (currentPrice - recentHigh) / recentHigh * 100

	if dropPct > s.config.DipMinDrop {
		return nil // 충분히 하락하지 않음
	}

	// 반등 확인: 최근 2개 스냅샷이 상승 중
	n := len(state.Prices)
	if n < 3 {
		return nil
	}
	prev1 := state.Prices[n-2].Price
	prev2 := state.Prices[n-3].Price

	// 저점에서 반등 시작 (현재가 > 직전가, 직전가 < 그 전 가)
	isBottoming := currentPrice > prev1 && prev1 <= prev2

	if !isBottoming {
		return nil
	}

	// 전일 종가 대비 위치 (전일 종가보다 너무 낮으면 약한 종목)
	if state.PrevClose > 0 {
		dailyChange := (currentPrice - state.PrevClose) / state.PrevClose * 100
		if dailyChange < -8 {
			return nil // 하루 -8% 이상은 공포 → 스킵
		}
	}

	entry := currentPrice
	stop := entry * (1 - s.config.StopLossPct/100)
	target1 := entry * (1 + s.config.TargetPct/100)
	target2 := entry * (1 + s.config.TargetPct*1.5/100)

	risk := entry - stop
	reward := target1 - entry
	if risk <= 0 || reward/risk < 1.0 {
		return nil
	}

	return &Signal{
		Stock:       model.Stock{Symbol: symbol},
		Type:        SignalBuy,
		Strategy:    "intraday_dip",
		Strength:    55 + math.Abs(dropPct)*3,
		Probability: 52,
		Reason: fmt.Sprintf("Dip buy: -%.1f%% from 30m high (%.2f→%.2f), bouncing",
			math.Abs(dropPct), recentHigh, currentPrice),
		Details: map[string]float64{
			"drop_pct":    dropPct,
			"recent_high": recentHigh,
			"bounce":      currentPrice - prev1,
		},
		Guide: &TradeGuide{
			EntryPrice:      entry,
			EntryType:       "market",
			StopLoss:        stop,
			StopLossPct:     -s.config.StopLossPct,
			Target1:         target1,
			Target1Pct:      s.config.TargetPct,
			Target2:         target2,
			Target2Pct:      s.config.TargetPct * 1.5,
			RiskRewardRatio: reward / risk,
		},
	}
}

// MarkExecuted 시그널 실행 완료 표시 (중복 진입 방지)
func (s *IntradayScanner) MarkExecuted(symbol, strategy string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[symbol]
	if !ok {
		return
	}

	switch strategy {
	case "intraday_orb":
		state.ORBTriggered = true
	case "intraday_dip":
		state.DipBuyTriggered = true
	}

	s.activeCount++
	log.Printf("[INTRADAY] Executed %s for %s (active: %d/%d)",
		strategy, symbol, s.activeCount, s.config.MaxPositions)
}

// RecordClose 장중 포지션 청산 기록
func (s *IntradayScanner) RecordClose(pnl float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.dailyPnL += pnl
	s.activeCount--
	if s.activeCount < 0 {
		s.activeCount = 0
	}

	// 일일 손실 한도 체크
	if s.capital > 0 {
		lossPct := -s.dailyPnL / s.capital * 100
		if lossPct >= s.config.MaxDailyLoss {
			s.lossLimitHit = true
			log.Printf("[INTRADAY] Daily loss limit hit: %.2f%% (limit: %.1f%%)",
				lossPct, s.config.MaxDailyLoss)
		}
	}
}

// GetDailyPnL 일일 장중 손익 조회
func (s *IntradayScanner) GetDailyPnL() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dailyPnL
}

// IsLossLimitHit 일일 손실 한도 도달 여부
func (s *IntradayScanner) IsLossLimitHit() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lossLimitHit
}

// GetActiveCount 활성 장중 포지션 수
func (s *IntradayScanner) GetActiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeCount
}

// SymbolCount 추적 중인 종목 수
func (s *IntradayScanner) SymbolCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.states)
}

// GetORBSymbols OR 수집 완료된 유효 종목 목록
func (s *IntradayScanner) GetORBSymbols() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []string
	for symbol, state := range s.states {
		if !state.ORCollected || state.ORHigh <= 0 {
			continue
		}
		rangePct := (state.ORHigh - state.ORLow) / state.ORLow * 100
		if rangePct >= s.config.ORBMinRange && rangePct <= s.config.ORBMaxRange {
			result = append(result, symbol)
		}
	}
	return result
}
