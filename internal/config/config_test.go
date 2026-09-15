package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Ensure a clean environment for this test.
	for _, k := range []string{
		"LISTEN_ADDR", "METRICS_ADDR", "DATA_DIR", "PUBLIC_BASE_URL",
		"ADMIN_USERNAME", "ADMIN_PASSWORD", "ONLINE_THRESHOLD",
		"RATE_LIMIT", "RATE_LIMIT_BURST", "METRICS_ALLOWED_CIDRS", "LOG_LEVEL",
	} {
		t.Setenv(k, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ListenAddr != "0.0.0.0:8080" {
		t.Errorf("ListenAddr = %q, want 0.0.0.0:8080", cfg.ListenAddr)
	}
	if cfg.MetricsAddr != "127.0.0.1:9090" {
		t.Errorf("MetricsAddr = %q, want 127.0.0.1:9090", cfg.MetricsAddr)
	}
	if cfg.DataDir != "/data" {
		t.Errorf("DataDir = %q, want /data", cfg.DataDir)
	}
	if cfg.AdminUsername != "admin" {
		t.Errorf("AdminUsername = %q, want admin", cfg.AdminUsername)
	}
	if cfg.OnlineThreshold != 30*time.Second {
		t.Errorf("OnlineThreshold = %v, want 30s", cfg.OnlineThreshold)
	}
	if cfg.RateLimit != 5*time.Second {
		t.Errorf("RateLimit = %v, want 5s", cfg.RateLimit)
	}
	if cfg.RateLimitBurst != 3 {
		t.Errorf("RateLimitBurst = %d, want 3", cfg.RateLimitBurst)
	}
	if len(cfg.MetricsAllowedCIDRs) != 0 {
		t.Errorf("MetricsAllowedCIDRs = %v, want empty", cfg.MetricsAllowedCIDRs)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "127.0.0.1:9999")
	t.Setenv("ONLINE_THRESHOLD", "45s")
	t.Setenv("RATE_LIMIT_BURST", "7")
	t.Setenv("METRICS_ALLOWED_CIDRS", "10.0.0.0/8, 192.168.1.5/32")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ListenAddr != "127.0.0.1:9999" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.OnlineThreshold != 45*time.Second {
		t.Errorf("OnlineThreshold = %v, want 45s", cfg.OnlineThreshold)
	}
	if cfg.RateLimitBurst != 7 {
		t.Errorf("RateLimitBurst = %d, want 7", cfg.RateLimitBurst)
	}
	if len(cfg.MetricsAllowedCIDRs) != 2 {
		t.Fatalf("MetricsAllowedCIDRs len = %d, want 2", len(cfg.MetricsAllowedCIDRs))
	}
	if cfg.MetricsAllowedCIDRs[0].String() != "10.0.0.0/8" {
		t.Errorf("CIDR[0] = %s, want 10.0.0.0/8", cfg.MetricsAllowedCIDRs[0])
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
	}{
		{"bad duration", "ONLINE_THRESHOLD", "notaduration"},
		{"zero duration", "RATE_LIMIT", "0s"},
		{"negative burst", "RATE_LIMIT_BURST", "-1"},
		{"non-numeric burst", "RATE_LIMIT_BURST", "abc"},
		{"bad cidr", "METRICS_ALLOWED_CIDRS", "10.0.0.0/99"},
		{"bad log level", "LOG_LEVEL", "verbose"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() with %s=%q: want error, got nil", tc.key, tc.val)
			}
		})
	}
}

func TestDBPath(t *testing.T) {
	cfg := &Config{DataDir: "/data"}
	if got := cfg.DBPath(); got != "/data/netprobe.db" {
		t.Errorf("DBPath() = %q, want /data/netprobe.db", got)
	}
}
