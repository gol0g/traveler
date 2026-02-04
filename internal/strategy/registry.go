package strategy

import (
	"fmt"
	"sync"

	"traveler/internal/provider"
)

// StrategyFactory 전략 생성 함수 타입
type StrategyFactory func(p provider.Provider) Strategy

// registry 전역 레지스트리
var (
	registry     = make(map[string]StrategyFactory)
	registryLock sync.RWMutex
)

// Register 전략 등록
// 새 전략 추가시: strategy.Register("breakout", NewBreakoutStrategy)
func Register(name string, factory StrategyFactory) {
	registryLock.Lock()
	defer registryLock.Unlock()
	registry[name] = factory
}

// Get 전략 가져오기
func Get(name string, p provider.Provider) (Strategy, error) {
	registryLock.RLock()
	factory, ok := registry[name]
	registryLock.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown strategy: %s (available: %v)", name, List())
	}

	return factory(p), nil
}

// List 등록된 전략 목록
func List() []string {
	registryLock.RLock()
	defer registryLock.RUnlock()

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// MustGet 전략 가져오기 (없으면 panic)
func MustGet(name string, p provider.Provider) Strategy {
	s, err := Get(name, p)
	if err != nil {
		panic(err)
	}
	return s
}

// init 기본 전략 등록
func init() {
	// Pullback 전략 등록
	Register("pullback", func(p provider.Provider) Strategy {
		return NewPullbackStrategy(DefaultPullbackConfig(), p)
	})

	// 추후 추가할 전략들:
	// Register("breakout", NewBreakoutStrategy)
	// Register("momentum", NewMomentumStrategy)
	// Register("mean-reversion", NewMeanReversionStrategy)
}

// StrategyInfo 전략 정보
type StrategyInfo struct {
	Name        string
	Description string
	Type        string // "trend-following", "counter-trend", "momentum"
	TimeFrame   string // "swing", "day", "scalp"
}

// GetInfo 전략 정보 가져오기
func GetInfo(name string, p provider.Provider) (*StrategyInfo, error) {
	s, err := Get(name, p)
	if err != nil {
		return nil, err
	}

	return &StrategyInfo{
		Name:        s.Name(),
		Description: s.Description(),
	}, nil
}

// AllInfo 모든 전략 정보
func AllInfo(p provider.Provider) []StrategyInfo {
	names := List()
	infos := make([]StrategyInfo, 0, len(names))

	for _, name := range names {
		if info, err := GetInfo(name, p); err == nil {
			infos = append(infos, *info)
		}
	}

	return infos
}
