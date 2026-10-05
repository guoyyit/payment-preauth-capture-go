package config

import (
	"encoding/json"
	"os"
	"time"
)

// Config groups the runtime configuration of the payment service.
type Config struct {
	// StorePath is the single durable snapshot file. All state is persisted
	// atomically to this path; reopening it performs crash recovery.
	StorePath string `json:"store_path"`

	// SweepInterval controls how often expired preauth holds are released.
	// A short interval makes auto-release timelier; zero/negative disables
	// the background timer (SweepExpired can still be called manually).
	SweepInterval time.Duration `json:"sweep_interval"`

	// DefaultPreAuthTTL is used when a caller omits the preauth TTL.
	DefaultPreAuthTTL time.Duration `json:"default_preauth_ttl"`

	// AmountUnit documents the unit used for every amount field. The service
	// uses integer minor units ("cents"); do not change this per deployment
	// without migrating historical data.
	AmountUnit string `json:"amount_unit"`
}

// Default returns sensible local-only defaults.
func Default() Config {
	return Config{
		StorePath:         "./data/payment-store.json",
		SweepInterval:     5 * time.Second,
		DefaultPreAuthTTL: 24 * time.Hour,
		AmountUnit:        "minor-unit (integer cents)",
	}
}

// LoadFromFile reads JSON configuration; an empty/missing path yields defaults.
func LoadFromFile(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	var rawCfg struct {
		StorePath         string `json:"store_path"`
		SweepInterval     string `json:"sweep_interval"`
		DefaultPreAuthTTL string `json:"default_preauth_ttl"`
		AmountUnit        string `json:"amount_unit"`
	}
	if err := json.Unmarshal(raw, &rawCfg); err != nil {
		return cfg, err
	}
	if rawCfg.StorePath != "" {
		cfg.StorePath = rawCfg.StorePath
	}
	if rawCfg.AmountUnit != "" {
		cfg.AmountUnit = rawCfg.AmountUnit
	}
	if rawCfg.SweepInterval != "" {
		d, err := time.ParseDuration(rawCfg.SweepInterval)
		if err != nil {
			return cfg, err
		}
		cfg.SweepInterval = d
	}
	if rawCfg.DefaultPreAuthTTL != "" {
		d, err := time.ParseDuration(rawCfg.DefaultPreAuthTTL)
		if err != nil {
			return cfg, err
		}
		cfg.DefaultPreAuthTTL = d
	}
	return cfg, nil
}
