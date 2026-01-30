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
	Scanner ScannerConfig `yaml:"scanner"`
	Pattern PatternConfig `yaml:"pattern"`
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
