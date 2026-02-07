package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration
type Config struct {
	API     APIConfig     `yaml:"api"`
	KIS     KISConfig     `yaml:"kis"`
	Trader  TraderConfig  `yaml:"trader"`
	Daemon  DaemonConfig  `yaml:"daemon"`
	Scanner ScannerConfig `yaml:"scanner"`
	Pattern PatternConfig `yaml:"pattern"`
}

// DaemonConfig holds daemon mode settings
type DaemonConfig struct {
	DailyTargetPct       float64 `yaml:"daily_target_pct"`        // 일일 목표 수익률 (예: 1.0 = 1%)
	DailyLossLimit       float64 `yaml:"daily_loss_limit"`        // 일일 최대 손실 (예: -2.0 = -2%)
	MaxTrades            int     `yaml:"max_trades"`              // 일일 최대 거래 횟수
	ScanIntervalMin      int     `yaml:"scan_interval_min"`       // 스캔 주기 (분)
	SleepOnExit          bool    `yaml:"sleep_on_exit"`           // 종료시 PC 절전
	WaitForMarket        bool    `yaml:"wait_for_market"`         // 마켓 열릴 때까지 대기
	MaxWaitHours         int     `yaml:"max_wait_hours"`          // 최대 대기 시간 (시간)
	ClosePositionsOnExit bool    `yaml:"close_positions_on_exit"` // 종료시 포지션 전량 청산 여부
}

// KISAccountConfig holds a single KIS account's credentials
type KISAccountConfig struct {
	AppKey    string `yaml:"app_key"`
	AppSecret string `yaml:"app_secret"`
	AccountNo string `yaml:"account_no"` // XXXXXXXX-XX 형식
}

// KISConfig holds KIS API settings
type KISConfig struct {
	// 해외 계좌 (기존 - 하위 호환)
	AppKey    string `yaml:"app_key"`
	AppSecret string `yaml:"app_secret"`
	AccountNo string `yaml:"account_no"` // XXXXXXXX-XX 형식

	// 국내 계좌 (별도 AppKey)
	Domestic KISAccountConfig `yaml:"domestic"`
}

// TraderConfig holds auto-trading settings
type TraderConfig struct {
	DryRun            bool    `yaml:"dry_run"`
	MaxPositions      int     `yaml:"max_positions"`
	MaxPositionPct    float64 `yaml:"max_position_pct"`
	RiskPerTrade      float64 `yaml:"risk_per_trade"`
	MonitorInterval   int     `yaml:"monitor_interval_sec"`
	CommissionRate    float64 `yaml:"commission_rate"`     // 수수료율 (편도, 예: 0.0025 = 0.25%)
	MinExpectedReturn float64 `yaml:"min_expected_return"` // 최소 기대수익률 (예: 0.01 = 1%)
}

// APIConfig holds API provider configurations
type APIConfig struct {
	Finnhub      ProviderConfig `yaml:"finnhub"`
	AlphaVantage ProviderConfig `yaml:"alphavantage"`
}

// ProviderConfig holds individual provider settings
type ProviderConfig struct {
	Key       string `yaml:"key"`
	RateLimit int    `yaml:"rate_limit"` // requests per minute
}

// ScannerConfig holds scanner settings
type ScannerConfig struct {
	Workers int           `yaml:"workers"`
	Timeout time.Duration `yaml:"timeout"`
}

// PatternConfig holds pattern detection settings
type PatternConfig struct {
	ConsecutiveDays       int     `yaml:"consecutive_days"`
	MorningDropThreshold  float64 `yaml:"morning_drop_threshold"`  // percent (negative value)
	CloseRiseThreshold    float64 `yaml:"close_rise_threshold"`    // percent (positive value)
	ReboundThreshold      float64 `yaml:"rebound_threshold"`       // percent from morning low
	MorningWindowMinutes  int     `yaml:"morning_window"`          // minutes after open
	ClosingWindowMinutes  int     `yaml:"closing_window"`          // minutes before close
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	return &Config{
		API: APIConfig{
			Finnhub: ProviderConfig{
				Key:       os.Getenv("FINNHUB_API_KEY"),
				RateLimit: 60,
			},
			AlphaVantage: ProviderConfig{
				Key:       os.Getenv("ALPHAVANTAGE_API_KEY"),
				RateLimit: 5,
			},
		},
		KIS: KISConfig{
			AppKey:    os.Getenv("KIS_APP_KEY"),
			AppSecret: os.Getenv("KIS_APP_SECRET"),
			AccountNo: os.Getenv("KIS_ACCOUNT_NO"),
		},
		Trader: TraderConfig{
			DryRun:            true,
			MaxPositions:      5,
			MaxPositionPct:    0.20,
			RiskPerTrade:      0.01,
			MonitorInterval:   30,
			CommissionRate:    0.0025, // 0.25% (KIS 해외주식 기본)
			MinExpectedReturn: 0.01,   // 1% (수수료 0.5% + 마진 0.5%)
		},
		Daemon: DaemonConfig{
			DailyTargetPct:       1.0,
			DailyLossLimit:       -2.0,
			MaxTrades:            10,
			ScanIntervalMin:      30,
			SleepOnExit:          true,
			WaitForMarket:        true,
			MaxWaitHours:         2,
			ClosePositionsOnExit: false, // 기본: 포지션 유지 (다음 날 계속 모니터링)
		},
		Scanner: ScannerConfig{
			Workers: 10,
			Timeout: 30 * time.Second,
		},
		Pattern: PatternConfig{
			ConsecutiveDays:       3,
			MorningDropThreshold:  -1.0,
			CloseRiseThreshold:    0.5,
			ReboundThreshold:      2.0,
			MorningWindowMinutes:  60,
			ClosingWindowMinutes:  60,
		},
	}
}

// Load loads configuration from a YAML file
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // Use defaults if file doesn't exist
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// Override with environment variables if set
	if key := os.Getenv("FINNHUB_API_KEY"); key != "" {
		cfg.API.Finnhub.Key = key
	}
	if key := os.Getenv("ALPHAVANTAGE_API_KEY"); key != "" {
		cfg.API.AlphaVantage.Key = key
	}
	if key := os.Getenv("KIS_APP_KEY"); key != "" {
		cfg.KIS.AppKey = key
	}
	if key := os.Getenv("KIS_APP_SECRET"); key != "" {
		cfg.KIS.AppSecret = key
	}
	if key := os.Getenv("KIS_ACCOUNT_NO"); key != "" {
		cfg.KIS.AccountNo = key
	}

	// 국내 KIS 환경변수
	if key := os.Getenv("KIS_KR_APP_KEY"); key != "" {
		cfg.KIS.Domestic.AppKey = key
	}
	if key := os.Getenv("KIS_KR_APP_SECRET"); key != "" {
		cfg.KIS.Domestic.AppSecret = key
	}
	if key := os.Getenv("KIS_KR_ACCOUNT_NO"); key != "" {
		cfg.KIS.Domestic.AccountNo = key
	}

	return cfg, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.API.Finnhub.Key == "" && c.API.AlphaVantage.Key == "" {
		return fmt.Errorf("at least one API key (FINNHUB_API_KEY or ALPHAVANTAGE_API_KEY) is required")
	}
	if c.Scanner.Workers < 1 {
		return fmt.Errorf("workers must be at least 1")
	}
	if c.Pattern.ConsecutiveDays < 1 {
		return fmt.Errorf("consecutive_days must be at least 1")
	}
	return nil
}
