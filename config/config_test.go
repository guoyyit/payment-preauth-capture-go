package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/config"
)

func TestLoadDefaultsWhenMissing(t *testing.T) {
	cfg, err := config.LoadFromFile(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorePath == "" || cfg.SweepInterval <= 0 || cfg.DefaultPreAuthTTL <= 0 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadParsesDurations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{
		"store_path": "/tmp/p.json",
		"sweep_interval": "250ms",
		"default_preauth_ttl": "2h",
		"amount_unit": "cents"
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorePath != "/tmp/p.json" {
		t.Fatalf("store path=%q", cfg.StorePath)
	}
	if cfg.SweepInterval != 250*time.Millisecond {
		t.Fatalf("sweep=%s", cfg.SweepInterval)
	}
	if cfg.DefaultPreAuthTTL != 2*time.Hour {
		t.Fatalf("ttl=%s", cfg.DefaultPreAuthTTL)
	}
}
